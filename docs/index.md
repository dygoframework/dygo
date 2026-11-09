# Dygo

A modular framework for Go: modules, dependency injection, lifecycle
hooks, controllers with typed handlers, and the guard, interceptor, pipe and
filter pipeline. It stays idiomatic Go: constructors, `context.Context`,
`error` values and `net/http`. The core has no third party dependencies.

```go
type GetIn struct {
    ID int `path:"id"`
}

func (c *UsersController) Routes(r dygo.Router) {
    r = r.Prefix("/users")
    r.Get("/{id}", dygo.Handle(c.Get), auth.Required())
}

func (c *UsersController) Get(ctx context.Context, in GetIn) (*User, error) {
    return c.users.Find(ctx, in.ID)
}

var UsersModule = &dygo.Module{
    Name:        "users",
    Providers:   []dygo.Provider{dygo.Provide(NewUsersService)},
    Controllers: []dygo.Provider{dygo.Provide(NewUsersController)},
}
```

- **[Getting started](getting-started.md)**: a tested JSON API in ten minutes.
- **Guides**: [modules and providers](guide-core.md), [HTTP](guide-http.md),
  [pipeline](guide-pipeline.md), [production](guide-production.md),
  [CLI](guide-cli.md), [transports](guide-transports.md), [AI](guide-ai.md).
- **Recipes**: [authentication](recipes/auth.md), [security](recipes/security.md), [SQL](recipes/sql.md),
  [testing](recipes/testing.md).
- **[Benchmarks](benchmarks.md)**: typed JSON handlers cost the same as hand
  written `net/http`.

Requires Go 1.27+. MIT license.
