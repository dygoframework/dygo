package dygo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"reflect"
	"sync"
	"syscall"
	"time"
)

// App is a running application context: every singleton is built and every
// startup hook has run.
type App struct {
	c       *container
	opts    options
	inited  []*node
	closeMu sync.Mutex
	closed  bool
	drain   func(context.Context) error

	transports []*transportState // guarded by closeMu

	routeInfos []RouteInfo // set by Create, for the CLI inspection
}

// Option configures [CreateApplicationContext].
type Option func(*options)

type options struct {
	overrides       []*override
	log             *slog.Logger
	shutdownTimeout time.Duration
	http            httpOptions

	contextIDStrategy ContextIDStrategy
}

// WithOverride replaces the provider registered under tok, in every module that
// provides it, with p. The original provider is not built when all of its
// tokens are overridden. Mostly used by tests; see package dygotest.
func WithOverride(tok Token, p Provider) Option {
	return func(o *options) { o.overrides = append(o.overrides, &override{tok: tok, p: p}) }
}

// WithLogger sets the logger the framework writes to. By default nothing is logged.
func WithLogger(l *slog.Logger) Option {
	return func(o *options) { o.log = l }
}

// WithShutdownTimeout bounds how long [App.Run] waits for shutdown hooks after a
// signal. The default is 10 seconds.
func WithShutdownTimeout(d time.Duration) Option {
	return func(o *options) { o.shutdownTimeout = d }
}

// CreateApplicationContext scans the module tree from root, validates the whole
// dependency graph, builds every singleton, and runs OnModuleInit and
// OnApplicationBootstrap. Wiring problems are reported together in a
// [*BootError]. ctx is passed to constructors that take a context.Context and
// to the startup hooks.
func CreateApplicationContext(ctx context.Context, root *Module, opts ...Option) (*App, error) {
	return createApp(ctx, root, opts, nil)
}

func newOptions(opts []Option) options {
	o := options{log: slog.New(slog.DiscardHandler), shutdownTimeout: 10 * time.Second}
	o.http.bodyLimit = defaultBodyLimit
	for _, fn := range opts {
		fn(&o)
	}
	return o
}

// createApp builds the application. beforeInit runs after every singleton is
// built and before the startup hooks; the HTTP layer compiles routes there so
// that route errors are reported like wiring errors.
func createApp(ctx context.Context, root *Module, opts []Option, beforeInit func(*App) []error) (*App, error) {
	o := newOptions(opts)
	insp := inspectionFromEnv()
	c := newContainer(ctx, o.log, o.overrides)
	c.strategy = o.contextIDStrategy
	if !c.build(root) {
		if insp != nil {
			return nil, insp.write(o.log, c, nil, c.bootError())
		}
		return nil, c.bootError()
	}
	if insp != nil && (!insp.routes || beforeInit == nil) {
		return nil, insp.write(o.log, c, nil, nil)
	}
	if err := c.instantiate(); err != nil {
		if insp != nil {
			return nil, insp.write(o.log, c, nil, &BootError{Problems: []error{err}})
		}
		return nil, &BootError{Problems: []error{err}}
	}
	c.booted = true
	app := &App{c: c, opts: o}
	c.app = app
	if beforeInit != nil {
		errs := beforeInit(app)
		if insp != nil {
			var bootErr error
			if len(errs) > 0 {
				bootErr = &BootError{Problems: errs}
			}
			return nil, insp.write(o.log, c, app.routeInfos, bootErr)
		}
		if len(errs) > 0 {
			return nil, &BootError{Problems: errs}
		}
	}
	inited, err := runInit(ctx, c.hookOrder())
	app.inited = inited
	if err != nil {
		if serr := app.Shutdown(ctx, ""); serr != nil {
			err = errors.Join(err, fmt.Errorf("shutting down after failed start: %w", serr))
		}
		return nil, &BootError{Problems: []error{err}}
	}
	o.log.Info("dygo: application context ready", "modules", len(c.modules), "instances", len(c.built))
	return app, nil
}

// Resolver is anything providers can be looked up from: an [*App] or a [*ModuleRef].
type Resolver interface {
	Get(tok Token) (any, error)
}

// Get returns the instance for tok. It first looks from the root module, then
// in every module. It returns
// [ErrNotFound] or [ErrAmbiguous] when the token does not match exactly one
// provider.
func (a *App) Get(tok Token) (any, error) {
	k := tok.key()
	if n, err := a.c.visible(a.c.root, k); err == nil && n != nil {
		return a.c.instanceOf(n)
	}
	var found []*node
	for _, mi := range a.c.modules {
		if n := mi.local[k]; n != nil && !containsNode(found, n) {
			found = append(found, n)
		}
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("%w for %s", ErrNotFound, tok)
	case 1:
		return a.c.instanceOf(found[0])
	default:
		return nil, fmt.Errorf("%w for %s: provided in %d modules; resolve it through that module's ModuleRef", ErrAmbiguous, tok, len(found))
	}
}

