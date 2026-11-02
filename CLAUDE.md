# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project state

Dygo is a Go web framework built from modules, dependency injection, controllers and an enhancer pipeline, that should still read like idiomatic Go. As of 2026-10-30, phases 0 to 5 are implemented (core container, modules, lifecycle, HTTP layer, `dygotest`, the enhancer pipeline with metadata and validation, the production packages, and the `dygo` CLI). Phase 6 (transports and background work) is next.

- `PLAN.md` is the source of truth for the overall design. Read it before writing code, and update it when a design decision changes.
- `docs/` holds the detailed plan per phase (`docs/phase-N-*.md`), the decision log (`docs/decisions.md`), and user guides. Update the phase doc's status when finishing work, and add an entry to `decisions.md` for any new design choice.

The module path `github.com/dygoframework/dygo` is a placeholder (decision D5) pending Dylan's confirmation; the license is still undecided. Ask before changing either.

## Commands

Go 1.27 minimum (D52). The core package has no third-party dependencies; keep it that way.

```sh
make test-unit   # fast: go test -race -short in every module (no databases, no project builds)
make test        # every test in every module; integration tests skip when their service is missing
make test-integration  # starts compose.test.yml (Postgres, MySQL, Redis), then every test
make vet         # go vet ./...
make lint        # golangci-lint v2.15.0 via go run (gofumpt, revive, errcheck, staticcheck)
make fmt         # gofumpt the tree
make bench       # every bench_test.go once; bench-save / bench-diff keep and compare a baseline (docs/guide-testing.md)
make fuzz        # fuzz the binder for 30s
go test . -run TestName          # single core test
make check       # dygo check and gen -check on the examples
go test ./cmd/dygo -update      # rewrite the CLI golden files after a template change
go run ./examples/worker         # phase 1 acceptance app, Ctrl+C to see shutdown hooks
cd examples/http-users && go run .   # acceptance app on :9900, /docs (tokens: admin-token, user-token)
docker build -f examples/http-users/Dockerfile .   # build context is the repo root
```

## Code map (phase 1)

- `token.go` tokens (`TypeOf[T]`, `NewToken[T]`), `provider.go` (`Provide`, `Value`, `Alias`, `As`, `For`, `Group`, scopes), `inject.go` (`In`, `Optional`, `Lazy`, parameter analysis).
- `graph.go` module scan, registration, visibility, linking, cycle detection, module levels, `BootError`. `container.go` instantiation. `lifecycle.go` hooks. `app.go` `CreateApplicationContext`, `App`, `Get[T]`, discovery, `Run`. `moduleref.go` `ModuleRef`.
- `dygotest/` is the testing module (named so it never clashes with the standard `testing` import); `dygotest/http.go` has the HTTP helpers.

## Code map (phase 2)

- `http_app.go` `Create`, `HTTPApp`, `Listen`/`Serve` and the shutdown drain. `http_options.go` app options. `router.go` `Router`, `RouteOption`s. `handler.go` `Handle`/`HandleNoInput`/`HandleRaw`, request state and ctx helpers. `response.go` output writing, `Stream`, `File`, `SSE`. `routes.go` route compilation, conflict checks, the per-request chain (`compiledRoute.ServeHTTP`), 404s. `versioning.go`. `request_scope.go` (`RequestScoped`, `OnRequestEnd`; `Scoped[T]` is in `inject.go`). `binding.go` public binder API.
- `internal/binder` compiles input structs into setter plans; `internal/pattern` joins and validates paths; `errs/` HTTP errors and the default writer.

## Code map (phase 3)

- `pipeline.go` enhancer interfaces, `ExecutionContext` (implemented by `reqState`), route and global binding options, boot time resolution (`enhancerResolver`), and the per-request run (`compiledRoute.run`/`intercept`). `routes.go` `compiledRoute.fail` sends errors through filters to the error writer.
- `meta/` typed metadata keys. `validate/` schemas, the validation pipe and `ValidationError`. `validate/playground/` is a separate Go module (go-playground/validator); `make test`, `vet` and `lint` cover it. `interceptors/` `Timeout`, `Logging`.

## Code map (phase 4)

