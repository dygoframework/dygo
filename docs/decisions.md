# Decisions

Each entry: the decision, the alternatives, and why. Change a decision by editing its entry and noting the date.

## D1. Runtime reflection first, codegen later (2026-10-29)
- **Chosen:** the container reads constructor signatures with `reflect` at boot. Phase 5 adds `dygo gen`, which emits plain Go wiring code from the same module definitions.
- **Alternative:** codegen only (Google Wire style) from day one.
- **Why:** runtime wiring is faster to build and iterate on, and users expect "register and go". All reflection happens once at boot, and every wiring error is reported before the app serves anything.

## D2. Own container instead of Uber dig (2026-10-29)
- **Chosen:** a small container in the root package.
- **Why:** module encapsulation (exports, imports, global modules), error messages that name the module path, and lifecycle ordering by module level are the core of the model. dig has a flat graph and would need to be fought rather than used. The API borrows dig's ideas (`dygo.In` parameter structs, groups) so it feels familiar.

## D3. `net/http` as the HTTP base (2026-10-29)
- **Chosen:** standard library `net/http` with the Go 1.22+ pattern router, behind an adapter interface.
- **Alternative:** Fiber (fasthttp).
- **Why:** compatibility with every existing Go middleware, `httptest`, and HTTP/2. Speed difference rarely matters behind a real database.

## D4. Validator tags first (2026-10-29)
- **Chosen:** `go-playground/validator` tags behind a `validate.Schema` interface (phase 3).
- **Why:** the most used Go validator. The interface keeps the door open for other libraries.

## D5. Module path (2026-10-29, confirmed by Dylan 2026-10-31)
- **Chosen:** `github.com/dygoframework/dygo`. Package name `dygo`.
- **Why:** the framework lives in its own `dygoframework` organization. It started as `github.com/dygoacademy/dygo`, and moved on 2026-11-03 for the first public beta, v1.0.0-beta.1.

## D6. Modules are pointers (2026-10-29)
- **Chosen:** `var Module = &dygo.Module{...}`, and `Imports []*dygo.Module`.
- **Why:** a module's identity is its pointer, so importing the same module from two places gives one shared instance. A dynamic module (`ForRoot(opts)`) returns a new pointer on each call, which is what you want for per-call configuration.

## D7. Provider groups are application wide (2026-10-29)
- **Chosen:** members of a group (for example `dygo.Group("app.guards")`) are collected from every module, not only visible ones.
- **Why:** groups exist for multi providers like global guards and plugin registries, which are global by nature. Keeps the visibility rules simple.

## D8. Errors over panics at boot (2026-10-29)
- **Chosen:** `CreateApplicationContext` returns an error that lists every problem found (`*dygo.BootError`). It never panics for a user mistake.
- **Why:** you fix all wiring problems in one run instead of one per restart.

## D9. Request scope deferred to phase 2 (2026-10-29)
- **Chosen:** phase 1 ships `Singleton` and `Transient` scopes only.
- **Why:** a request scope needs a request. It will be built with the HTTP layer as a per-request scope carried in `context.Context`; see D15 for the design.

## D10. HTTP API in the root package (2026-10-29, accepted)
- **Chosen:** `dygo.Handle`, `dygo.Router`, `dygo.Create` live in the root package; implementation in `internal/`. Errors in `dygo/errs`.
- **Why:** a sub-package named `http` clashes with `net/http` in every file that uses both, and `Handle[In, Out]` must be a package function anyway.

## D11. Go pattern syntax for paths (2026-10-29, accepted)
- **Chosen:** `/users/{id}`; a `:id` segment is a boot error with a hint.
- **Why:** `ServeMux` treats `:id` as a literal without complaint (checked on Go 1.24), which would be a silent 404. One syntax, the one Go developers already know.

## D12. Route conflicts are always errors (2026-10-29, accepted)
- **Chosen:** duplicates and ambiguous patterns are reported in the `BootError`; there is no "warn" policy.
- **Why:** `ServeMux` panics on them, so the table cannot be served anyway. `ServeMux` matches by specificity, so there is no order-dependent shadowing to report.

## D13. 1 MiB default body limit (2026-10-29, accepted)
- **Why:** safe default against memory abuse; `WithBodyLimit` and per-route `BodyLimit` override it.

## D14. JSON error body (2026-10-29, accepted)
- **Chosen:** `{"statusCode", "error", "message", "code", "details"}`.
- **Alternative:** RFC 9457 problem+json, possible later as a second writer.
- **Why:** one familiar shape that frontends can rely on.

## D15. Request scope is explicit: `Scoped[T]`, singleton controllers (2026-10-29, accepted)
- **Chosen:** `RequestScoped` providers are injected into singletons only through `dygo.Scoped[T]` (`Get(ctx)` per call). Controllers are always singletons. Direct injection is a boot error.
- **Alternative:** silent promotion of every consumer to request scope.
- **Why:** promotion hides a per-request construction cost across the whole graph. Method values passed to `Handle` cannot be re-bound per request, so request scoped controllers would need fragile tricks.

## D16. Server defaults (2026-10-29, accepted)
- **Chosen:** `ReadHeaderTimeout` 10s, `IdleTimeout` 120s, no read or write timeout.
- **Why:** protects against slowloris while letting SSE and large uploads work. Changeable with `WithHTTPServer`.

## D17. `ExecutionContext` is not a `context.Context` (2026-10-30, phase 3)
- **Chosen:** `ec.Context()` returns the request context and `ec.SetContext(ctx)` replaces it for later stages and the handler (a guard adds the user this way).
- **Alternative:** the plan's interface embedding `context.Context`.
- **Why:** a context derived from an embedding `ExecutionContext` and then set back would make `Value` lookups loop forever. The explicit method is the `http.Request.Context()` pattern Go developers know.

## D18. Pipes work on the whole bound input (2026-10-30, phase 3)
- **Chosen:** `Pipe.Transform(ec, in any) error`, where `in` is a pointer to the handler's input struct. Pipes can normalise fields in place or reject the request.
- **Alternative:** per-argument pipes returning a new value.
- **Why:** the binder already does the per-field parsing work with types. A Dygo handler has one input struct, so validation and transforms act on it. Measured cost: none (37 allocations either way).

## D19. Enhancer arguments and global enhancers (2026-10-30, phase 3)
- **Chosen:** `Guards`, `Interceptors`, `Pipes`, `Filters` and the `WithGlobal*` options take instances, plain functions, `dygo.Ref[T]()` for a provider visible from the controller's module, or a constructor (built once per module with injected parameters). Globals with DI are group members (`dygo.AppGuards` and so on). All resolved at boot.
- **Alternative:** the plan's `GuardType[T]()` and `app.UseGlobalGuards` after `Create`.
- **Why:** chains are composed once at boot, so globals must be known before routes compile; options give that. One generic `Ref` works for all four kinds. Constructors match `@UseGuards(AuthGuard)` without forcing a provider registration.

