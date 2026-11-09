package captcha

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adinvadim/dzencrawl/internal/apperr"
)

func client(url string) *Client {
	return &Client{HTTP: http.DefaultClient, Base: url, Poll: time.Millisecond, Deadline: time.Second, Key: func(context.Context) (string, error) { return "KEY", nil }}
}

func TestSolveImageCreatesTaskAndPollsUntilReady(t *testing.T) {
	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["clientKey"] != "KEY" {
			t.Errorf("key missing: %v", body)
		}
		switch r.URL.Path {
		case "/createTask":
			task := body["task"].(map[string]any)
			if task["type"] != "ImageToTextTask" || task["body"] != "aW1n" {
				t.Errorf("task %v", task)
			}
			_, _ = w.Write([]byte(`{"errorId":0,"taskId":7}`))
		case "/getTaskResult":
			if polls.Add(1) < 2 {
				_, _ = w.Write([]byte(`{"errorId":0,"status":"processing"}`))
				return
			}
			_, _ = w.Write([]byte(`{"errorId":0,"status":"ready","solution":{"text":"abc"}}`))
		}
	}))
	defer srv.Close()
	got, err := client(srv.URL).SolveImage(context.Background(), []byte("img"))
	if err != nil || got != "abc" {
		t.Fatal(got, err)
	}
}

func TestSolveVKSendsTheLinkAndProxyAndReturnsTheToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path == "/createTask" {
			task := body["task"].(map[string]any)
			if task["type"] != "VKCaptchaTask" || task["redirectUri"] != "https://id.vk.ru/not_robot_captcha?x=1" || task["userAgent"] != "UA" || task["proxyType"] != "socks5" || task["proxyAddress"] != "p.example" || task["proxyPort"] != float64(1080) || task["proxyLogin"] != "u" || task["proxyPassword"] != "secret" {
				t.Errorf("task %v", task)
			}
			_, _ = w.Write([]byte(`{"errorId":0,"taskId":7}`))
			return
		}
		_, _ = w.Write([]byte(`{"errorId":0,"status":"ready","solution":{"token":"tok"}}`))
	}))
	defer srv.Close()
	c := client(srv.URL)
	c.Proxy = func(context.Context) (string, error) { return "socks5://u:secret@p.example:1080", nil }
	got, err := c.SolveVK(context.Background(), "https://id.vk.ru/not_robot_captcha?x=1", "UA")
	if err != nil || got != "tok" {
		t.Fatal(got, err)
	}
}

func TestSolveVKWithoutAProxyIsUnsupportedBeforeSpending(t *testing.T) {
	c := client("http://127.0.0.1:1")
	c.Proxy = func(context.Context) (string, error) { return "", nil }
	if _, err := c.SolveVK(context.Background(), "link", "UA"); !apperr.Is(err, "captcha_unsupported") {
		t.Fatal(err)
	}
}

func TestSolveImageReportsProviderRejection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errorId":10,"errorCode":"ERROR_ZERO_BALANCE"}`))
	}))
	defer srv.Close()
	_, err := client(srv.URL).SolveImage(context.Background(), []byte("img"))
	if !apperr.Is(err, "captcha_failed") || !strings.Contains(err.Error(), "ZERO_BALANCE") {
		t.Fatal(err)
	}
}

func TestSolveImageStopsAtDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/createTask" {
			_, _ = w.Write([]byte(`{"errorId":0,"taskId":7}`))
			return
		}
		_, _ = w.Write([]byte(`{"errorId":0,"status":"processing"}`))
	}))
	defer srv.Close()
	c := client(srv.URL)
	c.Deadline = 20 * time.Millisecond
	if _, err := c.SolveImage(context.Background(), []byte("img")); !apperr.Is(err, "captcha_failed") {
		t.Fatal(err)
	}
}

func TestResolveKeyFromEnvironmentAndProcess(t *testing.T) {
	t.Setenv("TWOCAPTCHA_API_KEY", "from-env")
	if got, err := ResolveKey(context.Background(), nil); err != nil || got != "from-env" {
		t.Fatal(got, err)
	}
	t.Setenv("TWOCAPTCHA_API_KEY", "")
	got, err := ResolveKey(context.Background(), []string{"sh", "-c", `printf '{"version":1,"secret":"s3"}'`})
	if err != nil || got != "s3" {
		t.Fatal(got, err)
	}
	if _, err := ResolveKey(context.Background(), []string{"sh", "-c", `printf '{"version":9,"secret":"s3"}'`}); !apperr.Is(err, "captcha_failed") {
		t.Fatal(err)
	}
}
