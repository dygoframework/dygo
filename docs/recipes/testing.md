# Recipe: testing

Dygo apps are tested with the standard `testing` package. The `dygotest`
package boots a module the way `dygo.Create` does, with overrides, and shuts it
down when the test ends.

## Unit test a provider

A provider is a constructor, so the simplest test does not need Dygo at all:

```go
func TestPricing(t *testing.T) {
    svc := orders.NewPricing(fakeRates{"EUR": 1.1})
    if got := svc.Total(order); got != 110 {
        t.Fatalf("total = %d", got)
    }
}
```

Reach for `dygotest` when you want the wiring as well.

## Test a module's wiring

```go
func TestWiring(t *testing.T) {
    app := dygotest.New(orders.Module).
        OverrideValue(dygo.TypeOf[orders.Repo](), &fakeRepo{}).
        Compile(t) // fails the test on a boot error, closes the app on cleanup

    svc := dygo.MustGet[*orders.Service](app)
    // ...
}
```

`Compile` reports every wiring problem at once, with the module path, just as
the real boot would.

## Test HTTP routes

`CompileHTTP` gives a small client for the app's handler, with no network:

```go
func TestCreateOrder(t *testing.T) {
    srv := dygotest.New(orders.Module).
        With(validate.Use(validate.Methods())). // the same options as main
        CompileHTTP(t)

    var o orders.Order
    srv.POST("/orders").JSON(map[string]any{"sku": "pen", "qty": 2}).
        Do(t).Status(t, 201).JSON(t, &o)

    srv.GET("/orders/" + o.ID).Do(t).Status(t, 200)
    srv.POST("/orders").JSON(map[string]any{"qty": -1}).Do(t).Status(t, 400)
}
```

Keep the app's options in one function (`AppOptions()` in a `dygo new -t
modular` project) and pass them with `With(...)`, so tests and `main` use the
same global prefix, pipes and guards.

## Overrides

| Need | Use |
|---|---|
| replace a provider with a fixed value | `OverrideValue(dygo.TypeOf[T](), v)` |
| replace it with another constructor | `OverrideProvider(dygo.TypeOf[T](), dygo.Provide(NewFake))` |
| replace a named token | `OverrideValue(myToken, v)` |
| test a few providers without a module | `dygotest.Module([]dygo.Provider{...}, imports...)` |
| fixed config | `config.Env(map[string]string{...})` in `config.Load` or `config.ForRoot` |

An override replaces a token that some module already provides. It cannot add
one. When a feature module depends on a global module that the app's root
module imports (a database, config, auth), the test root must import it too:

```go
root := &dygo.Module{Name: "test", Imports: []*dygo.Module{database.Module, users.Module}}
srv := dygotest.New(root).
    OverrideValue(dygo.TypeOf[*database.Config](), &database.Config{}).
    OverrideValue(dygo.TypeOf[*database.DB](), &database.DB{}).
    CompileHTTP(t)
```

Every singleton is built at boot, including the ones nothing uses, so
overriding a provider does not skip its config: override the config as well,
as above. Overridden values still get their lifecycle hooks.

## Test guards, interceptors and pipes alone

They are interfaces with one method, so call them directly with a small fake
`ExecutionContext` that embeds the interface and implements only what the
enhancer uses:

```go
type fakeEC struct {
    dygo.ExecutionContext
    ctx context.Context
    req *http.Request
}

func (f *fakeEC) Context() context.Context       { return f.ctx }
func (f *fakeEC) SetContext(ctx context.Context) { f.ctx = ctx }
func (f *fakeEC) HTTP() dygo.HTTPHost            { return f }
func (f *fakeEC) Request() *http.Request         { return f.req }
func (f *fakeEC) Response() http.ResponseWriter  { return httptest.NewRecorder() }
```

For anything that reads route metadata, test through a route with
`CompileHTTP` instead: metadata is attached at registration.

## Catch wiring errors in CI

`dygo check` boots the application's wiring without starting servers and
fails with the same messages as a real boot. Add it next to the tests:

```sh
go vet ./... && go test -race ./... && dygo check
```

## End to end

The `app_test.go` that `dygo new` writes boots the whole
`AppModule` with test config. Keep one test like it, so a broken import in
any module fails CI and not the deploy.

## Fuzz your inputs

Bind errors are part of the contract: a bad path, query or body must be a 400,
never a 500. A Go fuzz test against a route finds the cases you did not think
of:

```go
func FuzzCreateOrder(f *testing.F) {
    srv := dygotest.New(orders.Module).CompileHTTP(f)
    f.Add(`{"sku":"pen","qty":2}`)
    f.Fuzz(func(t *testing.T, body string) {
        res := srv.POST("/orders").Body(strings.NewReader(body), "application/json").Do(t)
        if res.StatusCode >= 500 {
            t.Fatalf("%d for %q", res.StatusCode, body)
        }
    })
}
```
