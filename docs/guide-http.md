# Guide: HTTP

What phase 2 gives you. New to Dygo? Start with [Step by step: a CRUD controller](#step-by-step-a-crud-controller-get-post-put-patch-delete). Full example: `examples/http-users` (run it with `cd examples/http-users && go run .`).

## A controller

```go
type Controller struct{ svc *Service }

func NewController(svc *Service) *Controller { return &Controller{svc: svc} }

func (c *Controller) Routes(r dygo.Router) {
    r = r.Prefix("/users")
    r.Get("/", dygo.Handle(c.List))
    r.Get("/{id}", dygo.Handle(c.Get))
    r.Post("/", dygo.Handle(c.Create))              // 201 by default
    r.Delete("/{id}", dygo.Handle(c.Delete))         // return dygo.NoContent{} for 204
}

var Module = &dygo.Module{
    Name:        "users",
    Providers:   []dygo.Provider{dygo.Provide(NewService)},
    Controllers: []dygo.Provider{dygo.Provide(NewController)},
}
```

Paths use Go's syntax: `{id}`, `{path...}`. Writing `:id` is a startup error with a hint.

## Step by step: a CRUD controller (GET, POST, PUT, PATCH, DELETE)

Every handler has the same shape: **`func(ctx context.Context, in In) (Out, error)`**. `In` is a struct that says where each value comes from (path, query, body, ...). `Out` is what you send back, as JSON by default. Register the handler with `dygo.Handle(c.Method)` and the HTTP method you want. The full code below is in [`crud_example_test.go`](https://github.com/dygoframework/dygo/blob/main/crud_example_test.go), so it compiles and is tested.

### 1. Register the routes

```go
type BooksController struct{ store *BookStore }

func NewBooksController(s *BookStore) *BooksController { return &BooksController{store: s} }

func (c *BooksController) Routes(r dygo.Router) {
    r = r.Prefix("/books")
    r.Get("/", dygo.Handle(c.List))           // GET    /books?author=...
    r.Get("/{id}", dygo.Handle(c.Get))        // GET    /books/1
    r.Post("/", dygo.Handle(c.Create))        // POST   /books        -> 201
    r.Put("/{id}", dygo.Handle(c.Replace))    // PUT    /books/1      -> 200
    r.Patch("/{id}", dygo.Handle(c.Update))   // PATCH  /books/1      -> 200
    r.Delete("/{id}", dygo.Handle(c.Delete))  // DELETE /books/1      -> 204
}

var BooksModule = &dygo.Module{
    Name:        "books",
    Providers:   []dygo.Provider{dygo.Provide(NewBookStore)},
    Controllers: []dygo.Provider{dygo.Provide(NewBooksController)},
}
```

| To | Write |
|---|---|
| register routes | `r.Get("/", h)`, `r.Post("/", h)`, `r.Put("/{id}", h)`, `r.Patch("/{id}", h)`, `r.Delete("/{id}", h)` |
| read a path parameter as an int | `ID int \`path:"id"\`` in the input struct |
| read a query parameter | `Author string \`query:"author"\`` |
| read a JSON body | `Body CreateBookDto \`body:"json"\`` |
| answer 204 | return `dygo.NoContent{}`, or the route option `dygo.HTTPCode(204)` |
| return a pre-serialized JSON string | return `dygo.RawJSON(b)`: written as is, not validated |
| answer 404 | `return ..., errs.NotFound("...")` |

### 2. GET: read query and path parameters

```go
type ListBooksIn struct {
    Author string `query:"author"` // optional: "" when missing
}

func (c *BooksController) List(ctx context.Context, in ListBooksIn) ([]Book, error) {
    return c.store.List(in.Author), nil
}

type BookIDIn struct {
    ID int `path:"id"` // "abc" is a 400 before the handler runs
}

func (c *BooksController) Get(ctx context.Context, in BookIDIn) (Book, error) {
    b, ok := c.store.Get(in.ID)
    if !ok {
        return Book{}, errs.NotFound(fmt.Sprintf("book %d not found", in.ID))
    }
    return b, nil
}
```

A handler that needs nothing from the request uses `dygo.HandleNoInput(c.Hello)` with `func(ctx context.Context) (Out, error)`.

### 3. POST: read a JSON body

Put the body in a field tagged `body:"json"`. Fields inside it use normal `json` tags.

```go
type BookBody struct {
    Title  string `json:"title"`
    Author string `json:"author"`
}

type CreateBookIn struct {
    Body BookBody `body:"json"`
}

func (c *BooksController) Create(ctx context.Context, in CreateBookIn) (Book, error) {
    if in.Body.Title == "" {
        return Book{}, errs.BadRequest("title is required")
    }
    return c.store.Add(in.Body), nil // POST answers 201 Created
}
```

The request must have `Content-Type: application/json`, otherwise the answer is 400. Broken JSON is a 400 too, and a body over 1 MiB is a 413 (`dygo.BodyLimit(n)` changes that per route). `dygo.StrictJSON()` also rejects unknown fields. For real validation, implement `Validate(ctx) error` on the body type or use struct tags; see the [pipeline guide](guide-pipeline.md#pipes-and-validation).

### 4. PUT: a path parameter and a body together

One input struct can mix sources:

```go
type ReplaceBookIn struct {
    ID   int      `path:"id"`
    Body BookBody `body:"json"`
}

func (c *BooksController) Replace(ctx context.Context, in ReplaceBookIn) (Book, error) {
    b, ok := c.store.Replace(in.ID, in.Body)
    if !ok {
        return Book{}, errs.NotFound(fmt.Sprintf("book %d not found", in.ID))
    }
    return b, nil // 200
}
```

### 5. PATCH: change only the fields that were sent

Use pointers, so a field that was not sent (`nil`) is different from one sent empty (`""`):

```go
type UpdateBookIn struct {
    ID   int `path:"id"`
    Body struct {
        Title  *string `json:"title"`
        Author *string `json:"author"`
    } `body:"json"`
}

func (c *BooksController) Update(ctx context.Context, in UpdateBookIn) (Book, error) {
    b, ok := c.store.Get(in.ID)
    if !ok {
        return Book{}, errs.NotFound(fmt.Sprintf("book %d not found", in.ID))
    }
    if in.Body.Title != nil {
        b.Title = *in.Body.Title
    }
    if in.Body.Author != nil {
        b.Author = *in.Body.Author
    }
    c.store.Save(b)
    return b, nil
}
```

### 6. DELETE: answer 204 No Content

```go
func (c *BooksController) Delete(ctx context.Context, in BookIDIn) (dygo.NoContent, error) {
    if !c.store.Remove(in.ID) {
        return dygo.NoContent{}, errs.NotFound(fmt.Sprintf("book %d not found", in.ID))
    }
    return dygo.NoContent{}, nil // 204, empty body
}
```

To return the deleted item with 200 instead, return `(Book, error)`.

### 7. Status codes

| Situation | Status | How |
|---|---|---|
| GET, PUT, PATCH, DELETE with a value | 200 | default |
| POST | 201 | default |
| Any method, no body | 204 | return `dygo.NoContent{}` |
| Fixed other code | any | route option: `r.Post("/", h, dygo.HTTPCode(202))` |
| Code decided at run time | any | `dygo.SetStatus(ctx, 202)` inside the handler |
| Bad path, query or body value | 400 | automatic, lists every bad field |
| Errors you return | 4xx/5xx | `errs.BadRequest`, `NotFound`, `Conflict`, `Unauthorized`, `Forbidden`, ... |
| Any other error or a panic | 500 | automatic; the real error is logged, not sent |

### 8. Try it

```sh
curl -X POST localhost:9900/books -H 'Content-Type: application/json' -d '{"title":"Dune","author":"Herbert"}'
curl localhost:9900/books?author=Herbert
curl localhost:9900/books/1
curl -X PUT   localhost:9900/books/1 -H 'Content-Type: application/json' -d '{"title":"Dune Messiah","author":"Herbert"}'
curl -X PATCH localhost:9900/books/1 -H 'Content-Type: application/json' -d '{"author":"Frank Herbert"}'
curl -X DELETE -i localhost:9900/books/1    # HTTP/1.1 204 No Content
```

And in a test:

```go
srv := dygotest.New(BooksModule).CompileHTTP(t)
var b Book
srv.POST("/books").JSON(BookBody{Title: "Dune"}).Do(t).Status(t, 201).JSON(t, &b)
srv.PATCH("/books/1").JSON(map[string]string{"author": "Frank Herbert"}).Do(t).Status(t, 200)
srv.DELETE("/books/1").Do(t).Status(t, 204)
```

`dygo g resource books` (see the [CLI guide](guide-cli.md)) writes all of this for you: module, in-memory service, controller with the five routes, DTOs with validation, and a test.

## Typed input

```go
type ListIn struct {
    Pagination                                   // embedded structs are flattened
    Q      string    `query:"q"`
    Since  time.Time `query:"since"`             // RFC 3339
    Tenant string    `header:"X-Tenant" required:"true"`
}

type Pagination struct {
    Offset int `query:"offset" default:"0"`
    Limit  int `query:"limit"  default:"20"`
}

type CreateIn struct {
    Body struct {
        Email string `json:"email"`
    } `body:"json"`                               // also body:"form", body:"raw"
}

type UploadIn struct {
    Title string                `form:"title"`
    File  *multipart.FileHeader `file:"file"`
}
```

| Tag | From |
|---|---|
| `path:"id"` | path wildcard (always required) |
| `query:"q"` | query string; slices for repeated keys; pointers for optional |
| `header:"X-Tenant"` | request header |
| `cookie:"sid"` | cookie value, or `*http.Cookie` |
| `form:"name"`, `file:"f"` | multipart or urlencoded form |
| `body:"json"` / `"form"` / `"raw"` | request body |
| `default:"10"`, `required:"true"` | modifiers |

Types: strings, bools, ints, uints, floats, `time.Time`, `time.Duration`, anything implementing `encoding.TextUnmarshaler`, and types registered with `dygo.RegisterConverter`. Custom sources: `dygo.RegisterSource("user", fn)` then `User *User \`user:""\``.

An empty value (`?limit=`) counts as missing, so `default` applies. Every bad field is reported in one 400:

```json
{"statusCode":400,"error":"Bad Request","message":"invalid request",
 "details":[{"source":"path","name":"id","value":"zzz","reason":"must be 16 hex characters"}]}
```

## Output

- Any value: JSON. `string`: text/plain. `[]byte`: octet-stream. `dygo.NoContent{}`: 204.
- `dygo.Stream(reader, contentType)`, `dygo.File(path)`, `dygo.SSE(seq)`, `dygo.RedirectTo(url, code)`.
- Inside a handler: `dygo.SetStatus(ctx, 202)`, `dygo.SetHeader(ctx, k, v)`, `dygo.SetCookie(ctx, c)`, `dygo.Request(ctx)`, `dygo.RouteOf(ctx)`.
- Route options: `dygo.HTTPCode(200)`, `dygo.Header(k, v)`, `dygo.Redirect(url, code)`, `dygo.BodyLimit(n)`, `dygo.StrictJSON()`, `dygo.RouteMiddleware(mw)`, `dygo.Version("2")`, and `dygo.Options(...)` to combine them.

## Errors

```go
return nil, errs.NotFound("user not found", errs.Code("USER_NOT_FOUND"))
```

```json
{"statusCode":404,"error":"Not Found","message":"user not found","code":"USER_NOT_FOUND"}
```

Any other error becomes a 500 with a generic message, and the real error is logged. Panics are recovered the same way. A body over the limit (default 1 MiB) is a 413. Unknown paths and wrong methods get `404 Cannot GET /path`.

## App options

```go
app, err := dygo.Create(ctx, AppModule,
    dygo.WithGlobalPrefix("/api", "/health"),        // exclude /health
    dygo.WithVersioning(dygo.URIVersioning("1")),    // or HeaderVersioning, MediaTypeVersioning, CustomVersioning
    dygo.WithMiddleware(accessLog),                  // any func(http.Handler) http.Handler
    dygo.WithBodyLimit(4<<20),
    dygo.WithHTTPServer(func(s *http.Server) { s.WriteTimeout = 30 * time.Second }),
)
err = app.Listen(ctx, ":9900") // drains in-flight requests on Ctrl+C / SIGTERM
```

Long running handlers (SSE waiting for events) should also select on `dygo.Stopping(ctx)`, which closes when shutdown starts, so a waiting stream does not hold shutdown for the full timeout.

`app.Routes()` lists every route; `app.Handler()` gives the `http.Handler` for your own server.

## Request scope

```go
dygo.Provide(NewRequestMeta, dygo.WithScope(dygo.RequestScoped)) // may inject *http.Request

type Controller struct{ meta dygo.Scoped[*RequestMeta] }
meta, err := c.meta.Get(ctx) // one instance per request
```

Singletons (including every controller) reach request scoped values only through `dygo.Scoped[T]`. Implement `OnRequestEnd(ctx)` for per-request cleanup.

### Durable providers (multi-tenancy)

A durable provider is request scoped, but built once per context ID (usually a tenant) and shared by every request with that ID:

```go
dygo.Provide(NewTenantDB, dygo.WithScope(dygo.RequestScoped), dygo.Durable())

func NewTenantDB(cid dygo.ContextID, pools *Pools) (*TenantDB, error) { return pools.Open(cid.ID) }

app, err := dygo.Create(ctx, AppModule(), dygo.WithContextIDStrategy(func(r *http.Request) (string, any) {
	t := r.Header.Get("X-Tenant-ID")
	if !knownTenant(t) {
		return "", nil // no ID: durable providers act per request
	}
	return t, t // id, payload (cid.Payload)
}))
```

Controllers still use `dygo.Scoped[*TenantDB]`. A durable provider may inject `dygo.ContextID`, singletons and other durable providers; injecting `*http.Request` or a request scoped provider that is not durable fails at boot, since the instance outlives the request. Each instance is built exactly once per ID, even when requests race. Outside HTTP (a queue job, a microservice handler), `dygo.WithDurableContext(ctx, tenant, payload)` makes `Scoped.Get` reach the same instances. `app.ReleaseContext(ctx, tenant)` drops a tenant's instances, and they get `OnApplicationShutdown` then and at app shutdown. Validate the ID in the strategy: every new ID builds and keeps new instances.

## Testing

```go
srv := dygotest.New(AppModule).
    With(dygo.WithGlobalPrefix("/api")).
    OverrideValue(dygo.TypeOf[users.Repo](), fakeRepo).
    CompileHTTP(t)

var u users.User
srv.POST("/api/users").JSON(body).Do(t).Status(t, 201).JSON(t, &u)
```
