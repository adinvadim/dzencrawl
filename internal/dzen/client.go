package dzen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/adinvadim/dzencrawl/internal/apperr"
)

type Client struct {
	HTTP      *http.Client
	UserAgent string
	Delay     time.Duration
}

func NewClient(userAgent string, delay time.Duration) *Client {
	return &Client{HTTP: &http.Client{Timeout: 45 * time.Second}, UserAgent: userAgent, Delay: delay}
}

// FetchArticle downloads a public article page as server-rendered HTML.
func (c *Client) FetchArticle(ctx context.Context, link string) (string, error) {
	if err := sleep(ctx, c.Delay); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, addOutput(link), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("Dzen HTTP %d for article page", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	return string(data), err
}

// Feed items of these types are not articles and have no publication SSR.
func IsArticleItem(kind string) bool { return kind != "gif" && kind != "short_video" }

func (c *Client) ChannelPage(ctx context.Context, channel string, page int) (ChannelPage, error) {
	if page > 1 {
		if err := sleep(ctx, c.Delay); err != nil {
			return ChannelPage{}, err
		}
	}
	u := "https://dzen.ru/api/v3/launcher/more?channel_name=" + url.QueryEscape(channel) + "&page=" + strconv.Itoa(page)
	var out ChannelPage
	if err := c.getJSON(ctx, u, "", nil, &out); err != nil {
		return out, err
	}
	return out, nil
}

func (c *Client) AllComments(ctx context.Context, article Article, initial []Comment, cc CommentContext) ([]Comment, bool, error) {
	if cc.DocumentID == "" {
		return initial, article.CommentsCountKnown && article.CommentsCount == 0, nil
	}
	if article.CommentsCountKnown && article.CommentsCount == 0 {
		return initial, true, nil
	}
	params := url.Values{
		"publicationPublisherId": {cc.PublisherID}, "documentId": {cc.DocumentID}, "sorting": {"newest"},
		"batchSize": {"50"}, "withConfig": {"false"}, "withCurrentUser": {"false"},
		"subscriptionStateFor": {"none"}, "updateDefaultSorting": {"false"}, "sessionTs": {strconv.FormatInt(cc.ServerTS, 10)},
		"clientTs": {strconv.FormatInt(time.Now().UnixMilli(), 10)}, "rid": {cc.RID},
	}
	rootPages, err := c.commentPages(ctx, article.URL, "root-comments", params)
	if err != nil {
		var partial []Comment
		for _, roots := range rootPages {
			parsed, _, _ := parseCommentResponse(roots.mapValue(), article.ID, "")
			partial = append(partial, parsed...)
		}
		return dedupeComments(append(partial, initial...)), false, err
	}
	var all []Comment
	users := map[string]string{}
	meta := map[string]commentMeta{}
	for _, roots := range rootPages {
		parsed, pageUsers, pageMeta := parseCommentResponse(roots.mapValue(), article.ID, "")
		all = append(all, parsed...)
		mergeNames(users, pageUsers)
		for id, value := range pageMeta {
			meta[id] = value
		}
	}
	mergeNames(users, cc.Users)
	for id, m := range meta {
		if m.Children <= 0 {
			continue
		}
		childParams := cloneValues(params)
		childParams.Del("sorting")
		childParams.Del("batchSize")
		childParams.Del("subscriptionStateFor")
		childParams.Del("updateDefaultSorting")
		childParams.Set("rootCommentId", id)
		childPages, err := c.commentPages(ctx, article.URL, "child-comments", childParams)
		if err != nil {
			return dedupeComments(all), false, err
		}
		for _, children := range childPages {
			parsed, childUsers, _ := parseCommentResponse(children.mapValue(), article.ID, id)
			mergeNames(users, childUsers)
			all = append(all, parsed...)
		}
	}
	all = dedupeComments(all)
	for i := range all {
		all[i].AuthorName = users[all[i].AuthorID]
	}
	return all, article.CommentsCountKnown && int64(len(all)) >= article.CommentsCount, nil
}

func (c *Client) commentPages(ctx context.Context, referer, endpoint string, params url.Values) ([]commentResponse, error) {
	current := cloneValues(params)
	var pages []commentResponse
	for page := 0; page < 1000; page++ {
		var response commentResponse
		if err := c.commentsGET(ctx, referer, endpoint, current, &response); err != nil {
			return pages, err
		}
		if response.Status != "ok" {
			return pages, fmt.Errorf("Dzen comments returned status %q", response.Status)
		}
		pages = append(pages, response)
		if strings.TrimSpace(response.MoreItemsQueryString) == "" {
			return pages, nil
		}
		next, err := url.ParseQuery(response.MoreItemsQueryString)
		if err != nil {
			return pages, fmt.Errorf("decode comments cursor: %w", err)
		}
		for key, values := range next {
			current[key] = values
		}
	}
	return pages, errors.New("comments pagination exceeded 1000 pages")
}

func (c *Client) commentsGET(ctx context.Context, referer, endpoint string, params url.Values, out any) error {
	if err := sleep(ctx, c.Delay); err != nil {
		return err
	}
	u := "https://dzen.ru/api/comments/v2/" + endpoint + "?" + params.Encode()
	headers := map[string]string{
		"Accept": "application/json", "Referer": addOutput(referer), "X-Zen-Comments-Clid": "228", "X-Zen-Comments-Clid-V2": "1400",
		"X-Zen-Comments-Partner": "yandex", "X-Zen-Comments-Integration": "publishers_platform_yandex", "X-Zen-Comments-Product": "publishers_platform",
		"X-Zen-Comments-Place": "publishers_platform", "X-Zen-Comments-Page-Type-V2": "article_page",
	}
	return c.getJSON(ctx, u, referer, headers, out)
}

func (c *Client) getJSON(ctx context.Context, u, referer string, headers map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		if resp.StatusCode == http.StatusNotFound {
			return apperr.New("not_found", "Dzen does not know this channel or article")
		}
		return fmt.Errorf("Dzen HTTP %d", resp.StatusCode)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode Dzen JSON: %w", err)
	}
	return nil
}

