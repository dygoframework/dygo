package ai

import (
	"context"
	"time"
)

// SetSleep replaces the retry wait for a test and returns a function that
// puts it back.
func SetSleep(f func(context.Context, time.Duration) error) func() {
	old := sleep
	sleep = f
	return func() { sleep = old }
}

// Backoff exposes backoff to tests.
func Backoff(attempt int) time.Duration { return backoff(attempt) }

// Events exposes the SSE event sequence to tests.
var Events = events
