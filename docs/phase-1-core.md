# Phase 1: Core container, modules, lifecycle

**Goal:** the core without HTTP: an IoC container, encapsulated modules, lifecycle hooks, a standalone application context, and a testing module.

**Status:** first cut done on 2026-10-29. 31 core tests and an example app test pass with `-race`, `go vet` and `staticcheck` are clean, core coverage is about 89%.

## Feature coverage

| Feature | Dygo phase 1 | File |
|---|---|---|
| Providers by token (class, string, symbol) | `TypeOf[T]()`, `NewToken[T](name)` | `token.go` |
| Constructor injection | `Provide(ctor)`, parameters resolved by type | `provider.go`, `inject.go` |
| Injection by token | `dygo.In` struct field with `name:"..."` | `inject.go` |
| Optional dependencies | `Optional[T]` parameter, or `optional:"true"` field | `inject.go` |
| Value, class, factory and alias providers | `Value`, `Provide`, `Provide`, `Alias` | `provider.go` |
| Interface tokens | `As[I]()` | `provider.go` |
| Async providers | Constructor taking `context.Context` and returning `(T, error)` | `container.go` |
| Multi providers (global guard style) | `Group(name)` + `[]T` field tagged `group:"name"` | `inject.go` |
| Singleton and transient scopes | `Singleton`, `Transient` (`WithScope`) | `provider.go` |
| Request scope | Deferred to phase 2 ([D9](decisions.md)) | |
| Dependency cycles | `Lazy[T]` | `inject.go` |
| Modules with imports, providers, controllers, exports | `&dygo.Module{...}` | `module.go` |
| Encapsulation | Visibility rules below | `graph.go` |
| Global modules | `Global: true` | `graph.go` |
| Dynamic modules `ForRoot` / `ForFeature` | A function returning `*dygo.Module` | example `config` package |
| Re-exporting | `Exports` may list an imported token | `graph.go` |
| Standalone application context | `CreateApplicationContext(ctx, root, opts...)` | `app.go` |
| Getting a provider from the app | `app.Get(tok)`, `dygo.Get[T](app)`, `MustGet`, `GetNamed` | `app.go` |
| Container access from a module | `*ModuleRef` injection: `Get`, `Create`, `dygo.Construct[T]` | `moduleref.go` |
| Discovery | `app.Providers()`, `ProvidersImplementing[T]`, `app.Controllers()` | `app.go` |
| Lifecycle hooks (v12: by hierarchy level) | Five interfaces, run by module level | `lifecycle.go` |
| `enableShutdownHooks` | `app.Run(ctx)` handles SIGINT/SIGTERM | `app.go` |
| `Test.createTestingModule().overrideProvider()` | `dygotest.New(m).OverrideProvider/OverrideValue().Compile(t)` | `dygotest/` |
| `LazyModuleLoader` | Not in phase 1; see PLAN.md section 6 row 9 | |
| `ConfigurableModuleBuilder` | Not needed yet: a plain function does the job. Revisit in phase 4 with `config` | |

## Design

### Tokens
A token's identity is `(reflect.Type, name)`. `TypeOf[T]()` has an empty name; `NewToken[T]("x")` has name `x`. So `NewToken[string]("db.url")` and `NewToken[int]("db.url")` are different tokens, and a named token never collides with a type token.

### Providers and the tokens they register
- Default: the constructor's return type (or the value's dynamic type).
- `For(tok)`: registers under `tok` instead of the default. Can repeat.
- `As[I]()`: registers under `I` as well, sharing one instance.
- `Group(g)` without `For`/`As`: the provider only joins the group.
- Registering under a type the value is not assignable to is a boot error.

### Constructor parameters
`context.Context` (boot context), `*ModuleRef`, `Optional[T]`, `Lazy[T]`, a struct embedding `dygo.In` (fields with `name`, `optional`, `group` tags), or any other type (a required dependency).

### Visibility (encapsulation)
Seen from module M, a token resolves to the first match of:
1. A provider declared in M.
2. A token exported by one of M's imports (directly or re-exported). Two different imports exporting different providers for the same token is an error.
3. A token exported by a global module. Two globals disagreeing is an error.

Controllers are built like singletons but are never injectable or exportable. Groups are app wide ([D7](decisions.md)).

### Boot steps (`CreateApplicationContext`)
1. **Scan** imports depth first from the root. Module identity is the pointer ([D6](decisions.md)). Import cycles and missing names are errors.
2. **Register** providers per module, apply overrides, check duplicates and type compatibility.
3. **Check exports**: each exported token must be provided or imported.
4. **Link** every dependency to a provider using the visibility rules. A missing dependency error says which module has it and whether it needs an export, an import, or both.
5. **Detect cycles** over non-lazy edges, with a hint to use `Lazy[T]`.
6. **Compute levels**: level 0 for modules with no imports, else 1 + highest level of imports (non-global modules also count global modules).
7. All problems from steps 1 to 6 are returned together as `*BootError`.
8. **Instantiate** singletons, level by level, dependencies first. Constructor errors and panics stop the boot.
9. **Hooks**: every `OnModuleInit` by level, then every `OnApplicationBootstrap`. If one fails, already initialized instances are shut down before returning the error.

### Shutdown
`Shutdown(ctx, signal)` runs `OnModuleDestroy`, then `BeforeApplicationShutdown`, then `OnApplicationShutdown`, each over all instances in reverse startup order. Every hook runs even if an earlier one fails; errors are joined. It is idempotent. `Run(ctx)` waits for SIGINT, SIGTERM or `ctx` cancellation and then calls `Shutdown` with the shutdown timeout (default 10s).

### Concurrency
Boot is single threaded. After boot the container is read only: singletons are already built, and transient builds and `Lazy.Get` do not modify shared state, so `Get` is safe from many goroutines.

## Known limits and follow-ups
- Request scope and durable providers: phase 2 and phase 6.
- Module levels use imports only. A global module that uses another global module without importing it may get hooks in registration order rather than level order. Import what you use to be safe.
- `ModuleRef.Get` resolves only what is visible from that module (strict). `App.Get` is non-strict.
- Lifecycle hooks are not called on transient instances or instances from `ModuleRef.Create`.
- A provider that is overridden for every token it registers is skipped entirely, so tests can replace providers whose real dependencies are missing.

## Exit criteria (all met)
- [x] Worker app with several modules boots: `go run ./examples/worker`, stops cleanly on Ctrl+C.
- [x] A missing provider error names the module path and how to fix it.
- [x] Tests can override a provider (`examples/worker/app_test.go`).
- [x] `go vet`, `go test -race`, `staticcheck` clean.