type commentResponse struct {
	Status               string         `json:"status"`
	ServerTS             int64          `json:"serverTs"`
	UsersByID            map[string]any `json:"usersById"`
	Meta                 map[string]any `json:"meta"`
	MetaByCommentID      map[string]any `json:"metaByCommentId"`
	SubthreadByCommentID map[string]any `json:"subthreadByCommentId"`
	Items                []any          `json:"items"`
	MoreItemsQueryString string         `json:"moreItemsQueryString"`
}

func (r commentResponse) mapValue() map[string]any {
	return map[string]any{"status": r.Status, "serverTs": r.ServerTS, "usersById": r.UsersByID, "meta": r.Meta, "metaByCommentId": r.MetaByCommentID, "subthreadByCommentId": r.SubthreadByCommentID, "items": r.Items, "moreItemsQueryString": r.MoreItemsQueryString}
}
func cloneValues(v url.Values) url.Values {
	out := url.Values{}
	for k, values := range v {
		out[k] = append([]string(nil), values...)
	}
	return out
}
func addOutput(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	q.Set("output", "1")
	u.RawQuery = q.Encode()
	return u.String()
}
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
func mergeNames(dst, src map[string]string) {
	for k, v := range src {
		if dst[k] == "" {
			dst[k] = v
		}
	}
}
func dedupeComments(in []Comment) []Comment {
	seen := map[string]bool{}
	out := make([]Comment, 0, len(in))
	for _, x := range in {
		if x.ID == "" || seen[x.ID] {
			continue
		}
		seen[x.ID] = true
		out = append(out, x)
	}
	return out
}
func ChannelName(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		trimmed := strings.Trim(strings.TrimSpace(raw), "/")
		if strings.HasPrefix(trimmed, "dzen.ru/") || strings.HasPrefix(trimmed, "www.dzen.ru/") {
			return ChannelName("https://" + trimmed)
		}
		if strings.HasPrefix(trimmed, "id/") {
			return "", fmt.Errorf("channels addressed by id are not supported by the public feed; use the nickname URL")
		}
		return trimmed, nil
	}
	if u.Host != "dzen.ru" && u.Host != "www.dzen.ru" {
		return "", fmt.Errorf("expected dzen.ru channel URL")
	}
	p := strings.Trim(u.Path, "/")
	if strings.HasPrefix(p, "id/") {
		return "", fmt.Errorf("channels addressed by id are not supported by the public feed; use the nickname URL")
	}
	if p == "" || strings.Contains(p, "/") {
		return "", fmt.Errorf("expected channel URL like https://dzen.ru/example_channel")
	}
	return p, nil
}
