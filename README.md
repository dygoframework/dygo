# Dygo

A modular framework for Go: modules, dependency injection, lifecycle hooks, controllers with typed handlers, and the guard, interceptor, pipe and filter pipeline with typed metadata and validation. It stays idiomatic Go: constructors, `context.Context`, `error` values, no code generation required.

> Status: phases 0 to 6 of 7 done (core container, modules, HTTP layer, pipeline, production essentials, the `dygo` CLI, transports and background work). Phase 7 (v1.0) is in progress. Start with [getting started](docs/getting-started.md). Plans: [PLAN.md](PLAN.md) and [docs/](docs/README.md).

```go
var UsersModule = &dygo.Module{
    Name:      "users",
    Imports:   []*dygo.Module{store.Module},
    Providers: []dygo.Provider{
        dygo.Provide(NewStoreRepo, dygo.As[Repo]()),
        dygo.Provide(NewUserService),
    },
    Exports: []dygo.Token{dygo.TypeOf[*UserService]()},
}

func (c *UsersController) Routes(r dygo.Router) {
    r = r.Prefix("/users")
    r.Get("/{id}", dygo.Handle(c.Get))
}

type GetIn struct {
    ID int `path:"id"`
}

func (c *UsersController) Get(ctx context.Context, in GetIn) (*User, error) { ... }

app, err := dygo.Create(ctx, AppModule, dygo.WithGlobalPrefix("/api"))
err = app.Listen(ctx, ":9900")
```

## Try it

```sh
go test -race ./...
go run ./examples/worker     # Ctrl+C to watch shutdown hooks
(cd examples/http-users && go run .)  # users API on :9900, docs at /docs
(cd examples/academy && go test ./...)  # a course platform backend, used to dogfood v1.0
docker compose -f examples/http-users/compose.yaml up --build  # with Jaeger traces on :16686
```

Requires Go 1.27+. The core has no third party dependencies.

## Performance

Typed JSON handlers cost about the same as hand written `net/http` and are faster than chi, Gin and Huma on the same work. Method and numbers: [benchmarks/](benchmarks/README.md).

## Security

See [SECURITY.md](SECURITY.md) for how to report a vulnerability.

## License

MIT, see [LICENSE](LICENSE).
