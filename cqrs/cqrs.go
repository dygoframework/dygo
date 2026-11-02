// Package cqrs has command and query buses with typed handlers. A provider
// with a CQRS method registers its handlers:
//
//	type CreateUser struct{ Email string }
//	type UserByID struct{ ID string }
//
//	func (h *UserHandlers) CQRS(r *cqrs.Registry) {
//		cqrs.HandleCommand(r, h.create) // func(ctx, CreateUser) error
//		cqrs.HandleQuery(r, h.byID)     // func(ctx, UserByID) (*User, error)
//	}
//
// and callers inject the buses:
//
//	err := commands.Execute(ctx, CreateUser{Email: e})
//	u, err := cqrs.Ask[*User](ctx, queries, UserByID{ID: id})
//
// Every command and query type has exactly one handler; a second one fails at
// startup. Commands return only an error. Events are package events: publish
// them with *events.Emitter from a command handler.
package cqrs

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime/debug"
	"sync"

	"github.com/dygoframework/dygo"
)

// Handlers is a provider that registers command and query handlers. CQRS is
// called once, during OnModuleInit of the buses.
type Handlers interface {
	CQRS(r *Registry)
}

// ErrNoHandler is returned for a command or query nobody handles.
var ErrNoHandler = errors.New("cqrs: no handler")

// Handler is a command or query handler with the types erased, as seen by
// [Middleware]. For a command the result is nil.
type Handler func(ctx context.Context, msg any) (any, error)

// Middleware wraps every handler of a bus, for logging, metrics or a
// transaction per command.
type Middleware func(next Handler) Handler

// Registry is what [Handlers] providers register through.
type Registry struct {
	b    *buses
	errs *[]error
}

// HandleCommand registers the handler of command type C.
func HandleCommand[C any](r *Registry, fn func(ctx context.Context, cmd C) error) {
	r.add(r.b.commands.b.register(reflect.TypeFor[C](), func(ctx context.Context, m any) (any, error) {
		return nil, fn(ctx, m.(C))
	}))
}

// HandleQuery registers the handler of query type Q.
func HandleQuery[Q, R any](r *Registry, fn func(ctx context.Context, q Q) (R, error)) {
	r.add(r.b.queries.b.register(reflect.TypeFor[Q](), func(ctx context.Context, m any) (any, error) {
		return fn(ctx, m.(Q))
	}))
}

func (r *Registry) add(err error) {
	if err != nil {
		*r.errs = append(*r.errs, err)
	}
}

// ModuleOption configures [Module].
type ModuleOption func(*buses)

// Use adds middleware to both buses. The first one is the outermost.
func Use(mw ...Middleware) ModuleOption {
	return func(b *buses) { b.mw = append(b.mw, mw...) }
}

// Module provides *CommandBus and *QueryBus. It is global; import it once in
// the root module.
func Module(opts ...ModuleOption) *dygo.Module {
	return &dygo.Module{
		Name:   "cqrs",
		Global: true,
		Providers: []dygo.Provider{
			dygo.Provide(func(ref *dygo.ModuleRef) *buses {
				b := newBuses(opts)
				b.ref = ref
				return b
			}),
			dygo.Provide(func(b *buses) *CommandBus { return b.commands }),
			dygo.Provide(func(b *buses) *QueryBus { return b.queries }),
		},
		Exports: []dygo.Token{dygo.TypeOf[*CommandBus](), dygo.TypeOf[*QueryBus]()},
	}
}

// New returns the buses outside of a module, for tests, with a function that
// registers the handlers of a [Handlers] value.
func New(opts ...ModuleOption) (*CommandBus, *QueryBus, func(Handlers) error) {
	b := newBuses(opts)
	return b.commands, b.queries, func(h Handlers) error {
		var errs []error
		h.CQRS(&Registry{b: b, errs: &errs})
		return errors.Join(errs...)
	}
}

type buses struct {
	ref      *dygo.ModuleRef
	mw       []Middleware
	commands *CommandBus
	queries  *QueryBus
}

func newBuses(opts []ModuleOption) *buses {
	b := &buses{}
	for _, o := range opts {
		o(b)
	}
	b.commands = &CommandBus{bus{kind: "command", mw: b.mw, handlers: map[reflect.Type]Handler{}}}
	b.queries = &QueryBus{bus{kind: "query", mw: b.mw, handlers: map[reflect.Type]Handler{}}}
	return b
}

// OnModuleInit asks every [Handlers] provider for its handlers.
func (b *buses) OnModuleInit(context.Context) error {
	var errs []error
	for _, h := range dygo.Discover[Handlers](b.ref) {
		h.CQRS(&Registry{b: b, errs: &errs})
	}
	return errors.Join(errs...)
}

type bus struct {
	kind     string
	mw       []Middleware
	mu       sync.RWMutex
	handlers map[reflect.Type]Handler
}

func (b *bus) register(t reflect.Type, h Handler) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, dup := b.handlers[t]; dup {
		return fmt.Errorf("cqrs: %s %s has two handlers", b.kind, t)
	}
	for i := len(b.mw) - 1; i >= 0; i-- {
		h = b.mw[i](h)
	}
	b.handlers[t] = h
	return nil
}

func (b *bus) dispatch(ctx context.Context, msg any) (out any, err error) {
	if msg == nil {
		return nil, fmt.Errorf("cqrs: nil %s", b.kind)
	}
	b.mu.RLock()
	h, ok := b.handlers[reflect.TypeOf(msg)]
	b.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w for %s %T", ErrNoHandler, b.kind, msg)
	}
	defer func() {
		if v := recover(); v != nil {
			out, err = nil, fmt.Errorf("cqrs: %s %T panicked: %v\n%s", b.kind, msg, v, debug.Stack())
		}
	}()
	return h(ctx, msg)
}

// CommandBus runs commands.
type CommandBus struct{ b bus }

// Execute runs the handler of cmd's type.
func (c *CommandBus) Execute(ctx context.Context, cmd any) error {
	_, err := c.b.dispatch(ctx, cmd)
	return err
}

// QueryBus answers queries. Use [Ask] for a typed result.
type QueryBus struct{ b bus }

// Execute runs the handler of q's type.
func (q *QueryBus) Execute(ctx context.Context, query any) (any, error) {
	return q.b.dispatch(ctx, query)
}

// Ask runs the handler of q's type and returns its result as R.
func Ask[R any](ctx context.Context, bus *QueryBus, q any) (R, error) {
	var zero R
	out, err := bus.Execute(ctx, q)
	if err != nil {
		return zero, err
	}
	if out == nil {
		return zero, nil
	}
	r, ok := out.(R)
	if !ok {
		return zero, fmt.Errorf("cqrs: query %T returned %T, not %s", q, out, reflect.TypeFor[R]())
	}
	return r, nil
}
