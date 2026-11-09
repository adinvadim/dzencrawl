package mutate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/adinvadim/dzencrawl/internal/apperr"
	"github.com/adinvadim/dzencrawl/internal/store"
	"github.com/adinvadim/dzencrawl/internal/studio"
)

type fakeJournal struct {
	mu       sync.Mutex
	begun    []store.JournalEntry
	statuses map[string]string
}

func (j *fakeJournal) JournalBegin(_ context.Context, e store.JournalEntry, _ int64) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.begun = append(j.begun, e)
	return nil
}

func (j *fakeJournal) JournalFinish(_ context.Context, id, status, _ string, _ int64) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.statuses == nil {
		j.statuses = map[string]string{}
	}
	j.statuses[id] = status
	return nil
}

type server struct {
	*httptest.Server
	mu    sync.Mutex
	posts int
	reply func(n int, w http.ResponseWriter, r *http.Request)
	seen  []http.Header
	csrf  int
}

func newServer(t *testing.T, reply func(n int, w http.ResponseWriter, r *http.Request)) *server {
	t.Helper()
	s := &server{reply: reply}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if r.URL.Path == "/media-api/csrf-token" {
			s.csrf++
			_, _ = w.Write([]byte(`{"result":"token-` + strings.Repeat("x", s.csrf) + `"}`))
			return
		}
		s.posts++
		s.seen = append(s.seen, r.Header.Clone())
		s.reply(s.posts, w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func runner(s *server) (*Runner, *fakeJournal) {
	client := studio.New("Session_id=secret", "UA", 0)
	client.Base = s.URL
	client.Sleep = func(context.Context, time.Duration) error { return nil }
	journal := &fakeJournal{}
	return &Runner{Client: client, Journal: journal, Profile: "p", Pauses: []time.Duration{0, time.Millisecond, time.Millisecond}, Sleep: client.Sleep, Now: time.Now}, journal
}

func op() Op {
	return Op{Kind: "save", Target: "t", Request: studio.Request{Method: http.MethodPost, Path: "/x", Body: []byte(`{}`), ContentType: "application/json"}}
}

func hijackAndDrop(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}

func TestDroppedConnectionAfterDispatchIsNeverRetriedAndEndsUnknown(t *testing.T) {
	s := newServer(t, func(_ int, w http.ResponseWriter, _ *http.Request) { hijackAndDrop(t, w) })
	r, journal := runner(s)
	reads := 0
	o := op()
	o.Verify = func(context.Context) (Proof, error) { reads++; return Unproven, nil }
	out, err := r.Run(context.Background(), o)
	if !apperr.Is(err, "outcome_unknown") {
		t.Fatalf("got %v", err)
	}
	if s.posts != 1 {
		t.Fatalf("mutation sent %d times", s.posts)
	}
	if reads != len(r.Pauses) {
		t.Fatalf("expected bounded reads, got %d", reads)
	}
	if journal.statuses[out.JournalID] != "outcome_unknown" || apperr.As(err).ReconcileHint == "" {
		t.Fatalf("%+v %+v", journal.statuses, err)
	}
}

func TestProvenApplicationAfterDropIsConfirmed(t *testing.T) {
	s := newServer(t, func(_ int, w http.ResponseWriter, _ *http.Request) { hijackAndDrop(t, w) })
	r, _ := runner(s)
	o := op()
	o.Verify = func(context.Context) (Proof, error) { return Applied, nil }
	out, err := r.Run(context.Background(), o)
	if err != nil || out.Outcome != "confirmed" || s.posts != 1 {
		t.Fatalf("%+v %v posts=%d", out, err, s.posts)
	}
}

func TestJournalIsWrittenBeforeDispatch(t *testing.T) {
	var r *Runner
	var journal *fakeJournal
	s := newServer(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		if len(journal.begun) != 1 {
			t.Error("request reached the server before the journal entry")
		}
		_, _ = w.Write([]byte(`{}`))
	})
	r, journal = runner(s)
	if _, err := r.Run(context.Background(), op()); err != nil {
		t.Fatal(err)
	}
}

func TestCSRFRejectionRefreshesOnceAndResends(t *testing.T) {
	s := newServer(t, func(n int, w http.ResponseWriter, _ *http.Request) {
		if n == 1 {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"errors":[{"type":"invalid-csrf-token-error"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	})
	r, _ := runner(s)
	if _, err := r.Run(context.Background(), op()); err != nil {
		t.Fatal(err)
	}
	if s.posts != 2 || s.seen[0].Get("X-Csrf-Token") == s.seen[1].Get("X-Csrf-Token") {
		t.Fatalf("posts=%d %v", s.posts, s.seen)
	}
}

func TestRepeatedCSRFRejectionStops(t *testing.T) {
	s := newServer(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"errors":[{"type":"invalid-csrf-token-error"}]}`))
	})
	r, _ := runner(s)
	if _, err := r.Run(context.Background(), op()); !apperr.Is(err, "csrf_rejected") || s.posts != 2 {
		t.Fatalf("%v posts=%d", err, s.posts)
	}
}

func TestServerErrorAfterDispatchIsUnknownUnlessProven(t *testing.T) {
	s := newServer(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"errors":[{"type":"internal-error","message":"java.util.NoSuchElementException"}]}`))
	})
	r, _ := runner(s)
	o := op()
	o.Verify = func(context.Context) (Proof, error) { return Unproven, nil }
	if _, err := r.Run(context.Background(), o); !apperr.Is(err, "outcome_unknown") || s.posts != 1 {
		t.Fatalf("%v posts=%d", err, s.posts)
	}
	o.Verify = func(context.Context) (Proof, error) { return Applied, nil }
	out, err := r.Run(context.Background(), o)
	if err != nil || out.Outcome != "confirmed" {
		t.Fatal(out, err)
	}
	if _, err := r.Run(context.Background(), op()); !apperr.Is(err, "outcome_unknown") {
		t.Fatal("a mutation with no way to read it back must end unknown")
	}
}

