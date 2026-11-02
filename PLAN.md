# Dygo Framework: Plan

A Go framework built from modules, dependency injection, controllers, and an enhancer pipeline (middleware, guards, interceptors, pipes, exception filters). It should still read like idiomatic Go.

> Status: phases 0 to 2 done (2026-10-29), phases 3 and 4 done (2026-10-30). Detailed per-phase plans are in [docs/](docs/README.md).

---

## 1. Goals and principles

**Goals**

1. Give developers a clear mental model: `Module → Provider → Controller → Pipeline`.
2. Give Go developers code that looks like Go: plain structs, constructor functions, `context.Context`, `error` values, `net/http` compatibility.
3. Catch wiring mistakes **at startup, before serving traffic** (phase 1) and optionally **at compile time** through codegen (phase 5).
4. Typed handlers: request input and response output are Go types, so validation and OpenAPI come from the same source.

**Principles**

- **Explicit over magic.** Go has no decorators, so registration is a visible function call. That is a feature, not a gap.
- **Reflection only at boot.** Reflection is used to build the graph once. The hot request path does no reflection lookups.
- **Standard library first.** `net/http`, `log/slog`, `context`, `errors`, `iter`. Third party libraries sit behind adapters.
- **Small core, optional modules.** The core is the container, modules, lifecycle, and pipeline. Everything else (config, OpenAPI, NATS, and so on) is a separate package.
- **Minimum Go version: 1.27** since phase 7 (D52; it was 1.24 before: generic type aliases, `reflect.TypeFor`, range-over-func iterators, `net/http` method and wildcard patterns from 1.22).

---

## 2. What the developer writes

This is the target API. Everything in the rest of the plan exists to make this work.

```go
// users/service.go
type UserService struct {
    repo UserRepo
    log  *slog.Logger
}

// Constructor injection: the container reads the parameter types.
func NewUserService(repo UserRepo, log *slog.Logger) *UserService {
    return &UserService{repo: repo, log: log}
}

// Lifecycle hook, found by interface assertion.
func (s *UserService) OnModuleInit(ctx context.Context) error { return nil }
```

```go
// users/controller.go
type UserController struct{ svc *UserService }

func NewUserController(svc *UserService) *UserController { return &UserController{svc} }

// Replaces @Param, @Query, @Body: struct tags on an input type.
type GetUserIn struct {
    ID     string `path:"id"     validate:"uuid"`
    Expand bool   `query:"expand"`
}

type CreateUserIn struct {
    Body struct {
        Email string `json:"email" validate:"required,email"`
        Name  string `json:"name"  validate:"required,min=2"`
    } `body:"json"`
}

// Registers the routes, with a controller level guard and a role per route.
func (c *UserController) Routes(r dygo.Router) {
    r = r.Prefix("/users").Use(auth.Guard())       // controller level guard (phase 3)
    r.Get("/{id}", dygo.Handle(c.FindOne))
    r.Post("/", dygo.Handle(c.Create),
        auth.Roles("admin"),                         // route metadata (phase 3)
        dygo.HTTPCode(201))
}

func (c *UserController) FindOne(ctx context.Context, in GetUserIn) (*User, error) {
    return c.svc.Find(ctx, in.ID)
}

func (c *UserController) Create(ctx context.Context, in CreateUserIn) (*User, error) {
    return c.svc.Create(ctx, in.Body.Email, in.Body.Name)
}
```

```go
// users/module.go
var Module = &dygo.Module{
    Name:        "users",
    Imports:     []*dygo.Module{database.Module},
    Providers:   []dygo.Provider{
        dygo.Provide(NewUserService),
        dygo.Provide(NewPgUserRepo, dygo.As[UserRepo]()), // bind concrete → interface
    },
    Controllers: []dygo.Provider{dygo.Provide(NewUserController)},
    Exports:     []dygo.Token{dygo.TypeOf[*UserService]()},
}
```

```go
// main.go
func main() {
    app, err := dygo.Create(ctx, AppModule,
        dygo.WithGlobalPrefix("/api"),
    )
    if err != nil { log.Fatal(err) }   // missing provider, cycle, visibility error, route clash
    app.UseGlobalFilters(myFilter)     // phase 3
    app.Listen(ctx, ":9900")           // graceful shutdown on SIGINT/SIGTERM
}
```

