package ai_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/dygoframework/dygo/ai"
)

func TestErrorText(t *testing.T) {
	cases := []struct {
		err  *ai.Error
		want string
	}{
		{&ai.Error{Provider: "p", Status: 429, Kind: ai.ErrRateLimited, Message: "slow down"}, "ai: rate limited (p, status 429): slow down"},
		{&ai.Error{Provider: "p", Status: 500}, "ai: call failed (p, status 500)"},
		{&ai.Error{Provider: "p", Message: "boom"}, "ai: call failed (p): boom"},
		{&ai.Error{Provider: "p"}, "ai: call failed (p)"},
	}
	for _, c := range cases {
		if got := c.err.Error(); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
}

func TestErrorMessageIsCapped(t *testing.T) {
	long := strings.Repeat("é", 400) // 2 bytes each: the cut lands mid-rune
	got := (&ai.Error{Provider: "p", Message: long}).Error()
	if len(got) > 340 || !utf8.ValidString(got) || !strings.HasSuffix(got, "…") {
		t.Fatalf("len %d, valid %v: %q", len(got), utf8.ValidString(got), got)
	}
}

func TestErrorIsAndRetryable(t *testing.T) {
	e := fmt.Errorf("wrapped: %w", &ai.Error{Provider: "p", Kind: ai.ErrAuth})
	if !errors.Is(e, ai.ErrAuth) || ai.IsRetryable(e) {
		t.Fatal("auth error: want Is and not retryable")
	}
	if !ai.IsRetryable(&ai.Error{Kind: ai.ErrUnavailable, Retryable: true}) {
		t.Fatal("want retryable")
	}
	for _, err := range []error{ai.ErrRateLimited, ai.ErrUnavailable} {
		if !ai.IsRetryable(err) {
			t.Errorf("%v: want retryable", err)
		}
	}
	for _, err := range []error{ai.ErrInvalidRequest, context.Canceled, context.DeadlineExceeded, errors.New("x")} {
		if ai.IsRetryable(err) {
			t.Errorf("%v: want not retryable", err)
		}
	}
}

func TestPublicMessageHidesDetails(t *testing.T) {
	secret := "sk-secret-key and the user's prompt"
	for _, kind := range []error{ai.ErrRateLimited, ai.ErrTooLong, ai.ErrRefused, ai.ErrInvalidRequest, ai.ErrAuth, nil} {
		msg := ai.PublicMessage(&ai.Error{Provider: "p", Kind: kind, Message: secret})
		if msg == "" || strings.Contains(msg, "sk-") || strings.Contains(msg, "prompt") {
			t.Errorf("%v: %q", kind, msg)
		}
	}
	seen := map[string]bool{}
	for _, kind := range []error{ai.ErrRateLimited, ai.ErrTooLong, ai.ErrRefused, ai.ErrInvalidRequest, ai.ErrAuth} {
		seen[ai.PublicMessage(kind)] = true
	}
	if len(seen) != 5 {
		t.Fatalf("want 5 distinct messages, got %v", seen)
	}
}
