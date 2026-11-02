package dygo_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/dygotest"
	"github.com/dygoframework/dygo/errs"
)

// --- fixtures ---------------------------------------------------------------

type Item struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type ItemsController struct {
	mu    sync.Mutex
	items map[int]Item
}

func NewItemsController() *ItemsController {
	return &ItemsController{items: map[int]Item{1: {ID: 1, Name: "first"}}}
}

type GetItemIn struct {
	ID int `path:"id"`
}

type ListItemsIn struct {
	Limit int    `query:"limit" default:"10"`
	Q     string `query:"q"`
}

type CreateItemIn struct {
	Body struct {
		Name string `json:"name"`
	} `body:"json"`
}

func (c *ItemsController) Routes(r dygo.Router) {
	r = r.Prefix("/items")
	r.Get("/", dygo.Handle(c.List))
	r.Get("/{id}", dygo.Handle(c.Get))
	r.Post("/", dygo.Handle(c.Create))
	r.Delete("/{id}", dygo.Handle(c.Delete))
	r.Get("/boom", dygo.HandleNoInput(func(context.Context) (any, error) { panic("boom") }))
}

func (c *ItemsController) List(_ context.Context, in ListItemsIn) ([]Item, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []Item{}
	for _, it := range c.items {
		if len(out) < in.Limit && strings.Contains(it.Name, in.Q) {
			out = append(out, it)
		}
	}
	return out, nil
}

func (c *ItemsController) Get(_ context.Context, in GetItemIn) (*Item, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	it, ok := c.items[in.ID]
	if !ok {
		return nil, errs.NotFound("item not found", errs.Code("ITEM_NOT_FOUND"))
	}
	return &it, nil
}

