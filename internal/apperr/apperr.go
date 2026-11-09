// Package apperr defines the stable error codes and exit classes of the CLI.
package apperr

import (
	"errors"
	"fmt"
	"strings"
)

// Error is a classified failure with a stable code.
type Error struct {
	Code          string `json:"code"`
	Message       string `json:"message"`
	Detail        string `json:"detail,omitempty"`
	Retryable     bool   `json:"retryable"`
	Phase         string `json:"phase,omitempty"`
	ReconcileHint string `json:"reconcile_hint,omitempty"`
	Outcome       string `json:"-"`
	Diagnostics   any    `json:"diagnostics,omitempty"`
}

func (e *Error) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

// New builds an error with a code and formatted message.
func New(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// As returns err as *Error, classifying anything else as internal.
func As(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{Code: "internal_error", Message: err.Error()}
}

// Is reports whether err carries the given code.
func Is(err error, code string) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

// Exit returns the process exit class for a code.
func Exit(code string) int {
	switch {
	case code == "usage_error", strings.HasPrefix(code, "validation_"), code == "markdown_unsupported":
		return 2
	case code == "not_authenticated", code == "session_expired", code == "session_invalid", code == "csrf_rejected", code == "principal_mismatch":
		return 3
	case code == "needs_confirmation", strings.HasPrefix(code, "captcha_"):
		return 4
	case code == "forbidden", code == "not_found", code == "remote_validation", code == "content_blocked", code == "channel_restricted", code == "commenter_banned", code == "comment_edit_window_closed":
		return 5
	case code == "revision_conflict", code == "concurrent_edit_suspected":
		return 6
	case code == "network_before_dispatch", code == "rate_limited", code == "server_unavailable":
		return 7
	case code == "outcome_unknown", code == "reconcile_timeout":
		return 8
	case code == "contract_drift", code == "unexpected_response_shape":
		return 9
	case code == "interrupted":
		return 130
	}
	return 1
}
