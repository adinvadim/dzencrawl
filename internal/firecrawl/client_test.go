package firecrawl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestScrape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/scrape" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer key" {
			t.Fatal("missing auth")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["url"] != "https://dzen.ru/a/test?output=1" {
			t.Fatalf("url=%v", body["url"])
		}
		if body["onlyMainContent"] != true {
			t.Fatalf("onlyMainContent=%v", body["onlyMainContent"])
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"markdown":"# Article","rawHtml":"<html>ok</html>","metadata":{"title":"Article"}}}`))
	}))
	defer server.Close()
	c := New(server.URL, "key", time.Second)
	got, err := c.Scrape(context.Background(), "https://dzen.ru/a/test")
	if err != nil {
		t.Fatal(err)
	}
	if got.Markdown != "# Article" || got.RawHTML != "<html>ok</html>" {
		t.Fatalf("got=%+v", got)
	}
}
func TestScrapeRequiresKey(t *testing.T) {
	c := New("http://example.invalid", "", time.Second)
	if _, err := c.Scrape(context.Background(), "https://dzen.ru/a/test"); err == nil {
		t.Fatal("expected error")
	}
}