---

## 3. Architecture

### 3.1 Package layout

```
dygo/                     core: Module, Provider, Token, Container, App, lifecycle
(root package)            Router, handlers, binding, HTTP app (decision D10; implementation in internal/)
dygo/pipeline/            Guard, Interceptor, Pipe, Filter interfaces + chain builder
dygo/meta/                typed metadata keys and the Reflector
dygo/errs/                HTTPError types and default filter
dygo/validate/            Schema interface + adapters (go-playground/validator first)
dygo/dygotest/            TestingModule, OverrideProvider, OverrideGuard, httptest helpers
dygo/config/              config.ForRoot, typed config, .env loading
dygo/logger/              slog based logger module
dygo/openapi/             OpenAPI 3.1 from handler In/Out types
dygo/observe/             OpenTelemetry hooks via lifecycle (no monkey patching)
dygo/health/              Terminus equivalent
dygo/micro/               transports: NATS, gRPC, Kafka, Redis
dygo/ws/                  WebSocket gateways
dygo/schedule/            cron, interval, timeout
dygo/events/              in-process event bus
cmd/dygo/                 CLI: new, generate, routes, graph, check, start, gen (route directives), info
```

### 3.2 Boot sequence (`dygo.Create`)

1. **Scan** the module tree from the root module. Dynamic modules (`ForRoot(...)`) are just function calls that return a `dygo.Module`, so they are already values here.
2. **Register** every provider under its token. Record which module owns it and what that module exports.
3. **Analyze** each constructor with `reflect` (parameter types, return types, `error` return).
4. **Validate the graph:** missing providers, visibility (a module can only use what it declares or what an import exports), cycles, duplicate tokens, scope violations. All errors are collected and reported together, with the path through the module tree.
5. **Instantiate** singletons in topological order. Constructors may take `context.Context` and return `(T, error)` (this replaces async providers).
6. **Build routes:** call `Routes(r)` on each controller, compose the enhancer chain for each route **once**, detect conflicts and shadowed routes.
7. **Run lifecycle hooks** level by level (`OnModuleInit`, then `OnApplicationBootstrap`), leaves first.
8. **Serve.** On shutdown: stop accepting, drain in-flight requests, then `OnModuleDestroy`, `BeforeApplicationShutdown`, `OnApplicationShutdown` in reverse order.

`dygo.CreateApplicationContext(AppModule)` runs steps 1 to 5 and 7 only, for CLIs and workers.

---

## 4. Dependency injection design

| Need | Dygo |
|---|---|
| A provider | Any constructor function `func(deps...) T` or `func(deps...) (T, error)` |
| Type token | `reflect.Type` of the return value (`dygo.TypeOf[*UserService]()`) |
| Named token | Typed named token: `var DBURL = dygo.NewToken[string]("db.url")`, requested with `dygo.Inject(DBURL)` or a param struct field tag |
| An existing value | `dygo.Value(v)` |
| A constructor | `dygo.Provide(NewX)` |
| A factory with dependencies | Same as a constructor: the constructor *is* the factory |
| An alias | `dygo.Alias[A, B]()` |
| Interface binding | `dygo.Provide(NewPgRepo, dygo.As[UserRepo]())` |
| An optional dependency | `dygo.Optional[T]` wrapper type as a parameter |
| Many dependencies | A parameter struct embedding `dygo.In`, with `optional:"true"` and `name:"x"` tags (Uber dig style) |
| Async provider | Constructor takes `context.Context`, returns `(T, error)` |
| Multi providers (e.g. global guards) | Group tokens: `dygo.Provide(NewX, dygo.Group("guards"))`, injected as `[]T` |
| A dependency cycle | `dygo.Lazy[T]`: resolved on first `.Get()`. Cycles without `Lazy` fail at boot. |
| Singleton and transient scopes | `dygo.Singleton` (default), `dygo.Transient` |
| Request scope | `dygo.RequestScoped`: a child container per request carried in `context.Context`. Discouraged; prefer passing request data through `ctx`. |
| Container access from a module | `dygo.ModuleRef` injected like any provider |
| Discovery |  `container.Providers(filter)` over registered providers, plus `dygo.ProvidersImplementing[T]()` |

