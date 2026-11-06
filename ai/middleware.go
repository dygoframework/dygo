package ai

import (
	"context"
	"errors"
	"iter"
	"log/slog"
	"math/rand/v2"
	"time"
)

// Middleware wraps a model to add behaviour around every call, the way
// interceptors wrap a route.
type Middleware func(Model) Model

// Chain wraps m in mw. The first middleware is the outermost.
func Chain(m Model, mw ...Middleware) Model {
	for i := len(mw) - 1; i >= 0; i-- {
		m = mw[i](m)
	}
	return m
}

// funcs is a Model made of two functions, for middleware.
type funcs struct {
	chat   func(context.Context, Request) (Response, error)
	stream func(context.Context, Request) iter.Seq2[Chunk, error]
}

func (f funcs) Chat(ctx context.Context, r Request) (Response, error) { return f.chat(ctx, r) }

func (f funcs) Stream(ctx context.Context, r Request) iter.Seq2[Chunk, error] {
	return f.stream(ctx, r)
}

// Timeout ends a call that takes longer than d. For a stream, d covers the
// whole stream, not each chunk.
func Timeout(d time.Duration) Middleware {
	return func(next Model) Model {
		return funcs{
			chat: func(ctx context.Context, r Request) (Response, error) {
				ctx, cancel := context.WithTimeout(ctx, d)
				defer cancel()
				return next.Chat(ctx, r)
			},
			stream: func(ctx context.Context, r Request) iter.Seq2[Chunk, error] {
				return func(yield func(Chunk, error) bool) {
					ctx, cancel := context.WithTimeout(ctx, d)
					defer cancel()
					for c, err := range next.Stream(ctx, r) {
						if !yield(c, err) {
							return
						}
					}
				}
			},
		}
	}
}

// Retry limits. A vendor asking for a longer wait than maxRetryWait gets its
// error returned at once, so a request is never held for minutes.
const (
	maxRetries   = 5
	retryBase    = 500 * time.Millisecond
	maxRetryWait = 30 * time.Second
)

// sleep waits d or until ctx ends. Tests replace it.
var sleep = func(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Retry sends a call again, up to n more times (at most 5), when it fails
// with a retryable error ([IsRetryable]). It waits with exponential backoff
// and jitter, or as long as the vendor asked, up to 30 seconds. A stream is
// only retried if it failed before its first chunk, so a client never sees
// text twice.
func Retry(n int) Middleware {
	n = min(max(n, 0), maxRetries)
	return func(next Model) Model {
		return funcs{
			chat: func(ctx context.Context, r Request) (Response, error) {
				for attempt := 0; ; attempt++ {
					res, err := next.Chat(ctx, r)
					if err == nil || !waitRetry(ctx, err, attempt, n) {
						return res, err
					}
				}
			},
			stream: func(ctx context.Context, r Request) iter.Seq2[Chunk, error] {
				return func(yield func(Chunk, error) bool) {
					for attempt := 0; ; attempt++ {
						sent, failed := false, error(nil)
						for c, err := range next.Stream(ctx, r) {
							if err != nil && !sent {
								failed = err
								break
							}
							sent = true
							if !yield(c, err) || err != nil {
								return
							}
						}
						if failed == nil {
							return
						}
						if !waitRetry(ctx, failed, attempt, n) {
							yield(Chunk{}, failed)
							return
						}
					}
				}
			},
		}
	}
}

// waitRetry waits before the next attempt and reports whether to make it.
func waitRetry(ctx context.Context, err error, attempt, n int) bool {
	if attempt >= n || !IsRetryable(err) {
		return false
	}
	d := backoff(attempt)
	if e, ok := errors.AsType[*Error](err); ok && e.RetryAfter > 0 {
		if e.RetryAfter > maxRetryWait {
			return false
		}
		d = e.RetryAfter
	}
	return sleep(ctx, d) == nil
}

// backoff is full jitter: a random wait up to base*2^attempt, capped.
func backoff(attempt int) time.Duration {
	ceil := min(retryBase<<attempt, maxRetryWait)
	return time.Duration(rand.Int64N(int64(ceil)) + 1) //nolint:gosec // jitter, not a secret
}

// Log logs one line per call: the model, how long it took, token counts and
// the error, if any. Prompts and answers are never logged.
func Log(l *slog.Logger) Middleware {
	return func(next Model) Model {
		return funcs{
			chat: func(ctx context.Context, r Request) (Response, error) {
				start := time.Now()
				res, err := next.Chat(ctx, r)
				logCall(ctx, l, "chat", r.Model, start, res.Usage, err)
				return res, err
			},
			stream: func(ctx context.Context, r Request) iter.Seq2[Chunk, error] {
				return func(yield func(Chunk, error) bool) {
					start := time.Now()
					var usage Usage
					var failed error
					defer func() { logCall(ctx, l, "stream", r.Model, start, usage, failed) }()
					for c, err := range next.Stream(ctx, r) {
						if c.Usage != nil {
							usage = *c.Usage
						}
						failed = err
						if !yield(c, err) {
							return
						}
					}
				}
			},
		}
	}
}

func logCall(ctx context.Context, l *slog.Logger, kind, model string, start time.Time, u Usage, err error) {
	attrs := []slog.Attr{
		slog.String("kind", kind),
		slog.Duration("duration", time.Since(start)),
		slog.Int("input_tokens", u.InputTokens),
		slog.Int("output_tokens", u.OutputTokens),
	}
	if model != "" {
		attrs = append(attrs, slog.String("model", model))
	}
	if err != nil {
		l.LogAttrs(ctx, slog.LevelWarn, "ai call failed", append(attrs, slog.Any("error", err))...)
		return
	}
	l.LogAttrs(ctx, slog.LevelInfo, "ai call", attrs...)
}
