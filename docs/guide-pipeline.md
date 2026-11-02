# Guide: the pipeline

What phase 3 gives you: guards, interceptors, pipes, exception filters, typed metadata and validation. Full example: `examples/http-users` (the `auth` package and `users/controller.go`).

```
Request → Middleware → Guards → Interceptors (before) → bind → Pipes → Handler
        → Interceptors (after) → write
Exception filters receive any error or recovered panic from any stage.
```

## Where enhancers bind

| Level | How | Order |
|---|---|---|
| Global | `dygo.WithGlobalGuards(...)` and friends, or a provider in `dygo.AppGuards` / `AppInterceptors` / `AppPipes` / `AppFilters` | first |
| Controller | `r = r.Use(dygo.Guards(...))` in `Routes` | second |
| Route | `r.Get("/x", h, dygo.Guards(...))` | last |

Filters run the other way round: route, controller, global, then the error writer.

Every enhancer argument can be:

```go
dygo.Guards(myGuard)                     // an instance
dygo.Guards(func(ec dygo.ExecutionContext) (bool, error) { ... }) // a plain function
dygo.Guards(dygo.Ref[*AuthGuard]())      // a provider visible from the controller's module
dygo.Guards(NewAuthGuard)                // a constructor, built once per module with DI
```

Everything resolves at startup: a missing dependency or a wrong type is part of the `BootError`.

## Guards

```go
type AuthGuard struct{ tokens *Tokens }

func (g *AuthGuard) CanActivate(ec dygo.ExecutionContext) (bool, error) {
    tok, ok := strings.CutPrefix(ec.HTTP().Request().Header.Get("Authorization"), "Bearer ")
    if !ok {
        return false, errs.Unauthorized("missing bearer token") // 401
    }
    u, ok := g.tokens.Lookup(tok)
    if !ok {
        return false, nil // 403 Forbidden resource
    }
    ec.SetContext(context.WithValue(ec.Context(), userKey{}, u)) // the handler's ctx sees it
    return true, nil
}

// Global, with DI, like APP_GUARD:
dygo.Provide(NewAuthGuard, dygo.Group(dygo.AppGuards))
```

## Metadata

```go
var rolesKey = meta.NewKey[[]string]("roles")

func Roles(r ...string) dygo.RouteOption { return rolesKey.Set(r) }

r = r.Use(Roles("user"))                         // controller level
r.Delete("/{id}", dygo.Handle(c.Delete), Roles("admin")) // route level

roles, ok := rolesKey.Get(ec)               // route value, else controller value
all := meta.GetAllAndMerge(ec, rolesKey)    // controller + route: ["user", "admin"]
                                            // joined at startup and shared: do not modify
```

## Interceptors

```go
func Envelope(ec dygo.ExecutionContext, next dygo.CallHandler) (any, error) {
    out, err := next.Handle()   // runs later interceptors, pipes and the handler
    if err != nil {
        return nil, err
    }
    return map[string]any{"data": out}, nil
}

r.Get("/x", h, dygo.Interceptors(Envelope))
```

Not calling `next.Handle()` skips the handler (a cache); calling it twice retries. Built-ins: `interceptors.Timeout(5*time.Second)` (408 when the handler gives up on its deadline; the handler must honour `ctx`) and `interceptors.Logging(log)`.

## Pipes and validation

A pipe gets a pointer to the bound input struct after binding. Parsing (`ParseIntPipe` and friends) is already done by the binder, so pipes are for validation and normalising.

```go
// No dependency: input types check themselves.
app, err := dygo.Create(ctx, AppModule, validate.Use(validate.Methods()))

type NewUser struct {
    Email string `json:"email"`
    Name  string `json:"name"`
}

func (u *NewUser) Validate(ctx context.Context) error {
    var v validate.Errors
    if !strings.Contains(u.Email, "@") {
        v.Add("body", "email", "must be a valid email")
    }
    return v.Err()
}

type CreateIn struct {
    Body NewUser `body:"json"` // Validate is found on the Body field too
}
```

With struct tags (separate module `github.com/dygoframework/dygo/validate/playground`, wraps go-playground/validator):

```go
type CreateIn struct {
    Body struct {
        Email string `json:"email" validate:"required,email"`
    } `body:"json"`
    Limit int `query:"limit" validate:"lte=100"`
}

app, err := dygo.Create(ctx, AppModule, validate.Use(playground.New()))
```

Either way a failure is one 400 listing every field, named as the client sent it:

```json
{"statusCode":400,"error":"Bad Request","message":"validation failed",
 "details":[{"source":"body","name":"email","reason":"must be a valid email"},
            {"source":"query","name":"limit","value":"500","reason":"must be at most 100"}]}
```

`validate.WithErrorShape(fn)` changes `details` (for example to a list of message strings). `validate.Responses(schema)` is an interceptor that checks handler results and turns a bad one into a 500.

## Exception filters

```go
dygo.FilterFor(func(e *store.ConflictError, host dygo.ArgumentsHost) {
    errs.WriteJSON(host.HTTP().Response(), errs.ToBody(errs.Conflict(e.Error())))
})
```

`FilterFor` matches with `errors.As`, so wrapped errors are found. A recovered panic arrives as `*dygo.PanicError`. A filter that returns false (or a `FilterFor` that does not match) passes the error on; the default error writer is always last.

## Execution context

| Method | Gives |
|---|---|
| `Context()`, `SetContext(ctx)` | the request context; replace it for later stages and the handler |
| `HTTP().Request()`, `HTTP().Response()` | the request and response writer |
| `Route()` | method, path pattern, version, controller, handler name |
| `Controller()` | the controller's `reflect.Type` |
| `Kind()` | `dygo.KindHTTP` (other transports later) |

Enhancers are shared by all requests, so they must be safe for concurrent use.