func (c *ItemsController) Create(ctx context.Context, in CreateItemIn) (*Item, error) {
	if in.Body.Name == "" {
		return nil, errs.BadRequest("name is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	it := Item{ID: len(c.items) + 1, Name: in.Body.Name}
	c.items[it.ID] = it
	dygo.SetHeader(ctx, "Location", fmt.Sprintf("/items/%d", it.ID))
	return &it, nil
}

func (c *ItemsController) Delete(_ context.Context, in GetItemIn) (dygo.NoContent, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, in.ID)
	return dygo.NoContent{}, nil
}

func itemsModule() *dygo.Module {
	return &dygo.Module{Name: "items", Controllers: []dygo.Provider{dygo.Provide(NewItemsController)}}
}

type errorBody struct {
	StatusCode int    `json:"statusCode"`
	Error      string `json:"error"`
	Message    string `json:"message"`
	Code       string `json:"code"`
	Details    []struct {
		Source, Name, Reason string
	} `json:"details"`
}

// --- CRUD, binding, errors ---------------------------------------------------

func TestHTTPCrud(t *testing.T) {
	srv := dygotest.New(itemsModule()).With(dygo.WithGlobalPrefix("/api")).CompileHTTP(t)

	var it Item
	srv.GET("/api/items/1").Do(t).Status(t, 200).JSON(t, &it)
	if it.Name != "first" {
		t.Fatal(it)
	}

	res := srv.POST("/api/items").JSON(map[string]string{"name": "second"}).Do(t).Status(t, 201)
	if res.Header.Get("Location") != "/items/2" {
		t.Fatalf("Location = %q", res.Header.Get("Location"))
	}

	var list []Item
	srv.GET("/api/items?limit=1").Do(t).Status(t, 200).JSON(t, &list)
	if len(list) != 1 {
		t.Fatalf("limit not applied: %v", list)
	}

	srv.DELETE("/api/items/2").Do(t).Status(t, 204)

	var eb errorBody
	srv.GET("/api/items/2").Do(t).Status(t, 404).JSON(t, &eb)
	if eb.StatusCode != 404 || eb.Error != "Not Found" || eb.Message != "item not found" || eb.Code != "ITEM_NOT_FOUND" {
		t.Fatalf("%+v", eb)
	}
}

func TestHTTPBindErrorsAndLimits(t *testing.T) {
	srv := dygotest.New(itemsModule()).With(dygo.WithBodyLimit(64)).CompileHTTP(t)

	var eb errorBody
	srv.GET("/items/abc").Do(t).Status(t, 400).JSON(t, &eb)
	if eb.Message != "invalid request" || len(eb.Details) != 1 || eb.Details[0].Name != "id" {
		t.Fatalf("%+v", eb)
	}
	srv.POST("/items").Body(strings.NewReader(`{"name":`), "application/json").Do(t).Status(t, 400)
	srv.POST("/items").JSON(map[string]string{"name": strings.Repeat("x", 100)}).Do(t).Status(t, 413)
	srv.POST("/items").JSON(map[string]string{}).Do(t).Status(t, 400)
}

func TestHTTPNotFoundAndPanic(t *testing.T) {
	srv := dygotest.New(itemsModule()).CompileHTTP(t)
	var eb errorBody
	srv.GET("/nope").Do(t).Status(t, 404).JSON(t, &eb)
	if eb.Message != "Cannot GET /nope" {
		t.Fatal(eb.Message)
	}
	srv.NewRequest("PATCH", "/items/1").Do(t).Status(t, 404)
	srv.GET("/items/boom").Do(t).Status(t, 500).JSON(t, &eb)
	if eb.Message != "Internal Server Error" {
		t.Fatal(eb.Message)
	}
}

// --- route table errors --------------------------------------------------------

type badController struct{}

func (badController) Routes(r dygo.Router) {
	ok := dygo.HandleNoInput(func(context.Context) (string, error) { return "", nil })
	r.Get("/users/:id", ok)
	r.Get("/a", ok)
	r.Get("/a", ok)
	r.Get("/x/{a}", ok)
	r.Get("/{b}/y", ok)
	r.Get("/p/{id}", dygo.Handle(func(context.Context, struct {
		Name string `path:"name"`
	},
	) (string, error) {
		return "", nil
	}))
	r.Get("/nil", nil)
	r.Get("/v", ok, dygo.Version("2"))
}

type noRoutes struct{}

func TestHTTPRouteErrorsReportedTogether(t *testing.T) {
	_, err := dygo.Create(context.Background(), &dygo.Module{Name: "app", Controllers: []dygo.Provider{
		dygo.Provide(func() badController { return badController{} }),
		dygo.Provide(func() *noRoutes { return &noRoutes{} }),
	}})
	if err == nil {
		t.Fatal("expected errors")
	}
	mustContain(t, err.Error(),
		"7 problems",
		"has no Routes(r dygo.Router) method",
		"write {id} instead",
		"GET /a is registered twice",
		"conflicts with another route",
		"path:\"name\" has no {name}",
		"handler is nil",
		"versioning is off",
	)
}

// --- options, responses ----------------------------------------------------------

type miscController struct{ log *[]string }

func (c miscController) mw(name string) dygo.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			*c.log = append(*c.log, name)
			next.ServeHTTP(w, r)
		})
	}
}

func (c miscController) Routes(r dygo.Router) {
	r = r.Use(dygo.RouteMiddleware(c.mw("controller")))
	r.Get("/status", dygo.HandleNoInput(func(ctx context.Context) (string, error) {
		dygo.SetStatus(ctx, 202)
		dygo.SetCookie(ctx, &http.Cookie{Name: "a", Value: "b"})
		return "accepted", nil
	}), dygo.RouteMiddleware(c.mw("route")), dygo.Header("X-Static", "1"))
	r.Post("/code", dygo.HandleNoInput(func(context.Context) (map[string]int, error) {
		return map[string]int{"n": 1}, nil
	}), dygo.HTTPCode(200))
	r.Get("/old", dygo.HandleNoInput(func(context.Context) (any, error) { return nil, nil }), dygo.Redirect("/new", 301))
	r.Get("/stream", dygo.HandleNoInput(func(context.Context) (dygo.Responder, error) {
		return dygo.Stream(io.NopCloser(strings.NewReader("a,b\n")), "text/csv"), nil
	}))
	r.Get("/events", dygo.HandleNoInput(func(context.Context) (dygo.Responder, error) {
		seq := func(yield func(any) bool) {
			_ = yield(map[string]int{"n": 1}) && yield(dygo.SSEEvent{ID: "2", Event: "tick", Data: "two"})
		}
		return dygo.SSE(iter.Seq[any](seq)), nil
	}))
	r.Get("/raw", dygo.HandleRaw(func(w http.ResponseWriter, r *http.Request) error {
		if r.URL.Query().Get("fail") != "" {
			return errs.Forbidden("no")
		}
		_, err := io.WriteString(w, dygo.RouteOf(r.Context()).Path)
		return err
	}))
}

