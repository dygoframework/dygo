# Guide: production essentials

Config, logging, middleware, health checks, OpenAPI, rate limits and tracing. Everything is optional, and all of it is in `examples/http-users/main.go`.

## Config

```go
type Config struct {
    Port    int           `env:"PORT" default:"9900"`
    DBURL   string        `env:"DB_URL" required:"true"`
    Timeout time.Duration `env:"TIMEOUT" default:"5s"`
    Origins []string      `env:"CORS_ORIGINS"`          // comma separated
    Redis   RedisConfig   `envPrefix:"REDIS_"`          // REDIS_ADDR, REDIS_DB, ...
}

// Global module: every provider can take *Config.
Imports: []*dygo.Module{config.ForRoot[Config](config.Files(".env.local", ".env"))}

// Or load it yourself before Create (for the listen address, middleware options):
cfg, err := config.Load[Config](config.Files(".env"))
```

Real environment variables win over `.env` files. Every missing or bad value is reported together and stops the boot. `config.ForFeature[T](config.Prefix("USERS_"))` is a non-global module for one feature's settings; `config.Env(map)` replaces the environment in tests; `config.Validate(schema)` runs a validation schema after loading.

## Logging

```go
log := logger.New(logger.Options{})    // JSON when APP_ENV=production, level from LOG_LEVEL
app, err := dygo.Create(ctx, root, dygo.WithLogger(log), dygo.WithMiddleware(middleware.RequestID(), middleware.Logger(log)))
Imports: []*dygo.Module{logger.Module(log)} // providers can take *slog.Logger
```

`middleware.Logger` writes one line per request: method, path, status, bytes, duration_ms and request_id. `middleware.RequestIDFrom(ctx)` gives the ID anywhere.

## HTTP middleware

```go
dygo.WithMiddleware(
    middleware.RequestID(),
    middleware.Logger(log),
    middleware.SecureHeaders(middleware.SecureHeadersOptions{}), // Helmet defaults for an API
    middleware.CORS(middleware.CORSOptions{AllowOrigins: cfg.Origins, AllowCredentials: true}),
    middleware.CSRF(middleware.CSRFOptions{Secure: true}),       // only for cookie sessions
    middleware.Compress(),                                       // gzip bodies of 1 KiB or more; SSE streams are left alone
)
```

The first middleware is the outermost. They are plain `func(http.Handler) http.Handler`, so any Go middleware mixes in.

## Health checks

```go
Imports: []*dygo.Module{health.Module(health.Options{Use: []dygo.RouteOption{auth.Public()}})},
Providers: []dygo.Provider{
    dygo.Provide(func(db *sql.DB) health.Indicator { return health.Ping("db", db) }, dygo.Group(health.Indicators)),
},
```

`GET /health/live` is 200 while the process runs. `GET /health/ready` runs every indicator (concurrently, 3 s timeout) and answers 503 if one fails or once shutdown has started. Use `Options.Use` to mark the routes public for your auth guard, and exclude `/health` from a global prefix.

## OpenAPI

```go
Imports: []*dygo.Module{openapi.Module(openapi.Options{
    Info: openapi.Info{Title: "Users API", Version: "1.0"},
    BearerAuth: true,
})}

r = r.Prefix("/users").Use(openapi.Tags("users"), openapi.Response(401, "Missing token"))
r.Get("/{id}", dygo.Handle(c.Get), openapi.Summary("Get a user"), openapi.Response(404, "No such user"))
```

`GET /openapi.json` and Swagger UI at `GET /docs`. Nothing else to annotate: parameters come from the In tags, the body from `Body`, the response from Out. `validate:"..."`, `default`, `description` and `example` tags add constraints and docs. `openapi.Public()` removes the auth requirement from an operation; `openapi.Exclude()` hides a route. `openapi.Generate(app.Routes(), opts)` builds the document without serving it, for a file or a CI check.

## Rate limiting

```go
dygo.WithGlobalGuards(ratelimit.Guard(ratelimit.Options{Limit: 100, Window: time.Minute}))

r.Post("/login", dygo.Handle(c.Login), ratelimit.Limit(5, time.Minute))
r.Get("/status", h, ratelimit.Skip())
```

Each route counts separately per client IP, and requests refill evenly over the window (GCRA), so there is no burst at a window boundary. Behind a proxy, set `TrustProxy: true` and `ProxyHops` to the number of proxies you run (default 1): the client IP is read from X-Forwarded-For counting from the right, because the left part is whatever the client sent. `middleware.CompressMin(n)` changes the gzip threshold. Blocked requests get 429 with `Retry-After`. The default store is in memory; implement `ratelimit.Store` to share limits across instances.

## Tracing and metrics

```go
// go get github.com/dygoframework/dygo/observe (separate module)
Imports: []*dygo.Module{observe.Module(observe.Options{ServiceName: "users-api"})}
```

With `OTEL_EXPORTER_OTLP_ENDPOINT=http://collector:4318`, every request becomes a server span named `users.Controller.Get` (continuing an incoming `traceparent`), and `http.server.request.duration` is recorded. In your own code: `ctx, span := observe.Start(ctx, "UserService.Create"); defer span.End()`. Spans are flushed on shutdown.

## Docker

```sh
docker build -f examples/http-users/Dockerfile -t http-users .      # from the repository root
docker compose -f examples/http-users/compose.yaml up --build      # app on :9900, Jaeger on :16686
```

The image is distroless and runs as non-root (about 27 MB). `docker stop` sends SIGTERM, which drains requests, runs the shutdown hooks and flushes traces.
