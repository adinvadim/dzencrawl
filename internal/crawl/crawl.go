// Package crawl fills the local archive. It depends on read-only interfaces
// and cannot publish: nothing here imports the mutation code.
package crawl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/adinvadim/dzencrawl/internal/dzen"
	"github.com/adinvadim/dzencrawl/internal/store"
)

// Public reads public channel pages.
type Public interface {
	ChannelPage(ctx context.Context, channel string, page int) (dzen.ChannelPage, error)
	FetchArticle(ctx context.Context, link string) (string, error)
	AllComments(ctx context.Context, article dzen.Article, initial []dzen.Comment, cc dzen.CommentContext) ([]dzen.Comment, bool, error)
}

// Mine reads the signed-in author's publications.
type Mine interface {
	ListPublications(ctx context.Context, publisherID, state string, limit int) ([]dzen.Publication, error)
	GetPublication(ctx context.Context, publisherID, publicationID string) (dzen.Publication, error)
}

// Result summarizes a sync.
type Result struct {
	Records  int
	Warnings []string
}

// Degraded reports whether some items could not be archived.
func (r Result) Degraded() bool { return len(r.Warnings) != 0 }

type feedCheckpoint struct {
	Page     int  `json:"page"`
	Complete bool `json:"complete"`
}

// Options controls a public channel sync.
type Options struct {
	Channel     string
	MaxArticles int
	Refresh     bool
	MaxPages    int
	Now         func() time.Time
	Log         io.Writer
}

// SyncChannel archives a public channel, one bad item never stops the run.
func SyncChannel(ctx context.Context, st *store.Store, client Public, opts Options) (Result, error) {
	var result Result
	cursors := st.Checkpoints()
	startPage := 1
	if record, ok, err := cursors.Get(ctx, "public", "feed", opts.Channel); err != nil {
		return result, err
	} else if ok {
		var cp feedCheckpoint
		if json.Unmarshal([]byte(record.Value), &cp) == nil && !cp.Complete && cp.Page > 1 {
			startPage = cp.Page
		}
	}
	finished := false
	for page := startPage; ; page++ {
		if opts.MaxPages > 0 && page-startPage >= opts.MaxPages {
			break
		}
		feed, err := client.ChannelPage(ctx, opts.Channel, page)
		if err != nil {
			return result, fmt.Errorf("channel page %d: %w", page, err)
		}
		if len(feed.Items) == 0 {
			finished = true
			break
		}
		title := feed.Channel.Source.Title
		if title == "" {
			title = opts.Channel
		}
		if err := st.UpsertChannel(ctx, opts.Channel, title, "https://dzen.ru/"+opts.Channel, opts.Now().UnixMilli()); err != nil {
			return result, err
		}
		for _, item := range feed.Items {
			if opts.MaxArticles > 0 && result.Records >= opts.MaxArticles {
				break
			}
			if !dzen.IsArticleItem(item.Type) {
				continue
			}
			exists, err := st.HasCompleteArticle(ctx, item.PublicationObjectID, item.Link)
			if err != nil {
				return result, err
			}
			if exists && !opts.Refresh {
				continue
			}
			fmt.Fprintf(opts.Log, "fetch %s\n", item.Link)
			warning, err := archiveArticle(ctx, st, client, item.Link, opts)
			if err != nil {
				if ctx.Err() != nil {
					return result, ctx.Err()
				}
				result.Warnings = append(result.Warnings, item.Link+": "+err.Error())
				continue
			}
			if warning != "" {
				result.Warnings = append(result.Warnings, item.Link+": "+warning)
			}
			result.Records++
		}
		if opts.MaxArticles > 0 && result.Records >= opts.MaxArticles {
			break
		}
		done := feed.PageCount > 0 && page >= feed.PageCount
		if err := saveCheckpoint(ctx, cursors, opts.Channel, feedCheckpoint{Page: page + 1, Complete: done}); err != nil {
			return result, err
		}
		if done {
			finished = true
			break
		}
	}
	if finished {
		return result, saveCheckpoint(ctx, cursors, opts.Channel, feedCheckpoint{Page: 1, Complete: true})
	}
	return result, nil
}

type cursorSetter interface {
	Set(ctx context.Context, source, entityType, entityID, value string) error
}

func saveCheckpoint(ctx context.Context, cursors cursorSetter, channel string, cp feedCheckpoint) error {
	data, _ := json.Marshal(cp)
	return cursors.Set(ctx, "public", "feed", channel, string(data))
}

// archiveArticle returns a warning when the comments are only partly fetched.
func archiveArticle(ctx context.Context, st *store.Store, client Public, link string, opts Options) (string, error) {
	html, err := client.FetchArticle(ctx, link)
	if err != nil {
		return "", err
	}
	article, initial, cc, err := dzen.ParseArticle(html, link, opts.Channel)
	if err != nil {
		return "", err
	}
	comments, complete, commentErr := client.AllComments(ctx, article, initial, cc)
	article.CommentsComplete = complete && commentErr == nil
	if err := st.UpsertArticle(ctx, article, comments, opts.Now().UnixMilli()); err != nil {
		return "", err
	}
	fmt.Fprintf(opts.Log, "%s\t%s\tcomments=%d/%d\n", article.ID, article.Title, len(comments), article.CommentsCount)
	if commentErr != nil {
		return "comments are partial: " + commentErr.Error(), nil
	}
	return "", nil
}

// MineOptions controls a sync of the author's own publications.
type MineOptions struct {
	PublisherID string
	Now         func() time.Time
	Log         io.Writer
}

// SyncMine archives drafts, scheduled and published articles of the author.
func SyncMine(ctx context.Context, st *store.Store, client Mine, opts MineOptions) (Result, error) {
	var result Result
	if err := st.UpsertChannel(ctx, opts.PublisherID, opts.PublisherID, "https://dzen.ru/id/"+opts.PublisherID, opts.Now().UnixMilli()); err != nil {
		return result, err
	}
	for _, state := range []string{"draft", "pending", "published"} {
		items, err := client.ListPublications(ctx, opts.PublisherID, state, 0)
		if err != nil {
			return result, fmt.Errorf("list %s: %w", state, err)
		}
		for _, item := range items {
			full, err := client.GetPublication(ctx, opts.PublisherID, item.ID)
			if err != nil {
				if ctx.Err() != nil {
					return result, ctx.Err()
				}
				result.Warnings = append(result.Warnings, item.ID+": "+err.Error())
				continue
			}
			link := full.CommonURL
			if link == "" {
				link = "dzen://publication/" + full.ID
			}
			article := dzen.Article{
				ID: full.ID, PublisherID: full.PublisherID, Channel: opts.PublisherID, Title: full.Title, URL: link,
				Markdown: strings.TrimSpace(dzen.ContentMarkdown(full.ContentState, full.Images)), PublishedAt: full.PublishTime, ModifiedAt: full.ModTime,
				RawJSON: string(full.Raw), State: state, ProviderVersion: full.Version, ContentState: full.ContentState,
			}
			if err := st.UpsertArticle(ctx, article, nil, opts.Now().UnixMilli()); err != nil {
				return result, err
			}
			fmt.Fprintf(opts.Log, "%s\t%s\t%s\n", state, article.ID, article.Title)
			result.Records++
		}
	}
	return result, nil
}
