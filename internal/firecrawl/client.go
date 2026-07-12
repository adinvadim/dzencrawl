package firecrawl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

type Result struct {
	Markdown string
	RawHTML  string
	Metadata map[string]any
}

type scrapeResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Markdown string         `json:"markdown"`
		RawHTML  string         `json:"rawHtml"`
		Metadata map[string]any `json:"metadata"`
	} `json:"data"`
	Error string `json:"error"`
}

func New(baseURL, apiKey string, timeout time.Duration) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), APIKey: strings.TrimSpace(apiKey), HTTP: &http.Client{Timeout: timeout}}
}

// Scrape asks Firecrawl to render Dzen's public server view. output=1 avoids the
// optional SSO redirect while preserving the same public article.
func (c *Client) Scrape(ctx context.Context, rawURL string) (Result, error) {
	if c.APIKey == "" {
		return Result{}, fmt.Errorf("Firecrawl API key is empty")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return Result{}, err
	}
	q := u.Query()
	q.Set("output", "1")
	u.RawQuery = q.Encode()
	body := map[string]any{
		"url":             u.String(),
		"formats":         []string{"markdown", "rawHtml"},
		"onlyMainContent": true,
		"waitFor":         2500,
		"maxAge":          0,
	}
	payload, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/scrape", bytes.NewReader(payload))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("Firecrawl scrape: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return Result{}, err
	}
	if resp.StatusCode/100 != 2 {
		return Result{}, fmt.Errorf("Firecrawl HTTP %d: %s", resp.StatusCode, truncate(string(data), 500))
	}
	var decoded scrapeResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return Result{}, fmt.Errorf("decode Firecrawl response: %w", err)
	}
	if !decoded.Success {
		return Result{}, fmt.Errorf("Firecrawl scrape failed: %s", decoded.Error)
	}
	if strings.TrimSpace(decoded.Data.RawHTML) == "" {
		return Result{}, fmt.Errorf("Firecrawl returned no rawHtml")
	}
	return Result{Markdown: decoded.Data.Markdown, RawHTML: decoded.Data.RawHTML, Metadata: decoded.Data.Metadata}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
