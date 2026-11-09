package markdown

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/adinvadim/dzencrawl/internal/dzen"
	"github.com/adinvadim/dzencrawl/internal/store"
)

var unsafe = regexp.MustCompile(`[^\pL\pN._-]+`)

func Export(ctx context.Context, st *store.Store, dir string) (int, error) {
	articles, err := st.Articles(ctx)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, err
	}
	for _, a := range articles {
		comments, err := st.Comments(ctx, a.ID)
		if err != nil {
			return 0, err
		}
		channelDir := filepath.Join(dir, slug(a.Channel))
		if err := os.MkdirAll(channelDir, 0o700); err != nil {
			return 0, err
		}
		name := slug(a.Title)
		if len([]rune(name)) > 80 {
			name = string([]rune(name)[:80])
		}
		path := filepath.Join(channelDir, name+"-"+short(a.ID)+".md")
		if err := os.WriteFile(path, []byte(render(a, comments)), 0o600); err != nil {
			return 0, err
		}
	}
	return len(articles), nil
}

func render(a dzen.Article, comments []dzen.Comment) string {
	var b strings.Builder
	fmt.Fprintf(&b, "---\nid: %s\nchannel: %s\nurl: %s\npublished_at: %s\nmodified_at: %s\nviews: %d\nviews_till_end: %d\ntime_to_read_seconds: %d\nlikes: %d\ncomments_count: %d\ncomments_saved: %d\ncomments_complete: %t\n---\n\n", a.ID, a.Channel, a.URL, date(a.PublishedAt), date(a.ModifiedAt), a.Views, a.ViewsTillEnd, a.TimeToReadSeconds, a.Likes, a.CommentsCount, len(comments), a.CommentsComplete)
	body := strings.TrimSpace(a.Markdown)
	if !strings.HasPrefix(body, "# ") {
		fmt.Fprintf(&b, "# %s\n\n", a.Title)
	}
	b.WriteString(body)
	b.WriteString("\n\n## Комментарии\n\n")
	if len(comments) == 0 {
		b.WriteString("_Комментариев нет или они недоступны._\n")
		return b.String()
	}
	for _, c := range comments {
		indent := ""
		if c.ParentID != "" {
			indent = "> "
		}
		name := c.AuthorName
		if name == "" {
			name = "Аноним"
		}
		fmt.Fprintf(&b, "%s**%s** · %s · 👍 %d · 👎 %d\n\n%s%s\n\n", indent, name, date(c.CreatedAt), c.Likes, c.Dislikes, indent, strings.ReplaceAll(c.Text, "\n", "\n"+indent))
	}
	return b.String()
}
func slug(s string) string {
	s = strings.Trim(unsafe.ReplaceAllString(strings.TrimSpace(s), "-"), "-.")
	if s == "" {
		return "untitled"
	}
	return s
}
func short(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
func date(ms int64) string {
	if ms == 0 {
		return ""
	}
	if ms < 1e12 {
		ms *= 1000
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}
