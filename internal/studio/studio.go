// Package studio is the transport to Dzen's private author API. These are
// observed contracts, not provider promises: an unexpected response fails
// with contract_drift instead of a guess.
package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/adinvadim/dzencrawl/internal/apperr"
	"github.com/adinvadim/dzencrawl/internal/article"
	"github.com/adinvadim/dzencrawl/internal/dzen"
)

// Client talks to dzen.ru with one account's session.
type Client struct {
	HTTP      *http.Client
	Base      string
	Cookie    string
	UserAgent string
	Delay     time.Duration
	Sleep     func(ctx context.Context, d time.Duration) error

	mu   sync.Mutex
	last time.Time
	csrf string
}

// New builds a client. Redirects are never followed: a 302 means a lost session.
func New(cookie, userAgent string, delay time.Duration) *Client {
	return &Client{
		HTTP:      &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		Base:      "https://dzen.ru",
		Cookie:    cookie,
		UserAgent: userAgent,
		Delay:     delay,
		Sleep:     sleepContext,
	}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

func (c *Client) pace(ctx context.Context) error {
	c.mu.Lock()
	wait := c.Delay - time.Since(c.last)
	c.mu.Unlock()
	if wait > 0 {
		if err := c.Sleep(ctx, wait); err != nil {
			return err
		}
	}
	c.mu.Lock()
	c.last = time.Now()
	c.mu.Unlock()
	return nil
}

// Response is a completed HTTP exchange.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// Request describes one call.
type Request struct {
	Method      string
	Path        string
	Query       url.Values
	Body        []byte
	ContentType string
	Headers     map[string]string
	NoCSRF      bool
}

func (c *Client) build(ctx context.Context, r Request, csrf string) (*http.Request, error) {
	target := c.Base + r.Path
	if len(r.Query) > 0 {
		target += "?" + r.Query.Encode()
	}
	var body io.Reader
	if r.Body != nil {
		body = bytes.NewReader(r.Body)
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, target, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cookie", c.Cookie)
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", "https://dzen.ru")
	req.Header.Set("Referer", "https://dzen.ru/profile/editor")
	if r.ContentType != "" {
		req.Header.Set("Content-Type", r.ContentType)
	}
	if csrf != "" {
		req.Header.Set("X-Csrf-Token", csrf)
	}
	for k, v := range r.Headers {
		req.Header.Set(k, v)
	}
	return req, nil
}

// Get performs a read, retrying transient failures. Only reads are retried.
func (c *Client) Get(ctx context.Context, r Request) (*Response, error) {
	r.Method = http.MethodGet
	var lastErr error
	refreshed := false
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			if err := c.Sleep(ctx, time.Duration(attempt)*time.Second); err != nil {
				return nil, err
			}
		}
		if err := c.pace(ctx); err != nil {
			return nil, err
		}
		csrf := ""
		if !r.NoCSRF {
			var err error
			if csrf, err = c.CSRF(ctx, false); err != nil {
				return nil, err
			}
		}
		req, err := c.build(ctx, r, csrf)
		if err != nil {
			return nil, err
		}
		resp, err := c.exchange(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = apperr.New("network_before_dispatch", "Dzen is unreachable: %v", errors.Unwrap(err))
			continue
		}
		if resp.Status == 429 || resp.Status >= 500 {
			lastErr = Classify(resp)
			continue
		}
		if !r.NoCSRF && apperr.Is(Classify(resp), "csrf_rejected") && !refreshed {
			refreshed = true
			if _, err := c.CSRF(ctx, true); err != nil {
				return nil, err
			}
			attempt--
			continue
		}
		return resp, nil
	}
	return nil, lastErr
}

func (c *Client) exchange(req *http.Request) (*Response, error) {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	return &Response{Status: resp.StatusCode, Header: resp.Header, Body: data}, nil
}

