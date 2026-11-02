// Package welcomeemail sends the email a user gets after enrolling.
package welcomeemail

import (
	"context"
	"log/slog"
	"sync"

	"github.com/dygoframework/dygo/queue"
)

// JobName is the name producers add the job under:
//
//	q.Add(ctx, welcomeemail.JobName, welcomeemail.Payload{...})
const JobName = "welcome-email"

// Payload is the job's data, stored as JSON.
type Payload struct {
	Email  string `json:"email"`
	Course string `json:"course"`
}

// Processor handles welcome-email jobs. Sent records what was sent, standing
// in for a mail provider.
type Processor struct {
	log  *slog.Logger
	mu   sync.Mutex
	sent []Payload
}

// NewProcessor is the constructor.
func NewProcessor(log *slog.Logger) *Processor {
	return &Processor{log: log}
}

// Process registers the handlers.
func (p *Processor) Process(r *queue.Registry) {
	queue.Handle(r, JobName, p.handle)
}

func (p *Processor) handle(ctx context.Context, in Payload) error {
	p.log.InfoContext(ctx, "welcome email sent", "to", in.Email, "course", in.Course)
	p.mu.Lock()
	p.sent = append(p.sent, in)
	p.mu.Unlock()
	return nil
}

// Sent returns the emails sent so far.
func (p *Processor) Sent() []Payload {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Payload(nil), p.sent...)
}
