package ai_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"iter"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/dygoframework/dygo/ai"
	"github.com/dygoframework/dygo/ai/fake"
)

// noWait records retry waits instead of sleeping.
func noWait(t *testing.T) *[]time.Duration {
	var waits []time.Duration
	t.Cleanup(ai.SetSleep(func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		return ctx.Err()
	}))
	return &waits
}

// ctxModel records the context of each call.
type ctxModel struct{ ctx context.Context }

func (m *ctxModel) Chat(ctx context.Context, _ ai.Request) (ai.Response, error) {
	m.ctx = ctx
	return ai.Response{}, nil
}

func (m *ctxModel) Stream(ctx context.Context, _ ai.Request) iter.Seq2[ai.Chunk, error] {
	return func(yield func(ai.Chunk, error) bool) {
		m.ctx = ctx
		_ = yield(ai.Chunk{Text: "a"}, nil) && yield(ai.Chunk{Text: "b"}, nil)
	}
}

func TestTimeout(t *testing.T) {
	inner := &ctxModel{}
	m := ai.Chain(inner, ai.Timeout(time.Minute))
	if _, err := m.Chat(context.Background(), ai.Prompt("x")); err != nil {
		t.Fatal(err)
	}
	if _, ok := inner.ctx.Deadline(); !ok {
		t.Fatal("chat: no deadline")
	}
	n := 0
	for _, err := range m.Stream(context.Background(), ai.Prompt("x")) {
		if err != nil {
			t.Fatal(err)
		}
		n++
		if _, ok := inner.ctx.Deadline(); !ok {
			t.Fatal("stream: no deadline")
		}
		break // stopping early must not panic
	}
	if n != 1 {
		t.Fatalf("got %d chunks", n)
	}
	if inner.ctx.Err() == nil {
		t.Fatal("stream context not cancelled after the stream")
	}
}

func TestRetryChat(t *testing.T) {
	waits := noWait(t)
	f := fake.New(fake.Fail(ai.ErrRateLimited), fake.Fail(&ai.Error{Kind: ai.ErrUnavailable, Retryable: true}), fake.Reply("ok"))
	got, err := ai.Ask(context.Background(), ai.Chain(f, ai.Retry(2)), "x")
	if err != nil || got != "ok" || len(*waits) != 2 {
		t.Fatalf("got %q, %v, waits %v", got, err, *waits)
	}
}

func TestRetryGivesUp(t *testing.T) {
	waits := noWait(t)
	f := fake.New(fake.Fail(ai.ErrRateLimited), fake.Fail(ai.ErrRateLimited), fake.Reply("never"))
	if _, err := ai.Ask(context.Background(), ai.Chain(f, ai.Retry(1)), "x"); !errors.Is(err, ai.ErrRateLimited) {
		t.Fatalf("got %v", err)
	}
	if len(*waits) != 1 || len(f.Requests()) != 2 {
		t.Fatalf("waits %v, calls %d", *waits, len(f.Requests()))
	}
}

func TestRetrySkipsPermanentErrors(t *testing.T) {
	waits := noWait(t)
	f := fake.New(fake.Fail(ai.ErrAuth), fake.Reply("never"))
	if _, err := ai.Ask(context.Background(), ai.Chain(f, ai.Retry(3)), "x"); !errors.Is(err, ai.ErrAuth) {
		t.Fatalf("got %v", err)
	}
	if len(*waits) != 0 {
		t.Fatalf("waits %v", *waits)
	}
}

func TestRetryHonoursRetryAfter(t *testing.T) {
	waits := noWait(t)
	f := fake.New(fake.Fail(&ai.Error{Kind: ai.ErrRateLimited, Retryable: true, RetryAfter: 7 * time.Second}), fake.Reply("ok"))
	if _, err := ai.Ask(context.Background(), ai.Chain(f, ai.Retry(1)), "x"); err != nil {
		t.Fatal(err)
	}
	if len(*waits) != 1 || (*waits)[0] != 7*time.Second {
		t.Fatalf("waits %v", *waits)
	}
	// A vendor asking for minutes gets its error back at once.
	f.Add(fake.Fail(&ai.Error{Kind: ai.ErrRateLimited, Retryable: true, RetryAfter: time.Hour}), fake.Reply("never"))
	if _, err := ai.Ask(context.Background(), ai.Chain(f, ai.Retry(1)), "x"); !errors.Is(err, ai.ErrRateLimited) {
		t.Fatalf("got %v", err)
	}
	if len(*waits) != 1 {
		t.Fatalf("waits %v", *waits)
	}
}

func TestRetryStopsWhenContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := fake.New(fake.Fail(ai.ErrRateLimited), fake.Reply("never"))
	m := ai.Chain(f, ai.Retry(3))
	// The real sleep returns at once on a done context.
	if _, err := m.Chat(ctx, ai.Prompt("x")); err == nil {
		t.Fatal("want an error")
	}
}

