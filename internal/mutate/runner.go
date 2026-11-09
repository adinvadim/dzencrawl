// Package mutate sends changes to Dzen. Every mutation is journaled first,
// dispatched once, and proven by a read when the response leaves doubt.
package mutate

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/adinvadim/dzencrawl/internal/apperr"
	"github.com/adinvadim/dzencrawl/internal/store"
	"github.com/adinvadim/dzencrawl/internal/studio"
)

// Journal records mutation intents and outcomes.
type Journal interface {
	JournalBegin(ctx context.Context, e store.JournalEntry, now int64) error
	JournalFinish(ctx context.Context, opID, status, detail string, now int64) error
}

// Solver answers image captchas.
type Solver interface {
	SolveImage(ctx context.Context, image []byte) (string, error)
}

// Proof is what a read-after-write check established.
type Proof int

const (
	// Unproven means the read showed nothing conclusive yet.
	Unproven Proof = iota
	// Applied means the read shows the mutation took effect.
	Applied
)

// Op is one mutation.
type Op struct {
	Kind        string
	Target      string
	Request     studio.Request
	BaseVersion int64
	// Verify proves the outcome by reading. Nil means no read can prove it.
	Verify func(ctx context.Context) (Proof, error)
	// Check inspects a 2xx body; comment endpoints report failure inside it.
	Check func(body []byte) error
}

// Outcome is a finished mutation.
type Outcome struct {
	Outcome   string `json:"outcome"`
	JournalID string `json:"journal_id"`
	Body      []byte `json:"-"`
}

// Runner applies the mutation safety rules.
type Runner struct {
	Client   *studio.Client
	Journal  Journal
	Captcha  Solver
	Profile  string
	Attempts int
	Pauses   []time.Duration
	Sleep    func(ctx context.Context, d time.Duration) error
	Now      func() time.Time
}

// DefaultPauses is the bounded reconciliation schedule.
var DefaultPauses = []time.Duration{0, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}

func (r *Runner) now() int64 { return r.Now().UnixMilli() }

// Run sends a mutation exactly once, except for the explicit refusals that
// guarantee Dzen did not apply it.
func (r *Runner) Run(ctx context.Context, op Op) (Outcome, error) {
	journalID := newOpID()
	sum := sha256.Sum256(op.Request.Body)
	if err := r.Journal.JournalBegin(ctx, store.JournalEntry{OpID: journalID, Profile: r.Profile, Kind: op.Kind, Target: op.Target, BodySHA256: hex.EncodeToString(sum[:]), BaseVersion: op.BaseVersion}, r.now()); err != nil {
		return Outcome{}, err
	}
	out, err := r.dispatch(ctx, op)
	out.JournalID = journalID
	status, detail := out.Outcome, ""
	if err != nil {
		e := apperr.As(err)
		status, detail = e.Code, e.Message
		e.Phase = "dispatch"
		if e.Code == "outcome_unknown" {
			e.ReconcileHint = "journal " + journalID + ": read the object before trying again"
		}
		err = e
	}
	_ = r.Journal.JournalFinish(context.WithoutCancel(ctx), journalID, status, detail, r.now())
	return out, err
}

