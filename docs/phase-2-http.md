# Phase 2: HTTP layer (detailed plan)

**Goal:** controllers serve HTTP through typed handlers. The users CRUD from PLAN.md section 2 runs end to end, wiring and route mistakes are caught at boot, and shutdown drains in-flight requests.

**Depends on:** phase 1 (container, modules, `App.Controllers()`, lifecycle).
**Estimate:** about 3 weeks for one developer (task list in section 11).
**Status:** done (2026-10-29). See [section 16](#16-what-was-built) for results, benchmarks and differences from this plan. Decisions D10 to D16 in [decisions.md](decisions.md) are accepted.

---

## 1. Scope

**In phase 2**
- `dygo.Create` returning an HTTP app built on the phase 1 container.
- Controllers with a `Routes(r dygo.Router)` method; prefixes, host routing, global prefix with exclusions, versioning (URI, header, media type).
- Typed handlers `dygo.Handle[In, Out]` with a binder compiled at boot (path, query, header, cookie, JSON/form/raw body, multipart files).
- Responses: JSON by default, status codes, headers, redirects, `Responder` types (stream, file, SSE, no content).
- `dygo/errs` package and a default error writer with a JSON body.
- Route table checks at boot: invalid patterns, duplicates, ambiguous patterns.
- Request scoped providers.
- Net/http middleware at app, controller and route level.
- Graceful shutdown in a fixed order, with in-flight request draining.
- `dygotest` HTTP helpers.

**Not in phase 2 (phase 3)**: guards, interceptors, pipes, exception filters as pluggable enhancers, typed route metadata, validation. Phase 2 keeps a fixed internal chain, but it is built as a list of stages so phase 3 inserts enhancers without changing handler code (section 7).

---

## 2. What the developer writes

> For a step by step walkthrough of GET, POST, PUT, PATCH and DELETE handlers (path parameters, JSON bodies, partial updates, status codes), see the [HTTP guide](guide-http.md#step-by-step-a-crud-controller-get-post-put-patch-delete).

```go
// users/controller.go
type Controller struct{ svc *Service }

func NewController(svc *Service) *Controller { return &Controller{svc: svc} }

type GetIn struct {
    ID     uuid.UUID `path:"id"`                 // parsed by encoding.TextUnmarshaler
    Expand bool      `query:"expand"`
}

type ListIn struct {
    Page  int    `query:"page"  default:"1"`
    Limit int    `query:"limit" default:"20"`
    Sort  string `query:"sort"`
}

type CreateIn struct {
    Tenant string `header:"X-Tenant"`
    Body   struct {
        Email string `json:"email"`
        Name  string `json:"name"`
    } `body:"json"`
}

func (c *Controller) Routes(r dygo.Router) {
    r = r.Prefix("/users")
    r.Get("/", dygo.Handle(c.List))
    r.Get("/{id}", dygo.Handle(c.Get))
    r.Post("/", dygo.Handle(c.Create))            // 201 by default for POST
    r.Delete("/{id}", dygo.Handle(c.Delete), dygo.HTTPCode(204))
}

func (c *Controller) Get(ctx context.Context, in GetIn) (*User, error) {
    u, err := c.svc.Find(ctx, in.ID)
    if errors.Is(err, ErrNoUser) {
        return nil, errs.NotFound("user not found", errs.Code("USER_NOT_FOUND"))
    }
    return u, err
}
```

```go
// users/module.go
var Module = &dygo.Module{
    Name:        "users",
    Providers:   []dygo.Provider{dygo.Provide(NewService)},
    Controllers: []dygo.Provider{dygo.Provide(NewController)},
}
```

```go
// main.go
func main() {
    ctx := context.Background()
    app, err := dygo.Create(ctx, AppModule,
        dygo.WithGlobalPrefix("/api", "/health"),   // exclude /health
        dygo.WithVersioning(dygo.URIVersioning("1")), // default version v1
    )
    if err != nil {
        fmt.Fprintln(os.Stderr, err)
        os.Exit(1)
    }
    if err := app.Listen(ctx, ":9900"); err != nil { // blocks; drains on SIGINT/SIGTERM
        fmt.Fprintln(os.Stderr, err)
        os.Exit(1)
    }
}
```

---

## 3. Package layout

The HTTP API lives in the **root package** so user code says `dygo.Handle`, `dygo.Router`, `dygo.Create`. Implementation details go in `internal/`. A sub-package named `http` would clash with `net/http` in every file that uses both, so there is none ([D10](decisions.md)).

```
http_app.go          Create, HTTPApp, Listen, Serve, Handler, shutdown order
router.go            Router interface, route builder, prefixes, host, Use
route_options.go     RouteOption, HTTPCode, Header, Redirect, WithMiddleware, Version
handler.go           Handler, Handle, HandleNoInput, HandleRaw
response.go          Responder, NoContent, Stream, File, SSE, RedirectTo, response helpers on ctx
versioning.go        VersioningOptions, URI/Header/MediaType/Custom, VersionNeutral
request_scope.go     RequestScoped scope, per-request container in context
routetable.go        route table, pattern validation, conflict reporting, dump for `dygo routes`
internal/binder/     compiled binder: plan per In type, converters, BindError
internal/pattern/    path joining, ":id" detection, pattern normalization
errs/                HTTPError, constructors, options, default error writer
dygotest/http.go     CompileHTTP, request helpers, JSON assertions
examples/http-users/ phase 2 acceptance app
```

---

## 4. Public API

```go
// ---- app ----
func Create(ctx context.Context, root *Module, opts ...Option) (*HTTPApp, error)

type HTTPApp struct {
    *App // phase 1 application context: Get, Providers, Shutdown...
}
func (a *HTTPApp) Listen(ctx context.Context, addr string) error   // ListenAndServe + signals + graceful drain
func (a *HTTPApp) Serve(ctx context.Context, ln net.Listener) error // same, on an existing listener
func (a *HTTPApp) Handler() http.Handler                            // full stack, for httptest or custom servers
func (a *HTTPApp) Routes() []RouteInfo                              // for logs, `dygo routes`, OpenAPI (phase 4)

// New Options (all also accepted by CreateApplicationContext, which ignores the HTTP ones)
func WithGlobalPrefix(prefix string, exclude ...string) Option
func WithVersioning(v VersioningOptions) Option
func WithMiddleware(mw ...Middleware) Option          // app level, outermost
func WithHTTPServer(fn func(*http.Server)) Option      // timeouts, TLS, ErrorLog
func WithBodyLimit(n int64) Option                     // default 1 MiB, per route override with BodyLimit
func WithErrorWriter(w errs.Writer) Option             // replaced by filters in phase 3

type Middleware = func(http.Handler) http.Handler

// ---- controllers and router ----
type Controller interface{ Routes(r Router) }

type Router interface {
    Prefix(p string) Router                          // returns a child router; parent unchanged
    Host(h string) Router                            // "api.example.com" or "{tenant}.example.com"
    Version(v ...string) Router                      // versions for routes below
    Use(opts ...RouteOption) Router                  // options applied to every route below
    Get(path string, h Handler, opts ...RouteOption)
    Post(path string, h Handler, opts ...RouteOption)
    Put(path string, h Handler, opts ...RouteOption)
    Patch(path string, h Handler, opts ...RouteOption)
    Delete(path string, h Handler, opts ...RouteOption)
    Head(path string, h Handler, opts ...RouteOption)
    Options(path string, h Handler, opts ...RouteOption)
    All(path string, h Handler, opts ...RouteOption) // any method
}

// ---- handlers ----
type Handler interface{ /* unexported methods: only built by the functions below */ }

func Handle[In, Out any](fn func(context.Context, In) (Out, error)) Handler
func HandleNoInput[Out any](fn func(context.Context) (Out, error)) Handler
func HandleRaw(fn func(w http.ResponseWriter, r *http.Request) error) Handler

// ---- route options ----
type RouteOption interface{ /* unexported */ }
func HTTPCode(code int) RouteOption
func Header(key, value string) RouteOption                 // static response header
func Redirect(url string, code int) RouteOption            // like @Redirect
func WithRouteMiddleware(mw ...Middleware) RouteOption
func BodyLimit(n int64) RouteOption
func Version(v ...string) RouteOption
func Options(opts ...RouteOption) RouteOption              // compose, like applyDecorators

// ---- inside a typed handler ----
func Request(ctx context.Context) *http.Request            // escape hatch
func SetStatus(ctx context.Context, code int)
func SetHeader(ctx context.Context, key, value string)
func SetCookie(ctx context.Context, c *http.Cookie)
func RouteOf(ctx context.Context) RouteInfo                // method, pattern, controller, handler name

// ---- responses ----
type Responder interface{ Respond(w http.ResponseWriter, r *http.Request) error }
type NoContent struct{}                                     // 204
func Stream(r io.Reader, contentType string) Responder       // StreamableFile
func File(path string) Responder                            // http.ServeContent, ranges, ETag
func SSE[T any](events iter.Seq[T]) Responder               // text/event-stream, JSON data lines
func RedirectTo(url string, code int) Responder             // dynamic redirect

// ---- request scope ----
const RequestScoped Scope = 2
type Scoped[T any] struct{ /* unexported */ }
func (s Scoped[T]) Get(ctx context.Context) (T, error)
type OnRequestEnd interface{ OnRequestEnd(ctx context.Context) }
```

Notes:
- `Router.Get` and friends return nothing. Chaining from one call to the next made it too easy to forget that `Prefix` returns a child; `r = r.Prefix(...)` is explicit.
- `Handler` is an interface with unexported methods so `Handle` can carry compiled binding plans while user code only passes it around.

---

## 5. Routing

### 5.1 Paths and patterns ([D11](decisions.md))
- Dygo uses **Go's pattern syntax**: `/users/{id}`, `/files/{path...}`, `/{$}` for an exact trailing slash. A `:id` segment is **rejected at boot** with a hint (`use {id}`), because `ServeMux` would silently treat `:id` as a literal (verified on Go 1.24).
- The full path is `globalPrefix + version prefix + controller prefixes + route path`, joined with exactly one slash between parts. A route path of `/` under a prefix means the prefix itself (`GET /users` exact), not a subtree.
- Trailing slashes are strict: `/users` and `/users/` are different. A `WithTrailingSlashRedirect()` option can be added later if needed.
- `GET` routes also answer `HEAD`, as `ServeMux` already does. `All` registers without a method.

### 5.2 Route table and conflict checks ([D12](decisions.md))
Routes are collected into a table first, then registered on a fresh `http.ServeMux`.

1. Validate each pattern (`:param`, empty segments, unknown method, wildcard not at the end).
2. Duplicates: same host + method + pattern after normalizing parameter names (`/users/{id}` equals `/users/{name}`), unless they differ by version (handled by the version dispatcher, 5.3).
3. Register on the mux inside `recover()`. `ServeMux` panics on ambiguous patterns (for example `/a/{x}` versus `/{y}/b`) and its message explains the overlap; that message becomes a `BootError` problem naming both controllers.

All problems are collected and reported together, like phase 1. There is no "warn" policy: `ServeMux` cannot serve a conflicting table, so conflicts are always errors. Because `ServeMux` already matches by **specificity** (`/users/me` beats `/users/{id}` regardless of order), Dygo gets specificity matching for free and has no shadowing problem.

### 5.3 Versioning
```go
dygo.WithVersioning(dygo.URIVersioning("1"))                 // /v1/users, default version 1
dygo.WithVersioning(dygo.HeaderVersioning("X-API-Version", "1"))
dygo.WithVersioning(dygo.MediaTypeVersioning("v", "1"))       // Accept: application/json;v=2
dygo.WithVersioning(dygo.CustomVersioning(func(*http.Request) []string, "1"))
dygo.VersionNeutral                                         // route matches any version
```
- URI versioning adds `/v{n}` to the pattern, so `ServeMux` does the work.
- Header, media type and custom versioning register **one** mux pattern per method+path and put a small dispatcher behind it that picks the route by the extracted version (exact match, then `VersionNeutral`, then 404).
- Version precedence: route option, then `Router.Version`, then the default version.

### 5.4 Middleware
Plain `func(http.Handler) http.Handler`, so any Go middleware works. Order from outside in: app (`WithMiddleware`) → controller (`r.Use(dygo.WithRouteMiddleware(...))`) → route. Module level middleware with route matching is phase 3.

---

## 6. Binding (`internal/binder`)

### 6.1 Compile once, run per request
`Handle[In, Out]` builds a **plan** for `In` the first time it sees the type (cached per type, guarded by `sync.Map`): a flat slice of setters `{fieldIndex []int, source, name, converter, default, required}`. Per request the binder walks the slice; no tag parsing or name lookups happen on the hot path. Embedded (anonymous) structs are flattened, so a shared `Pagination` struct can be embedded in many inputs.

A bad `In` type (unknown tag, unsupported field type, two `body` fields, `path:"id"` with no `{id}` in the route pattern) is a **boot error**, because handlers are compiled when the route table is built.

### 6.2 Sources
| Tag | Source | Field types |
|---|---|---|
| `path:"id"` | `r.PathValue("id")` | scalar |
| `query:"tag"` | `r.URL.Query()` | scalar, slice (repeated keys), pointer |
| `header:"X-Tenant"` | `r.Header` (canonical) | scalar, slice |
| `cookie:"sid"` | `r.Cookie` | scalar, `*http.Cookie` |
| `body:"json"` | JSON body, `json.Decoder` with size limit | any struct, map, slice |
| `body:"form"` | `application/x-www-form-urlencoded` | struct with `form:"x"` fields |
| `body:"raw"` | raw body | `[]byte`, `string`, `io.Reader` |
| `form:"name"` | multipart field | scalar |
| `file:"avatar"` | multipart file | `*multipart.FileHeader`, `[]*multipart.FileHeader` |

Extra tags: `default:"10"` (replaces `DefaultValuePipe`), `required:"true"` for query/header/cookie (missing value is a 400). Path params are always required.

### 6.3 Converters (no parse pipes needed)
`string`, `bool`, all ints and uints (with overflow check), floats, `time.Time` (RFC 3339), `time.Duration`, any `encoding.TextUnmarshaler` (covers `uuid.UUID`, `netip.Addr`, custom enums), pointers (nil when missing) and slices of all of these. Custom converters: `dygo.RegisterConverter[T](func(string) (T, error))`. Custom sources: `dygo.RegisterSource("user", func(*http.Request) (any, error))`, used as `user:""` on a field.

### 6.4 Errors
Binding collects **all** field problems, then returns one `*BindError{Fields []FieldError{Source, Name, Value, Reason}}`. The default error writer turns it into a 400 (section 8). JSON syntax errors report the byte offset; a body over the limit is a 413.

### 6.5 Body rules
- JSON decoding uses `encoding/json` (`DisallowUnknownFields` off by default, `dygo.StrictJSON()` route option to turn it on). An adapter interface keeps the door open for faster codecs.
- Empty body with a `body:"json"` field leaves the field zero (validation in phase 3 decides whether that is allowed).
- Body limit default 1 MiB ([D13](decisions.md)), per route `BodyLimit`, applied with `http.MaxBytesReader`.

---

## 7. Request handling chain

Each route compiles to one `http.Handler` at boot:

```
app middleware → controller middleware → route middleware
  → recover (panic → 500)
  → request scope setup (only if the route's controller graph needs it)
  → [phase 3: guards → interceptors (before)]
  → bind In                    (BindError → 400)
  → [phase 3: pipes / validation]
  → call handler
  → [phase 3: interceptors (after)]
  → write Out or error
```

The chain is a `[]stage` built per route, where a stage is `func(*reqState, next func() error) error`. Phase 3 adds stages; nothing else changes. `reqState` is pooled (`sync.Pool`) and holds the request, a buffered status/headers set by `SetStatus`/`SetHeader`/`SetCookie`, route info and the request scope.

The handler's `ctx` is `r.Context()` plus Dygo's request state, so `context.Canceled` propagates when the client goes away and `dygo.Request(ctx)` works.

---

## 8. Responses and errors

### 8.1 Writing `Out`
| `Out` value | Response |
|---|---|
| implements `Responder` | it writes the response itself |
| `NoContent` | 204, no body |
| `[]byte` | 200, `application/octet-stream` |
| `string` | 200, `text/plain; charset=utf-8` |
| anything else | JSON (`application/json`), including `nil` pointer → `null` |

Status: `SetStatus` in the handler, else `HTTPCode` option, else 201 for POST and 200 for everything else. `Header` options and `SetHeader` calls are applied before the body. Content negotiation beyond JSON is phase 4.

### 8.2 `dygo/errs`
```go
type HTTPError struct {
    Status  int
    Code    string // machine readable error code
    Message string
    Details any
    Cause   error  // logged, never sent
}
func (e *HTTPError) Error() string
func (e *HTTPError) Unwrap() error

func New(status int, msg string, opts ...Option) *HTTPError
func BadRequest(msg string, opts ...Option) *HTTPError  // + Unauthorized, Forbidden, NotFound, MethodNotAllowed,
                                                        //   Conflict, Gone, PayloadTooLarge, UnprocessableEntity,
                                                        //   TooManyRequests, Internal, NotImplemented, ServiceUnavailable
func Code(c string) Option
func Details(d any) Option
func Cause(err error) Option

type Writer func(w http.ResponseWriter, r *http.Request, err error)
func DefaultWriter(log *slog.Logger) Writer
```

### 8.3 Default error body ([D14](decisions.md))
One fixed shape, so frontends can rely on it:

```json
{ "statusCode": 404, "error": "Not Found", "message": "user not found", "code": "USER_NOT_FOUND" }
```

| Error | Status | Body |
|---|---|---|
| `*errs.HTTPError` (via `errors.As`) | its `Status` | its message, code, details |
| `*BindError` | 400 | `message: "invalid request"`, `details: [{source, name, reason}]` |
| body too large | 413 | |
| `context.Canceled` | 499, nothing written | client is gone |
| `context.DeadlineExceeded` | 503 | |
| anything else, or a panic | 500 | generic `"Internal Server Error"`; real error and stack logged |

Unknown routes and wrong methods use the same body shape (404, 405 with `Allow`) by wrapping the mux.

---

## 9. Request scope ([D9](decisions.md), [D15](decisions.md))

- New scope `dygo.RequestScoped`. A request scoped provider is built at most once per request, the first time something in that request needs it.
- **Storage:** a `requestScope{mu sync.Mutex; instances map[*node]reflect.Value}` created per request and stored in the request state. `container.ensure` gains a branch: request scoped nodes look up or build in the scope from the context; singletons stay as they are. Concurrency stays safe because shared node state is still never written after boot.
- **Injectable only in request scope:** `*http.Request`, and `context.Context` resolves to the **request** context (not the boot context) for request scoped constructors.
- **Scope rule (boot error):** a singleton may not inject a request scoped provider directly. Silently promoting the singleton to request scope would make request scope slow; Dygo makes the per-request lookup visible instead.
- **`dygo.Scoped[T]`:** the way a singleton (including every controller) uses a request scoped value. It is resolved per call from the request context:

  ```go
  type Controller struct{ tx dygo.Scoped[*sql.Tx] }

  func (c *Controller) Create(ctx context.Context, in CreateIn) (*User, error) {
      tx, err := c.tx.Get(ctx) // built once for this request, shared by everything in it
      ...
  }
  ```

  `Scoped[T].Get` outside a request returns an error. The boot error for a direct injection suggests `Scoped[T]`.
- **Controllers are always singletons.** `Routes` is called once at boot with method values, which cannot be re-bound to a new receiver per request, so request scoped controllers are not supported; `Scoped[T]` covers the same need. This is deliberate ([D15](decisions.md)).
- No lifecycle hooks on request scoped instances. An optional `dygo.OnRequestEnd` interface (`OnRequestEnd(ctx)`) is called when the request finishes, for cleanup such as returning a DB transaction.

---

## 10. Shutdown order and serving

In this order, with a drain of in-flight requests:

1. Signal received (or `ctx` cancelled).
2. `OnModuleDestroy` on all instances (reverse order).
3. `BeforeApplicationShutdown(signal)`.
4. `http.Server.Shutdown(ctx)`: stop accepting, **drain in-flight requests** within the shutdown timeout. Long lived connections (SSE) see their request context cancelled via `BaseContext` cancellation.
5. `OnApplicationShutdown(signal)`.

This needs one change in phase 1: `runShutdown` gets a hook between steps 3 and 5 (`func(ctx) error`), and `App.Shutdown` keeps its current behaviour when no server is attached.

`Listen` returns `nil` after a clean shutdown and the error otherwise. Startup hooks already ran in `Create`, so `Listen` only starts serving and the app is fully initialized before the first request.

Server defaults ([D16](decisions.md)): `ReadHeaderTimeout 10s`, `IdleTimeout 120s`, no read/write timeout (so SSE and uploads work); everything changeable with `WithHTTPServer`.

---

## 11. Testing helpers (`dygotest`)

```go
srv := dygotest.New(AppModule).
    OverrideValue(dygo.TypeOf[users.Repo](), fakeRepo).
    CompileHTTP(t) // *dygotest.Server, wraps httptest.Server, closed on cleanup

res := srv.GET("/api/v1/users/42").Header("X-Tenant", "a").Do(t)
res.Status(t, 200)
var u users.User
res.JSON(t, &u)

srv.POST("/api/v1/users").JSON(map[string]string{"email": "x"}).Do(t).Status(t, 201)
```

Also `dygotest.Handler(t, app)` for in-process tests with `httptest.NewRecorder` (no network).

---

## 12. Tasks (in order)

| # | Task | Output | Est. |
|---|---|---|---|
| 1 | Phase 1 changes: `RequestScoped` scope constant, shutdown hook between phases, `Option` struct gains HTTP fields | small PR on core | 0.5 d |
| 2 | `internal/pattern`: join, validate, `:param` detection, normalization | + table tests | 1 d |
| 3 | `Router` implementation, `RouteOption`s, route table, `Controller` discovery from `App.Controllers()` (boot error if a controller has no `Routes`) | | 2 d |
| 4 | Route table registration with conflict capture, 404/405 handlers, `Routes()` dump | | 1 d |
| 5 | `internal/binder`: plan compiler, sources, converters, defaults, `BindError` | + table tests per source and type, fuzz test for converters | 3 d |
| 6 | `Handle`, `HandleNoInput`, `HandleRaw`, request state, chain of stages, recover | | 1.5 d |
| 7 | Response writing, `Responder`, `NoContent`, `Stream`, `File`, `SSE`, `RedirectTo`, ctx helpers | | 1.5 d |
| 8 | `errs` package and default writer | | 1 d |
| 9 | Versioning (URI first, then header, media type, custom dispatcher) | | 1.5 d |
| 10 | Request scope in the container and per request, `Scoped[T]`, scope rule validation, `OnRequestEnd` | | 2 d |
| 11 | `Create`, `Listen`, `Serve`, `Handler`, shutdown order, server defaults | | 1 d |
| 12 | `dygotest` HTTP helpers | | 1 d |
| 13 | `examples/http-users` (in-memory repo, CRUD, versioning, SSE endpoint) + e2e tests | | 1.5 d |
| 14 | Benchmarks and docs: `guide-http.md`, update PLAN.md, CLAUDE.md, this file's status | | 1 d |

Total about 19 working days.

## 13. Test plan

- **Unit:** pattern joining and validation; every binder source and converter, defaults, required, pointer and slice handling, embedded structs, error aggregation; response writer per `Out` kind; error writer per error kind; version extraction per strategy.
- **Boot errors:** `:id` pattern, duplicate route, ambiguous routes, `path` tag without matching wildcard, controller without `Routes`, singleton depending on request scope. Each asserts the message names the controller and module.
- **Request scope:** one instance per request, distinct across concurrent requests (`-race`), `*http.Request` injection, `Scoped[T].Get` from a singleton controller, error outside a request, `OnRequestEnd` called.
- **Shutdown:** a slow in-flight request completes during shutdown; hooks run in the order of section 10; SSE stream is cancelled.
- **E2E:** `examples/http-users` with `dygotest` (create 201, get 200, list with defaults, 404 with code, 400 on bad UUID with field details, 413 on huge body, versioned route, SSE).
- **Fuzz:** binder converters and query parsing (`go test -fuzz`).
- **Benchmarks** (recorded below when done): bare `ServeMux` handler vs `HandleRaw` vs `Handle` with path + query + JSON body; allocations per request.

## 14. Exit criteria
- [x] `examples/http-users` e2e suite passes with `-race`.
- [x] Every boot error in the test plan is reported together in one `BootError`.
- [x] Graceful shutdown test passes (in-flight request completes).
- [x] Benchmarks recorded in section 16; after the optimization pass `Handle` matches equivalent hand-written code (37 allocations each, time within noise).
- [x] `go vet`, `staticcheck` clean; core still has no third party dependencies.

## 15. Open questions (defaults chosen)
1. **Path syntax:** Go `{id}` (default) vs `:id` translated to `{id}`. Translating is easy but hides that Dygo is on `ServeMux`; rejecting with a hint keeps one syntax.
2. **Error body:** the `statusCode`/`message` JSON shape (default) vs RFC 9457 `application/problem+json`. Problem+JSON can be an alternative writer in phase 3.
3. **Request scope:** explicit `Scoped[T]` and singleton-only controllers (default) vs silent promotion to request scope.

---

## 16. What was built

### Files
`http_app.go`, `http_options.go`, `router.go`, `handler.go`, `response.go`, `routes.go`, `versioning.go`, `request_scope.go`, `binding.go`, `internal/pattern`, `internal/binder`, `errs/`, `dygotest/http.go`, `examples/http-users`.

### Tests
- Root package: CRUD, bind errors, 413, 404 shape, panic → 500, all route table errors reported together (7 at once), responses and middleware order, URI / header / media type versioning, request scope (20 concurrent requests, `OnRequestEnd`, `Scoped` outside a request), scope rule errors, graceful shutdown order with an in-flight request.
- `internal/binder`: every source, converters, defaults, required, error aggregation, strict JSON, content type, body limit, multipart, raw, form, custom source and converter, compile errors, plus a fuzz target.
- `internal/pattern`: join, validate, normalize.
- `examples/http-users`: e2e CRUD, conflict, strict JSON, v1/v2, bad ID, 404 code, pagination, 413, SSE.
- All green with `go test -race -count=5 ./...`; `go vet` and `staticcheck` clean. Coverage: root 87%, binder 82%, pattern 98%.

### Benchmarks
Go 1.24, 4 vCPU Xeon 2.1 GHz, `httptest` request + recorder included in every number (`make bench`, `-benchtime 2s`, median of 3):

| Benchmark | ns/op | B/op | allocs/op |
|---|---|---|---|
| Bare `ServeMux`, write path value | ~4,300 | 6,585 | 24 |
| `dygo.HandleRaw`, same work | ~4,580 | 7,129 | 28 |
| Hand-written: path + query + JSON in + JSON out | ~5,850 | 8,043 | 37 |
| `dygo.Handle[In, Out]`, same work | ~5,900 | 7,824 | 37 |

The routing chain costs about 0.3 µs and 4 allocations (request state, context value, `WithContext`, body limit reader). A typed handler now matches hand-written code on allocations and is within measurement noise on time.

### Optimization pass (2026-10-29)
Before the pass, `Handle` cost ~6,700 ns, 8,635 B and 46 allocations (about 1 µs and 9 allocations over hand-written code). Changes:
- **Binder sets values in place.** Converters write straight into the destination field instead of returning a new `reflect.Value`, path and cookie values no longer allocate a one-element slice, and the per-field error closure became a method.
- **JSON input** is read into a pooled buffer and decoded with `json.Unmarshal` (a `json.Decoder` only for `StrictJSON`). This also **rejects trailing data** after the JSON value, which was silently accepted before.
- **Content-Type check** without `mime.ParseMediaType` (which allocated a map per request).
- **JSON output** encoded into a pooled buffer instead of `json.Marshal` plus an append that reallocated.
- **One allocation for request state:** the response writer wrapper lives inside the request state, and the request scope is created lazily inside it on first use instead of eagerly with its own context value.
- **Panics log once,** with the stack, through the error writer (they were logged twice).
- **Raw handlers keep `http.Flusher` and `http.Hijacker`**, so SSE helpers and WebSocket libraries that type-assert the writer work inside `HandleRaw`.
- **`dygo.Stopping(ctx)`** lets long streams end promptly on shutdown; before, an SSE client waiting for events held shutdown for the whole shutdown timeout. `examples/http-users` uses it.
- **Behaviour change:** an empty query, header or form value (`?limit=`) now counts as missing, so `default` applies, instead of failing to parse.

### Differences from the plan
- **Wrong method gives 404, not 405.** A catch-all route produces JSON 404s (`Cannot POST /x`), and with it `ServeMux` no longer reports 405.
- **Missing version header uses the default version** for header and media type versioning, instead of failing.
- **`dygo.Create[T]` from phase 1 was renamed `dygo.Construct[T]`**, because `dygo.Create` is now the HTTP app constructor.
- **`reqState` is not pooled** on purpose: handlers may keep the context (and so the state) alive in goroutines after returning, and pooling would hand that state to another request.
- `dygo.BindError` and `dygo.FieldError` are aliases of the internal binder types so callers can use `errors.As`.

### Follow-ups for phase 3
- Insert guards, interceptors, pipes and filters as stages in `compiledRoute.ServeHTTP` (the endpoint already separates `call` from writing).
- Module level middleware with route matching (`MiddlewareConsumer.forRoutes`).
- Validation tags on `In` types.