// Send performs a mutation exactly once. dispatched reports whether the
// request may have reached Dzen, which forbids a blind retry.
func (c *Client) Send(ctx context.Context, r Request, csrf string) (resp *Response, dispatched bool, err error) {
	if err := c.pace(ctx); err != nil {
		return nil, false, err
	}
	var wrote bool
	var mu sync.Mutex
	trace := &httptrace.ClientTrace{WroteHeaders: func() {
		mu.Lock()
		wrote = true
		mu.Unlock()
	}}
	req, err := c.build(httptrace.WithClientTrace(ctx, trace), r, csrf)
	if err != nil {
		return nil, false, err
	}
	resp, err = c.exchange(req)
	mu.Lock()
	defer mu.Unlock()
	return resp, wrote, err
}

// CSRF returns a token, fetching a fresh one when forced or absent.
func (c *Client) CSRF(ctx context.Context, fresh bool) (string, error) {
	c.mu.Lock()
	token := c.csrf
	c.mu.Unlock()
	if token != "" && !fresh {
		return token, nil
	}
	resp, err := c.Get(ctx, Request{Path: "/media-api/csrf-token", NoCSRF: true})
	if err != nil {
		return "", err
	}
	if err := Classify(resp); err != nil {
		return "", err
	}
	var out struct {
		Result string `json:"result"`
	}
	if json.Unmarshal(resp.Body, &out) != nil || out.Result == "" {
		return "", apperr.New("contract_drift", "csrf-token response changed shape")
	}
	c.mu.Lock()
	c.csrf = out.Result
	c.mu.Unlock()
	return out.Result, nil
}

