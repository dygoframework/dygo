package ai_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/dygoframework/dygo/ai"
	"github.com/dygoframework/dygo/ai/fake"
)

// The cost Dygo adds around a call: a real call takes hundreds of
// milliseconds, so these should stay in the low microseconds.

func BenchmarkChatMiddleware(b *testing.B) {
	f := fake.New()
	for range b.N {
		f.Add(fake.Reply("ok"))
	}
	m := ai.Chain(f, ai.Log(slog.New(slog.NewTextHandler(io.Discard, nil))), ai.Retry(2), ai.Timeout(time.Minute))
	ctx, req := context.Background(), ai.Prompt("hi")
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := m.Chat(ctx, req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStreamMiddlewareCollect(b *testing.B) {
	parts := make([]string, 100)
	for i := range parts {
		parts[i] = "token "
	}
	f := fake.New()
	for range b.N {
		f.Add(fake.Chunks(parts...))
	}
	m := ai.Chain(f, ai.Log(slog.New(slog.NewTextHandler(io.Discard, nil))), ai.Retry(2), ai.Timeout(time.Minute))
	ctx, req := context.Background(), ai.Prompt("hi")
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := ai.Collect(m.Stream(ctx, req)); err != nil {
			b.Fatal(err)
		}
	}
}