**Build or reuse?** Uber `dig` already does constructor reflection well, and `fx.Module` has private providers. We still recommend a **small custom container** (target under 2k lines) because the module visibility rules, error messages that name the module path, request scope, and lifecycle ordering are the heart of a module framework, and wrapping dig would fight its model. Keep the API close to dig/fx so the learning curve is low.

---

## 5. Request pipeline design

The order is fixed:

```
Request → Middleware → Guards → Interceptors (before) → Pipes (bind + validate) → Handler
        → Interceptors (after) → Response
Exception filters catch any error or recovered panic at any stage.
```

### 5.1 Interfaces

```go
// Middleware: plain net/http, so every existing Go middleware works.
type Middleware func(http.Handler) http.Handler

type Guard interface {
    CanActivate(ctx ExecutionContext) (bool, error)
}

// Interceptor: no RxJS. "next" runs the rest of the chain and returns its result.
type Interceptor interface {
    Intercept(ctx ExecutionContext, next CallHandler) (any, error)
}
type CallHandler interface{ Handle() (any, error) } // next.Handle(), like next.handle()

// Pipe: transforms or validates the bound input struct (a pointer to In).
type Pipe interface {
    Transform(ctx ExecutionContext, in any) error
}

// Filter: turns an error into a response. Errors are matched with errors.As.
type ExceptionFilter interface {
    Catch(err error, host ArgumentsHost) bool // true if handled
}
```

Typed helpers wrap these with generics, for example `dygo.FilterFor[*NotFoundError](func(e *NotFoundError, h ArgumentsHost) {...})`, which replaces `@Catch(NotFoundException)`.

### 5.2 Binding levels

Three levels, all as explicit calls:

- **Global:** `dygo.WithGlobalGuards(...)` (and `WithGlobalInterceptors`, `WithGlobalPipes`, `WithGlobalFilters`) options, or a provider in the `dygo.AppGuards` group so it can have dependencies.
- **Controller:** `r.Prefix("/users").Use(...)`.
- **Route:** options on `r.Get(path, handler, opts...)`.

