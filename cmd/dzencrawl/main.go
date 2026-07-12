package main

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	appconfig "github.com/adinvadim/dzencrawl/internal/config"
	"github.com/adinvadim/dzencrawl/internal/dzen"
	fc "github.com/adinvadim/dzencrawl/internal/firecrawl"
	mdexport "github.com/adinvadim/dzencrawl/internal/markdown"
	"github.com/adinvadim/dzencrawl/internal/store"
	"github.com/openclaw/crawlkit/control"
	"github.com/openclaw/crawlkit/embed"
	"github.com/openclaw/crawlkit/vector"
)

var version = "dev"

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "dzencrawl:", err)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string, out, errOut io.Writer) error {
	global := flag.NewFlagSet("dzencrawl", flag.ContinueOnError)
	global.SetOutput(errOut)
	configPath := global.String("config", "", "config file")
	dbPath := global.String("db", "", "database override")
	if err := global.Parse(args); err != nil {
		return err
	}
	rest := global.Args()
	if len(rest) == 0 {
		help(out)
		return nil
	}
	cmd := rest[0]
	cmdArgs := rest[1:]
	if cmd == "version" {
		fmt.Fprintln(out, version)
		return nil
	}
	if cmd == "init" {
		p, err := appconfig.WriteStarter(*configPath)
		if err == nil {
			fmt.Fprintln(out, p)
		}
		return err
	}
	if cmd == "metadata" {
		return metadata(out)
	}
	cfg, err := appconfig.Load(*configPath)
	if err != nil {
		return err
	}
	if *dbPath != "" {
		cfg.DBPath, err = filepath.Abs(*dbPath)
		if err != nil {
			return err
		}
	}
	switch cmd {
	case "doctor":
		return doctor(ctx, out, cfg)
	case "sync":
		return syncChannel(ctx, out, errOut, cfg, cmdArgs)
	case "status":
		return status(ctx, out, cfg, cmdArgs)
	case "export-md":
		return exportMD(ctx, out, cfg)
	case "search":
		return search(ctx, out, cfg, cmdArgs)
	case "embed":
		return embedAll(ctx, out, cfg, cmdArgs)
	case "semantic-search":
		return semanticSearch(ctx, out, cfg, cmdArgs)
	case "sql":
		return sqlQuery(ctx, out, cfg, cmdArgs)
	default:
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func help(w io.Writer) {
	fmt.Fprintln(w, `dzencrawl — local-first Dzen archive

Usage: dzencrawl [--config FILE] [--db FILE] COMMAND

Commands:
  init                         write starter config
  doctor                       check Firecrawl and local configuration
  sync CHANNEL [flags]         crawl a dzen.ru channel
  export-md                    export articles and comments to Markdown
  search QUERY                 full-text search
  embed [--force]              create/update article embeddings
  semantic-search QUERY        vector search
  status [--json]              archive counts
  sql QUERY                    read-only SQL
  metadata                     crawlkit control manifest
  version                      print version

sync flags: --max-articles N --refresh`)
}
func open(cfg appconfig.Config) (*store.Store, error) { return store.Open(cfg.DBPath) }

func doctor(ctx context.Context, w io.Writer, cfg appconfig.Config) error {
	delay, _ := time.ParseDuration(cfg.Dzen.RequestDelay)
	report := map[string]any{"db_path": cfg.DBPath, "markdown_dir": cfg.MarkdownDir, "firecrawl_base_url": cfg.Firecrawl.BaseURL, "firecrawl_key_env": cfg.Firecrawl.APIKeyEnv, "firecrawl_key_present": cfg.FirecrawlKey() != "", "dzen_request_delay": delay.String(), "embeddings_enabled": cfg.Embeddings.Enabled}
	if cfg.Embeddings.Enabled {
		report["embeddings"] = embed.CheckProvider(ctx, cfg.EmbedConfig())
	}
	return writeJSON(w, report)
}
func status(ctx context.Context, w io.Writer, cfg appconfig.Config, args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, err := open(cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	x, err := st.Status(ctx)
	if err != nil {
		return err
	}
	if *jsonOut {
		return writeJSON(w, controlStatus(x))
	}
	return writeJSON(w, x)
}

func syncChannel(ctx context.Context, out, errOut io.Writer, cfg appconfig.Config, args []string) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	maxArticles := fs.Int("max-articles", 0, "maximum new articles; 0 means all")
	refresh := fs.Bool("refresh", false, "re-fetch existing articles")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("sync requires a Dzen channel URL or nickname")
	}
	channel, err := dzen.ChannelName(fs.Arg(0))
	if err != nil {
		return err
	}
	if cfg.FirecrawlKey() == "" {
		return fmt.Errorf("%s is required", cfg.Firecrawl.APIKeyEnv)
	}
	timeout, _ := time.ParseDuration(cfg.Firecrawl.Timeout)
	delay, _ := time.ParseDuration(cfg.Dzen.RequestDelay)
	fire := fc.New(cfg.Firecrawl.BaseURL, cfg.FirecrawlKey(), timeout)
	dz := dzen.NewClient(cfg.Dzen.UserAgent, delay)
	st, err := open(cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	now := time.Now().UnixMilli()
	count := 0
	pageLimit := cfg.Dzen.MaxPages
	for page := 1; ; page++ {
		if pageLimit > 0 && page > pageLimit {
			break
		}
		feed, err := dz.ChannelPage(ctx, channel, page)
		if err != nil {
			return fmt.Errorf("channel page %d: %w", page, err)
		}
		if len(feed.Items) == 0 {
			break
		}
		title := feed.Channel.Source.Title
		if title == "" {
			title = channel
		}
		if err := st.UpsertChannel(ctx, channel, title, "https://dzen.ru/"+channel, now); err != nil {
			return err
		}
		for _, item := range feed.Items {
			if *maxArticles > 0 && count >= *maxArticles {
				break
			}
			exists, err := st.HasArticle(ctx, item.Link)
			if err != nil {
				return err
			}
			if exists && !*refresh {
				continue
			}
			fmt.Fprintf(errOut, "scrape %s\n", item.Link)
			scraped, err := fire.Scrape(ctx, item.Link)
			if err != nil {
				return fmt.Errorf("%s: %w", item.Link, err)
			}
			article, initial, cc, err := dzen.ParseArticle(scraped.RawHTML, scraped.Markdown, item.Link, channel)
			if err != nil {
				return fmt.Errorf("parse %s: %w", item.Link, err)
			}
			comments, complete, commentErr := dz.AllComments(ctx, article, initial, cc)
			if commentErr != nil {
				fmt.Fprintf(errOut, "warning: comments for %s are partial: %v\n", item.Link, commentErr)
				comments = initial
			}
			article.CommentsComplete = complete
			if err := st.UpsertArticle(ctx, article, comments, time.Now().UnixMilli()); err != nil {
				return err
			}
			count++
			fmt.Fprintf(out, "%s\t%s\tviews=%d\tcomments=%d/%d\n", article.ID, article.Title, article.Views, len(comments), article.CommentsCount)
		}
		if *maxArticles > 0 && count >= *maxArticles {
			break
		}
		if feed.PageCount > 0 && page >= feed.PageCount {
			break
		}
	}
	fmt.Fprintf(out, "synced %d articles from https://dzen.ru/%s\n", count, channel)
	if cfg.Embeddings.Enabled && count > 0 {
		return embedAll(ctx, out, cfg, nil)
	}
	return nil
}

func exportMD(ctx context.Context, w io.Writer, cfg appconfig.Config) error {
	st, err := open(cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	n, err := mdexport.Export(ctx, st, cfg.MarkdownDir)
	if err == nil {
		fmt.Fprintf(w, "exported %d articles to %s\n", n, cfg.MarkdownDir)
	}
	return err
}
func search(ctx context.Context, w io.Writer, cfg appconfig.Config, args []string) error {
	if len(args) == 0 {
		return errors.New("search requires a query")
	}
	st, err := open(cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	rows, err := st.Search(ctx, strings.Join(args, " "), 20)
	if err != nil {
		return err
	}
	for _, x := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\n", x.URL, x.Title, x.Snippet)
	}
	return nil
}

func embedAll(ctx context.Context, w io.Writer, cfg appconfig.Config, args []string) error {
	fs := flag.NewFlagSet("embed", flag.ContinueOnError)
	force := fs.Bool("force", false, "recompute all")
	if err := fs.Parse(args); err != nil {
		return err
	}
	provider, err := embed.NewProvider(cfg.EmbedConfig())
	if err != nil {
		return err
	}
	st, err := open(cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	articles, err := st.Articles(ctx)
	if err != nil {
		return err
	}
	n := 0
	for _, a := range articles {
		sum := sha256.Sum256([]byte(a.Title + "\n" + a.Markdown))
		hash := hex.EncodeToString(sum[:])
		old, err := st.EmbeddingHash(ctx, a.ID)
		if err != nil {
			return err
		}
		if old == hash && !*force {
			continue
		}
		batch, err := provider.Embed(ctx, []string{a.Title + "\n\n" + a.Markdown})
		if err != nil {
			return fmt.Errorf("embed %s: %w", a.ID, err)
		}
		if len(batch.Vectors) != 1 {
			return fmt.Errorf("embedding provider returned %d vectors", len(batch.Vectors))
		}
		if err := st.SaveEmbedding(ctx, a.ID, batch.Model, hash, batch.Vectors[0], time.Now().UnixMilli()); err != nil {
			return err
		}
		n++
	}
	fmt.Fprintf(w, "embedded %d articles\n", n)
	return nil
}
func semanticSearch(ctx context.Context, w io.Writer, cfg appconfig.Config, args []string) error {
	if len(args) == 0 {
		return errors.New("semantic-search requires a query")
	}
	provider, err := embed.NewProvider(cfg.EmbedConfig())
	if err != nil {
		return err
	}
	q, err := provider.Embed(ctx, []string{strings.Join(args, " ")})
	if err != nil {
		return err
	}
	st, err := open(cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	rows, err := st.Embeddings(ctx)
	if err != nil {
		return err
	}
	candidates := make([]vector.SearchCandidate[store.EmbeddingRow], 0, len(rows))
	for _, x := range rows {
		candidates = append(candidates, vector.SearchCandidate[store.EmbeddingRow]{Item: x, Vector: x.Vector})
	}
	results, err := vector.Search(ctx, q.Vectors[0], candidates, vector.SearchOptions[store.EmbeddingRow]{Limit: 20, InvalidVector: vector.InvalidVectorSkip, TieLess: func(a, b store.EmbeddingRow) bool { return a.ArticleID < b.ArticleID }})
	if err != nil {
		return err
	}
	for _, x := range results {
		fmt.Fprintf(w, "%.4f\t%s\t%s\n", x.Score, x.Item.URL, x.Item.Title)
	}
	return nil
}

func sqlQuery(ctx context.Context, w io.Writer, cfg appconfig.Config, args []string) error {
	if len(args) == 0 {
		return errors.New("sql requires a SELECT query")
	}
	st, err := open(cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	rows, err := st.ReadOnlySQL(ctx, strings.Join(args, " "))
	if err != nil {
		return err
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	cw := csv.NewWriter(w)
	cw.Comma = '\t'
	_ = cw.Write(cols)
	values := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for rows.Next() {
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		record := make([]string, len(cols))
		for i, v := range values {
			if b, ok := v.([]byte); ok {
				record[i] = string(b)
			} else {
				record[i] = fmt.Sprint(v)
			}
		}
		_ = cw.Write(record)
	}
	cw.Flush()
	return rows.Err()
}
func metadata(w io.Writer) error {
	cfg := appconfig.Default()
	path, _ := appconfig.DefaultPath()
	m := control.NewManifest("dzencrawl", "Dzen Crawl", "dzencrawl")
	m.Description = "Local-first Dzen channel archive powered by Firecrawl."
	m.Paths = control.Paths{DefaultConfig: path, ConfigEnv: "DZENCRAWL_CONFIG", DefaultDatabase: cfg.DBPath}
	m.Capabilities = []string{"metadata", "status", "doctor", "sync", "markdown", "search", "embeddings", "sql"}
	m.Privacy = control.Privacy{ExportsSecrets: false, LocalOnlyScopes: []string{"dzen", "firecrawl", "sqlite"}}
	m.Commands = map[string]control.Command{"status": {Title: "Status", Argv: []string{"dzencrawl", "status", "--json"}, JSON: true}, "doctor": {Title: "Doctor", Argv: []string{"dzencrawl", "doctor"}, JSON: true}, "sync": {Title: "Sync", Argv: []string{"dzencrawl", "sync"}, Mutates: true}, "export-md": {Title: "Export Markdown", Argv: []string{"dzencrawl", "export-md"}, Mutates: true}}
	return writeJSON(w, m)
}
func controlStatus(x store.Status) control.Status {
	counts := []control.Count{control.NewCount("channels", "Channels", int64(x.Channels)), control.NewCount("articles", "Articles", int64(x.Articles)), control.NewCount("comments", "Comments", int64(x.Comments)), control.NewCount("embeddings", "Embeddings", int64(x.Embeddings))}
	s := control.NewStatus("dzencrawl", fmt.Sprintf("%d articles and %d comments", x.Articles, x.Comments))
	s.State = "current"
	s.DatabasePath = x.DBPath
	s.DatabaseBytes = x.DBBytes
	s.Counts = counts
	if x.LastSyncAt > 0 {
		s.LastSyncAt = time.UnixMilli(x.LastSyncAt).UTC().Format(time.RFC3339)
	}
	s.Databases = []control.Database{control.SQLiteDatabase("primary", "Dzen archive", "archive", x.DBPath, true, counts)}
	return s
}
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
