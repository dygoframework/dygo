package dygotest_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/dygotest"
)

type greeter struct{ word string }

func newGreeter() *greeter { return &greeter{word: "hello"} }

type echoIn struct {
	Name  string `path:"name"`
	Trace string `header:"X-Trace"`
	Body  struct {
		Msg string `json:"msg"`
	} `body:"json"`
}

type echoOut struct {
	Word  string `json:"word"`
	Name  string `json:"name"`
	Trace string `json:"trace"`
	Msg   string `json:"msg"`
}

type echoController struct{ g *greeter }

func newEchoController(g *greeter) *echoController { return &echoController{g: g} }

func (c *echoController) Routes(r dygo.Router) {
	h := dygo.Handle(func(_ context.Context, in echoIn) (echoOut, error) {
		return echoOut{Word: c.g.word, Name: in.Name, Trace: in.Trace, Msg: in.Body.Msg}, nil
	})
	r.Get("/echo/{name}", h)
	r.Post("/echo/{name}", h)
	r.Put("/echo/{name}", h)
	r.Patch("/echo/{name}", h)
	r.Delete("/echo/{name}", h)
}

var echoModule = &dygo.Module{
	Name:        "echo",
	Providers:   []dygo.Provider{dygo.Provide(newGreeter)},
	Controllers: []dygo.Provider{dygo.Provide(newEchoController)},
}

func TestCompile(t *testing.T) {
	app := dygotest.Module([]dygo.Provider{dygo.Provide(newGreeter)}).Compile(t)
	if got := dygo.MustGet[*greeter](app).word; got != "hello" {
		t.Fatalf("word = %q", got)
	}
}

func TestOverrides(t *testing.T) {
	tok := dygo.TypeOf[*greeter]()
	tests := []struct {
		name string
		b    func() *dygotest.Builder
	}{
		{"value", func() *dygotest.Builder {
			return dygotest.New(echoModule).OverrideValue(tok, &greeter{word: "hi"})
		}},
		{"provider", func() *dygotest.Builder {
			return dygotest.New(echoModule).OverrideProvider(tok, dygo.Provide(func() *greeter { return &greeter{word: "hi"} }))
		}},
		{"with", func() *dygotest.Builder {
			return dygotest.New(echoModule).With(dygo.WithOverride(tok, dygo.Value(&greeter{word: "hi"})))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := tt.b().Compile(t)
			if got := dygo.MustGet[*greeter](app).word; got != "hi" {
				t.Fatalf("word = %q", got)
			}
		})
	}
}

func TestBuildError(t *testing.T) {
	needs := func(*greeter) int { return 1 }
	b := dygotest.Module([]dygo.Provider{dygo.Provide(needs)})
	if _, err := b.Build(t.Context()); err == nil {
		t.Fatal("Build: no error for a missing dependency")
	}
	if _, err := b.BuildHTTP(t.Context()); err == nil {
		t.Fatal("BuildHTTP: no error for a missing dependency")
	}
}

func TestServerMethods(t *testing.T) {
	srv := dygotest.New(echoModule).CompileHTTP(t)
	tests := []struct {
		name   string
		req    *dygotest.Req
		status int
	}{
		{"GET", srv.GET("/echo/ann"), http.StatusOK},
		{"POST", srv.POST("/echo/ann"), http.StatusCreated},
		{"PUT", srv.PUT("/echo/ann"), http.StatusOK},
		{"PATCH", srv.PATCH("/echo/ann"), http.StatusOK},
		{"DELETE", srv.DELETE("/echo/ann"), http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out echoOut
			tt.req.Header("X-Trace", "t1").JSON(map[string]string{"msg": tt.name}).Do(t).Status(t, tt.status).JSON(t, &out)
			want := echoOut{Word: "hello", Name: "ann", Trace: "t1", Msg: tt.name}
			if out != want {
				t.Fatalf("got %+v, want %+v", out, want)
			}
		})
	}
}

func TestReqBodyAndWith(t *testing.T) {
	srv := dygotest.New(echoModule).CompileHTTP(t)
	res := srv.NewRequest(http.MethodPost, "/echo/bob").
		Body(strings.NewReader(`{"msg":"raw"}`), "application/json").
		With(func(r *http.Request) { r.Header.Set("X-Trace", "edited") }).
		Do(t).Status(t, http.StatusCreated)
	if txt := res.Text(); !strings.Contains(txt, `"msg":"raw"`) || !strings.Contains(txt, `"trace":"edited"`) {
		t.Fatalf("body = %s", txt)
	}
	// A body without a content type keeps the request's header untouched.
	res = srv.GET("/echo/bob").Body(nil, "").Do(t).Status(t, http.StatusOK)
	if res.Header.Get("Content-Type") == "" {
		t.Fatal("no content type on the response")
	}
}

func TestJSONPanicsOnBadValue(t *testing.T) {
	srv := dygotest.New(echoModule).CompileHTTP(t)
	defer func() {
		if recover() == nil {
			t.Fatal("JSON did not panic")
		}
	}()
	srv.POST("/x").JSON(make(chan int))
}

// fakeTB records failures instead of stopping the test, so the failure paths
// of the helpers can be checked.
type fakeTB struct {
	testing.TB
	failed string
}

func (f *fakeTB) Helper() {}

func (f *fakeTB) Fatalf(format string, args ...any) {
	f.failed = format
	panic(errFatal)
}

var errFatal = errors.New("fatal")

func catchFatal(t *testing.T, f *fakeTB, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != errFatal {
			t.Fatalf("recovered %v, want a Fatalf call", r)
		}
		if f.failed == "" {
			t.Fatal("Fatalf not called")
		}
	}()
	fn()
}

func TestResFailures(t *testing.T) {
	srv := dygotest.New(echoModule).CompileHTTP(t)
	res := srv.GET("/echo/x").Do(t)
	t.Run("status", func(t *testing.T) {
		f := &fakeTB{TB: t}
		catchFatal(t, f, func() { res.Status(f, http.StatusTeapot) })
	})
	t.Run("json", func(t *testing.T) {
		f := &fakeTB{TB: t}
		var n int
		catchFatal(t, f, func() { res.JSON(f, &n) })
	})
	t.Run("bad request", func(t *testing.T) {
		f := &fakeTB{TB: t}
		catchFatal(t, f, func() { srv.NewRequest("BAD METHOD", "/").Do(f) })
	})
	t.Run("server down", func(t *testing.T) {
		f := &fakeTB{TB: t}
		down := &dygotest.Server{URL: "http://127.0.0.1:1", Client: http.DefaultClient}
		catchFatal(t, f, func() { down.GET("/").Do(f) })
	})
	t.Run("compile", func(t *testing.T) {
		f := &fakeTB{TB: t}
		needs := func(*greeter) int { return 1 }
		catchFatal(t, f, func() { dygotest.Module([]dygo.Provider{dygo.Provide(needs)}).Compile(f) })
	})
	t.Run("compile http", func(t *testing.T) {
		f := &fakeTB{TB: t}
		needs := func(*greeter) int { return 1 }
		catchFatal(t, f, func() { dygotest.Module([]dygo.Provider{dygo.Provide(needs)}).CompileHTTP(f) })
	})
}
