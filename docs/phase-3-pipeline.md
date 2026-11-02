# Phase 3: Pipeline and metadata

**Goal:** the full enhancer pipeline with a fixed order and three binding levels.

```
Request → Middleware → Guards → Interceptors (before) → Pipes (bind + validate) → Handler
        → Interceptors (after) → Response
Exception filters catch any error or recovered panic at any stage.
```

**Depends on:** phase 2.

## Public API

```go
type Middleware func(http.Handler) http.Handler

type Guard interface{ CanActivate(ctx ExecutionContext) (bool, error) }
type GuardFunc func(ctx ExecutionContext) (bool, error)

type CallHandler func() (any, error)
type Interceptor interface{ Intercept(ctx ExecutionContext, next CallHandler) (any, error) }

type ArgumentMetadata[T any] struct { Source string; Name string; Type reflect.Type }
type Pipe interface{ Transform(ctx ExecutionContext, value any, meta ArgumentMetadata[any]) (any, error) }

type ArgumentsHost interface { Kind() ContextKind; HTTP() HTTPHost /* RPC(), WS() later */ }
type ExceptionFilter interface{ Catch(err error, host ArgumentsHost) bool }
func FilterFor[E error](fn func(e E, host ArgumentsHost)) ExceptionFilter // matches with errors.As

type ExecutionContext interface {
    context.Context
    ArgumentsHost
    Handler() RouteInfo        // method, pattern, handler name
    Controller() reflect.Type
    Meta() meta.Reader
}

// Binding
func Guards(g ...any) RouteOption        // instances or GuardType[T]()
func Interceptors(i ...any) RouteOption
func Pipes(p ...any) RouteOption
func Filters(f ...any) RouteOption
func GuardType[T Guard]() any             // resolved through DI at boot

// Global
func (a *HTTPApp) UseGlobalGuards(g ...Guard)   // + Interceptors, Pipes, Filters
const AppGuards = "dygo.app.guards"            // group name: Provide(NewX, Group(AppGuards)) gives DI to global enhancers
```

## Metadata (`dygo/meta`)

```go
type Key[T any] struct{ name string }
func NewKey[T any](name string) Key[T]
func Set[T any](k Key[T], v T) dygo.RouteOption
func Get[T any](ctx ExecutionContext, k Key[T]) (T, bool)          // route first, then controller
func GetAllAndMerge[T any](ctx ExecutionContext, k Key[[]T]) []T
func GetAllAndOverride[T any](ctx ExecutionContext, k Key[T]) (T, bool)
```

Replaces `SetMetadata`, `Reflector.get`, `Reflector.createDecorator`. Composition (`applyDecorators`) is `dygo.Options(opts...)`.

## Validation (`dygo/validate`)

```go
type Schema interface{ Validate(ctx context.Context, v any) error } // returns *ValidationError
type ValidationError struct{ Fields []FieldError }
func Use(s Schema) dygo.Option   // global validator, default: validator tags adapter
```

- Adapter `validate/playground` wraps `go-playground/validator/v10` (the first and only third party dependency so far, isolated in its own sub-module so the core stays dependency free).
- Option for the validation error response shape: `validate.WithErrorShape(func([]FieldError) any)`.
- Response validation (like `StandardSchemaSerializerInterceptor`): opt-in interceptor `validate.Responses()`.

## Internal design
- The chain per route is composed **once at boot** into a single function; global, controller, and route enhancers are concatenated in that order.
- Guards returning `false` produce 403 (`errs.Forbidden`); errors from guards go to filters.
- Filters run from most specific (route) to global; the first returning `true` wins; the default filter is last.
- `ExecutionContext` is a pooled struct to avoid allocations.

## Tasks
1. Interfaces, chain composer, per level binding.
2. DI resolution of enhancer types and the `AppGuards`/`AppInterceptors`/`AppPipes`/`AppFilters` groups.
3. `meta` package.
4. `validate` package and playground adapter.
5. Built-in enhancers: `ClassSerializer` equivalent is not needed (JSON tags do it); ship `Timeout(d)` interceptor, `Logging` interceptor, `Recover` filter.
6. Example update: auth guard + `Roles` metadata + logging interceptor.
7. Benchmarks with 2 guards + 2 interceptors.

## Exit criteria
- Example passes e2e tests for 401/403/400/200 paths.
- Pipeline overhead with 2 guards + 2 interceptors stays under a few hundred nanoseconds over phase 2 and allocations are recorded here.

## Status: done (2026-10-30)