func (r *Runner) dispatch(ctx context.Context, op Op) (Outcome, error) {
	csrfRefreshed := false
	sessionRefreshed := false
	rateRetried := false
	captchaLeft := 3
	preflightLeft := 3
	for {
		csrf, err := r.Client.CSRF(ctx, csrfRefreshed)
		if err != nil {
			return Outcome{}, err
		}
		resp, dispatched, err := r.Client.Send(ctx, op.Request, csrf)
		if err != nil {
			if ctx.Err() != nil && !dispatched {
				return Outcome{}, apperr.New("interrupted", "cancelled before the request was sent")
			}
			if dispatched {
				return r.reconcile(context.WithoutCancel(ctx), op, "the request was sent but no response arrived")
			}
			if preflightLeft > 0 {
				preflightLeft--
				if err := r.Sleep(ctx, time.Second); err != nil {
					return Outcome{}, err
				}
				continue
			}
			return Outcome{}, apperr.New("network_before_dispatch", "could not reach Dzen")
		}
		if resp.Status/100 == 2 {
			if op.Check != nil {
				if err := op.Check(resp.Body); err != nil {
					if apperr.Is(err, "captcha_required") && r.Captcha != nil && captchaLeft > 0 {
						captchaLeft--
						if err := r.solve(ctx, &op, resp.Body); err != nil {
							return Outcome{}, err
						}
						continue
					}
					return Outcome{}, err
				}
			}
			return Outcome{Outcome: "confirmed", Body: resp.Body}, nil
		}
		classified := studio.Classify(resp)
		switch {
		case apperr.Is(classified, "csrf_rejected") && !csrfRefreshed:
			csrfRefreshed = true
			continue
		case apperr.Is(classified, "session_expired") && !sessionRefreshed && r.Client.Refresh(ctx):
			sessionRefreshed = true
			continue
		case apperr.Is(classified, "rate_limited") && !rateRetried:
			rateRetried = true
			if err := r.Sleep(ctx, 5*time.Second); err != nil {
				return Outcome{}, err
			}
			continue
		case apperr.Is(classified, "captcha_required") && r.Captcha != nil && captchaLeft > 0:
			captchaLeft--
			if err := r.solve(ctx, &op, resp.Body); err != nil {
				return Outcome{}, err
			}
			continue
		}
		if resp.Status >= 500 {
			return r.reconcile(context.WithoutCancel(ctx), op, fmt.Sprintf("Dzen answered HTTP %d after receiving the request", resp.Status))
		}
		return Outcome{}, classified
	}
}

// reconcile performs bounded reads; without proof the outcome stays unknown.
func (r *Runner) reconcile(ctx context.Context, op Op, reason string) (Outcome, error) {
	if op.Verify != nil {
		for _, pause := range r.Pauses {
			if pause > 0 {
				if err := r.Sleep(ctx, pause); err != nil {
					break
				}
			}
			if proof, err := op.Verify(ctx); err == nil && proof == Applied {
				return Outcome{Outcome: "confirmed"}, nil
			}
		}
	}
	return Outcome{}, apperr.New("outcome_unknown", "%s; the change could not be proven", reason)
}

type captchaEnvelope struct {
	CaptchaData *struct {
		Key        string `json:"key"`
		ImageURL   string `json:"imageUrl"`
		CaptchaURL string `json:"captchaUrl"`
	} `json:"captchaData"`
}

func (r *Runner) solve(ctx context.Context, op *Op, body []byte) error {
	var envelope captchaEnvelope
	_ = json.Unmarshal(body, &envelope)
	if envelope.CaptchaData == nil || envelope.CaptchaData.ImageURL == "" || envelope.CaptchaData.Key == "" {
		return apperr.New("captcha_unsupported", "Dzen asks for a captcha that cannot be solved automatically")
	}
	image, err := r.Client.Download(ctx, envelope.CaptchaData.ImageURL)
	if err != nil {
		return apperr.New("captcha_failed", "could not download the captcha image")
	}
	answer, err := r.Captcha.SolveImage(ctx, image)
	if err != nil {
		return err
	}
	headers := map[string]string{}
	for k, v := range op.Request.Headers {
		headers[k] = v
	}
	headers["captcha-key"] = envelope.CaptchaData.Key
	headers["captcha-rep"] = url.QueryEscape(answer)
	op.Request.Headers = headers
	return nil
}

// CheckCommentStatus treats only status "ok" as success.
func CheckCommentStatus(body []byte) error {
	var out struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return apperr.New("contract_drift", "comment response changed shape")
	}
	switch out.Status {
	case "ok":
		return nil
	case "captcha-required":
		return apperr.New("captcha_required", "Dzen asks for a captcha")
	case "error":
		return apperr.New("remote_validation", "Dzen rejected the comment")
	}
	return apperr.New("contract_drift", "comment response has status %q", out.Status)
}

func newOpID() string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// IsCancel reports a context cancellation.
func IsCancel(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
