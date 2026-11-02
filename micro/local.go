package micro

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"

	"github.com/dygoframework/dygo"
)

// Local is a transport inside the process: a [dygo.Transport] for the
// handlers and a [ClientTransport] that calls them. Tests use it to run a
// service's handlers without a broker; messages are copied as if they went
// over the wire.
//
//	local := micro.NewLocal(micro.ServerOptions{})
//	app.ConnectMicroservice(local)
//	client := micro.NewClient(local)
type Local struct {
	opts ServerOptions

	mu     sync.RWMutex
	hs     *Handlers
	closed bool
	wg     sync.WaitGroup
	// OnError, when set, gets errors of event handlers and failed requests.
	OnError func(ctx context.Context, m *Message, err error)
}

// NewLocal returns an in-process transport.
func NewLocal(o ServerOptions) *Local { return &Local{opts: o} }

// Listen implements dygo.Transport.
func (l *Local) Listen(_ context.Context, a *dygo.App) error {
	hs, err := Collect(a, l.opts)
	if err != nil {
		return err
	}
	l.mu.Lock()
	l.hs, l.closed = hs, false
	l.mu.Unlock()
	return nil
}

// Close implements dygo.Transport.
func (l *Local) Close(ctx context.Context) error {
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
	done := make(chan struct{})
	go func() { l.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *Local) handlers() (*Handlers, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.hs == nil || l.closed {
		return nil, errors.New("micro: local transport is not listening")
	}
	l.wg.Add(1)
	return l.hs, nil
}

func clone(m *Message) *Message {
	return &Message{Pattern: m.Pattern, Header: maps.Clone(m.Header), Data: slices.Clone(m.Data)}
}

// Request implements ClientTransport.
func (l *Local) Request(ctx context.Context, m *Message) ([]byte, error) {
	hs, err := l.handlers()
	if err != nil {
		return nil, err
	}
	defer l.wg.Done()
	e, ok := hs.Message(m.Pattern)
	if !ok {
		return nil, ErrNoHandler
	}
	m = clone(m)
	reply, err := e.Serve(ctx, m)
	if err != nil {
		l.report(ctx, m, err)
	}
	return reply, nil
}

// Publish implements ClientTransport. Event handlers run before it returns.
func (l *Local) Publish(ctx context.Context, m *Message) error {
	hs, err := l.handlers()
	if err != nil {
		return err
	}
	defer l.wg.Done()
	for _, e := range hs.Events(m.Pattern) {
		cm := clone(m)
		if _, err := e.Serve(ctx, cm); err != nil {
			l.report(ctx, cm, err)
		}
	}
	return nil
}

func (l *Local) report(ctx context.Context, m *Message, err error) {
	if l.OnError != nil && !errors.Is(err, dygo.ErrFiltered) {
		l.OnError(ctx, m, err)
	}
}