func TestHTTPResponsesAndMiddleware(t *testing.T) {
	var log []string
	c := miscController{log: &log}
	srv := dygotest.New(&dygo.Module{Name: "app", Controllers: []dygo.Provider{dygo.Value(c)}}).
		With(dygo.WithMiddleware(c.mw("app"))).CompileHTTP(t)

	res := srv.GET("/status").Do(t).Status(t, 202)
	if res.Text() != "accepted" || res.Header.Get("X-Static") != "1" || len(res.Cookies()) != 1 {
		t.Fatalf("%q %v", res.Text(), res.Header)
	}
	if strings.Join(log, ",") != "app,controller,route" {
		t.Fatalf("middleware order: %v", log)
	}
	srv.POST("/code").Do(t).Status(t, 200)

	srv.Client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if res := srv.GET("/old").Do(t).Status(t, 301); res.Header.Get("Location") != "/new" {
		t.Fatal(res.Header)
	}
	if res := srv.GET("/stream").Do(t).Status(t, 200); res.Text() != "a,b\n" || res.Header.Get("Content-Type") != "text/csv" {
		t.Fatal(res.Text())
	}
	want := "data: {\"n\":1}\n\nid: 2\nevent: tick\ndata: two\n\n"
	if res := srv.GET("/events").Do(t).Status(t, 200); res.Text() != want {
		t.Fatalf("SSE body %q", res.Text())
	}
	if res := srv.GET("/raw").Do(t).Status(t, 200); res.Text() != "/raw" {
		t.Fatal(res.Text())
	}
	srv.GET("/raw?fail=1").Do(t).Status(t, 403)
}

// --- versioning ------------------------------------------------------------------

type versionedController struct{}

func (versionedController) Routes(r dygo.Router) {
	say := func(s string) dygo.Handler {
		return dygo.HandleNoInput(func(context.Context) (string, error) { return s, nil })
	}
	r.Get("/hello", say("v1")) // default version
	r.Get("/hello", say("v2"), dygo.Version("2"))
	r.Get("/health", say("ok"), dygo.Version(dygo.VersionNeutral))
}

