# Dygo

[![CI](https://github.com/dygoframework/dygo/actions/workflows/ci.yml/badge.svg)](https://github.com/dygoframework/dygo/actions/workflows/ci.yml)
[![Coverage](https://coveralls.io/repos/github/dygoframework/dygo/badge.svg?branch=main)](https://coveralls.io/github/dygoframework/dygo?branch=main)

A modular, secure-by-default web framework for Go. Modules, dependency
injection and typed handlers, on plain `net/http`, with a core that uses only
the standard library.

```sh
go install github.com/dygoframework/dygo/cmd/dygo@latest
dygo new shop -security
cd shop && dygo start
```

That is a running API on <http://localhost:9900>, with login, roles, security
headers, CORS, rate limits and OpenAPI docs at `/docs`. Requires Go 1.27+.

## Secure by default

Dygo is checked against the [OWASP Top 10](docs/owasp.md) on every change, and
the safe choice is the one you get without asking:

- **Deny by default.** With `dygo g security` (or `dygo new -security`),
  every route needs a logged-in user unless you open it. Roles, authorities
  and owner checks are one line each.
- **Tokens done right.** Signed tokens with a fixed algorithm (`alg: none`
  and algorithm swaps are refused), PBKDF2 passwords, rotating refresh tokens
  stored hashed, and reuse of an old refresh token ends the login. Or use
  Keycloak, Auth0, Okta or Entra ID with `-oidc`.
- **Strict input.** Unknown JSON fields, trailing data and oversized bodies
  are refused before your code runs. The request binder is fuzzed.
- **No leaks.** A 500 never shows the underlying error, and passwords and
  tokens are never logged. Failed logins, denied requests and bad tokens are.
- **Limits on.** Body size limits, login and refresh rate limits, secure
  headers and CORS are part of a new project.
- **Small supply chain.** The core has no third party dependencies. CI runs
  gosec, govulncheck and fuzzing on every module, and Dependabot keeps the
  rest current.

Report a vulnerability privately, see [SECURITY.md](SECURITY.md).

## Easy to use

A feature is a service, a controller and a module. `dygo g resource products`
writes one for you, or write it by hand:

```go
type GetIn struct {
    ID int `path:"id"` // "abc" is a 400 before your code runs
}

func (c *Controller) Routes(r dygo.Router) {
    r = r.Prefix("/products")
    r.Get("/{id}", dygo.Handle(c.Get))
}

func (c *Controller) Get(ctx context.Context, in GetIn) (Product, error) {
    p, ok := c.svc.Find(in.ID)
    if !ok {
        return Product{}, errs.NotFound("product not found")
    }
    return p, nil // written as JSON
}

var Module = &dygo.Module{
    Name:        "products",
    Providers:   []dygo.Provider{dygo.Provide(NewService)},
    Controllers: []dygo.Provider{dygo.Provide(NewController)},
}

func main() {
    ctx := context.Background()
    app, err := dygo.Create(ctx, &dygo.Module{Name: "app", Imports: []*dygo.Module{Module}})
    if err != nil {
        log.Fatal(err) // every wiring problem at once: missing provider, cycle, route clash
    }
    log.Fatal(app.Listen(ctx, ":9900")) // graceful shutdown on Ctrl+C
}
```

Constructors are injected, inputs are bound from tags, outputs are JSON, and
the whole app is checked when it starts. Testing is just as short:

```go
srv := dygotest.New(products.Module).CompileHTTP(t)
srv.GET("/products/abc").Do(t).Status(t, 400)
```

## Fast

Typed JSON handlers cost the same as hand written `net/http`, and are about
2x faster than Gin and Huma, with the fewest allocations:

| `POST /users/42` with JSON in and out | Time per request | Allocations |
|---|---|---|
| net/http (hand written) | 2.49 µs | 13 |
| **Dygo** | **2.51 µs** | **11** |
| chi | 3.23 µs | 16 |
| Gin | 4.63 µs | 21 |
| Huma | 5.39 µs | 20 |

Method, all scenarios and raw results: [benchmarks/](benchmarks/README.md).

## Learn more

- [Getting started](docs/getting-started.md): a tested JSON API in ten minutes
- Guides: [core](docs/guide-core.md), [HTTP](docs/guide-http.md),
  [pipeline](docs/guide-pipeline.md), [production](docs/guide-production.md),
  [CLI](docs/guide-cli.md), [transports](docs/guide-transports.md), [AI](docs/guide-ai.md)
- Recipes: [security](docs/recipes/security.md), [SQL databases](docs/recipes/sql.md),
  [testing](docs/recipes/testing.md)
- Examples: [examples/](examples)

## License

MIT, see [LICENSE](LICENSE).
