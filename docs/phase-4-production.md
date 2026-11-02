# Phase 4: Production essentials

**Goal:** what every real service needs on day one, each as an optional module.

**Depends on:** phases 1 to 3.

## Packages

### `dygo/config`
- `config.ForRoot[T any](opts ...Option) *dygo.Module`: loads `.env` files and the environment into a typed struct `T` using `env:"DB_URL"` and `default:"..."` tags, validates it with `validate.Schema`, exports `T` as a singleton. Fails boot on invalid config.
- `config.ForFeature[T]` for namespaced config (`registerAs`) with a prefix: `env:"prefix=USERS_"`.
- `Global: true` by default, like `isGlobal`.

### `dygo/logger`
- Provides `*slog.Logger` app wide. Text in dev, JSON in prod. Structured attributes are the default.
- Request logger middleware with request ID, method, path, status, latency.

### `dygo/openapi`
- Walks the phase 2 route table: method, path, `In` tags → parameters and request body, `Out` → response schema, `errs` codes → error responses.
- Route options `openapi.Summary`, `openapi.Tags`, `openapi.Deprecated`.
- Serves `/openapi.json` and a docs UI at `/docs` (Scalar or Swagger UI from an embedded file).
- Validator tags map to JSON Schema constraints (`min`, `max`, `email`, `uuid`).

### `dygo/observe`
- OpenTelemetry tracer and meter providers as DI providers.
- Spans named in framework terms: `UserController.FindOne`, `UserService` (opt-in wrapper), lifecycle phases.
- Hooks into the pipeline and lifecycle; nothing is monkey patched. `WithInstrument(observe.Module(...))` option on `Create`.

### `dygo/health`
- `/health/live`, `/health/ready`; indicators as a provider group `health.Indicators`; built-in SQL ping and HTTP ping indicators.

### Security and HTTP extras
- CORS (`WithCORS`), security headers (Helmet equivalent), CSRF for cookie sessions, request body size limit, compression, request ID.
- `dygo/ratelimit`: guard backed by `x/time/rate` (memory) or Redis.

## Tasks
1. config, logger.
2. openapi (largest item).
3. observe with OpenTelemetry.
4. health, security extras, ratelimit.
5. Dockerfile and example deployment for `examples/http-users`.

## Exit criteria
- The example runs in Docker with typed config, JSON logs, `/docs`, traces exported to an OTLP collector, and health checks.

## Status: done (2026-10-30)

### What was built

| Package | What it does |
|---|---|
| `config` | `Load[T]`, `ForRoot[T]` (global), `ForFeature[T]`; `env`, `default`, `required`, `envPrefix` tags; `.env` files (env vars win); `Prefix`, `Env` (tests), `Validate(schema)`. All problems reported at once; a bad config fails the boot. |
| `logger` | `New(Options)`: JSON when `APP_ENV=production`, level from `LOG_LEVEL`; `Module(log)` provides `*slog.Logger`. |
| `middleware` | `RequestID`, `Logger` (one line per request with status, bytes, duration_ms, request_id), `CORS`, `SecureHeaders` (Helmet defaults for APIs), `CSRF` (double submit cookie), `Compress` (gzip, skips SSE and compressed types, keeps Flush and Hijack). |
| `health` | `Module(Options)`: `/health/live`, `/health/ready` in the terminus body shape; indicators via the `health.Indicators` group; `Ping` (any `PingContext`), `HTTP`, `Func`. Ready answers 503 once shutdown starts. |
| `openapi` | `Generate(routes, opts)` and `Module(opts)` serving `/openapi.json` and Swagger UI at `/docs`. Parameters from In tags, body from `Body`, responses from Out, components for named types (generics and recursion handled), validator tags to JSON Schema, `description` and `example` tags. Route options `Summary`, `Description`, `Tags`, `Deprecated`, `OperationID`, `Response`, `ResponseOf[T]`, `Exclude`, `Public`; `BearerAuth`. |
| `ratelimit` | `Guard(Options)` with per route counters per client, `Limit(n, window)` and `Skip()` metadata, rate limit headers, 429 with `Retry-After`; `Store` interface with a sharded `MemoryStore`. |
| `observe` (separate module) | `Module(Options)`: tracer and meter providers, OTLP/HTTP export from `OTEL_EXPORTER_OTLP_ENDPOINT`, W3C trace context continued, a server span per request named `Controller.Handler`, an `http.server.request.duration` histogram, `observe.Start` for your own spans, flush on shutdown. |

Core additions: `RouteInfo.Input`, `Output`, `Status`, `Metadata`; `dygo.RoutesOf(ctx)`; version neutral routes without versioning ([D26](decisions.md)).

### Example and Docker
`examples/http-users` is now its own module ([D27](decisions.md)) with typed config, JSON logs, all the middleware, health checks, OpenAPI with bearer auth and public routes, a global rate limit, and tracing. `examples/http-users/Dockerfile` (distroless, 27 MB image) and `compose.yaml` (the app plus Jaeger).

Checked on 2026-10-30: the image builds, `/health/ready`, `/docs` and the API answer in the container, logs are JSON, traces for `users.Controller.Create` and others show up in Jaeger, and `docker stop` shuts down in about 0.2 s.

### Differences from the plan
- No `WithCORS` option: all HTTP extras are middleware ([D25](decisions.md)).
- Rate limiting has its own small store instead of `x/time/rate`; a Redis store is left for later behind the `Store` interface.
- The docs UI is Swagger UI from a pinned CDN URL, not an embedded file ([D24](decisions.md)).
- `observe` instruments routes through an interceptor in the `AppInterceptors` group; lifecycle phase spans and a service wrapper were not built (use `observe.Start` in your own code).
- Validator tags map to JSON Schema in `openapi` by reading the `validate` tag directly, so the playground adapter is not required for the docs.

### Review pass (2026-10-30)

`BenchmarkStack` in `middleware/` runs the example's middleware (RequestID, Logger, SecureHeaders, CORS, Compress) around a small JSON response.

| | ns/op added over the bare handler | allocs/op added |
|---|---|---|
| Before | about 41,000 | 34 |
| After | about 4,500 | 20 |

Fixes:
- **Compress** gzipped every response, and resetting the compressor (about 37 µs) dominated small JSON bodies, which could even come out larger. It now holds back the first 1 KiB and sends smaller bodies as they are (`DefaultCompressMinSize`, or `CompressMin(n)`); Flush ends buffering so streams still stream. Writers are pooled.
- **Static headers** (SecureHeaders, CORS, Vary) are built once and shared instead of allocating a slice per header per response.
- **middleware.Logger** skips building the log line when its level is off, keeps attributes on the stack, and keeps `io.ReaderFrom` so file responses still use sendfile. The core response writer gained `ReadFrom` for the same reason.
- **ratelimit**: the fixed window let a client send twice the limit across a window boundary; the store now uses GCRA (one timestamp per client and route, smooth refill) ([D28](decisions.md)). With `TrustProxy`, the client IP was the leftmost X-Forwarded-For entry, which the client controls; it is now counted from the right (`ProxyHops`, default 1).
- **health**: a panicking indicator ran on its own goroutine and would have crashed the process; it is now reported as down.
- **observe**: a status set with `dygo.SetStatus` (such as the 503 from `/health/ready`) was recorded as 200; the new `dygo.ResponseStatus(ctx)` gives interceptors the real status.