func TestURIVersioning(t *testing.T) {
	srv := dygotest.New(&dygo.Module{Name: "app", Controllers: []dygo.Provider{dygo.Value(versionedController{})}}).
		With(dygo.WithGlobalPrefix("/api", "/health"), dygo.WithVersioning(dygo.URIVersioning("1"))).
		CompileHTTP(t)
	for path, want := range map[string]string{"/api/v1/hello": "v1", "/api/v2/hello": "v2", "/health": "ok"} {
		if got := srv.GET(path).Do(t).Status(t, 200).Text(); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	srv.GET("/api/v3/hello").Do(t).Status(t, 404)
	if n := len(srv.App.Routes()); n != 3 {
		t.Fatalf("routes: %v", srv.App.Routes())
	}
}

func TestHeaderAndMediaTypeVersioning(t *testing.T) {
	mod := func() *dygo.Module {
		return &dygo.Module{Name: "app", Controllers: []dygo.Provider{dygo.Value(versionedController{})}}
	}
	srv := dygotest.New(mod()).With(dygo.WithVersioning(dygo.HeaderVersioning("X-API-Version", "1"))).CompileHTTP(t)
	if got := srv.GET("/hello").Header("X-API-Version", "2").Do(t).Status(t, 200).Text(); got != "v2" {
		t.Fatal(got)
	}
	if got := srv.GET("/hello").Do(t).Status(t, 200).Text(); got != "v1" {
		t.Fatal("missing header should use the default version:", got)
	}
	withV2 := func(r *http.Request) { r.Header.Set("X-API-Version", "2") } // dygotest Req.With
	if got := srv.GET("/hello").With(withV2).Do(t).Status(t, 200).Text(); got != "v2" {
		t.Fatal("Req.With:", got)
	}
	if got := srv.GET("/health").Header("X-API-Version", "9").Do(t).Status(t, 200).Text(); got != "ok" {
		t.Fatal(got)
	}
	srv.GET("/hello").Header("X-API-Version", "9").Do(t).Status(t, 404)

	mt := dygotest.New(mod()).With(dygo.WithVersioning(dygo.MediaTypeVersioning("v", "1"))).CompileHTTP(t)
	if got := mt.GET("/hello").Header("Accept", "application/json;v=2").Do(t).Status(t, 200).Text(); got != "v2" {
		t.Fatal(got)
	}
}

// --- request scope ---------------------------------------------------------------

type RequestInfo struct {
	ID    int64
	Path  string
	ended *atomic.Int64
}

func (ri *RequestInfo) OnRequestEnd(context.Context) { ri.ended.Add(1) }

type Audit struct{ Info *RequestInfo }

type scopedController struct {
	info  dygo.Scoped[*RequestInfo]
	audit dygo.Scoped[*Audit]
}

func (c *scopedController) Routes(r dygo.Router) {
	r.Get("/who", dygo.HandleNoInput(func(ctx context.Context) (string, error) {
		info := c.info.MustGet(ctx)
		audit := c.audit.MustGet(ctx)
		if audit.Info != info {
			return "", errors.New("request scoped instance not shared within the request")
		}
		return fmt.Sprintf("%d %s", info.ID, info.Path), nil
	}))
}

func TestRequestScope(t *testing.T) {
	var counter, ended atomic.Int64
	mod := &dygo.Module{
		Name: "app",
		Providers: []dygo.Provider{
			dygo.Provide(func(r *http.Request) *RequestInfo {
				return &RequestInfo{ID: counter.Add(1), Path: r.URL.Path, ended: &ended}
			}, dygo.WithScope(dygo.RequestScoped)),
			dygo.Provide(func(i *RequestInfo) *Audit { return &Audit{Info: i} }, dygo.WithScope(dygo.RequestScoped)),
		},
		Controllers: []dygo.Provider{dygo.Provide(func(i dygo.Scoped[*RequestInfo], a dygo.Scoped[*Audit]) *scopedController {
			return &scopedController{info: i, audit: a}
		})},
	}
	srv := dygotest.New(mod).CompileHTTP(t)

	var wg sync.WaitGroup
	seen := sync.Map{}
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := srv.GET("/who").Do(t).Status(t, 200).Text()
			if _, dup := seen.LoadOrStore(body, true); dup {
				t.Errorf("request scoped value reused across requests: %s", body)
			}
		}()
	}
	wg.Wait()
	if counter.Load() != 20 || ended.Load() != 20 {
		t.Fatalf("built %d, ended %d; want 20 each", counter.Load(), ended.Load())
	}
	if _, err := srv.App.Controllers()[0].(*scopedController).info.Get(context.Background()); err == nil {
		t.Fatal("Scoped.Get outside a request should fail")
	}
}

func TestRequestScopeRules(t *testing.T) {
	_, err := dygo.Create(context.Background(), &dygo.Module{
		Name: "app",
		Providers: []dygo.Provider{
			dygo.Provide(func() *RequestInfo { return nil }, dygo.WithScope(dygo.RequestScoped)),
			dygo.Provide(func(*RequestInfo) *Audit { return nil }),
			dygo.Provide(func(*http.Request) *Config { return nil }),
		},
		Controllers: []dygo.Provider{dygo.Provide(func() *scopedController { return nil }, dygo.WithScope(dygo.RequestScoped))},
	})
	if err == nil {
		t.Fatal("expected scope errors")
	}
	mustContain(t, err.Error(),
		"is singleton but injects request scoped",
		"Inject dygo.Scoped[*dygo_test.RequestInfo]",
		"*http.Request, which is only available",
		"must be a singleton",
	)
}

