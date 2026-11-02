// Package events is an in-process event emitter.
// Events are Go values and listeners subscribe by type, so a listener gets
// the event already typed:
//
//	type OrderCreated struct{ ID string }
//
//	type Mailer struct{ ... }
//
//	func (m *Mailer) Events(r *events.Registry) {
//		events.On(r, m.orderCreated)
//		events.On(r, m.audit, events.Async())
//	}
//
//	func (m *Mailer) orderCreated(ctx context.Context, e OrderCreated) error { ... }
//
// Any provider with an Events method is a [Listener]; the emitter finds the
// listeners of the whole application at startup. Import [Module] once, in the
// root module, and inject *events.Emitter where events are emitted:
//
//	err := s.events.Emit(ctx, OrderCreated{ID: id})
//
// A listener of an interface type gets every event that implements it.
// dygo generate listener writes a listener.
package events

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"runtime/debug"
	"slices"
	"sync"

	"github.com/dygoframework/dygo"
)

// Listener is a provider that subscribes to events. Events is called once,
// during OnModuleInit of the emitter.
type Listener interface {
	Events(r *Registry)
}

// Registry is what a [Listener] subscribes through.
type Registry struct {
	e     *Emitter
	owner any
}

// Option configures one subscription.
type Option func(*sub)

// Async runs the listener in its own goroutine, so Emit does not wait for it
// and does not see its error. Errors and panics go to the module's error
// handler. Shutdown waits for running async listeners (see [Emitter]).
func Async() Option { return func(s *sub) { s.async = true } }

// On subscribes fn to events of type E: E itself, or every type that
// implements E when E is an interface.
func On[E any](r *Registry, fn func(ctx context.Context, e E) error, opts ...Option) {
	r.e.subscribe(r.owner, reflect.TypeFor[E](), func(ctx context.Context, ev any) error {
		return fn(ctx, ev.(E))
	}, opts)
}

// Subscribe is [On] for code that is not a Listener, such as a test. It
// returns a function that removes the subscription.
func Subscribe[E any](e *Emitter, fn func(ctx context.Context, e E) error, opts ...Option) (unsubscribe func()) {
	s := e.subscribe(nil, reflect.TypeFor[E](), func(ctx context.Context, ev any) error {
		return fn(ctx, ev.(E))
	}, opts)
	return func() { e.unsubscribe(s) }
}

type sub struct {
	typ   reflect.Type
	fn    func(ctx context.Context, ev any) error
	async bool
	owner any
}

// ModuleOption configures [Module].
type ModuleOption func(*config)

type config struct {
	onError func(ctx context.Context, ev any, err error)
}

// OnError sets the handler for errors and panics of async listeners. The
// default logs them with slog.Default().
func OnError(fn func(ctx context.Context, ev any, err error)) ModuleOption {
	return func(c *config) { c.onError = fn }
}

// Module provides the *Emitter. It is global, so import it once in the root
// module.
func Module(opts ...ModuleOption) *dygo.Module {
	cfg := &config{onError: func(ctx context.Context, ev any, err error) {
		slog.Default().ErrorContext(ctx, "events: async listener failed", "event", fmt.Sprintf("%T", ev), "err", err)
	}}
	for _, o := range opts {
		o(cfg)
	}
	return &dygo.Module{
		Name:   "events",
		Global: true,
		Providers: []dygo.Provider{
			dygo.Provide(func(ref *dygo.ModuleRef) *Emitter { return newEmitter(ref, cfg) }),
		},
		Exports: []dygo.Token{dygo.TypeOf[*Emitter]()},
	}
}

// Emitter delivers events to listeners. It finds every [Listener] provider in
// OnModuleInit and, in BeforeApplicationShutdown, stops starting async
// listeners and waits for the running ones.
type Emitter struct {
	ref *dygo.ModuleRef
	cfg *config

	mu      sync.RWMutex
	subs    []*sub
	byType  map[reflect.Type][]*sub // listeners per event type, filled lazily
	closing bool
	wg      sync.WaitGroup
}

// New returns an emitter outside of a module, for tests and small programs.
func New() *Emitter {
	return newEmitter(nil, &config{onError: func(context.Context, any, error) {}})
}

func newEmitter(ref *dygo.ModuleRef, cfg *config) *Emitter {
	return &Emitter{ref: ref, cfg: cfg, byType: map[reflect.Type][]*sub{}}
}

// OnModuleInit asks every Listener for its subscriptions.
func (e *Emitter) OnModuleInit(context.Context) error {
	if e.ref == nil {
		return nil
	}
	for _, l := range dygo.Discover[Listener](e.ref) {
		l.Events(&Registry{e: e, owner: l})
	}
	return nil
}

// BeforeApplicationShutdown waits for running async listeners until ctx is
// done. Events emitted afterwards run only their synchronous listeners.
func (e *Emitter) BeforeApplicationShutdown(ctx context.Context, _ string) error {
	e.mu.Lock()
	e.closing = true
	e.mu.Unlock()
	return e.Wait(ctx)
}

// Wait blocks until every running async listener has returned, or ctx is
// done.
func (e *Emitter) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() { e.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("events: waiting for async listeners: %w", ctx.Err())
	}
}

func (e *Emitter) subscribe(owner any, t reflect.Type, fn func(context.Context, any) error, opts []Option) *sub {
	s := &sub{typ: t, fn: fn, owner: owner}
	for _, o := range opts {
		o(s)
	}
	e.mu.Lock()
	e.subs = append(e.subs, s)
	clear(e.byType)
	e.mu.Unlock()
	return s
}

func (e *Emitter) unsubscribe(s *sub) {
	e.mu.Lock()
	e.subs = slices.DeleteFunc(e.subs, func(x *sub) bool { return x == s })
	clear(e.byType)
	e.mu.Unlock()
}

// listeners returns the subscriptions for events of type t, in subscription
// order.
func (e *Emitter) listeners(t reflect.Type) []*sub {
	e.mu.RLock()
	ls, ok := e.byType[t]
	e.mu.RUnlock()
	if ok {
		return ls
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, s := range e.subs {
		if s.typ == t || (s.typ.Kind() == reflect.Interface && t.Implements(s.typ)) {
			ls = append(ls, s)
		}
	}
	e.byType[t] = ls
	return ls
}

// startAsync counts a new async listener, unless shutdown has begun.
func (e *Emitter) startAsync() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.closing {
		return false
	}
	e.wg.Add(1)
	return true
}

// Emit delivers ev to its listeners. Synchronous listeners run in order in
// the calling goroutine; Emit returns their errors joined (a panic becomes an
// error) after all of them ran. Async listeners start in their own goroutine
// with a context that is not cancelled with ctx.
func (e *Emitter) Emit(ctx context.Context, ev any) error {
	if ev == nil {
		return errors.New("events: Emit(nil)")
	}
	var errs []error
	for _, s := range e.listeners(reflect.TypeOf(ev)) {
		if s.async {
			if !e.startAsync() {
				continue
			}
			go func() {
				defer e.wg.Done()
				actx := context.WithoutCancel(ctx)
				if err := call(actx, s, ev); err != nil {
					e.cfg.onError(actx, ev, err)
				}
			}()
			continue
		}
		if err := call(ctx, s, ev); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// HasListeners reports whether events of ev's type have a listener.
func (e *Emitter) HasListeners(ev any) bool {
	return len(e.listeners(reflect.TypeOf(ev))) > 0
}

func call(ctx context.Context, s *sub, ev any) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("events: listener for %s panicked: %v\n%s", s.typ, v, debug.Stack())
		}
	}()
	return s.fn(ctx, ev)
}
