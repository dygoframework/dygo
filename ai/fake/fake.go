// Package fake is a scripted [ai.Model] for tests: no network, no keys, no
// cost.
//
//	m := fake.New(fake.Reply("Paris"), fake.Fail(ai.ErrRateLimited))
//	got, _ := ai.Ask(ctx, m, "Capital of France?") // "Paris"
//	_, err := ai.Ask(ctx, m, "Again?")             // errors.Is(err, ai.ErrRateLimited)
//	m.Requests()                                   // both requests, to check the prompts
//
// Each call takes the next step of the script, from Chat or Stream alike.
// A call after the last step fails with [ErrNoMoreSteps].
package fake

import (
	"context"
	"errors"
	"iter"
	"slices"
	"strings"
	"sync"

	"github.com/dygoframework/dygo/ai"
)

// ErrNoMoreSteps is returned when the script has run out.
var ErrNoMoreSteps = errors.New("fake: no more steps in the script")

// Step is one scripted answer.
type Step struct {
	chunks []string
	err    error
	usage  ai.Usage
}

// Reply answers with text.
func Reply(text string) Step { return Step{chunks: []string{text}} }

// Chunks answers with text in pieces, so Stream yields one chunk each.
func Chunks(parts ...string) Step { return Step{chunks: parts} }

// Fail fails the call with err. Pass an ai sentinel such as
// [ai.ErrRateLimited], or an [*ai.Error].
func Fail(err error) Step { return Step{err: err} }

// Broken streams parts, then fails with err: a stream cut in the middle.
// Chat fails with err.
func Broken(err error, parts ...string) Step { return Step{chunks: parts, err: err} }

// WithUsage sets the token counts the step reports.
func (s Step) WithUsage(input, output int) Step {
	s.usage = ai.Usage{InputTokens: input, OutputTokens: output}
	return s
}

// Model is the scripted model. It is safe for concurrent use.
type Model struct {
	mu    sync.Mutex
	steps []Step
	reqs  []ai.Request
}

// New returns a model that plays steps in order.
func New(steps ...Step) *Model { return &Model{steps: steps} }

// Add appends steps to the script.
func (m *Model) Add(steps ...Step) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.steps = append(m.steps, steps...)
}

// Requests returns a copy of every request made so far, in order.
func (m *Model) Requests() []ai.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.reqs)
}

// next validates and records req, then takes the next step.
func (m *Model) next(ctx context.Context, req ai.Request) (Step, error) {
	if err := ctx.Err(); err != nil {
		return Step{}, err
	}
	if err := req.Validate(); err != nil {
		return Step{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reqs = append(m.reqs, clone(req))
	if len(m.steps) == 0 {
		return Step{}, ErrNoMoreSteps
	}
	s := m.steps[0]
	m.steps = m.steps[1:]
	return s, nil
}

// clone copies the slices of req, so a caller reusing them later cannot
// change what was recorded.
func clone(req ai.Request) ai.Request {
	req.Messages = slices.Clone(req.Messages)
	for i := range req.Messages {
		req.Messages[i].Parts = slices.Clone(req.Messages[i].Parts)
	}
	if req.Temperature != nil {
		t := *req.Temperature
		req.Temperature = &t
	}
	return req
}

// Chat answers with the next step.
func (m *Model) Chat(ctx context.Context, req ai.Request) (ai.Response, error) {
	s, err := m.next(ctx, req)
	if err != nil {
		return ai.Response{}, err
	}
	if s.err != nil {
		return ai.Response{}, s.err
	}
	return ai.Response{
		Message:    ai.Message{Role: ai.RoleAssistant, Parts: []ai.Part{ai.TextPart{Text: strings.Join(s.chunks, "")}}},
		StopReason: "end",
		Usage:      s.usage,
	}, nil
}

// Stream yields the next step's chunks. The last chunk carries the usage and
// stop reason; a [Broken] step ends with its error instead.
func (m *Model) Stream(ctx context.Context, req ai.Request) iter.Seq2[ai.Chunk, error] {
	return func(yield func(ai.Chunk, error) bool) {
		s, err := m.next(ctx, req)
		if err != nil {
			yield(ai.Chunk{}, err)
			return
		}
		for i, text := range s.chunks {
			if err := ctx.Err(); err != nil {
				yield(ai.Chunk{}, err)
				return
			}
			c := ai.Chunk{Text: text}
			if i == len(s.chunks)-1 && s.err == nil {
				u := s.usage
				c.Usage, c.StopReason = &u, "end"
			}
			if !yield(c, nil) {
				return
			}
		}
		if s.err != nil {
			yield(ai.Chunk{}, s.err)
		}
	}
}
