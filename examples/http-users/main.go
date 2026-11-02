// Command http-users is the acceptance app for phases 2 to 4: a users CRUD
// API with typed handlers, URI versioning, a global prefix, request scope,
// SSE, graceful shutdown, a global auth guard with roles, input validation,
// typed config, JSON logs, OpenAPI docs at /docs, health checks, rate limits,
// security headers and OpenTelemetry traces.
//
// It is its own Go module (it uses the observe module). From this folder:
//
//	go run .
//	open http://localhost:9900/docs
//	curl -X POST localhost:9900/api/v1/users -H 'Authorization: Bearer admin-token' \
//	     -H 'Content-Type: application/json' -d '{"email":"a@b.c","name":"Ada Lovelace"}'
//	curl -H 'Authorization: Bearer user-token' localhost:9900/api/v1/users
//	curl -H 'Authorization: Bearer user-token' localhost:9900/api/v1/users/events   # then create users
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/config"
	"github.com/dygoframework/dygo/examples/http-users/auth"
	"github.com/dygoframework/dygo/examples/http-users/users"
	"github.com/dygoframework/dygo/health"
	"github.com/dygoframework/dygo/logger"
	"github.com/dygoframework/dygo/middleware"
	"github.com/dygoframework/dygo/observe"
	"github.com/dygoframework/dygo/openapi"
	"github.com/dygoframework/dygo/ratelimit"
	"github.com/dygoframework/dygo/validate"
)

// AppModule is the root module. cfg and log are provided to every module.
func AppModule(cfg *Config, log *slog.Logger) *dygo.Module {
	return &dygo.Module{
		Name: "app",
		Imports: []*dygo.Module{
			logger.Module(log),
			auth.Module,
			users.Module,
			// Health checks and API docs are public and not rate limited.
			health.Module(health.Options{Use: []dygo.RouteOption{auth.Public(), ratelimit.Skip()}}),
			openapi.Module(openapi.Options{
				Info:       openapi.Info{Title: "Users API", Version: "1.0", Description: "The Dygo example app."},
				BearerAuth: true,
				Use:        []dygo.RouteOption{auth.Public(), ratelimit.Skip()},
			}),
			observe.Module(observe.Options{ServiceName: "http-users"}),
		},
		Providers: []dygo.Provider{dygo.Value(cfg)},
		Exports:   []dygo.Token{dygo.TypeOf[*Config]()},
	}
}

// Options are shared by main and the e2e test. The auth guard is global
// through the dygo.AppGuards group in auth.Module; tracing is global through
// observe.Module.
func Options(cfg *Config, log *slog.Logger) []dygo.Option {
	return []dygo.Option{
		dygo.WithLogger(log),
		dygo.WithGlobalPrefix("/api", "/health", "/docs", "/openapi.json"),
		dygo.WithVersioning(dygo.URIVersioning("1")),
		dygo.WithMiddleware(
			middleware.RequestID(),
			middleware.Logger(log),
			middleware.SecureHeaders(middleware.SecureHeadersOptions{}),
			middleware.CORS(middleware.CORSOptions{AllowOrigins: cfg.CORSOrigins, ExposeHeaders: []string{middleware.RequestIDHeader}}),
			middleware.Compress(),
		),
		dygo.WithGlobalGuards(ratelimit.Guard(ratelimit.Options{Limit: cfg.RateLimit, Window: cfg.RateWindow, TrustProxy: cfg.TrustProxy})),
		validate.Use(validate.Methods()),
	}
}

func main() {
	log := logger.New(logger.Options{}) // JSON when APP_ENV=production, level from LOG_LEVEL
	cfg, err := config.Load[Config](config.Files(".env.local", ".env"))
	if err != nil {
		log.Error("bad configuration", "err", err)
		os.Exit(1)
	}
	ctx := context.Background()
	app, err := dygo.Create(ctx, AppModule(cfg, log), Options(cfg, log)...)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, r := range app.Routes() {
		log.Debug("route", "route", r.String(), "handler", r.Handler)
	}
	if err := app.Listen(ctx, ":"+strconv.Itoa(cfg.Port)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
