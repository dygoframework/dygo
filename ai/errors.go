package ai

import (
	"errors"
	"fmt"
	"time"
	"unicode/utf8"
)

// Errors a call can end with. Providers wrap vendor errors in [*Error] whose
// Kind is one of these, so callers check them with errors.Is whatever the
// vendor.
var (
	ErrInvalidRequest = errors.New("ai: invalid request")
	ErrAuth           = errors.New("ai: authentication failed")
	ErrRateLimited    = errors.New("ai: rate limited")
	ErrTooLong        = errors.New("ai: input too long for the model")
	ErrRefused        = errors.New("ai: the model refused")
	ErrUnavailable    = errors.New("ai: provider unavailable")
	ErrTooLarge       = errors.New("ai: answer larger than the limit")
	ErrNotSupported   = errors.New("ai: not supported by this provider")
)

// maxMessage caps Error.Message, so a vendor cannot fill logs with a huge body.
const maxMessage = 300

// Error is a failed call to a provider.
//
// Message comes from the vendor and is shown in logs, never to the end user:
// [PublicMessage] gives the text that is safe to send to a client. Providers
// must not put API keys, headers or prompts in it.
type Error struct {
	Provider string
	// Status is the HTTP status, when there was one.
	Status int
	// Kind is one of the Err values of this package, or nil.
	Kind    error
	Message string
	// Retryable is true when the same call may work later.
	Retryable bool
	// RetryAfter is the wait the vendor asked for, if any.
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	msg := truncate(e.Message, maxMessage)
	kind := "ai: call failed"
	if e.Kind != nil {
		kind = e.Kind.Error()
	}
	switch {
	case e.Status != 0 && msg != "":
		return fmt.Sprintf("%s (%s, status %d): %s", kind, e.Provider, e.Status, msg)
	case e.Status != 0:
		return fmt.Sprintf("%s (%s, status %d)", kind, e.Provider, e.Status)
	case msg != "":
		return fmt.Sprintf("%s (%s): %s", kind, e.Provider, msg)
	}
	return fmt.Sprintf("%s (%s)", kind, e.Provider)
}

// Unwrap returns Kind, so errors.Is(err, ai.ErrRateLimited) works.
func (e *Error) Unwrap() error { return e.Kind }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "…"
}

// IsRetryable reports whether a call that failed with err may work if sent
// again: rate limits and an unavailable provider. Cancelled or timed out
// contexts and bad requests are not retryable.
func IsRetryable(err error) bool {
	if e, ok := errors.AsType[*Error](err); ok {
		return e.Retryable
	}
	return errors.Is(err, ErrRateLimited) || errors.Is(err, ErrUnavailable)
}

// PublicMessage is a short text about err that is safe to show an end user.
// It never contains the vendor's message, the prompt or any key.
func PublicMessage(err error) string {
	switch {
	case errors.Is(err, ErrRateLimited):
		return "The assistant is busy. Try again in a moment."
	case errors.Is(err, ErrTooLong):
		return "The message is too long."
	case errors.Is(err, ErrRefused):
		return "The assistant can't help with that."
	case errors.Is(err, ErrInvalidRequest):
		return "The request is not valid."
	}
	return "The assistant is not available right now."
}
