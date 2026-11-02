// Package notify turns domain events into background jobs.
package notify

import (
	"context"

	"github.com/dygoframework/dygo/events"
	"github.com/dygoframework/dygo/queue"

	"github.com/dygoframework/dygo/examples/academy/internal/modules/enrollments"
	"github.com/dygoframework/dygo/examples/academy/internal/modules/welcomeemail"
)

// Listener handles events. Every provider with an Events method is found by
// events.Module at startup.
type Listener struct {
	q *queue.Queue
}

// NewListener is the constructor.
func NewListener(q *queue.Queue) *Listener {
	return &Listener{q: q}
}

// Events subscribes the handlers.
func (l *Listener) Events(r *events.Registry) {
	events.On(r, l.enrolled)
}

// enrolled queues the welcome email, so a slow mail server never slows the
// request down.
func (l *Listener) enrolled(ctx context.Context, e enrollments.Enrolled) error {
	_, err := l.q.Add(ctx, welcomeemail.JobName, welcomeemail.Payload{Email: e.Email, Course: e.Course})
	return err
}