type errorBody struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Errors  []struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"errors"`
}

// ErrorType returns the provider error type from a response body.
func ErrorType(body []byte) (string, string) {
	var e errorBody
	if json.Unmarshal(body, &e) != nil {
		return "", ""
	}
	if len(e.Errors) > 0 {
		return e.Errors[0].Type, e.Errors[0].Message
	}
	return e.Type, e.Message
}

// Classify maps a response to a stable error, or nil for success.
func Classify(r *Response) error {
	if r.Status/100 == 2 {
		return nil
	}
	if r.Status/100 == 3 {
		return apperr.New("session_expired", "Dzen redirected to sign-in; the session has ended")
	}
	kind, message := ErrorType(r.Body)
	html := strings.Contains(r.Header.Get("Content-Type"), "text/html")
	switch {
	case kind == "invalid-csrf-token-error":
		return apperr.New("csrf_rejected", "Dzen rejected the CSRF token")
	case kind == "captcha-required-error":
		return apperr.New("captcha_required", "Dzen asks for a captcha")
	case r.Status == 401:
		return apperr.New("session_expired", "the Dzen session has ended; run: dzen auth import")
	case r.Status == 403:
		return apperr.New("forbidden", "this account may not do that")
	case r.Status == 404 && html:
		return apperr.New("contract_drift", "the Dzen endpoint no longer exists")
	case r.Status == 404:
		return apperr.New("not_found", "Dzen does not know this object")
	case r.Status == 429:
		return apperr.New("rate_limited", "Dzen is rate limiting this account")
	case kind == "validation-error" || r.Status == 400:
		return apperr.New("remote_validation", "Dzen rejected the request (%s)", first(kind, strconv.Itoa(r.Status)))
	case kind == "internal-error":
		e := apperr.New("remote_validation", "Dzen could not apply the request (internal-error)")
		e.Detail = trim(message)
		return e
	}
	return apperr.New("server_unavailable", "Dzen answered HTTP %d", r.Status)
}

func first(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func trim(s string) string {
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

func decode(r *Response, out any) error {
	if err := Classify(r); err != nil {
		return err
	}
	if err := json.Unmarshal(r.Body, out); err != nil {
		return apperr.New("contract_drift", "Dzen response changed shape")
	}
	return nil
}

// User is the signed-in principal.
type User struct {
	UID         int64
	Login       string
	PublisherID string
}

// User verifies the session and identifies its account.
func (c *Client) User(ctx context.Context) (User, error) {
	resp, err := c.Get(ctx, Request{Path: "/editor-api/v2/user"})
	if err != nil {
		return User{}, err
	}
	var out struct {
		User struct {
			UID         int64  `json:"uid"`
			Login       string `json:"login"`
			PublisherID string `json:"publisherId"`
		} `json:"user"`
		Publisher struct {
			ID string `json:"id"`
		} `json:"publisher"`
	}
	if err := decode(resp, &out); err != nil {
		return User{}, err
	}
	pub := first(out.User.PublisherID, out.Publisher.ID)
	if out.User.Login == "" || pub == "" {
		return User{}, apperr.New("contract_drift", "user response has no login or publisher")
	}
	return User{UID: out.User.UID, Login: out.User.Login, PublisherID: pub}, nil
}

func decodeImages(raw json.RawMessage) article.Images {
	images := article.Images{}
	if len(raw) == 0 {
		return images
	}
	if json.Unmarshal(raw, &images) == nil {
		return images
	}
	var list []article.ImageInfo
	if json.Unmarshal(raw, &list) == nil {
		for _, item := range list {
			images[item.ID] = item
		}
	}
	return images
}

// GetPublication reads one publication with its full body and image directory.
func (c *Client) GetPublication(ctx context.Context, publisherID, publicationID string) (dzen.Publication, error) {
	resp, err := c.Get(ctx, Request{Path: "/editor-api/v2/publisher/" + url.PathEscape(publisherID) + "/publication/" + url.PathEscape(publicationID)})
	if err != nil {
		return dzen.Publication{}, err
	}
	var out struct {
		Publications []json.RawMessage `json:"publications"`
		Images       json.RawMessage   `json:"images"`
	}
	if err := decode(resp, &out); err != nil {
		return dzen.Publication{}, err
	}
	if len(out.Publications) != 1 {
		return dzen.Publication{}, apperr.New("not_found", "publication %s was not found", publicationID)
	}
	p, err := dzen.DecodePublication(out.Publications[0], decodeImages(out.Images))
	if err != nil {
		return p, apperr.New("contract_drift", "publication response changed shape")
	}
	if p.ID == "" || p.Type == "" {
		return p, apperr.New("contract_drift", "publication response has no id or type")
	}
	return p, nil
}

// ListPublications lists draft, pending or published items, newest first.
func (c *Client) ListPublications(ctx context.Context, publisherID, state string, limit int) ([]dzen.Publication, error) {
	seen := map[string]bool{}
	var out []dzen.Publication
	after := ""
	for {
		pageSize := min(limit-len(out), 100)
		if pageSize <= 0 {
			return out, nil
		}
		query := url.Values{"publisherId": {publisherID}, "state": {state}, "pageSize": {strconv.Itoa(pageSize)}, "view": {"list"}}
		if after != "" {
			query.Set("publicationIdAfter", after)
		}
		resp, err := c.Get(ctx, Request{Path: "/editor-api/v3/publications", Query: query})
		if err != nil {
			return nil, err
		}
		var page struct {
			Drafts       []json.RawMessage `json:"drafts"`
			Publications []json.RawMessage `json:"publications"`
		}
		if err := decode(resp, &page); err != nil {
			return nil, err
		}
		added := 0
		for _, raw := range append(page.Drafts, page.Publications...) {
			p, err := dzen.DecodePublication(raw, nil)
			if err != nil {
				return nil, apperr.New("contract_drift", "publication list changed shape")
			}
			if p.ID == "" || seen[p.ID] {
				continue
			}
			if p.Status == "" {
				p.Status = state
			}
			seen[p.ID] = true
			out = append(out, p)
			after = p.ID
			added++
		}
		if added < pageSize {
			return out, nil
		}
	}
}

// CommentHeaders are sent with every comments API call.
func CommentHeaders() map[string]string {
	return map[string]string{"X-Zen-Comments-Clid": "228", "X-Zen-Comments-Clid-V2": "1400", "X-Zen-Comments-Partner": "yandex", "X-Zen-Comments-Integration": "publishers_platform_yandex", "X-Zen-Comments-Product": "publishers_platform", "X-Zen-Comments-Place": "publishers_platform", "X-Zen-Comments-Page-Type-V2": "article_page"}
}

// CommentPage is one page of comment results.
type CommentPage struct {
	Status string          `json:"status"`
	Raw    json.RawMessage `json:"-"`
	Next   string          `json:"moreItemsQueryString"`
}

// RootComments reads a page of comments under a publication.
func (c *Client) RootComments(ctx context.Context, publisherID, publicationID, sorting string, limit int, cursor string) (json.RawMessage, string, error) {
	query := url.Values{"documentId": {"native:" + publicationID}, "publicationPublisherId": {publisherID}, "publisherId": {publisherID}, "sorting": {sorting}, "batchSize": {strconv.Itoa(limit)}, "withConfig": {"false"}, "withCurrentUser": {"true"}, "rnd": {strconv.FormatInt(time.Now().UnixMilli(), 10)}}
	if cursor != "" {
		next, err := url.ParseQuery(cursor)
		if err != nil {
			return nil, "", apperr.New("usage_error", "invalid cursor")
		}
		for k, v := range next {
			query[k] = v
		}
	}
	resp, err := c.Get(ctx, Request{Path: "/api/comments/v2/root-comments", Query: query, Headers: CommentHeaders()})
	if err != nil {
		return nil, "", err
	}
	var page CommentPage
	if err := decode(resp, &page); err != nil {
		return nil, "", err
	}
	if page.Status != "ok" {
		return nil, "", apperr.New("contract_drift", "comments answered status %q", page.Status)
	}
	return resp.Body, page.Next, nil
}

// ChildComments reads the replies under a root comment.
func (c *Client) ChildComments(ctx context.Context, publisherID, publicationID, rootID string) (json.RawMessage, error) {
	query := url.Values{"documentId": {"native:" + publicationID}, "publicationPublisherId": {publisherID}, "rootCommentId": {rootID}, "withConfig": {"false"}, "withCurrentUser": {"true"}, "rnd": {strconv.FormatInt(time.Now().UnixMilli(), 10)}}
	resp, err := c.Get(ctx, Request{Path: "/api/comments/v2/child-comments", Query: query, Headers: CommentHeaders()})
	if err != nil {
		return nil, err
	}
	if err := Classify(resp); err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// ChannelComments reads the studio's channel-wide comment feed.
func (c *Client) ChannelComments(ctx context.Context, publisherID string) (json.RawMessage, error) {
	resp, err := c.Get(ctx, Request{Path: "/editor-api/v2/publisher/" + url.PathEscape(publisherID) + "/comments"})
	if err != nil {
		return nil, err
	}
	if err := Classify(resp); err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// BannedUsers reads the channel's block list.
func (c *Client) BannedUsers(ctx context.Context, publisherID string) (json.RawMessage, error) {
	resp, err := c.Get(ctx, Request{Path: "/api/comments/banned-users", Query: url.Values{"publisherId": {publisherID}}})
	if err != nil {
		return nil, err
	}
	if err := Classify(resp); err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// Download fetches a binary resource, for captcha images.
func (c *Client) Download(ctx context.Context, target string) ([]byte, error) {
	if strings.HasPrefix(target, "/") {
		target = c.Base + target
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	if strings.HasPrefix(target, c.Base) {
		req.Header.Set("Cookie", c.Cookie)
	}
	resp, err := c.exchange(req)
	if err != nil {
		return nil, err
	}
	if resp.Status/100 != 2 {
		return nil, fmt.Errorf("download failed: HTTP %d", resp.Status)
	}
	return resp.Body, nil
}

// MultipartImage encodes a file for add-image.
func MultipartImage(filename string, data []byte) ([]byte, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("image", filename)
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(data); err != nil {
		return nil, "", err
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}