func TestExpiredSessionResendsOnlyTheRejectedRequest(t *testing.T) {
	s := newServer(t, func(n int, w http.ResponseWriter, _ *http.Request) {
		if n == 1 {
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	})
	r, _ := runner(s)
	refreshes := 0
	r.Client.RefreshSession = func(context.Context) (string, error) { refreshes++; return "Session_id=new", nil }
	if _, err := r.Run(context.Background(), op()); err != nil {
		t.Fatal(err)
	}
	if s.posts != 2 || refreshes != 1 || s.seen[1].Get("Cookie") != "Session_id=new" {
		t.Fatalf("posts=%d refreshes=%d", s.posts, refreshes)
	}
}

func TestRateLimitWaitsThenRetriesOnce(t *testing.T) {
	s := newServer(t, func(_ int, w http.ResponseWriter, _ *http.Request) { w.WriteHeader(429) })
	r, _ := runner(s)
	if _, err := r.Run(context.Background(), op()); !apperr.Is(err, "rate_limited") || s.posts != 2 {
		t.Fatalf("%v posts=%d", err, s.posts)
	}
}

type fakeSolver struct {
	images []string
	answer string
}

func (f *fakeSolver) SolveImage(_ context.Context, image []byte) (string, error) {
	f.images = append(f.images, string(image))
	return f.answer, nil
}

func commentOp() Op {
	o := op()
	o.Check = CheckCommentStatus
	return o
}

func TestCommentCaptchaIsSolvedAndAnswerSent(t *testing.T) {
	var s *server
	s = newServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/captcha.png":
			_, _ = w.Write([]byte("IMG"))
		case n == 1:
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "captcha-required", "captchaData": map[string]any{"key": "K1", "imageUrl": s.URL + "/captcha.png"}})
		default:
			if r.Header.Get("captcha-key") != "K1" || r.Header.Get("captcha-rep") != "a+b" {
				t.Errorf("answer headers: %v", r.Header)
			}
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}
	})
	r, _ := runner(s)
	solver := &fakeSolver{answer: "a b"}
	r.Captcha = solver
	if _, err := r.Run(context.Background(), commentOp()); err != nil {
		t.Fatal(err)
	}
	if len(solver.images) != 1 || solver.images[0] != "IMG" {
		t.Fatal(solver.images)
	}
}

func TestCaptchaWithoutImageIsUnsupported(t *testing.T) {
	s := newServer(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"captcha-required","captchaData":{"captchaUrl":"https://id.vk.com/x"}}`))
	})
	r, _ := runner(s)
	r.Captcha = &fakeSolver{answer: "x"}
	if _, err := r.Run(context.Background(), commentOp()); !apperr.Is(err, "captcha_unsupported") || s.posts != 1 {
		t.Fatalf("%v posts=%d", err, s.posts)
	}
}

func TestCaptchaBudgetIsBounded(t *testing.T) {
	var s *server
	s = newServer(t, func(_ int, w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/captcha.png" {
			_, _ = w.Write([]byte("IMG"))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "captcha-required", "captchaData": map[string]any{"key": "K", "imageUrl": s.URL + "/captcha.png"}})
	})
	r, _ := runner(s)
	solver := &fakeSolver{answer: "x"}
	r.Captcha = solver
	if _, err := r.Run(context.Background(), commentOp()); !apperr.Is(err, "captcha_required") || len(solver.images) != 3 {
		t.Fatalf("%v solved=%d", err, len(solver.images))
	}
}

func TestDisabledCaptchaStops(t *testing.T) {
	s := newServer(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"captcha-required"}`))
	})
	r, _ := runner(s)
	if _, err := r.Run(context.Background(), commentOp()); !apperr.Is(err, "captcha_required") || s.posts != 1 {
		t.Fatalf("%v posts=%d", err, s.posts)
	}
}

func TestCommentHTTP200WithErrorStatusIsRejected(t *testing.T) {
	s := newServer(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"error"}`))
	})
	r, _ := runner(s)
	if _, err := r.Run(context.Background(), commentOp()); !apperr.Is(err, "remote_validation") {
		t.Fatal(err)
	}
}

func TestScheduleWindow(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	got, err := ScheduleWindow(now.Add(2*time.Hour).In(time.FixedZone("MSK", 3*3600)), now)
	if err != nil || got != "2026-01-01T14:00:00.000Z" {
		t.Fatal(got, err)
	}
	for _, at := range []time.Time{now.Add(30 * time.Minute), now.Add(366 * 24 * time.Hour)} {
		if _, err := ScheduleWindow(at, now); !apperr.Is(err, "validation_schedule_window") {
			t.Fatal(err)
		}
	}
}