## D20. `CallHandler` is an interface with `Handle()` (2026-10-30, phase 3)
- **Chosen:** `next.Handle()`. The request state holds the first four handlers inline.
- **Why:** a `func() (any, error)` closure would cost one allocation per interceptor per request. With the interface, 2 guards + 2 interceptors + 1 pipe add zero allocations.

## D21. Validation: `Methods()` in core, go-playground as a separate module (2026-10-30, phase 3)
- **Chosen:** `validate.Methods()` calls `Validate(ctx) error` on the input and its Body field, with no dependency. `validate/playground` (its own `go.mod`, validator v10.30.1, Go 1.24 compatible) maps `validate:"..."` tags and reports the request name and source of each field. A `*ValidationError` is a 400 with the same details shape as binding errors; `WithErrorShape` changes it.
- **Why:** keeps the core dependency free (D2) while giving validation tags to those who want them.

## D22. Controller level metadata and enhancers come from `Router.Use` (2026-10-30, phase 3)
- **Chosen:** options passed to `Router.Use` are controller level; options on the route are route level. `meta.Key[T]` has `Set`, `Get` (route, then controller), `Route`, `Controller`, plus `meta.GetAllAndMerge`.
- **Why:** Go has no class decorators; `Router.Use` already scopes options to every route of the controller.

## D23. The request is cloned only on demand (2026-10-30, phase 3 performance pass)
- **Chosen:** `compiledRoute.ServeHTTP` keeps the incoming `*http.Request` and puts the state in a derived context given to typed handlers. `reqState.request()` clones the request with the current context only when an `*http.Request` is handed out, caching the clone in an atomic pointer until the context changes again.
- **Why:** `r.WithContext` copies the whole request (about 250 bytes) on every request, and again on every `SetContext`. Most typed handlers never look at the request. Saves one allocation per request, two for routes whose guard sets the context.

## D24. Phase 4 packages are dependency free except OpenTelemetry (2026-10-30, phase 4)
- **Chosen:** `config`, `logger`, `middleware` (request ID, request log, CORS, security headers, CSRF, gzip), `health`, `openapi` and `ratelimit` use only the standard library and live in the main module. `observe` (OpenTelemetry v1.40.0, the newest that still supports Go 1.24) is a separate Go module, like `validate/playground`.
- **Alternative:** `x/time/rate` for rate limiting, a Scalar or Swagger UI embedded in the binary.
- **Why:** keeps D2 (no third party code in the core). The rate limiter is a small sharded fixed window counter behind a `Store` interface, so a Redis store can be added without changing the guard. The docs UI loads swagger-ui-dist from jsDelivr (pinned, configurable) instead of adding megabytes to every binary.

## D25. Security and logging are middleware, not app options (2026-10-30, phase 4)
- **Chosen:** CORS, security headers, CSRF, compression, request IDs and request logs are `func(http.Handler) http.Handler` values passed to `dygo.WithMiddleware`.
- **Alternative:** the plan's `WithCORS` option on `Create`.
- **Why:** one mechanism instead of a growing option list, and the same middleware works with any Go router. App middleware runs before routing, which CORS preflights need.

## D26. Route metadata on `RouteInfo`, route table from a request (2026-10-30, phase 4)
- **Chosen:** `RouteInfo` gained `Input`, `Output`, `Status` and `Metadata(key)`; `dygo.RoutesOf(ctx)` returns the table from inside a request. A version neutral route no longer needs versioning to be on.
- **Why:** package `openapi` needs the typed handlers' types and the route options (summary, tags), and its handler must read the finished table, which does not exist yet while `Routes` methods run. Library modules such as `health` and `openapi` use `dygo.Version(dygo.VersionNeutral)` so they work with or without versioning.

## D27. The example app is its own Go module (2026-10-30, phase 4)
- **Chosen:** `examples/http-users` has its own `go.mod` with `replace` directives for the framework and `observe`.
- **Why:** it uses `observe`, whose OpenTelemetry dependencies must not enter the root `go.mod`. `make test`, `vet` and `lint` and CI cover it, and its Dockerfile builds from the repository root.

## D28. GCRA for the in-memory rate limiter (2026-10-30, phase 4 review)
- **Chosen:** the generic cell rate algorithm: per client and route, one "theoretical arrival time"; requests refill evenly at window/limit.
- **Alternative:** a fixed window counter (first version), or a sliding log.
- **Why:** a fixed window allows up to twice the limit around a window boundary; a sliding log stores every request. GCRA is exact, stores one int64 per key, and gives `Remaining` and `Retry-After` directly. `X-RateLimit-Reset` is now the time until the full limit is available again.

## D29. The CLI inspects an app by running it up to dygo.Create (2026-10-30, phase 5)
- **Chosen:** when `DYGO_INSPECT_OUT` is set, `dygo.Create` and `CreateApplicationContext` write a JSON report (graph, routes, boot errors) to that file and return `dygo.ErrInspected` without running startup hooks or serving. `DYGO_INSPECT=routes` builds providers so controllers can register routes; otherwise nothing is built. `dygo.Describe(root)` gives the same graph in code.
- **Alternative:** a static analysis of module literals with `go/packages` and a types-only copy of the validator (`internal/graph`, as planned).
- **Why:** roots are often functions of config (`AppModule(cfg, log)`), which static analysis cannot evaluate, and a second validator would drift from the real one. Running `main` handles every app shape and reports exactly the errors the app would hit. The cost is that `main` must get as far as `dygo.Create` (config must load).

## D30. dygo gen generates routes, not the container (2026-10-30, phase 5, confirmed by Dylan 2026-10-31)
- **Chosen:** `dygo gen` turns `//dygo:` comment directives into `Routes` methods. Wiring is checked with `dygo check` (D29) and `dygo.Describe` in a test.
- **Alternative:** generate `NewGeneratedApp` that calls constructors directly (like Google Wire), as planned.
- **Why:** boot reflection costs microseconds, so the gain of generated wiring is compile time errors. `dygo check` in CI catches the same errors with the same messages, without a second container implementation (2 to 3 thousand lines covering scopes, Lazy, Scoped, groups, overrides, enhancers and lifecycle) that works only for statically known module trees. Revisit if users need reflection-free builds (TinyGo, for example).