func containsNode(ns []*node, n *node) bool {
	for _, x := range ns {
		if x == n {
			return true
		}
	}
	return false
}

// Get resolves type T from r.
func Get[T any](r Resolver) (T, error) {
	return getAs[T](r, TypeOf[T]())
}

// MustGet is [Get] that panics on error. Use it in main and tests.
func MustGet[T any](r Resolver) T {
	v, err := Get[T](r)
	if err != nil {
		panic(err)
	}
	return v
}

// GetNamed resolves a named token from r.
func GetNamed[T any](r Resolver, tok NamedToken[T]) (T, error) {
	return getAs[T](r, tok)
}

func getAs[T any](r Resolver, tok Token) (T, error) {
	var zero T
	v, err := r.Get(tok)
	if err != nil || v == nil {
		return zero, err
	}
	t, ok := v.(T)
	if !ok {
		return zero, fmt.Errorf("dygo: %s resolved to %T", tok, v)
	}
	return t, nil
}

// ProviderInfo describes one built singleton, for discovery.
type ProviderInfo struct {
	Module     string
	Type       reflect.Type
	Source     string
	Instance   any
	Controller bool
}

// Providers lists every built singleton provider and controller in build order.
func (a *App) Providers() []ProviderInfo {
	out := make([]ProviderInfo, 0, len(a.c.built))
	for _, n := range a.c.built {
		out = append(out, ProviderInfo{
			Module: n.mod.name, Type: n.out, Source: n.p.src,
			Instance: n.instance.Interface(), Controller: n.controller,
		})
	}
	return out
}

// Controllers returns every controller instance, in module level order.
func (a *App) Controllers() []any {
	var out []any
	for _, mi := range a.c.modulesByLevel() {
		for _, n := range mi.controllers {
			out = append(out, n.instance.Interface())
		}
	}
	return out
}

// ProvidersImplementing returns every built singleton whose instance implements
// or is of type T. Transports use it to find their handlers (controllers,
// gateways, message handlers). Code inside a provider uses [Discover].
func ProvidersImplementing[T any](a *App) []T { return implementing[T](a.c) }

// Discover is [ProvidersImplementing] for a provider: it returns every built
// singleton in the application that implements or is of type T. Call it from
// OnModuleInit or later, when every singleton is built. Packages such as
// events and schedule use it to find their listeners and jobs.
func Discover[T any](ref *ModuleRef) []T { return implementing[T](ref.c) }

func implementing[T any](c *container) []T {
	var out []T
	seen := map[any]bool{}
	for _, n := range c.built {
		v, ok := n.instance.Interface().(T)
		if !ok {
			continue
		}
		if n.instance.Comparable() {
			if seen[n.instance.Interface()] {
				continue
			}
			seen[n.instance.Interface()] = true
		}
		out = append(out, v)
	}
	return out
}

// Shutdown runs OnModuleDestroy, BeforeApplicationShutdown and
// OnApplicationShutdown on every initialized instance, in reverse startup order.
// It is safe to call more than once; later calls do nothing.
func (a *App) Shutdown(ctx context.Context, signal string) error {
	a.closeMu.Lock()
	defer a.closeMu.Unlock()
	if a.closed {
		return nil
	}
	a.closed = true
	drain := func(ctx context.Context) error {
		var err error
		if a.drain != nil {
			err = a.drain(ctx)
		}
		return errors.Join(err, a.closeTransports(ctx), a.c.releaseAll(ctx, signal))
	}
	err := runShutdown(ctx, a.inited, signal, drain)
	a.c.log.Info("dygo: application context closed", "signal", signal)
	return err
}

// Close is Shutdown without a signal.
func (a *App) Close(ctx context.Context) error { return a.Shutdown(ctx, "") }

// Run blocks until ctx is cancelled or the process receives SIGINT or
// SIGTERM, then shuts the application down within the shutdown timeout.
// Connected transports are started first (see [App.ConnectMicroservice]).
func (a *App) Run(ctx context.Context) error {
	if err := a.StartAllMicroservices(ctx); err != nil {
		return errors.Join(err, a.shutdownWithTimeout(ctx, ""))
	}
	name, _ := a.waitForStop(ctx, nil)
	return a.shutdownWithTimeout(ctx, name)
}

// waitForStop blocks until a signal arrives, ctx is done, or failed delivers
// an error. It returns the signal name, if any, and the error from failed.
func (a *App) waitForStop(ctx context.Context, failed <-chan error) (string, error) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigs)
	select {
	case s := <-sigs:
		name := signalName(s)
		a.c.log.Info("dygo: received signal, shutting down", "signal", name)
		return name, nil
	case <-ctx.Done():
		return "", nil
	case err := <-failed:
		return "", err
	}
}

func (a *App) shutdownWithTimeout(ctx context.Context, signal string) error {
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.opts.shutdownTimeout)
	defer cancel()
	return a.Shutdown(sctx, signal)
}

func signalName(s os.Signal) string {
	switch s {
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGTERM:
		return "SIGTERM"
	default:
		return s.String()
	}
}
