package ai_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/ai"
	"github.com/dygoframework/dygo/ai/fake"
	"github.com/dygoframework/dygo/dygotest"
)

func TestCollect(t *testing.T) {
	m := fake.New(fake.Chunks("Hel", "lo").WithUsage(5, 2))
	res, err := ai.Collect(m.Stream(context.Background(), ai.Prompt("hi")))
	if err != nil {
		t.Fatal(err)
	}
	if res.Text() != "Hello" || res.Usage.Total() != 7 || res.StopReason != "end" || res.Message.Role != ai.RoleAssistant {
		t.Fatalf("got %+v", res)
	}
}

func TestCollectError(t *testing.T) {
	m := fake.New(fake.Broken(ai.ErrUnavailable, "partial"))
	if _, err := ai.Collect(m.Stream(context.Background(), ai.Prompt("hi"))); !errors.Is(err, ai.ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
}

func TestCollectStopsAtLimit(t *testing.T) {
	big := strings.Repeat("x", ai.MaxAnswerBytes/2+1)
	m := fake.New(fake.Chunks(big, big, "never read"))
	if _, err := ai.Collect(m.Stream(context.Background(), ai.Prompt("hi"))); !errors.Is(err, ai.ErrTooLarge) {
		t.Fatalf("got %v", err)
	}
}

func serveSSE(t *testing.T, ctx context.Context, step fake.Step) string {
	t.Helper()
	m := fake.New(step)
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
	if err := ai.SSE(m.Stream(ctx, ai.Prompt("hi"))).Respond(rec, req); err != nil {
		t.Fatal(err)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	return rec.Body.String()
}

func TestSSE(t *testing.T) {
	body := serveSSE(t, context.Background(), fake.Chunks("Hel", "", "lo"))
	want := "data: Hel\n\ndata: lo\n\nevent: end\ndata: \n\n"
	if body != want {
		t.Fatalf("got %q, want %q", body, want)
	}
}

func TestSSEErrorIsPublic(t *testing.T) {
	err := &ai.Error{Provider: "p", Kind: ai.ErrRateLimited, Message: "key sk-123 over quota"}
	body := serveSSE(t, context.Background(), fake.Broken(err, "par"))
	if !strings.Contains(body, "data: par\n") || !strings.Contains(body, "event: error\n") {
		t.Fatalf("got %q", body)
	}
	if strings.Contains(body, "sk-123") || strings.Contains(body, "event: end") {
		t.Fatalf("leaked or ended: %q", body)
	}
}

func TestSSECancelledSendsNothingMore(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := fake.New(fake.Reply("x"))
	var got []any
	for ev := range ai.Events(m.Stream(ctx, ai.Prompt("hi"))) {
		got = append(got, ev)
	}
	if len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestEventsStopEarly(t *testing.T) {
	m := fake.New(fake.Chunks("a", "b"))
	n := 0
	for range ai.Events(m.Stream(context.Background(), ai.Prompt("hi"))) {
		n++
		break
	}
	if n != 1 {
		t.Fatalf("got %d", n)
	}
}

func TestProvide(t *testing.T) {
	f := fake.New(fake.Reply("hi"))
	var order []string
	mark := func(name string) ai.Middleware {
		return func(next ai.Model) ai.Model {
			order = append(order, name)
			return next
		}
	}
	root := &dygo.Module{Name: "app", Imports: []*dygo.Module{ai.Provide(f, mark("outer"), mark("inner"))}}
	app := dygotest.New(root).Compile(t)
	m, err := dygo.Get[ai.Model](app)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := ai.Ask(context.Background(), m, "x"); got != "hi" {
		t.Fatalf("got %q", got)
	}
	if strings.Join(order, ",") != "inner,outer" { // built inside out
		t.Fatalf("order %v", order)
	}
}
