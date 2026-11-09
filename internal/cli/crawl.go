package cli

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/openclaw/crawlkit/control"
	"github.com/openclaw/crawlkit/embed"
	"github.com/openclaw/crawlkit/vector"

	"github.com/adinvadim/dzencrawl/internal/apperr"
	"github.com/adinvadim/dzencrawl/internal/config"
	"github.com/adinvadim/dzencrawl/internal/crawl"
	"github.com/adinvadim/dzencrawl/internal/dzen"
	"github.com/adinvadim/dzencrawl/internal/markdown"
	"github.com/adinvadim/dzencrawl/internal/store"
)

func (c *runContext) archive() (*store.Store, error) {
	if override := c.value("db"); override != "" {
		abs, err := filepath.Abs(override)
		if err != nil {
			return nil, err
		}
		c.cfg.DBPath = abs
	}
	return c.store()
}

func runCrawlSync(c *runContext) error {
	st, err := c.archive()
	if err != nil {
		return err
	}
	defer st.Close()
	scope := "mine"
	if !c.has("mine") {
		if err := c.need(1, "crawl sync <channel> | --mine"); err != nil {
			return err
		}
		scope = c.args[0]
	} else if len(c.args) != 0 {
		return apperr.New("usage_error", "--mine takes no channel")
	}
	runID, err := st.BeginRun(c.ctx, scope, c.app.Now().UnixMilli())
	if err != nil {
		return err
	}
	var result crawl.Result
	if c.has("mine") {
		err = c.withAccount(func(acc *account) error {
			var syncErr error
			result, syncErr = crawl.SyncMine(c.ctx, st, acc.client, crawl.MineOptions{PublisherID: acc.sess.PublisherID, Now: c.app.Now, Log: c.app.Stderr})
			return syncErr
		})
	} else {
		var channel string
		if channel, err = dzen.ChannelName(c.args[0]); err != nil {
			err = apperr.New("usage_error", "%v", err)
		} else {
			delay, _ := time.ParseDuration(c.cfg.Dzen.RequestDelay)
			maxArticles := 0
			if raw := c.value("max-articles"); raw != "" {
				if _, scanErr := fmt.Sscan(raw, &maxArticles); scanErr != nil || maxArticles < 0 {
					return apperr.New("usage_error", "--max-articles must be a number")
				}
			}
			result, err = crawl.SyncChannel(c.ctx, st, dzen.NewClient(c.cfg.Dzen.UserAgent, delay), crawl.Options{Channel: channel, MaxArticles: maxArticles, Refresh: c.has("refresh"), MaxPages: c.cfg.Dzen.MaxPages, Now: c.app.Now, Log: c.app.Stderr})
		}
	}
	finish := "ok"
	cause := ""
	switch {
	case err != nil:
		finish, cause = "failed", apperr.As(err).Code
	case result.Degraded():
		finish = "degraded"
	}
	_ = st.FinishRun(context.WithoutCancel(c.ctx), runID, finish, result.Records, len(result.Warnings), cause, c.app.Now().UnixMilli())
	if err != nil {
		return err
	}
	for _, warning := range result.Warnings {
		fmt.Fprintln(c.app.Stderr, "dzen: warning:", warning)
	}
	if result.IncompleteComments > 0 {
		fmt.Fprintf(c.app.Stderr, "dzen: warning: %d articles have fewer comments than Dzen reports\n", result.IncompleteComments)
	}
	if result.Degraded() {
		return apperr.New("server_unavailable", "synced %d records, %d could not be archived", result.Records, len(result.Warnings))
	}
	if c.cfg.Embeddings.Enabled && result.Records > 0 {
		if err := embedArticles(c, st, false); err != nil {
			return err
		}
	}
	return c.emit("confirmed", map[string]any{"records": result.Records, "incomplete_comments": result.IncompleteComments}, func(w io.Writer) { fmt.Fprintf(w, "synced %d records\n", result.Records) })
}

func runCrawlStatus(c *runContext) error {
	st, err := c.archive()
	if err != nil {
		return err
	}
	defer st.Close()
	x, err := st.Status(c.ctx)
	if err != nil {
		return err
	}
	if c.jsonMode() {
		return writeJSON(c.app.Stdout, controlStatus(x))
	}
	fmt.Fprintf(c.app.Stdout, "channels=%d articles=%d comments=%d embeddings=%d last=%s\n", x.Channels, x.Articles, x.Comments, x.Embeddings, x.LastResult)
	return nil
}