func TestRetryCount(t *testing.T) {
	waits := noWait(t)
	steps := make([]fake.Step, 10)
	for i := range steps {
		steps[i] = fake.Fail(ai.ErrRateLimited)
	}
	f := fake.New(steps...)
	_, _ = ai.Ask(context.Background(), ai.Chain(f, ai.Retry(100)), "x")
	if len(f.Requests()) != 6 { // capped at 5 retries
		t.Fatalf("calls %d", len(f.Requests()))
	}
	f2 := fake.New(fake.Fail(ai.ErrRateLimited))
	_, _ = ai.Ask(context.Background(), ai.Chain(f2, ai.Retry(-1)), "x")
	if len(f2.Requests()) != 1 || len(*waits) != 5 {
		t.Fatalf("calls %d, waits %d", len(f2.Requests()), len(*waits))
	}
}

func TestBackoff(t *testing.T) {
	for attempt := range 12 {
		d := ai.Backoff(attempt)
		ceil := min(500*time.Millisecond<<attempt, 30*time.Second)
		if d <= 0 || d > ceil {
			t.Fatalf("attempt %d: %v", attempt, d)
		}
	}
}

func TestRetryStream(t *testing.T) {
	noWait(t)
	f := fake.New(fake.Fail(ai.ErrUnavailable), fake.Chunks("a", "b"))
	res, err := ai.Collect(ai.Chain(f, ai.Retry(1)).Stream(context.Background(), ai.Prompt("x")))
	if err != nil || res.Text() != "ab" {
		t.Fatalf("got %q, %v", res.Text(), err)
	}
}

func TestRetryStreamNeverRepeatsText(t *testing.T) {
	waits := noWait(t)
	f := fake.New(fake.Broken(ai.ErrUnavailable, "half"), fake.Reply("never"))
	var text strings.Builder
	var last error
	for c, err := range ai.Chain(f, ai.Retry(3)).Stream(context.Background(), ai.Prompt("x")) {
		text.WriteString(c.Text)
		last = err
	}
	if text.String() != "half" || !errors.Is(last, ai.ErrUnavailable) || len(*waits) != 0 {
		t.Fatalf("got %q, %v, waits %v", text.String(), last, *waits)
	}
}

func TestRetryStreamGivesUp(t *testing.T) {
	noWait(t)
	f := fake.New(fake.Fail(ai.ErrAuth))
	if _, err := ai.Collect(ai.Chain(f, ai.Retry(3)).Stream(context.Background(), ai.Prompt("x"))); !errors.Is(err, ai.ErrAuth) {
		t.Fatalf("got %v", err)
	}
}

func TestRetryStreamStopEarly(t *testing.T) {
	f := fake.New(fake.Chunks("a", "b"))
	for _, err := range ai.Chain(f, ai.Retry(1)).Stream(context.Background(), ai.Prompt("x")) {
		if err != nil {
			t.Fatal(err)
		}
		break
	}
}

type logLine map[string]any

func logs(buf *bytes.Buffer) []logLine {
	var out []logLine
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var m logLine
		_ = json.Unmarshal([]byte(l), &m)
		out = append(out, m)
	}
	return out
}

func TestLogNeverLogsPrompts(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewJSONHandler(&buf, nil))
	f := fake.New(fake.Reply("the answer").WithUsage(3, 2), fake.Fail(&ai.Error{Provider: "p", Kind: ai.ErrAuth}))
	m := ai.Chain(f, ai.Log(l))
	req := ai.Prompt("my secret prompt")
	req.Model = "m1"
	_, _ = m.Chat(context.Background(), req)
	_, _ = m.Chat(context.Background(), req)
	out := buf.String()
	if strings.Contains(out, "secret prompt") || strings.Contains(out, "the answer") {
		t.Fatalf("logged content: %s", out)
	}
	lines := logs(&buf)
	if len(lines) != 2 || lines[0]["msg"] != "ai call" || lines[0]["model"] != "m1" || lines[0]["input_tokens"] != 3.0 || lines[0]["kind"] != "chat" {
		t.Fatalf("got %v", lines)
	}
	if lines[1]["level"] != "WARN" || !strings.Contains(lines[1]["error"].(string), "authentication") {
		t.Fatalf("got %v", lines[1])
	}
}

func TestLogStream(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewJSONHandler(&buf, nil))
	f := fake.New(fake.Chunks("a", "b").WithUsage(4, 1), fake.Broken(ai.ErrUnavailable, "c"), fake.Chunks("d", "e"))
	m := ai.Chain(f, ai.Log(l))
	if _, err := ai.Collect(m.Stream(context.Background(), ai.Prompt("x"))); err != nil {
		t.Fatal(err)
	}
	_, _ = ai.Collect(m.Stream(context.Background(), ai.Prompt("x")))
	for _, err := range m.Stream(context.Background(), ai.Prompt("x")) {
		if err != nil {
			t.Fatal(err)
		}
		break
	}
	lines := logs(&buf)
	if len(lines) != 3 || lines[0]["kind"] != "stream" || lines[0]["output_tokens"] != 1.0 || lines[1]["level"] != "WARN" || lines[2]["level"] != "INFO" {
		t.Fatalf("got %v", lines)
	}
}
