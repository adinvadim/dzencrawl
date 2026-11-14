package studio

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/adinvadim/dzencrawl/internal/apperr"
)

func TestClassifyMapsProviderResponsesToStableCodes(t *testing.T) {
	jsonHeader := http.Header{"Content-Type": {"application/json"}}
	htmlHeader := http.Header{"Content-Type": {"text/html"}}
	cases := []struct {
		status int
		header http.Header
		body   string
		code   string
	}{
		{302, jsonHeader, ``, "session_expired"},
		{401, jsonHeader, `{}`, "session_expired"},
		{403, jsonHeader, `{}`, "forbidden"},
		{404, jsonHeader, `{}`, "not_found"},
		{404, htmlHeader, `<html>`, "contract_drift"},
		{429, jsonHeader, `{}`, "rate_limited"},
		{400, jsonHeader, `{"errors":[{"type":"invalid-csrf-token-error"}]}`, "csrf_rejected"},
		{400, jsonHeader, `{"type":"captcha-required-error"}`, "captcha_required"},
		{400, jsonHeader, `{"errors":[{"type":"validation-error"}]}`, "remote_validation"},
		{500, jsonHeader, `{"errors":[{"type":"internal-error","message":"x"}]}`, "remote_validation"},
		{503, jsonHeader, `upstream`, "server_unavailable"},
	}
	for _, tc := range cases {
		err := Classify(&Response{Status: tc.status, Header: tc.header, Body: []byte(tc.body)})
		if !apperr.Is(err, tc.code) {
			t.Errorf("%d %s: got %v, want %s", tc.status, tc.body, err, tc.code)
		}
	}
	if Classify(&Response{Status: 200}) != nil {
		t.Fatal("2xx must be nil")
	}
}

func client(url string) *Client {
	c := New("Session_id=secret", "UA", 0)
	c.Base = url
	c.Sleep = func(context.Context, time.Duration) error { return nil }
	return c
}

func TestGetRetriesTransientFailuresOnly(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/media-api/csrf-token" {
			fmt.Fprint(w, `{"result":"t"}`)
			return
		}
		calls++
		if calls < 3 {
			w.WriteHeader(503)
			return
		}
		fmt.Fprint(w, `{"user":{"login":"a","publisherId":"p"}}`)
	}))
	defer srv.Close()
	user, err := client(srv.URL).User(context.Background())
	if err != nil || user.Login != "a" || calls != 3 {
		t.Fatal(user, err, calls)
	}
}

func TestUserWithoutIdentityIsContractDrift(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"result":"t","something":"else"}`)
	}))
	defer srv.Close()
	if _, err := client(srv.URL).User(context.Background()); !apperr.Is(err, "contract_drift") {
		t.Fatal(err)
	}
}

func TestListPublicationsMergesKeysAndStopsOnShortPage(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/media-api/csrf-token" {
			fmt.Fprint(w, `{"result":"t"}`)
			return
		}
		queries = append(queries, r.URL.RawQuery)
		item := func(id string) string {
			return fmt.Sprintf(`{"id":%q,"content":{"type":"article"}}`, id)
		}
		if strings.Contains(r.URL.RawQuery, "publicationIdAfter") {
			fmt.Fprintf(w, `{"drafts":[%s],"publications":[]}`, item("c"))
			return
		}
		fmt.Fprintf(w, `{"drafts":[%s],"publications":[%s,%s]}`, item("a"), item("a"), item("b"))
	}))
	defer srv.Close()
	got, err := client(srv.URL).ListPublications(context.Background(), "p", "pending", 2)
	if err != nil || len(got) != 2 || got[0].Status != "pending" {
		t.Fatalf("%+v %v", got, err)
	}
	if len(queries) != 1 {
		t.Fatalf("%v", queries)
	}
}

func TestListPublicationsTakesStatusTimeAndAddressFromThePublicationsCopy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/media-api/csrf-token" {
			fmt.Fprint(w, `{"result":"t"}`)
			return
		}
		fmt.Fprint(w, `{"drafts":[`+
			`{"id":"a","draftStatus":"no-changes","content":{"type":"article"}},`+
			`{"id":"b","draftStatus":"no-changes","content":{"type":"article"}},`+
			`{"id":"c","draftStatus":"no-changes","content":{"type":"video"}}],`+
			`"publications":[`+
			`{"id":"a","status":"published","publishTime":1794316146213,"commonUrl":"/a/avL1O-0yqTgY37kq","content":{"type":"article"}},`+
			`{"id":"b","status":"banned","publishTime":1585391847861,"commonUrl":"/a/Xnkyvdk5fnG2a2zK","content":{"type":"article"}},`+
			`{"id":"c","status":"published","publishTime":1794000000000,"commonUrl":"/video/watch/x","content":{"type":"video"}}]}`)
	}))
	defer srv.Close()
	got, err := client(srv.URL).ListPublications(context.Background(), "p", "published", 10)
	if err != nil || len(got) != 3 {
		t.Fatalf("%+v %v", got, err)
	}
	want := []struct{ status, url, typ string }{{"published", "/a/avL1O-0yqTgY37kq", "article"}, {"banned", "/a/Xnkyvdk5fnG2a2zK", "article"}, {"published", "/video/watch/x", "video"}}
	for i, w := range want {
		p := got[i]
		if p.Status != w.status || p.CommonURL != w.url || p.Type != w.typ || p.PublishTime == 0 || p.DraftStatus != "no-changes" {
			t.Fatalf("%d: %+v", i, p)
		}
	}
}

func TestMutationsDoNotFollowRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	defer srv.Close()
	resp, dispatched, err := client(srv.URL).Send(context.Background(), Request{Method: http.MethodPost, Path: "/x", Body: []byte(`{}`)}, "t")
	if err != nil || !dispatched || resp.Status != http.StatusFound {
		t.Fatal(resp, dispatched, err)
	}
	if !apperr.Is(Classify(resp), "session_expired") {
		t.Fatal("redirect must mean a lost session")
	}
}

func TestSendIsNotDispatchedWhenTheServerIsDown(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	_, dispatched, err := client(url).Send(context.Background(), Request{Method: http.MethodPost, Path: "/x", Body: []byte(`{}`)}, "t")
	if err == nil || dispatched {
		t.Fatal(dispatched, err)
	}
}

func TestMultipartImageUsesImageField(t *testing.T) {
	body, contentType, err := MultipartImage("a.png", []byte("bytes"))
	if err != nil || !strings.HasPrefix(contentType, "multipart/form-data") || !strings.Contains(string(body), `name="image"`) {
		t.Fatal(string(body), contentType, err)
	}
}

func TestDownloadSendsCookiesOnlyToTheAccountsOwnHost(t *testing.T) {
	var cookies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookies = append(cookies, r.Header.Get("Cookie"))
		fmt.Fprint(w, "img")
	}))
	defer srv.Close()
	c := client(srv.URL)
	if _, err := c.Download(context.Background(), srv.URL+"/own.png"); err != nil {
		t.Fatal(err)
	}
	lookalike := New("Session_id=secret", "UA", 0)
	lookalike.Base = strings.TrimSuffix(srv.URL, srv.URL[strings.LastIndex(srv.URL, ":"):])
	if _, err := lookalike.Download(context.Background(), srv.URL+"/other.png"); err != nil {
		t.Fatal(err)
	}
	if cookies[0] == "" || cookies[1] != "" {
		t.Fatalf("%q", cookies)
	}
}