func TestRawHandlerKeepsWriterInterfaces(t *testing.T) {
	var flusher, hijacker bool
	h := dygo.HandleRaw(func(w http.ResponseWriter, _ *http.Request) error {
		_, flusher = w.(http.Flusher)
		_, hijacker = w.(http.Hijacker)
		return nil
	})
	srv := dygotest.New(&dygo.Module{Name: "app", Controllers: []dygo.Provider{
		dygo.Value(routesFunc(func(r dygo.Router) { r.Get("/raw", h) })),
	}}).CompileHTTP(t)
	srv.GET("/raw").Do(t).Status(t, 200)
	if !flusher || !hijacker {
		t.Fatalf("flusher=%v hijacker=%v", flusher, hijacker)
	}
}

// routesFunc adapts a function to dygo.Controller.
type routesFunc func(r dygo.Router)

func (f routesFunc) Routes(r dygo.Router) { f(r) }

func TestShutdownEndsStreamsPromptly(t *testing.T) {
	stream := dygo.HandleNoInput(func(ctx context.Context) (dygo.Responder, error) {
		stop := dygo.Stopping(ctx)
		return dygo.SSE(iter.Seq[string](func(yield func(string) bool) {
			if !yield("hello") {
				return
			}
			<-stop // a producer blocked waiting for events
		})), nil
	})
	app, err := dygo.Create(context.Background(), &dygo.Module{Name: "app", Controllers: []dygo.Provider{
		dygo.Value(routesFunc(func(r dygo.Router) { r.Get("/events", stream) })),
	}}, dygo.WithShutdownTimeout(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- app.Serve(ctx, ln) }()
	resp, err := http.Get("http://" + ln.Addr().String() + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	buf := make([]byte, 64)
	if _, err := resp.Body.Read(buf); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	cancel()
	if err := <-served; err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("shutdown took %v with an open stream", d)
	}
}

// --- graceful shutdown -----------------------------------------------------------

type slowController struct {
	started chan struct{}
	log     *[]string
	mu      *sync.Mutex
}

func (c *slowController) Routes(r dygo.Router) {
	r.Get("/slow", dygo.HandleNoInput(func(context.Context) (string, error) {
		close(c.started)
		time.Sleep(200 * time.Millisecond)
		c.mu.Lock()
		*c.log = append(*c.log, "handler done")
		c.mu.Unlock()
		return "done", nil
	}))
}

func (c *slowController) BeforeApplicationShutdown(context.Context, string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	*c.log = append(*c.log, "before")
	return nil
}

func (c *slowController) OnApplicationShutdown(context.Context, string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	*c.log = append(*c.log, "shutdown")
	return nil
}

func TestGracefulShutdownDrainsInFlightRequests(t *testing.T) {
	var log []string
	ctrl := &slowController{started: make(chan struct{}), log: &log, mu: &sync.Mutex{}}
	app, err := dygo.Create(context.Background(), &dygo.Module{Name: "app", Controllers: []dygo.Provider{dygo.Value(ctrl)}},
		dygo.WithShutdownTimeout(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- app.Serve(ctx, ln) }()

	got := make(chan string, 1)
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/slow")
		if err != nil {
			got <- "error: " + err.Error()
			return
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		got <- fmt.Sprintf("%d %s", resp.StatusCode, b)
	}()
	<-ctrl.started
	cancel() // like SIGTERM while the request is in flight

	if r := <-got; r != "200 done" {
		t.Fatalf("in-flight request: %s", r)
	}
	if err := <-served; err != nil {
		t.Fatal(err)
	}
	ctrl.mu.Lock()
	defer ctrl.mu.Unlock()
	if strings.Join(log, ",") != "before,handler done,shutdown" {
		t.Fatalf("shutdown order: %v", log)
	}
}