func runCrawlSearch(c *runContext) error {
	if len(c.args) == 0 {
		return apperr.New("usage_error", "usage: dzen crawl search <query>")
	}
	limit, err := c.limit(20)
	if err != nil {
		return err
	}
	st, err := c.archive()
	if err != nil {
		return err
	}
	defer st.Close()
	rows, err := st.Search(c.ctx, strings.Join(c.args, " "), limit)
	if err != nil {
		return err
	}
	items := make([]map[string]any, 0, len(rows))
	for _, x := range rows {
		items = append(items, map[string]any{"id": x.ID, "title": x.Title, "url": x.URL, "snippet": x.Snippet})
	}
	return c.emit("confirmed", map[string]any{"items": items}, func(w io.Writer) {
		for _, x := range rows {
			fmt.Fprintf(w, "%s\t%s\t%s\n", x.URL, x.Title, x.Snippet)
		}
	})
}

func embedArticles(c *runContext, st *store.Store, force bool) error {
	provider, err := embed.NewProvider(c.cfg.EmbedConfig())
	if err != nil {
		return err
	}
	articles, err := st.Articles(c.ctx)
	if err != nil {
		return err
	}
	done := 0
	for _, a := range articles {
		sum := sha256.Sum256([]byte(c.cfg.Embeddings.Model + "\n" + a.Title + "\n" + a.Markdown))
		hash := hex.EncodeToString(sum[:])
		old, err := st.EmbeddingHash(c.ctx, a.ID)
		if err != nil {
			return err
		}
		if old == hash && !force {
			continue
		}
		batch, err := provider.Embed(c.ctx, []string{a.Title + "\n\n" + a.Markdown})
		if err != nil {
			return fmt.Errorf("embed %s: %w", a.ID, err)
		}
		if len(batch.Vectors) != 1 {
			return fmt.Errorf("embedding provider returned %d vectors", len(batch.Vectors))
		}
		if err := st.SaveEmbedding(c.ctx, a.ID, batch.Model, hash, batch.Vectors[0], c.app.Now().UnixMilli()); err != nil {
			return err
		}
		done++
	}
	fmt.Fprintf(c.app.Stderr, "embedded %d articles\n", done)
	return nil
}

func runCrawlEmbed(c *runContext) error {
	st, err := c.archive()
	if err != nil {
		return err
	}
	defer st.Close()
	return embedArticles(c, st, c.has("force"))
}

func runCrawlSemantic(c *runContext) error {
	if len(c.args) == 0 {
		return apperr.New("usage_error", "usage: dzen crawl semantic-search <query>")
	}
	provider, err := embed.NewProvider(c.cfg.EmbedConfig())
	if err != nil {
		return err
	}
	q, err := provider.Embed(c.ctx, []string{strings.Join(c.args, " ")})
	if err != nil {
		return err
	}
	if len(q.Vectors) != 1 {
		return fmt.Errorf("embedding provider returned %d vectors", len(q.Vectors))
	}
	st, err := c.archive()
	if err != nil {
		return err
	}
	defer st.Close()
	rows, err := st.Embeddings(c.ctx)
	if err != nil {
		return err
	}
	candidates := make([]vector.SearchCandidate[store.EmbeddingRow], 0, len(rows))
	for _, x := range rows {
		if x.Model == q.Model {
			candidates = append(candidates, vector.SearchCandidate[store.EmbeddingRow]{Item: x, Vector: x.Vector})
		}
	}
	results, err := vector.Search(c.ctx, q.Vectors[0], candidates, vector.SearchOptions[store.EmbeddingRow]{Limit: 20, InvalidVector: vector.InvalidVectorSkip, TieLess: func(a, b store.EmbeddingRow) bool { return a.ArticleID < b.ArticleID }})
	if err != nil {
		return err
	}
	for _, x := range results {
		fmt.Fprintf(c.app.Stdout, "%.4f\t%s\t%s\n", x.Score, x.Item.URL, x.Item.Title)
	}
	return nil
}

func runCrawlExport(c *runContext) error {
	st, err := c.archive()
	if err != nil {
		return err
	}
	defer st.Close()
	n, err := markdown.Export(c.ctx, st, c.cfg.MarkdownDir)
	if err != nil {
		return err
	}
	return c.emit("confirmed", map[string]any{"articles": n, "dir": c.cfg.MarkdownDir}, func(w io.Writer) { fmt.Fprintf(w, "exported %d articles to %s\n", n, c.cfg.MarkdownDir) })
}

func runCrawlSQL(c *runContext) error {
	if len(c.args) == 0 {
		return apperr.New("usage_error", "usage: dzen crawl sql <select>")
	}
	st, err := c.archive()
	if err != nil {
		return err
	}
	defer st.Close()
	rows, closer, err := st.ReadOnlySQL(c.ctx, strings.Join(c.args, " "))
	if err != nil {
		return apperr.New("usage_error", "query rejected: %v", err)
	}
	defer closer()
	defer rows.Close()
	cols, _ := rows.Columns()
	out := csv.NewWriter(c.app.Stdout)
	out.Comma = '\t'
	_ = out.Write(cols)
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
		_ = out.Write(record)
	}
	out.Flush()
	return rows.Err()
}