Enhancers can be passed as instances, plain functions, `dygo.Ref[*AuthGuard]()` (a provider visible from the controller's module), or a constructor such as `NewAuthGuard`, which the container builds with DI. See decisions D17 to D22 for how phase 3 differs from this first sketch.

### 5.3 Execution context

`ExecutionContext` exposes `Kind()` (`HTTP` now; `RPC`, `WS` later), `HTTP()`, `Context()`/`SetContext()`, `Route()` (route info), `Controller()` (type), and the metadata readers used by package `meta`. One guard works across HTTP, gRPC, NATS, and WebSockets.

### 5.4 Metadata

```go
var RolesKey = meta.NewKey[[]string]("roles")

func Roles(r ...string) dygo.RouteOption { return meta.Set(RolesKey, r) }

// inside a guard
roles, ok := meta.Get(ctx, RolesKey)              // typed, no casts
roles = meta.GetAllAndMerge(ctx, RolesKey)        // controller + route
```

Metadata is stored on the route at boot, so reading it is a map lookup.

### 5.5 Binding and validation (replaces param decorators and pipes)

- `dygo.Handle[In, Out](func(context.Context, In) (Out, error))` creates the handler. At boot it reflects on `In` once and compiles a binder: `path`, `query`, `header`, `cookie`, `body`, `form`, `file` tags.
- Built-in parse pipes (`ParseInt`, `ParseUUID`, `ParseBool`, `ParseEnum`, `ParseDate`, defaults) become **the binder's type conversion**: a field of type `int` or `uuid.UUID` is parsed automatically, and `default:"10"` replaces `DefaultValuePipe`.
- Validation goes through a `validate.Schema` interface (Dygo's Standard Schema). The first adapter is `go-playground/validator` tags. Others can be added (ozzo-validation, JSON Schema) without changing handlers.
- Because `In` and `Out` are real types, **OpenAPI and validation share one source** with no manual annotation.
- Escape hatch: `dygo.HandleRaw(func(w http.ResponseWriter, r *http.Request) error)`.

### 5.6 Errors

- `errs.HTTPError{Status, Code, Message, Details}` with constructors `errs.NotFound(...)`, `errs.BadRequest(...)`, and so on. `Code` is a machine readable error code.
- The default filter maps `HTTPError` to JSON, validation errors to 400 with field details, `context.Canceled` to 499, and everything else to 500. Panics are recovered and treated as errors.

---

## 6. What Go cannot do directly

The common root causes: Go has **no decorators or annotations**, **no runtime metadata on functions or methods**, **no parameter names in reflection**, **no dynamic class loading**, **no proxies or AOP**, and **no exceptions**. Each row gives the Go workaround Dygo uses.

| # | Feature | Why Go can't do it directly | Dygo workaround |
|---|---|---|---|
| 1 | **Decorators** on classes and methods | Go has no annotations on types, funcs, or methods. | **Registration API**: `dygo.Module{...}` values, `Routes(r dygo.Router)` method on controllers, `dygo.Provide(NewX)`. Optional later: **comment directives** (`//dygo:get /users/{id}`) read by `dygo gen`, the same approach as `swag` and `//go:generate`. |
| 2 | **Reflection metadata** on methods | No place to attach arbitrary data to a method at runtime. | Metadata attached at **route registration** with **typed keys** (`meta.NewKey[T]`). Read back with `meta.Get(ctx, key)`, fully type safe. |
| 3 | **Constructor injection by type** | Partly possible: `reflect` *can* read a function's parameter types, but not struct method params by name, and not unexported fields. | **Reflect on constructor functions** at boot (like Uber dig/fx). For compile-time safety, an optional **`dygo gen`** step generates plain wiring code (like Google Wire), with zero reflection at runtime. |
| 4 | **String tokens** | Constructor params have no names or annotations. | **Typed tokens** `dygo.NewToken[T]("name")`, injected through a **param struct** with `name:"..."` tags, or via a distinct named type (`type DBURL string`). |
| 5 | **Property injection** | No annotations on fields; unexported fields can't be set without `unsafe`. | Param struct with `dygo.In` embedding and exported, tagged fields. Constructor injection is the recommended default. |
| 6 | **Parameter decorators** (body, query, path, custom) | Method parameters have no names or annotations. | **Typed input struct with tags** bound by `dygo.Handle[In, Out]`. Custom param decorators become **custom tag resolvers** (`dygo.RegisterTag("user", resolveUserFromCtx)`) or a field of a type with a registered resolver. |
| 7 | **Decorator composition** | No decorators to compose. | Route options are values, so compose them with a function: `func AdminOnly() dygo.RouteOption { return dygo.Options(auth.Guard(), auth.Roles("admin")) }`. |
| 8 | **Dynamic modules** (`ForRoot`, `ForRootAsync`, `ForFeature`) | Works naturally with package functions. | Package functions returning `dygo.Module`: `config.ForRoot(opts)`. `ForRootAsync` takes an options **constructor** that the container injects. A generic `dygo.ConfigurableModule[Opts]` builder generates both variants. |
| 9 | **Lazy loading modules** | Go links everything into one binary at build time; the `plugin` package is Linux/macOS only and fragile. | **Lazy instantiation**, not lazy loading: `dygo.LazyModule(m)` builds its providers on first resolve through `ModuleRef.LoadLazy(ctx, m)`. Code size is decided by build tags. |
| 10 | **Observable interceptors** | No Observables in Go. | Interceptor calls `next.Handle()` and works on the returned `(any, error)`. Timeouts use `context.WithTimeout`. Streams (SSE, WS, gRPC streaming) use **channels or `iter.Seq`**. |
| 11 | **Exceptions** | Go uses error values; panics are not for control flow. | Handlers return `error`. Typed errors + `errors.As` drive filters. Panics are recovered and routed to filters as a last resort. |
| 12 | **Class inheritance** and mapped types | No inheritance; no mapped types. | **Struct embedding** for reuse, delegation to a default filter, and **generics** for DTO helpers where possible. A partial type becomes pointer fields or `dygo.Optional[T]` fields. |
| 13 | **Runtime type annotations for validation** | Types exist at runtime in Go, so this is mostly *easier*. | Struct tags + `validate.Schema` interface. Go wins here: no manual type duplication. |
| 14 | **AOP proxies / method wrapping** (e.g. caching a service method, transparent interception of any class) | No dynamic proxies or method swizzling. | Enhancers wrap **routes and message handlers** only, which covers the real use. For service methods: explicit decorator pattern (wrap the interface: `cache.Wrap[UserRepo](repo)`), or codegen'd wrappers. |
| 15 | **Event, cron and message handlers on methods, found by scanning** | Can't discover annotated methods. | Providers implement a registration method (`Handlers(r events.Registry)`, `Jobs(s schedule.Registry)`) found by **interface assertion** at boot. Discovery works over registered providers. |
| 16 | **Request-scoped providers with scope bubbling** | Possible but costly; Go's convention is `context.Context`. | Supported via a per-request child container, but the docs steer users to `ctx` values and singletons. Durable providers: `dygo.Durable()` with `WithContextIDStrategy` (D46). |
| 17 | **Hot reload** | Compiled binary. | Use `air` or `dygo start --watch` (rebuild + restart). Go builds are fast enough. |
| 18 | **Method overloading, optional args** | No overloading or default params. | **Functional options** (`dygo.WithX(...)`) and variadic route options. |

**Where Go helps**, worth putting in the README: compile-time type checking of handler input and output, one static binary, goroutines for CPU work, and OpenAPI from real types.

---

## 7. Packages (later phases)

| Dygo package | Built on |
|---|---|
| `dygo/config` | env + `.env` + typed struct with `env:"X"` tags, validated at boot |
| `dygo/logger` | `log/slog`, JSON or text, structured params by default |
| `dygo/openapi` | reflection of `In`/`Out` types, `/docs` UI |
| `dygo/observe` | OpenTelemetry, hooked into lifecycle and pipeline |
| `dygo/health` | liveness, readiness, DB pings |
| `dygo/sql`, `dygo/gorm`, `dygo/ent` | `database/sql`, pgx, GORM, ent |
| `dygo/auth` | guards + `golang-jwt` |
| `dygo/ratelimit` | `x/time/rate` or Redis |
| `dygo/cache` | interceptor + memory/Redis |
| `dygo/micro` | NATS, gRPC, Kafka, Redis |
| `dygo/ws` | `coder/websocket` |
| `dygo/graphql` | `gqlgen` (schema-first codegen fits Go) |
| `dygo/schedule`, `dygo/queue`, `dygo/events`, `dygo/cqrs` | own cron parser (D41), memory or `asynq` (D44) |
| `dygo/dygotest` | `testing` + `httptest` |
| `cmd/dygo` | templates + `go/ast` codegen |

---

## 8. Phased roadmap

Sizes are rough, for one developer.

### Phase 0: Decisions and skeleton (about 1 week)
- Choose the module path, license, Go 1.24 minimum. Chosen: `github.com/dygoframework/dygo` (D5), MIT (D36).
- Repo layout from 3.1, CI (`go test`, `go vet`, `golangci-lint`), benchmarks folder.
- Write the target API from section 2 as an `examples/` app that does not compile yet. It is the acceptance test for phases 1 to 3.

### Phase 1: Core container and modules (2 to 3 weeks)
- Tokens, `Provide`, `Value`, `Alias`, `As`, `Group`, `Optional`, `Lazy`, param structs.
- Module scan, visibility rules, global modules, dynamic modules (`ForRoot` pattern).
- Graph validation with full error reports. Singleton and transient scopes.
- Lifecycle hooks by hierarchy level, graceful shutdown.
- `CreateApplicationContext`, `ModuleRef`, `dygo/dygotest` with `OverrideProvider`.
- **Done when:** a worker app with 3 modules boots, a missing provider error names the module path, and tests override a provider.

### Phase 2: HTTP layer (2 to 3 weeks)
- Router on `net/http` `ServeMux` patterns, behind an adapter interface (chi or others later).
- Controllers with `Routes(r)`, prefixes, global prefix, versioning (URI and header).
- `dygo.Handle[In, Out]` binder: path, query, header, cookie, JSON body, multipart files.
- Response writing, status codes, headers, redirects, `StreamableFile` equivalent, SSE via `iter.Seq`.
- `errs` package and default filter. Route conflict and shadow detection.
- **Done when:** the `examples/` app's users CRUD runs.

### Phase 3: Pipeline and metadata (2 weeks)
- Guards, interceptors, pipes, filters at global, controller, and route levels, built once per route.
- Enhancers resolved through DI (`Ref[T]`, constructors, `AppGuards` group).
- `ExecutionContext`, typed metadata, `GetAllAndMerge`.
- `validate.Schema` + validator adapter, validation error shape option.
- **Done when:** auth guard + roles metadata + logging interceptor + validation work in the example, and a benchmark shows pipeline overhead under a few hundred nanoseconds per request over bare `net/http`.
- **Status:** done 2026-10-30. 2 guards + 2 interceptors + 1 pipe add no allocations; see docs/phase-3-pipeline.md.

### Phase 4: Production essentials (3 weeks)
- `config`, `logger`, `openapi`, `health`, `observe` (OpenTelemetry), CORS, security headers, rate limiting, request IDs.
- **Done when:** the example ships with OpenAPI docs, traces, health checks, and runs in Docker.
- **Status:** done 2026-10-30; see docs/phase-4-production.md.

### Phase 5: CLI and optional codegen (2 to 3 weeks)
- `dygo new`, `dygo generate module|controller|service|resource`, `dygo start --watch`, `dygo info`.
- `dygo gen`: reads modules and constructors with `go/packages`, generates wiring code and route tables so the app needs no reflection at runtime, and turns wiring errors into compile errors. Optionally reads `//dygo:` comment directives.
- **Built (2026-10-30):** the CLI plus `dygo check`; `dygo gen` generates routes from directives only, and wiring is checked by running the real validator (decisions D29 to D31). Dylan confirmed this scope (D30) on 2026-10-31.
- **Done when:** the example builds with both runtime and generated wiring and both pass the same tests.

### Phase 6: Transports and background work (4+ weeks, can run in parallel)
- **Status (2026-10-31):** done for events, schedule, cqrs, cache, queue (memory + asynq), micro (local, NATS, gRPC), ws and hybrid apps. Durable providers (D46), the Redis and Kafka transports (D47, D48) and GraphQL (D49) are done too. Request scoped WebSocket gateways are the one item left out. See [docs/phase-6-transports.md](docs/phase-6-transports.md).
- `micro` (NATS first, then gRPC with real status code mapping, Kafka), hybrid apps.
- `ws` gateways, `schedule`, `events`, `queue`, `cqrs`, `cache`, request scope polish, durable providers.

### Phase 7: v1.0 (ongoing)
- API freeze, docs site, performance tuning, and a real app built on it.

### Phase 8: Database (about 3 weeks)
- `dygo/sql` sub-module for MySQL and Postgres: pool from config, boot ping, health, custom queries, `Repository[T]` CRUD, transactions, error mapping, test helpers, migrations and `--db` generators. See [docs/phase-8-database.md](docs/phase-8-database.md).

### Phase 9: Security (done 2026-11-02)
- `dygo g security`: a deny-by-default security layer for APIs in one package (login included): deny by default, ordered path rules, roles, authorities and a role hierarchy, method-level checks, refresh tokens, an OAuth2 resource server (OIDC, JWKS), JSON 401/403, secure headers and CORS, test helpers, and `dygo g security -oidc`. See [docs/phase-9-security.md](docs/phase-9-security.md) and the [security recipe](docs/recipes/security.md).

---

## 9. Risks

- **Too much magic for Go users.** Mitigate by keeping every feature visible as a function call and keeping `net/http` compatibility.
- **Reflection errors only at startup.** Mitigate with excellent boot error messages in phase 1, then codegen in phase 5.
- **Generic handler limits.** Go generics cannot have methods with type parameters, so `Handle[In, Out]` must be a package function, not a router method. Already reflected in the API above.
- **Scope creep.** The list of possible packages is huge. Phases 1 to 4 are the product; phase 6 packages are added on demand.

## 10. Prior art to study

Uber **fx/dig** (runtime DI, lifecycle), Google **Wire** (codegen DI), **Huma** (typed handlers + OpenAPI from Go types), **Kratos** and **go-zero** (full-stack Go frameworks), **gqlgen** (schema-first codegen), **swag** (comment directives).

## 11. Open decisions (defaults chosen, change if you prefer)

1. **Runtime reflection first, codegen later** (default) vs codegen only from day one.
2. **Own container** (default) vs build on Uber dig.
3. **`net/http` base** (default) vs Fiber/fasthttp (faster, but breaks compatibility with Go middleware).
4. **Validator:** `go-playground/validator` tags first (default).
