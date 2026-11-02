package interceptors_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/errs"
	"github.com/dygoframework/dygo/interceptors"
)

// fakeEC is the part of an ExecutionContext the interceptors use.
type fakeEC struct {
	dygo.ExecutionContext
	ctx context.Context
}

func (f *fakeEC) Context() context.Context       { return f.ctx }
func (f *fakeEC) SetContext(ctx context.Context) { f.ctx = ctx }
func (f *fakeEC) Route() dygo.RouteInfo {
	return dygo.RouteInfo{Method: "GET", Path: "/things", Handler: "Things.List"}
}

type next func() (any, error)

func (n next) Handle() (any, error) { return n() }

func TestTimeout(t *testing.T) {
	ec := &fakeEC{ctx: context.Background()}
	ic := interceptors.Timeout(10 * time.Millisecond)

	_, err := ic.Intercept(ec, next(func() (any, error) {
		<-ec.Context().Done() // the handler sees the deadline
		return nil, ec.Context().Err()
	}))
	var he *errs.HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusRequestTimeout || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}

	// A deadline error that is not ours passes through unchanged.
	other := context.DeadlineExceeded
	ec = &fakeEC{ctx: context.Background()}
	if _, err := interceptors.Timeout(time.Hour).Intercept(ec, next(func() (any, error) { return nil, other })); err != other {
		t.Fatalf("got %v", err)
	}

	out, err := interceptors.Timeout(time.Hour).Intercept(ec, next(func() (any, error) { return "ok", nil }))
	if out != "ok" || err != nil {
		t.Fatalf("got %v %v", out, err)
	}
}

func TestLogging(t *testing.T) {
	var buf bytes.Buffer
	ic := interceptors.Logging(slog.New(slog.NewTextHandler(&buf, nil)))
	ec := &fakeEC{ctx: context.Background()}

	if out, err := ic.Intercept(ec, next(func() (any, error) { return 1, nil })); out != 1 || err != nil {
		t.Fatal(out, err)
	}
	if _, err := ic.Intercept(ec, next(func() (any, error) { return nil, errors.New("boom") })); err == nil {
		t.Fatal("error lost")
	}
	out := buf.String()
	for _, want := range []string{"request handled", "request failed", "route=/things", "handler=Things.List", "err=boom"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}

	// With Info disabled nothing is logged and the result is unchanged.
	buf.Reset()
	quiet := interceptors.Logging(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	if out, _ := quiet.Intercept(ec, next(func() (any, error) { return 2, nil })); out != 2 || buf.Len() != 0 {
		t.Fatalf("out=%v log=%q", out, buf.String())
	}
	_ = interceptors.Logging(nil) // nil means slog.Default()
}