- Main module, standard library only: `config/` (typed env config), `logger/`, `middleware/` (RequestID, Logger, CORS, SecureHeaders, CSRF, Compress), `health/`, `openapi/` (`Generate` walks `RouteInfo.Input/Output/Metadata`), `ratelimit/`.
- Separate Go modules with their own `go.mod` and `replace` back to the root: `validate/playground`, `observe` (OpenTelemetry v1.40.0, the last line that supports Go 1.24), `examples/http-users` (Dockerfile and compose.yaml inside). Keep third party imports out of the root `go.mod`.

## Code map (phase 5)

- `inspect.go` `Describe`, `GraphInfo`, the `DYGO_INSPECT_OUT` report and `ErrInspected` (called from `createApp`).
- `cmd/dygo/`, standard library only: `main.go` dispatch and flags, `generate.go` (`new`, `generate`, templates in `templates/`), `astedit.go` (finding and editing `&dygo.Module{}` literals), `inspect.go` (`routes`, `check`, `info`), `graph.go`, `start.go` (`-watch`), `gen.go` (route directives). Golden files in `testdata/`.

## Architecture

**Core idea:** Go has no decorators, so everything is an explicit registration call: `dygo.Module{...}` values declare modules, `dygo.Provide(NewX)` registers providers, a `Routes(r dygo.Router)` method on controllers registers routes, and struct tags on a typed input (`path`, `query`, `header`, `cookie`, `body`, `form`, `file`) replace parameter decorators. `PLAN.md` section 6 lists every feature Go cannot do directly and the workaround chosen; follow those choices rather than inventing new ones.

**Package layout:** a small core (`dygo/`: modules, DI, lifecycle, and the HTTP API, see decision D10) plus `dygo/errs`, `dygo/dygotest`, `dygo/meta`, `dygo/validate`, `dygo/interceptors`. Everything else (config, logger, openapi, micro, ws, and so on) is an optional separate package. Third-party libraries sit behind adapters.

**Boot sequence (`dygo.Create`):** scan the module tree, register providers by token, reflect on constructors, validate the whole graph (missing providers, module visibility, cycles, duplicates, scope violations, all reported together with the module path), instantiate singletons in topological order, build routes and compose each route's enhancer chain once, run lifecycle hooks by hierarchy level (leaves first), then serve. Shutdown drains in-flight requests, then runs destroy hooks in reverse. `CreateApplicationContext` is the same without routes or serving.

**DI:** a custom container (target under 2k lines), API kept close to Uber dig/fx. Tokens are `reflect.Type` of a constructor's return value, or typed named tokens (`dygo.NewToken[T]`). Constructors may take `context.Context` and return `(T, error)`, which replaces async providers. Cycles fail at boot unless broken with `dygo.Lazy[T]`.

**Request pipeline:** the order is fixed: Middleware → Guards → Interceptors (before) → Pipes (bind + validate) → Handler → Interceptors (after), with exception filters catching errors and recovered panics at any stage. Middleware is plain `func(http.Handler) http.Handler`. Interceptors call `next.Handle()` and work on `(any, error)`, with no RxJS equivalent. Filters match errors with `errors.As`. Enhancers bind at global, controller, and route level.

**Typed handlers:** `dygo.Handle[In, Out]` must be a package function, not a router method, because Go methods cannot have type parameters. It reflects on `In` once at boot to compile a binder; type conversion does the parsing, so there are no parse pipes. `In`/`Out` types are the single source for both validation (`validate.Schema`, `go-playground/validator` first) and OpenAPI.

**Metadata:** typed keys (`meta.NewKey[T]`) attached at route registration (`key.Set(v)`, controller level through `Router.Use`) and read with `key.Get(ec)` / `meta.GetAllAndMerge`. No untyped string maps.

## Design rules

- Reflection happens only at boot. The request hot path must do no reflection lookups, and a typed handler should not allocate more than equivalent hand-written code (check with `make bench`); Phase 3's target is pipeline overhead under a few hundred nanoseconds per request over bare `net/http`.
- Standard library first: `net/http` (ServeMux method and wildcard patterns), `log/slog`, `context`, `errors`, `iter`. Keep `net/http` compatibility so existing Go middleware works.
- Every feature stays visible as a function call. Prefer explicit registration and interface assertion (for lifecycle hooks, event handlers, jobs) over hidden discovery.
- Wiring errors must be caught before serving traffic, with messages that name the path through the module tree. `dygo check` and `dygo.Describe` catch them in CI without starting the app.
- Request-scoped providers are supported but discouraged in docs; prefer passing request data through `ctx`.
