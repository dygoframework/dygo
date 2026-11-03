# Getting started

Dygo is a modular framework for Go: modules, dependency injection,
lifecycle hooks, controllers with typed handlers, and the guard, interceptor,
pipe and filter pipeline. It runs on `net/http`, and the core has no third
party dependencies.

This page takes you from nothing to a tested JSON API in about ten minutes.

## Install

You need Go 1.27 or newer.

```sh
go install github.com/dygoframework/dygo/cmd/dygo@latest
dygo new shop
cd shop
go run .
```

Open <http://localhost:9900/health/live> for a liveness check and
<http://localhost:9900/docs> for the OpenAPI UI. To lay out the project
yourself instead, `go get github.com/dygoframework/dygo` is all you need.

## The pieces

| You write | What it is |
|---|---|
| `var Module = &dygo.Module{...}` | a module: providers, imports, exports, controllers |
| `func NewService(repo Repo) *Service` | a provider: a constructor whose parameters are injected |
| `func (c *Controller) Routes(r dygo.Router)` | a controller: registers its routes |
| `func (c *Controller) Get(ctx, in GetIn) (Out, error)` | a typed handler: input bound from tags, output written as JSON |
| `dygo.Create(ctx, AppModule)` | boots the app: checks the whole graph first |

## A feature module

Generate one, or write the same files by hand:

```sh
dygo g resource products
```

The heart of it is a service, a controller and a module:

```go
package products

type Product struct {
    ID    int    `json:"id"`
    Name  string `json:"name"`
    Price int    `json:"price"`
}

type Service struct {
    mu    sync.Mutex
    items map[int]Product
    next  int
}

func NewService() *Service { return &Service{items: map[int]Product{}} }

func (s *Service) Find(id int) (Product, bool) {
    s.mu.Lock()
    defer s.mu.Unlock()
    p, ok := s.items[id]
    return p, ok
}

func (s *Service) Add(name string, price int) Product {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.next++
    p := Product{ID: s.next, Name: name, Price: price}
    s.items[p.ID] = p
    return p
}

type Controller struct{ svc *Service }

func NewController(svc *Service) *Controller { return &Controller{svc: svc} }

func (c *Controller) Routes(r dygo.Router) {
    r = r.Prefix("/products")
    r.Get("/{id}", dygo.Handle(c.Get))
    r.Post("/", dygo.Handle(c.Create)) // 201 by default for POST
}

type GetIn struct {
    ID int `path:"id"` // "abc" is a 400 before your code runs
}

func (c *Controller) Get(ctx context.Context, in GetIn) (Product, error) {
    p, ok := c.svc.Find(in.ID)
    if !ok {
        return Product{}, errs.NotFound("product not found")
    }
    return p, nil
}

type CreateIn struct {
    Body struct {
        Name  string `json:"name"`
        Price int    `json:"price"`
    } `body:"json"`
}

func (c *Controller) Create(ctx context.Context, in CreateIn) (Product, error) {
    return c.svc.Add(in.Body.Name, in.Body.Price), nil
}

var Module = &dygo.Module{
    Name:        "products",
    Providers:   []dygo.Provider{dygo.Provide(NewService)},
    Controllers: []dygo.Provider{dygo.Provide(NewController)},
    Exports:     []dygo.Token{dygo.TypeOf[*Service]()},
}
```

Import it from the root module and start the app:

```go
var AppModule = &dygo.Module{
    Name:    "app",
    Imports: []*dygo.Module{products.Module},
}

func main() {
    ctx := context.Background()
    app, err := dygo.Create(ctx, AppModule, dygo.WithGlobalPrefix("/api"))
    if err != nil {
        log.Fatal(err) // every wiring problem at once: missing provider, cycle, route clash
    }
    if err := app.Listen(ctx, ":9900"); err != nil { // graceful shutdown on Ctrl+C
        log.Fatal(err)
    }
}
```

## Errors

Return an error from the handler. `errs` gives JSON error bodies:

```go
return Product{}, errs.Conflict("name taken", errs.Code("PRODUCT_EXISTS"))
```

```json
{"statusCode":409,"error":"Conflict","message":"name taken","code":"PRODUCT_EXISTS"}
```

Any other error is a 500 with a generic message, and the real error is
logged, never sent.

## Validation

Give the input a `Validate` method and turn validation on once:

```go
func (in *CreateIn) Validate(ctx context.Context) error {
    var v validate.Errors
    if in.Body.Name == "" {
        v.Add("body", "name", "is required")
    }
    return v.Err()
}

app, err := dygo.Create(ctx, AppModule, validate.Use(validate.Methods()))
```

For `validate:"required,email"` struct tags, use the
`github.com/dygoframework/dygo/validate/playground` module instead.

## Test it

```go
func TestCreateProduct(t *testing.T) {
    srv := dygotest.New(products.Module).CompileHTTP(t)

    var p products.Product
    srv.POST("/products").JSON(map[string]any{"name": "pen", "price": 2}).
        Do(t).Status(t, 201).JSON(t, &p)
    srv.GET("/products/" + strconv.Itoa(p.ID)).Do(t).Status(t, 200)
    srv.GET("/products/abc").Do(t).Status(t, 400)
}
```

## Where next

- Guides: [core](guide-core.md), [HTTP](guide-http.md),
  [pipeline](guide-pipeline.md), [production](guide-production.md),
  [CLI](guide-cli.md), [transports](guide-transports.md).
- Recipes: [authentication](recipes/auth.md), [security](recipes/security.md), [SQL databases](recipes/sql.md),
  [testing](recipes/testing.md).
- Full API: `go doc github.com/dygoframework/dygo` or pkg.go.dev.
