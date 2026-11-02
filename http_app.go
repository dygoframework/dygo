package dygo

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/dygoframework/dygo/errs"
)

// HTTPApp is an application with an HTTP server.
// It embeds the application context, so Get, Providers and Shutdown work as
// on [App].
type HTTPApp struct {
	*App
	handler   http.Handler
	routes    []RouteInfo
	errWriter errs.Writer
	stopping  chan struct{}
	stopOnce  sync.Once
}

// Create builds the application like [CreateApplicationContext], then asks
// every controller for its routes and compiles them. Route problems (bad
// patterns, duplicates, conflicts, bad handler input types) are reported in
// the same [*BootError] as wiring problems, before any startup hook runs.
func Create(ctx context.Context, root *Module, opts ...Option) (*HTTPApp, error) {
	ha := &HTTPApp{stopping: make(chan struct{})}
	_, err := createApp(ctx, root, opts, func(a *App) []error {
		ha.App = a
		ha.errWriter = a.opts.http.errorWriter
		if ha.errWriter == nil {
			ha.errWriter = errs.DefaultWriter(a.c.log)
		}
		problems := ha.buildRoutes()
		a.routeInfos = ha.routes
		return problems
	})
	if err != nil {
		return nil, err
	}
	return ha, nil
}

// Handler returns the application's http.Handler, for httptest or a server
// you manage yourself.
func (a *HTTPApp) Handler() http.Handler { return a.handler }

// Routes lists every route, sorted by path.
func (a *HTTPApp) Routes() []RouteInfo { return a.routes }

// Listen serves on addr until ctx is cancelled or the process gets SIGINT or
// SIGTERM, then shuts down: OnModuleDestroy, BeforeApplicationShutdown, drain
// in-flight requests, OnApplicationShutdown. It returns nil after a clean
// shutdown.
func (a *HTTPApp) Listen(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return errors.Join(err, a.shutdownWithTimeout(ctx, ""))
	}
	return a.Serve(ctx, ln)
}

// Serve is Listen on an existing listener. Connected transports are started
// first (see [App.ConnectMicroservice]).
func (a *HTTPApp) Serve(ctx context.Context, ln net.Listener) error {
	if err := a.StartAllMicroservices(ctx); err != nil {
		_ = ln.Close()
		return errors.Join(err, a.shutdownWithTimeout(ctx, ""))
	}
	srv := &http.Server{
		Handler:           a.handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(a.c.log.Handler(), slog.LevelWarn),
	}
	for _, fn := range a.opts.http.serverFns {
		fn(srv)
	}
	a.closeMu.Lock()
	a.drain = func(sctx context.Context) error {
		a.stopOnce.Do(func() { close(a.stopping) })
		err := srv.Shutdown(sctx)
		if err != nil {
			_ = srv.Close()
		}
		return err
	}
	a.closeMu.Unlock()

	failed := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			failed <- err
		}
	}()
	a.c.log.Info("dygo: listening", "addr", ln.Addr().String(), "routes", len(a.routes))
	sig, serveErr := a.waitForStop(ctx, failed)
	return errors.Join(serveErr, a.shutdownWithTimeout(ctx, sig))
}
