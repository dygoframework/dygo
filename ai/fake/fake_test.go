package fake_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/dygoframework/dygo/ai"
	"github.com/dygoframework/dygo/ai/fake"
)

func TestScript(t *testing.T) {
	m := fake.New(fake.Reply("one"), fake.Chunks("t", "wo").WithUsage(1, 2))
	ctx := context.Background()
	res, err := m.Chat(ctx, ai.Prompt("a"))
	if err != nil || res.Text() != "one" || res.StopReason != "end" {
		t.Fatalf("got %+v, %v", res, err)
	}
	res, err = m.Chat(ctx, ai.Prompt("b"))
	if err != nil || res.Text() != "two" || res.Usage.Total() != 3 {
		t.Fatalf("got %+v, %v", res, err)
	}
	if _, err := m.Chat(ctx, ai.Prompt("c")); !errors.Is(err, fake.ErrNoMoreSteps) {
		t.Fatalf("got %v", err)
	}
	for _, err := range m.Stream(ctx, ai.Prompt("d")) {
		if !errors.Is(err, fake.ErrNoMoreSteps) {
			t.Fatalf("got %v", err)
		}
	}
}

func TestStreamChunks(t *testing.T) {
	m := fake.New(fake.Chunks("a", "b").WithUsage(2, 2))
	var got []ai.Chunk
	for c, err := range m.Stream(context.Background(), ai.Prompt("x")) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, c)
	}
	if len(got) != 2 || got[0].Usage != nil || got[1].Usage == nil || got[1].StopReason != "end" {
		t.Fatalf("got %+v", got)
	}
}

func TestBrokenAndFail(t *testing.T) {
	m := fake.New(fake.Broken(ai.ErrUnavailable, "a"), fake.Broken(ai.ErrUnavailable, "a"), fake.Fail(ai.ErrRefused))
	ctx := context.Background()
	var texts []string
	var last error
	for c, err := range m.Stream(ctx, ai.Prompt("x")) {
		texts = append(texts, c.Text)
		last = err
	}
	if len(texts) != 2 || texts[0] != "a" || !errors.Is(last, ai.ErrUnavailable) {
		t.Fatalf("got %v, %v", texts, last)
	}
	if _, err := m.Chat(ctx, ai.Prompt("x")); !errors.Is(err, ai.ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
	for _, err := range m.Stream(ctx, ai.Prompt("x")) {
		if !errors.Is(err, ai.ErrRefused) {
			t.Fatalf("got %v", err)
		}
	}
}

func TestValidatesAndHonoursContext(t *testing.T) {
	m := fake.New(fake.Reply("x"))
	if _, err := m.Chat(context.Background(), ai.Request{}); !errors.Is(err, ai.ErrInvalidRequest) {
		t.Fatalf("got %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Chat(ctx, ai.Prompt("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if len(m.Requests()) != 0 {
		t.Fatal("bad calls must not be recorded")
	}
}

func TestStreamStopsOnCancel(t *testing.T) {
	m := fake.New(fake.Chunks("a", "b", "c"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var last error
	n := 0
	for _, err := range m.Stream(ctx, ai.Prompt("x")) {
		n++
		last = err
		cancel()
	}
	if n != 2 || !errors.Is(last, context.Canceled) {
		t.Fatalf("n %d, %v", n, last)
	}
	m.Add(fake.Chunks("a", "b"))
	for _, err := range m.Stream(context.Background(), ai.Prompt("x")) {
		if err != nil {
			t.Fatal(err)
		}
		break
	}
}

func TestRequestsAreCopies(t *testing.T) {
	temp := 0.5
	req := ai.Prompt("original")
	req.Temperature = &temp
	m := fake.New(fake.Reply("x"))
	_, _ = m.Chat(context.Background(), req)
	req.Messages[0].Parts[0] = ai.TextPart{Text: "changed"}
	temp = 1
	got := m.Requests()[0]
	if got.Messages[0].Text() != "original" || *got.Temperature != 0.5 {
		t.Fatalf("got %+v", got)
	}
}

func TestConcurrent(t *testing.T) {
	m := fake.New()
	for range 50 {
		m.Add(fake.Reply("x"))
	}
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() { _, _ = m.Chat(context.Background(), ai.Prompt("x")) })
	}
	wg.Wait()
	if len(m.Requests()) != 50 {
		t.Fatalf("got %d", len(m.Requests()))
	}
}