## D31. The CLI edits Go files by text insertion, not by printing the AST (2026-10-30, phase 5)
- **Chosen:** find the module literal with `go/parser`, insert the new element as text at the AST offsets, then `go/format`. The CLI uses only the standard library and lives in the main module.
- **Alternative:** modify the AST and print it with `go/printer`, or use `golang.org/x/tools` (`astutil`, `go/packages`).
- **Why:** printing a modified AST misplaces comments; insertion leaves the rest of the file byte for byte. No third party code keeps `go install` fast and the core rule (no dependencies) intact.

## D32. dygo new -t modular: a layout split by module (2026-10-31, phase 5, Dylan's request)
- **Chosen:** `dygo new <name> -t modular` puts `main.go` and `app.module.go` (`AppModule`, `AppOptions`) at the project root, both package `main`. Infrastructure is in `internal/{config,database,health,common}` and features in `internal/modules/<feature>`. A `dygo.json` tells the CLI where features go. The default stays the simple one-package layout, as Dylan asked.
- **Alternative:** a first version with `cmd/server/main.go` and an `internal/app` package holding the root module and a hello controller. Dylan found `internal/app` hard to tell apart from a feature module, so the root module became one file at the top. A `src/` folder was also possible, but Go projects keep `main` at the root or in `cmd/`.
- **Why:** Dylan wants projects structured by module, with the entry point and root module easy to find. `internal/` is the Go convention for app-private packages. A small JSON file is simpler than guessing the layout.
- **Changed by [D62](#d62-modular-layout-uses-src-not-internal-2026-11-01-phase-5-dylans-request):** the folders are now under `src/`.

## D33. Maker-style generators use only what phases 0 to 5 provide (2026-10-31, phase 5, Dylan's pick)
- **Chosen:** Dylan picked six generators: entity, crud, command, test, controller and auth. The CLI matches the framework's current phase. Generators for phase 6 packages (jobs, cron, events, gateways) come with those packages. `dygo g entity -fields` (and a prompt in a terminal) writes the entity, the create and update bodies with `Validate`, `Entity` and `Apply`, so that every field lives in one file. Running it again adds fields by AST insertion (D31), including sample values in the package's tests. `g crud <Entity>` builds the CRUD around an existing entity, and `g resource` does both steps at once.
- **Alternative:** templates with fields spread over the service and controller, or regenerating files from a schema.
- **Why:** when one file holds every per-field line, adding a field never touches the user's service or controller code. Regenerating would overwrite their edits.

## D34. Console commands are providers, run through package console (2026-10-31, phase 5)
- **Chosen:** a command is any provider with `Name()` and `Run(ctx, args)`. `console.Run` builds the application context, finds commands with `dygo.ProvidersImplementing`, runs the one named by the first argument, and shuts down, so hooks run. New projects' `main.go` hands `os.Args[1:]` to `console.Main` before starting the server (`go run . seed`, `go run . help`).
- **Alternative:** a separate `cmd/<name>/main.go` per command.
- **Why:** `AppModule` is in package `main` in both layouts, so another binary cannot import it. One binary also gets the same config, DI and lifecycle.

## D35. dygo g auth uses only the standard library (2026-10-31, phase 5)
- **Chosen:** the generated `auth` module hashes passwords with PBKDF2-HMAC-SHA256 (`crypto/pbkdf2`, Go 1.24, 600k iterations) and issues HS256 JWTs with `crypto/hmac`. Users are kept in memory behind a small `Users` type. The module is global, and routes opt in with `auth.Required()` or `auth.Roles(...)`. `AUTH_SECRET` is required in production and must be at least 32 characters. Without it, a random secret is used in development.
- **Alternative:** bcrypt or argon2 from `golang.org/x/crypto`, a JWT library, or a global guard with `Public()` (like `examples/http-users`).
- **Why:** with no dependencies, `go mod tidy` needs no network and the code is easy to read and replace. An opt-in guard keeps `/health` and `/docs` open and existing tests passing.

## D36. MIT license (2026-10-31, Dylan's pick)
- **Chosen:** MIT.
- **Alternative:** Apache-2.0 (explicit patent grant and NOTICE rules).
- **Why:** the license of most Go web frameworks (Gin, Echo, Fiber), so adopters expect it and need no legal review.

## D37. One pipeline for every transport, built through `NewEnhancers` (2026-10-31, phase 6)
- **Chosen:** `ArgumentsHost` gained `RPC()` and `WS()` next to `HTTP()`, and `ContextKind` gained `KindRPC` and `KindWS`. Transport packages describe each handler as a `dygo.Endpoint` and get a `*dygo.Pipeline` from `dygo.NewEnhancers`; `Pipeline.Run` runs guards, interceptors, pipes (on the decoded input) and the handler, recovers panics as `*PanicError` and sends errors through the filters (`ErrFiltered` when one caught it). Enhancers are the same `RouteOption` values as HTTP (`dygo.Guards(...)`, metadata keys). `RouteInfo.Method` is the kind and `Path` the pattern, so `interceptors.Logging` and `meta` keys work unchanged.
- **Alternative:** a separate enhancer interface per transport, or exporting the internal route compiler.
- **Why:** one guard works on HTTP, NATS, gRPC and WebSockets. The core stays transport-free: it knows nothing about NATS or gRPC types, which arrive as `any` through `RPCHost.Message()` and `WSHost.Client()`.

## D38. Transports do not inherit the HTTP app's global enhancers by default (2026-10-31, phase 6)
- **Chosen:** a transport runs its own global enhancers (`ServerOptions.Global`, `Options.Global`). `InheritAppConfig: true` adds the `WithGlobal*` options and the `App*` groups, for hybrid apps.
- **Why:** existing global filters and guards read `host.HTTP()`, which is nil for other transports, so inheriting them silently would panic or refuse every message.

## D39. Hybrid apps: `App.ConnectMicroservice` and `dygo.Transport` (2026-10-31, phase 6)
- **Chosen:** `dygo.Transport` is `Listen(ctx, *App) error` plus `Close(ctx) error`. `ConnectMicroservice` adds one; `StartAllMicroservices` starts them (rolling back the started ones on failure), and `HTTPApp.Listen`, `Serve` and `App.Run` call it. Shutdown closes transports together with the HTTP drain, after `BeforeApplicationShutdown` and before `OnApplicationShutdown`.
- **Why:** a familiar model (`ConnectMicroservice`, `StartAllMicroservices`), and transports stop taking messages at the same point HTTP stops taking requests, so services they use are still alive.

## D40. Discovery from providers: `dygo.Discover[T]` and `ModuleRef.App` (2026-10-31, phase 6)
- **Chosen:** packages whose work is a provider (events, schedule, cqrs, queue, ws) find their handlers in `OnModuleInit` with `dygo.Discover[T](ref)`, the provider-side `ProvidersImplementing`. `ModuleRef.App()` gives the app to packages that build pipelines (ws). Handlers are found by interface: `Events(r)`, `Jobs(s)`, `CQRS(r)`, `Process(r)`, `Patterns(r)`, `Gateway(g)`, `GRPC(r)`.
- **Why:** replaces `@OnEvent`, `@Cron`, `@Processor`, `@MessagePattern` scanning with a method the reader can find, and keeps registration errors (bad cron, duplicate handler) inside the boot error.

## D41. Own cron parser, no robfig/cron (2026-10-31, phase 6)
- **Chosen:** `schedule.ParseCron` reads 5 fields, or 6 with seconds first, names, ranges, steps and `@hourly`-style macros, with standard day-of-month OR day-of-week semantics. Jobs skip a tick while the previous run is still going unless `AllowOverlap()` is set.
- **Alternative:** `robfig/cron` (planned in PLAN.md section 7), overlapping runs by default.
- **Why:** about 200 lines keeps `schedule` in the dependency-free main module. Skipping overlaps is the safer default for jobs that touch a database.

## D42. cqrs has commands and queries; events stay in package events (2026-10-31, phase 6)
- **Chosen:** `CommandBus.Execute` returns only an error; `cqrs.Ask[R]` returns a typed query result. One handler per type, checked at boot. Bus middleware (`cqrs.Use`) replaces per-bus interceptors. Events are published with `*events.Emitter`; no sagas.
- **Why:** a second event bus would duplicate package events, and two global modules both providing an emitter would clash.

## D43. cache: JSON values, and `Wrap` caches functions, not interfaces (2026-10-31, phase 6)
- **Chosen:** `cache.Store` holds bytes (`NewMemory` is an LRU with TTL); values are JSON so a Redis store behaves the same. `cache.Interceptor` caches GET results (JSON, strings, bytes; not Responders), keyed by URL or `cache.Key`, with `cache.TTL` and `cache.NoCache` route options. `GetOrSet` collapses concurrent misses into one call.
- **Alternative:** `cache.Wrap[I]` returning an implementation of a service interface, as PLAN.md said.
- **Why:** Go cannot create methods at runtime, so wrapping an interface needs generated code. `cache.Wrap(c, prefix, ttl, fn)` caches one method and a small decorator struct holds the wrapped methods. A `dygo g` generator for the decorator can come later.

## D44. queue: a driver interface, memory in core, asynq as a module (2026-10-31, phase 6)
- **Chosen:** `queue.Driver` (`Enqueue`, `Work`) with an in-memory driver (retries with doubling backoff, failed jobs kept for inspection) and `queue/asynq` for Redis. `queue.SkipRetry` maps to `asynq.SkipRetry`; attempts map to `MaxRetry + 1`. A payload that does not decode fails without retry.
- **Alternative:** River on Postgres.
- **Why:** asynq is closest to bullmq (Redis, named task types, retries, delays, unique IDs). River can be another driver later without changing processors.

## D45. micro wire format, NATS and gRPC modules, WebSocket frames (2026-10-31, phase 6)
- **Chosen:** micro payloads are JSON and replies are `{"data": ...}` or `{"error": <errs.Body>}`, so an `*errs.HTTPError` from a handler comes out of `micro.Send` as the same error on the caller. `micro.NewLocal` runs handlers in process for tests. NATS (`micro/nats`, nats.go v1.49, the last release for Go 1.24) maps patterns to subjects, so wildcards work; the client's `Before` hook runs before each request. gRPC (`micro/grpc`, grpc-go v1.80) wraps generated services with interceptors and maps HTTP statuses to real codes (`Code`, `HTTPStatus`, `ToHTTPError`). WebSockets (`ws`, coder/websocket) use `{"event", "data", "id"}` frames, ack replies with the same id, and an `exception` event for errors; refused connections close with 1008 and the disconnect handler gets the close code and reason.
- **Why:** each broker library stays in its own module (D24). The error body is the one HTTP clients already get (D14).

## D46. Durable providers: `Durable()`, `ContextIDStrategy`, `ContextID` (2026-10-31, phase 6, Dylan's request)
- **Chosen:** `dygo.Durable()` on a `RequestScoped` provider, `dygo.WithContextIDStrategy(func(*http.Request) (id, payload))`, and one cached scope per ID. Durable providers inject `dygo.ContextID{ID, Payload}` instead of the request. An empty ID falls back to per-request instances. `WithDurableContext(ctx, id, payload)` resolves them outside HTTP. Instances are built once per ID even under concurrent requests (unlike plain request scope, where the first of two racing builds wins), because they usually own pools. `App.ReleaseContext` and shutdown call `OnApplicationShutdown` on them; `DurableContexts` lists the IDs.
- **Alternative:** a rule that a tree is durable only when every request scoped dependency is durable, silently falling back to per-request otherwise.
- **Why:** a silent fallback hides the cost the feature is meant to remove, so a durable provider that injects `*http.Request` or a non-durable request scoped provider is a boot error that names the fix.

## D47. Redis transport: pub/sub with a per-client reply channel (2026-10-31, phase 6)
- **Chosen:** `micro/redis` (go-redis v9.22) subscribes plain channels, and patterns containing `*`, `?` or `[` with PSUBSCRIBE. Requests travel in an envelope `{id, reply, header, data}`; each client listens on one inbox channel `_dygo.inbox.<random>` and matches replies by id. A PUBLISH that reaches no subscriber fails at once with `micro.ErrNoHandler` instead of waiting for the timeout.
- **Why:** plain pub/sub gives the expected semantics (no persistence, every server instance gets events). One inbox per client keeps subscriptions constant however many requests are in flight.

## D48. Kafka transport: franz-go, consumer groups, regexp patterns (2026-10-31, phase 6)
- **Chosen:** `micro/kafka` uses franz-go v1.20 (pure Go, with `kfake` for tests, so no broker or Docker in `make test`). A server consumes in one consumer group; a pattern starting with `^` is a regexp, others are exact topics. Request/response uses the headers `dygo-reply-topic` and `dygo-correlation-id`, and each client creates its own reply topic `dygo.reply.<random>` with kadm and deletes it on close. A polled batch is handled before the next poll, so offsets commit only for handled messages.
- **Alternative:** segmentio/kafka-go, or confluent-kafka-go (cgo).
- **Why:** franz-go is the most complete pure Go client and its in-memory fake keeps the integration test self-contained.

## D49. GraphQL: gqlgen schema-first, guards around root fields, `KindGraphQL` (2026-10-31, phase 6)
- **Chosen:** the `graphql` module serves a gqlgen `ExecutableSchema` provided in the module; the root resolver is a normal provider, so resolvers get services by injection. `graphql.Endpoint(Options)` is a controller on the app's HTTP server (POST and GET, optional GraphiQL). The core gains `KindGraphQL` and `ArgumentsHost.GraphQL()` (`Field()`, `Args()`, `Request()`), the GqlExecutionContext. Guards, interceptors and filters run around each root field (`AroundRootFields`); `Options.Global` applies to all and `Options.Fields["Mutation.deleteUser"]` to one, and an unknown field name fails boot. `*errs.HTTPError` becomes a GraphQL error with `extensions.code` (NOT_FOUND, UNAUTHENTICATED, ...) and `statusCode`; other errors and panics become "Internal server error" and are logged. No `dygo g` generator: `gqlgen generate` already writes the resolver stubs.
- **Alternative:** code-first with graph-gophers/graphql-go, or enhancers on every nested field.
- **Why:** gqlgen is the standard Go choice and type safe, and schema first. Root fields are where resolver methods (and their guards) sit; wrapping every nested field would cost a pipeline per value.

## D50. Encode once on the transport hot paths (2026-10-31, phase 6 performance review)
- **Chosen:** the core gains `dygo.RawJSON`, an `Out` that is already encoded JSON and is written as is with the route's status and headers; the cache interceptor returns it on hits and misses, so a cached body is neither encoded twice nor validated again. `micro.EncodeReply` writes `{"data":...}` by hand and `DecodeReply` decodes in one pass into the caller's value; micro/redis splices encoded payloads into its envelopes the same way. ws encodes a broadcast frame once for all clients. The graphql module keeps parsed queries in an LRU (`Options.QueryCacheSize`, 1000 by default, negative turns it off) and enables automatic persisted queries, as gqlgen's `NewDefaultServer` does.
- **Alternative:** keep `json.RawMessage`, which `encoding/json` compacts and validates on every encode.
- **Why:** benchmarks in the review (`/perf-review/` in the project files): GraphQL 2x faster with half the allocations, a 24 KB cache hit 8x, a 100-client broadcast 2.7x, a 24 KB micro reply encode 3x and decode 1.5x.

## D51. Request scoped WebSocket gateways: an opt-in scope per message (2026-10-31, phase 6)
- **Chosen:** `ws.PerMessageScope()`, a route option for a gateway (`g.Use`) or one event, opens a request scope per message with the new core hook `App.WithRequestScope(ctx, req)`. Request scoped providers resolved with `dygo.Scoped[T].Get(ctx)` are built for that message, `OnRequestEnd` runs after the handler and its ack, `*http.Request` and the context ID strategy see the handshake request (so durable providers resolve per tenant), and resolving after the end is an error. The scope is created lazily, as for HTTP. The gateway stays a singleton; without the option, request scoped providers keep belonging to the handshake request and live as long as the connection.
- **Alternative:** build the gateway itself per message.
- **Why:** a gateway also holds connection hooks and is what the server dispatches to, so rebuilding it per message would cost a construction per frame and break `OnConnection` state. Making only the dependencies per message gives the behaviour that matters (per-message state and cleanup), and opt-in keeps singleton gateways free. `WithRequestScope` is exported so micro and queue handlers can open the same kind of scope.

## D52. Go 1.27 minimum for v1.0 (2026-10-31, phase 7, Dylan's pick)
- **Chosen:** every module's `go.mod` says `go 1.27.0`, the latest release, and the third party dependencies are upgraded to their latest versions (grpc v1.85, otel v1.47, x/net v0.62, gqlgen v0.17.98). CI tests 1.27.x and stable; lint uses golangci-lint v2.15.0 and govulncheck v1.9.0.
- **Alternative:** keep 1.24 (no longer supported) or move to 1.26, the oldest supported release.
- **Why:** Go 1.24 and 1.25 get no security fixes, and the fixed versions of x/net, otel and grpc need Go 1.25 or 1.26, so observe, micro/grpc and examples/http-users had reachable vulnerabilities. Dylan chose the latest Go. The cost is that users on Go 1.26 cannot use Dygo until they upgrade.

## D53. Transactions travel in `ctx` (2026-10-31, phase 8, Dylan's pick)
- **Chosen:** inside `db.Tx(ctx, fn)`, every query and repository call that gets that `ctx` runs in the transaction. Guardrails: `sql.Detach(ctx)` (used by async events, queue producers and schedule jobs), `sql.ErrTxDone` with the query text, `sql.InTx(ctx)`.
- **Alternative:** pass a `sql.Querier` or `repo.WithTx(tx)` by hand: explicit and checked by the compiler, but every repository and generated service method would need it.
- **Why:** `Repository[T]` and generated CRUD services can share one transaction without extra parameters. This replaces the SQL recipe's earlier advice not to carry a transaction in `ctx`.

## D54. The database module is `github.com/dygoframework/dygo/sql` (2026-10-31, phase 8, Dylan's pick)
- **Chosen:** a sub-module with its own go.mod at `sql/`, with `sql/postgres` and `sql/mysql` dialect packages. It uses only Dygo's public API, so it can move to its own repository later.
- **Alternative:** `dygo/db`; or a separate repository from the start.
- **Why:** PLAN.md section 7 already names it, and one repository keeps core and sql changes in one PR while the core API can still move. The name clashes with `database/sql`, so the package re-exports what users need and most files import only one.

## D55. A built-in migration runner (2026-10-31, phase 8, Dylan's pick)
- **Chosen:** `sql/migrate`: numbered `.up.sql` and `.down.sql` files from an `embed.FS`, a `dygo_migrations` table, and a lock so one instance migrates at a time. Run from a console command; `OnBoot()` for small single instance services.
- **Alternative:** wrap goose or golang-migrate.
- **Why:** no extra dependency or release cadence to follow, and the runner is small.

## D56. Always `?` placeholders (2026-10-31, phase 8, Dylan's pick)
- **Chosen:** queries are written with `?`, and the Postgres dialect rewrites them to `$1, $2` once per query string (skipping quotes and comments; `??` is a literal `?`).
- **Alternative:** each database's own placeholders.
- **Why:** the same query, repository and generated code run on MySQL and Postgres. Release plan (also Dylan's pick): milestones 1 to 3 ship as `sql/v0.1.0-beta.1`, the rest as `sql/v0.1.0`.

## D57. How `Repository[T]` maps rows and reports misses (2026-10-31, phase 8)
- **Chosen:** `db` tag options `pk`, `auto`, `created`, `updated`; exactly one `pk`, checked when the repository is built at boot. The `Dialect` interface grows `Quote` (identifiers are always quoted) and `Returning`: Postgres reads `auto` columns back with `RETURNING`, MySQL from `LastInsertId`, so MySQL allows one integer `auto` column, and `CreateMany` fills `auto` columns only on Postgres. `Update`, `Patch` and `Delete` return `ErrNotFound` (404) when no row matched; the MySQL dialect adds `clientFoundRows=true` so an update with unchanged values still counts as a match. `OrderBy` and `Patch` accept only mapped column names and return `ErrUnknownColumn` (400). `created`/`updated` are set in Go, UTC to the microsecond. `CreateMany` splits large slices (1000 rows, or fewer to stay under 65535 parameters) and runs the batches in one transaction. `sql.Using(name)` puts a repository on a named pool.
- **Alternative:** composite keys and an `ID` interface; leaving timestamps to database defaults; ignoring zero affected rows.
- **Why:** one key covers almost every table and keeps the API small (custom queries cover the rest). Setting timestamps in Go means the struct matches the row without a second read on MySQL. A 404 on a missing row is what a REST handler wants, and `clientFoundRows` keeps that from misfiring. Checking sort and patch names against the tags keeps request input out of the SQL text. Filling MySQL batch IDs would rely on `auto_increment_increment` and lock mode, so it is left out, as Django does.

## D58. Database commands: `dygo db`, run by the app (2026-10-31, phase 8, Dylan's pick)
- **Chosen:** one `dygo db` group (`init`, `create`, `drop`, `migrate`, `rollback`, `status`, `schema create|drop`) plus `dygo g migration` and `dygo new -db`. Commands that touch the database are `db:*` commands of the app, run by `migrate.Main` before the app is created; `dygo db <cmd>` builds the main package and runs it with `db:<cmd>`, and production runs `./app db:migrate`. Migrations stay the only source of the schema: `schema create` applies every migration and `schema drop` rolls them back (`--all` drops every table). `drop`, `rollback` and `schema drop` need `--force` where data goes, and `--allow-production` under `APP_ENV=production`.
- **Alternative:** the CLI opens the database itself (it would need both drivers, breaking its standard-library-only rule, and could not see the app's own driver options); console commands of the booted app (every other service the app needs would have to be up, and `db:create` cannot boot against a database that does not exist). For the schema, a schema update or diff from entity mapping.
- **Why:** the app already has the driver, the migrations (embedded) and `.env`, so one code path serves development and production. Dylan picked option C: build the migration based commands now, and add a differ from richer `db` tags (`db diff`, `db schema update`) as a later milestone if it is still wanted, since Dygo's tags carry only column names, not types, sizes or indexes.


## D59. `dygo g migration` fills the SQL from the entity (2026-10-31, phase 8, Dylan's ask)
- **Chosen:** when the migration name is about an entity's table, the generator writes `CREATE TABLE` (or `ALTER TABLE ADD/DROP COLUMN`) and its undo, for the database in `DATABASE_URL`. It compares the entity struct with the table the earlier migration files build, replaying their `CREATE`, `ALTER`, `RENAME` and `DROP TABLE` statements, so it needs no database connection and no driver in the CLI. Columns follow `sql.Repository`'s mapping (`db` tag or snake_case name, `id` as the primary key by default); types come from the Go types. Added `NOT NULL` columns get a default so existing rows are valid; dropped columns get a comment that data goes with them. Renames (Dylan's pick, same day): asked in a terminal, Django style, for a dropped column with a new column of the same kind, or given with `-rename old:new`; written as `RENAME COLUMN`. Types: changed only when the field's kind of type changes (text, integer, float, bool, time, bytes, json), with `ALTER COLUMN TYPE ... USING` on Postgres and `MODIFY COLUMN` on MySQL, plus `NOT NULL` set or dropped when a field becomes or stops being a pointer (NULLs filled with the zero value first).
- **Alternative:** compare exact types (every hand-written `VARCHAR(80)` for a Go string would be "changed"); guess renames without asking (Rails does not guess; Django asks); compare with the live database, which needs both drivers in the CLI or a `db:diff` command in the app; keep a schema snapshot file next to the migrations (EF Core), which goes stale when a migration is written by hand.
- **Why:** it keeps option C of D58 (migrations stay the source of the schema, no sync from structs) while removing the blank file Dylan hit. Replaying the files sees hand-written migrations too. Asking keeps a rename from silently losing a column's data. The live-database differ (`db diff`, `db schema update`) is still a later milestone, as is an expand and contract `-safe` mode for zero-downtime renames.

## D60. Safe (expand and contract) migrations (2026-11-01, phase 8, Dylan's ask)
- **Chosen:** `dygo g migration -safe` writes two migrations. The expand migration adds new columns and, for a rename, adds the new column, copies the data and installs a trigger that copies writes in both directions (a PL/pgSQL function on Postgres; two single-statement `BEFORE INSERT`/`BEFORE UPDATE` triggers on MySQL, which `migrate.Split` can split without `DELIMITER`). The old column's default is dropped meanwhile, so a row the new code inserts reaches the trigger with `NULL` there. The contract migration, the next version, starts with `-- dygo:after-deploy`: `Migrator.Up` stops before it (and before every later version, keeping the file order) unless the `Contract` option is set (`db:migrate --contract`); `db:schema:create` sets it, `status --check` ignores held migrations, and `OnBoot` holds them too. A type change under the same column name is refused with a hint to rename the column (the trigger then casts).
- **Alternative:** views over the renamed table (Postgres only, and writes through views need rules); dual writes in application code; a separate tool (pgroll, gh-ost) that rewrites the table.
- **Why:** triggers work the same on both databases, keep old and new code consistent without code changes, and live in ordinary migrations that roll back. The marker keeps the contract in the normal numbered sequence, so `status`, rollbacks and fresh databases need nothing new.

## D61. Warn about data loss on type changes (2026-11-01, phase 8, Dylan's ask)
- **Chosen:** `dygo g migration` classifies each type change by the kinds of D59 (`typeChangeRisk`): values that change for good (`DATA LOSS`: float to integer rounds, integer to `REAL`/`DOUBLE` rounds large values, integer to `BOOLEAN` on Postgres merges numbers into true, text cut to `VARCHAR(n)` on Postgres, `BYTEA` to hex text) and values that may make the migration fail (`MAY FAIL`: text that does not parse, numbers too big, pairs Postgres cannot cast, too long text on MySQL). Both go in a comment above the statement in the up and down files, and up changes are printed as `WARNING:` lines. Under `-safe`, a rename that changes the kind gets the same comment on the copy, noting that the old column keeps the values until `--contract` and that writes of values that do not convert fail while the sync runs.
- **Alternative:** refuse lossy changes unless a flag such as `-allow-loss` is given; check the live data (needs a database connection, see D58).
- **Why:** the generator already compares only kinds, so the risks are known without a database. A warning keeps the generated files a starting point to review, as D59 says, while making sure the loss is seen before `dygo db migrate`.

## D62. Modular layout uses src/, not internal/ (2026-11-01, phase 5, Dylan's request)
- **Chosen:** `dygo new -t modular` puts infrastructure in `src/{config,database,health,common}` and features in `src/modules/<feature>`; `dygo.json` has `"featuresDir": "src/modules"`. `main.go` and `app.module.go` stay at the root.
- **Alternative:** keep `internal/` (D32), the Go convention for app-private packages.
- **Why:** Dygo targets developers who look for code in `src/`. What `internal/` adds is that other Go modules cannot import the packages, which matters for libraries, not for an application binary. Projects made before this keep working, because the CLI reads `featuresDir` from their `dygo.json`, and `dygo db init` falls back to `internal/database`.

## D63. dygo g writes flat files in a simple project (2026-11-01, phase 5, Dylan's request)
- **Chosen:** in the root of a project whose `dygo.json` says `"layout": "simple"` (now written by `dygo new`), `dygo g` writes the feature into the root package as `products.module.go`, `products.service.go`, `products.controller.go`, `products.go` and their tests, with prefixed names (`ProductsModule`, `ProductsService`, `NewProductsService`, `TestProductsCRUD`). Services, controllers and commands register in the module named like the feature, else in `AppModule`. The same templates serve both modes through `{{.P}}` and `{{.Package}}`. `dygo g auth` and the phase 6 kinds (`listener`, `cron`, `job`, `gateway`, `rpc`) still write a package, because other code uses them by package name (`auth.Required()`, `jobs.JobName`). The simple template's controller file is now `app.controller.go`, to match.
- **Alternative:** a fixed `modules/` folder, or refusing feature generators in a simple project and offering a `dygo convert -t modular`.
- **Why:** a simple project promised one package, but `dygo g` added a folder per feature, which left it half flat and half modular. File names like `products.service.go` say what each file holds. `dygo.json` makes the mode explicit, so existing projects without it, and examples like `examples/http-users`, keep making packages.

## D64. Default port 9900, and dygo new writes .env (2026-11-01, Dylan's request)
- **Chosen:** generated projects, examples and docs use port 9900 (`PORT=9900` in `.env.example`, `default:"9900"` in `Config`, `EXPOSE 9900` in the Dockerfiles). `dygo new` also writes `.env`, a copy of `.env.example` with its own header, so `make run` works with no copy step. The core still has no built-in port: `app.Listen` takes the address.
- **Alternative:** keep 3000; or keep `.env` a manual `cp` as before.
- **Why:** 9900 is Dylan's pick for the Dygo default (it was 9200 for a few hours on 2026-11-01, then Dylan moved it to 9900). A new project should run on the first try; `.env` stays git ignored, so the README still says to copy it after a clone.

## D65. Projects use the dygo CLI, with no Makefile (2026-11-01, Dylan's request)
- **Chosen:** `dygo new` no longer writes a Makefile. Its closing message names only `cd <name>` and `dygo start`, then points to `dygo help` (and `dygo help db` after `-db`). The project README lists `dygo start`, `dygo start -watch`, `dygo check`, `dygo routes`, `dygo g`, and plain `go test`, `go build` and `docker build`.
- **Alternative:** keep the Makefile as a thin wrapper, or add `dygo test` and `dygo build`.
- **Why:** two ways to run the same thing (`make run` and `dygo start`) confused new users, and the first screen after `dygo new` showed too many commands. `go test` and `go build` are already one command, so wrapping them adds nothing. The framework repository keeps its own Makefile for its contributors.

## D66. .env files explain every setting (2026-11-01, Dylan's request)
- **Chosen:** `.env` and `.env.example` have the same settings, each with a comment above it saying what it does and which values it takes. Their headers differ: `.env` says it is local and git ignored, and what wins over it (real variables, then `.env.local`); `.env.example` says to commit it, how to make `.env` from it, and to keep secrets out. `dygo db init` uses the same headers and comments `DATABASE_URL` when it adds it.
- **Alternative:** one line per variable with no comments, as before.
- **Why:** Dylan asked for a clearer `.env`. It is the first file a new user edits, so it should answer "what is this and what can I put here" without opening the docs.

## D67. `dygo g migration` compares every entity (2026-11-01, phase 8, Dylan's pick)
- **Chosen:** `dygo g migration` fills the SQL for every entity whose table differs from what the earlier migration files build, whatever the name; the name is optional (default `update_schema`), and with no name and no changes nothing is written. `-rename` and `-safe` still work on the one table the name is about.
- **Alternative:** the name picks the entity (D59), which left `users_products_migration` empty because the name matched no single table.
- **Why:** Dylan hit that empty migration and asked for a migration that diffs every entity. The comparison stays against the migration files, not the live database (D58, D59), so the CLI needs no driver.

## D68. Generated services store data in the database set by `.env` (2026-11-01, phase 8, Dylan's pick)
- **Chosen:** `dygo g crud` and `dygo g resource` write a service on `*sql.Repository[T]` (with `sql.ForFeature` in the module and `db:"id,pk,auto"` on the entity's ID). The `sql` module opens `DATABASE_URL=memory://` as an in-memory database, so `.env` alone decides: memory by default, Postgres or MySQL after `dygo db init`. `dygo new` imports `sql.Module(nil)` (no driver: memory only, with a hint to run `dygo db init` for a real URL), which `dygo db init` turns into the driver's module. `g crud` adds `sql.Module(nil)` and the ID tag to apps made before this.
- **Alternatives:** keep in-memory maps in the services and switch them by hand (the recipe until now), or a `g crud --db` flag.
- **Why:** Dylan saw "database connected" for Postgres while every service said "stored in memory". His rules: switching to the real database is changing `.env`, `g crud` only generates code, keep it simple. One repository type with a memory mode keeps one version of the generated code and no flags.

## D69. `dygo g security` next to `dygo g auth` (2026-11-02, phase 9, Dylan's pick)
- **Chosen:** two generators. `dygo g auth` stays the small version (login, routes protected one by one). `dygo g security` is the deny-by-default version: every route locked unless a rule opens it. `dygo new -security` runs it for a new project.
- **Alternative:** turn `dygo g auth` into the full security layer.
- **Why:** Dylan wants both a "just login" option and a "lock the whole app" option, and to keep each simple.

## D70. Security targets stateless APIs, not server-rendered apps (2026-11-02, phase 9, Dylan's ask)
- **Chosen:** bearer tokens from the `Authorization` header only; no sessions, form login, remember-me cookies, CSRF or login pages. OAuth2 resource server (JWKS) comes in a later milestone; no authorization server, PAR or OAuth2 client for now.
- **Why:** Dylan asked to focus on API features. CSRF and session fixation only exist with cookies and sessions, so stateless APIs do not need CSRF protection.

## D71. One `security/` package with login included (2026-11-02, phase 9, Dylan's pick)
- **Chosen:** `dygo g security` writes one package: rules, guard, roles, checks, and the register, login and me routes, users and tokens. An app uses `g auth` or `g security`, not both; each refuses when it finds the other, and `g security` prints the steps to switch.
- **Alternative:** `g security` on top of an `auth/` package from `g auth`, so apps import both.
- **Why:** one import is simpler. The login code starts from the `g auth` templates so the two stay alike.

## D72. Rules match route patterns, checked once per route (2026-11-02, phase 9)
- **Chosen:** the rules in `security/rules.go` match the matched route's pattern (`RouteInfo.Path`, such as `/orders/{id}`) and the request method, first match wins, and the answer is cached per route. Route options (`Public`, `Roles`, `Authorities`) and `Require` in services add to the rules; only `Public` can open a route. Non-HTTP handlers (micro, queue, events) are not checked.
- **Alternative:** match the raw request URL, as servlet filters do.
- **Why:** routing has already cleaned the path, so `/admin/../x` or `%2F` cannot slip past a rule, and the hot path does one map lookup instead of a pattern scan (design rule: no work per request that can be done once).

## D73. Generators read `"security": true` from `dygo.json` (2026-11-02, phase 9)
- **Chosen:** `dygo g security` sets `"security": true` in `dygo.json`. `g resource`, `g crud` and `g controller` then write protected routes (CRUD writes need `admin` by default) and tests that boot `security.Module` and sign in with `securitytest`. `-public`, `-public-read` and `-write-role` change it per command. Without `dygo.json`, a package made by `g security` in the root or features directory counts.
- **Alternative:** a `-secure` flag on every generator, or detecting the package only.
- **Why:** Dylan asked how generators know the customer opted in. A setting in the project file that the CLI already reads for the layout is visible and editable, and needs no flag on every command.

## D74. Refresh tokens are opaque, hashed and rotated (2026-11-02, phase 9)
- **Chosen:** login returns a 15 minute access token (JWT) and a refresh token: 32 random bytes, stored as its SHA-256 in `refresh_tokens`, grouped by login in `refresh_families`. `POST /auth/refresh` spends it and returns a new pair; sending a spent token again revokes the whole family (the user and a thief both lose the login). `POST /auth/logout` revokes the family. Users and refresh tokens are stored where `DATABASE_URL` says (memory or SQL). Register, login and refresh allow 10 calls a minute per client IP. Login rehashes a password hashed with fewer PBKDF2 iterations than the current setting.
- **Alternative:** JWT refresh tokens (cannot be revoked without a store anyway), a deny list of access tokens checked on every request, or a rate limit keyed by email.
- **Why:** a spent token sent again shows it was stolen. Keeping access tokens short avoids a store lookup per request. Guards run before the body is bound, so the email is not known when the rate limit runs.

## D75. `dygo g security -oidc` is a resource server only (2026-11-02, phase 9)
- **Chosen:** `-oidc` writes the same rules, guard, roles and checks, but replaces users, passwords, own tokens and the login routes with `security/verifier.go`: OIDC discovery from `OAUTH_ISSUER_URL`, the JWKS key set (RS256 and ES256 on P-256), checks of `iss`, `aud` (`OAUTH_AUDIENCE`), `exp` and `nbf`, roles from `OAUTH_ROLES_CLAIM` (a dotted path, `realm_access.roles` for Keycloak) and scopes (`scope`/`scp`) as authorities. Keys are fetched again after an hour, or for an unknown `kid` at most once a minute. Production needs an https issuer and an audience. `securitytest` starts a fake provider with an RSA key, so `securitytest.User`, `Admin` and `As` work the same in both modes and generated CRUD tests do not change.
- **Alternative:** one package that switches between own tokens and OIDC at run time, or a third party JOSE library.
- **Why:** a resource server needs only an issuer URI; an app that delegates login should not carry password and token issuing code it never runs. The standard library covers RSA and ECDSA verification, and keeps the core free of third party code.

## D76. Checked tokens are cached; refresh tokens are spent with a conditional update (2026-11-02, phase 9)
- **Chosen:** `security/tokencache.go` keeps tokens that passed every check, keyed by the whole token (a seeded hash plus an exact compare), until the token's `exp` plus the clock skew, in 16 shards of at most 1024 tokens. Both `Tokens.Verify` and the OIDC `Verifier.Verify` use it. `RefreshTokens` spends a token with `UPDATE refresh_tokens SET used = true WHERE hash = ? AND used = false` and treats zero rows as a reuse; `memory://` keeps a lock. The OIDC verifier fetches keys without holding its lock, and the guard's rule cache is a copy-on-write map.
- **Alternative:** no cache (check every token), a cache keyed by a SHA-256 of the token, or a per-token lock for refresh.
- **Why:** measured on a generated app, a request with an OIDC token went from 52 µs to 2 µs, and with an own token from 6.2 µs to 1.7 µs. Access tokens are never revoked before `exp` (D74), so caching a verified token does not change what is accepted. The conditional update removes a global queue on refresh and closes the two-instance race noted in milestone 3.

## D77. OWASP Top 10 checks run in CI (2026-11-02)
- **Chosen:** an `owasp` CI job (`make owasp`): gosec through golangci-lint on every module (also part of `make lint`), govulncheck, `make test-owasp` (the tests `docs/owasp.md` maps to each category, including a fresh `dygo new -security` project), and fuzzing. Actions are pinned to commits and Dependabot updates Go modules and actions weekly. New generated tests: `TestForgedTokens`, `TestSecurityLog` (failed logins are now logged), `TestUnknownKeyFetchLimit`. `dygo g security` adds `middleware.SecureHeaders` when the app has none.
- **Alternative:** a ZAP scan of a running example on every PR, or a separate gosec binary.
- **Why:** the checks run in a few minutes with tools the repo already uses. gosec's CLI exclusions (file paths, commands, 0644 files) are scoped to `cmd/dygo`, where input comes from the developer at the terminal. A ZAP scan can come later as a nightly job.