### What was built
- `pipeline.go`: `Guard`, `Interceptor`, `CallHandler`, `Pipe`, `ExceptionFilter` and their `...Func` adapters; `ExecutionContext`, `ArgumentsHost`, `HTTPHost`; `FilterFor[E]`; `PanicError`; route options `Guards`, `Interceptors`, `Pipes`, `Filters`, `SetMetadata`; options `WithGlobalGuards`, `WithGlobalInterceptors`, `WithGlobalPipes`, `WithGlobalFilters`; groups `AppGuards`, `AppInterceptors`, `AppPipes`, `AppFilters`; `Ref[T]`, `RefToken`. Enhancers are resolved at boot (bad arguments and missing dependencies are in the `BootError`) and each route's chain is composed once.
- `meta/`: `meta.Key[T]` with `Set`, `Get`, `Route`, `Controller`; `meta.Set`, `meta.Get`, `meta.GetAllAndMerge`, `meta.GetAllAndOverride`.
- `validate/`: `Schema`, `ValidationError` (a 400 through the new `errs.Converter`), `Errors` builder, `Methods()`, `Pipe`, `Use`, `WithErrorShape`, `Responses`.
- `validate/playground/`: separate Go module adapting go-playground/validator v10.30.1.
- `interceptors/`: `Timeout(d)` (408 when the handler gives up on the deadline) and `Logging(log)`.
- Example `examples/http-users`: global auth guard from DI (`auth.Module`, `dygo.AppGuards`), `auth.Roles` and `auth.Public` metadata, `validate.Methods()` validation, `interceptors.Logging`. e2e tests cover 401, 403, 400 and 200.

### Order (tested in `TestPipelineOrder`)
Guards, interceptors and pipes: global, then controller (`Router.Use`), then route. Filters: route, then controller, then global, then the error writer. A guard returning false gives `403 Forbidden resource`; a guard error goes to the filters as is.

### Benchmarks (same machine as phase 2, `make bench`)

| Benchmark | ns/op | allocs/op |
|---|---|---|
| HandleTyped (no enhancers) | ~5,900 to 6,200 | 37 |
| Pipeline: 2 guards + 2 interceptors + 1 pipe | ~5,900 to 6,300 | 37 |
| Hand-written net/http equivalent | ~5,700 to 6,100 | 37 |

The pipeline adds no allocations and its time is within run-to-run noise (well under the "few hundred nanoseconds" target).

### Differences from the plan
- `ExecutionContext` has `Context()`/`SetContext()` instead of embedding `context.Context` ([D17](decisions.md)); `Handler()` is named `Route()`.
- Pipes take the whole input pointer, `Transform(ec, in any) error`, with no `ArgumentMetadata` ([D18](decisions.md)).
- `GuardType[T]()` became the generic `Ref[T]()`, constructors are accepted too, and `UseGlobalGuards` became `WithGlobalGuards` options ([D19](decisions.md)).
- `CallHandler` is an interface (`next.Handle()`) ([D20](decisions.md)).
- `validate.Use` defaults to nothing: pass `validate.Methods()` (core) or `playground.New()` ([D21](decisions.md)).
- No separate `Recover` filter: panics were already recovered in phase 2 and now reach filters as `*dygo.PanicError`.

### Performance pass (2026-10-30)

A new `BenchmarkRealistic` models the example's routes: a global guard that puts the user in the context and checks merged roles, validation through `validate.Methods()`, and `interceptors.Logging` with Info turned off.

| Benchmark | allocs/op before | after |
|---|---|---|
| HandleTyped (no enhancers) | 37 | 36 |
| Pipeline: 2 guards + 2 interceptors + 1 pipe | 37 | 36 |
| Realistic | 45 | 37 |
| Hand-written net/http equivalent | 37 | 37 |

Changes:
- The request is no longer cloned with `r.WithContext` on every request, and `SetContext` no longer clones it either. Typed handlers get the context directly; a clone is made only when something needs an `*http.Request` whose `Context()` carries the Dygo state: raw handlers, `dygo.Request(ctx)`, `ec.HTTP().Request()`, custom binder sources, `Responder`s, redirects and the error writer ([D23](decisions.md)).
- `meta.GetAllAndMerge` returns a slice joined once at startup (new `ExecutionContext.MergedMetadata`) instead of allocating one per call. The slice is shared, so callers must not modify it.
- `interceptors.Logging` checks `log.Enabled` first and costs nothing when Info is off; when on it uses `LogAttrs`.
- `validate.Methods()` merges results without building a slice of errors.

Time per request is within run-to-run noise for all of these (about 5.6 to 6.4 µs on this machine); allocations are the stable signal.