func runInit(c *runContext) error {
	path, err := config.WriteStarter(c.value("config"))
	if err != nil {
		return err
	}
	return c.emit("confirmed", map[string]any{"config": path}, func(w io.Writer) { fmt.Fprintln(w, path) })
}

func runDoctor(c *runContext) error {
	report := map[string]any{"config_db": c.cfg.DBPath, "markdown_dir": c.cfg.MarkdownDir, "sessions_dir": c.cfg.SessionsDir, "embeddings_enabled": c.cfg.Embeddings.Enabled}
	names, _ := c.sessions().List()
	report["accounts"] = len(names)
	if c.cfg.Embeddings.Enabled {
		report["embeddings"] = embed.CheckProvider(c.ctx, c.cfg.EmbedConfig())
	}
	if len(names) != 0 {
		if err := c.withAccount(func(acc *account) error {
			user, err := acc.client.User(c.ctx)
			if err != nil {
				return err
			}
			if _, err := acc.client.ListPublications(c.ctx, acc.sess.PublisherID, "published", 1); err != nil {
				return err
			}
			report["login"], report["contract"] = user.Login, "ok"
			return nil
		}); err != nil {
			report["contract"] = apperr.As(err).Code
		}
	}
	return c.emit("confirmed", report, func(w io.Writer) { _ = writeJSON(w, report) })
}

func runMetadata(c *runContext) error {
	cfg := config.Default()
	path, _ := config.DefaultPath()
	m := control.NewManifest("dzen", "Dzen", "dzen")
	m.Description = "Manage Dzen articles and comments; a local-first archive for search."
	m.Paths = control.Paths{DefaultConfig: path, ConfigEnv: "DZEN_CONFIG", DefaultDatabase: cfg.DBPath}
	m.Capabilities = []string{"metadata", "status", "doctor", "sync", "markdown", "search", "embeddings", "sql"}
	m.Privacy = control.Privacy{ExportsSecrets: false, LocalOnlyScopes: []string{"dzen", "sqlite"}}
	m.Commands = map[string]control.Command{"status": {Title: "Status", Argv: []string{"dzen", "crawl", "status", "--json"}, JSON: true}, "doctor": {Title: "Doctor", Argv: []string{"dzen", "doctor", "--json"}, JSON: true}, "sync": {Title: "Sync", Argv: []string{"dzen", "crawl", "sync", "--mine"}, Mutates: true}, "export-md": {Title: "Export Markdown", Argv: []string{"dzen", "crawl", "export-md"}, Mutates: true}}
	return writeJSON(c.app.Stdout, m)
}

func controlStatus(x store.Status) control.Status {
	counts := []control.Count{control.NewCount("channels", "Channels", int64(x.Channels)), control.NewCount("articles", "Articles", int64(x.Articles)), control.NewCount("comments", "Comments", int64(x.Comments)), control.NewCount("embeddings", "Embeddings", int64(x.Embeddings))}
	s := control.NewStatus("dzen", fmt.Sprintf("%d articles and %d comments", x.Articles, x.Comments))
	s.State = "current"
	if x.LastResult == "degraded" || x.LastResult == "failed" {
		s.State = "degraded"
	}
	s.DatabasePath = x.DBPath
	s.DatabaseBytes = x.DBBytes
	s.Counts = counts
	if x.LastSyncAt > 0 {
		s.LastSyncAt = time.UnixMilli(x.LastSyncAt).UTC().Format(time.RFC3339)
	}
	s.Databases = []control.Database{control.SQLiteDatabase("primary", "Dzen archive", "archive", x.DBPath, true, counts)}
	return s
}

func runCompletion(c *runContext) error {
	if err := c.need(1, "completion <bash|zsh|fish>"); err != nil {
		return err
	}
	seen := map[string]bool{}
	var words []string
	for _, item := range commands {
		word := strings.Fields(item.path)[0]
		if !seen[word] {
			seen[word] = true
			words = append(words, word)
		}
	}
	list := strings.Join(words, " ")
	switch c.args[0] {
	case "bash":
		fmt.Fprintf(c.app.Stdout, "complete -W %q dzen\n", list)
	case "zsh":
		fmt.Fprintf(c.app.Stdout, "#compdef dzen\ncompadd %s\n", list)
	case "fish":
		fmt.Fprintf(c.app.Stdout, "complete -c dzen -f -a %q\n", list)
	default:
		return apperr.New("usage_error", "unknown shell %q", c.args[0])
	}
	return nil
}
