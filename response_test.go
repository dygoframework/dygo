package dygo_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/dygotest"
)

// --- responders and route options --------------------------------------------

type point struct{ X, Y int }

func init() {
	dygo.RegisterConverter(func(s string) (point, error) {
		var p point
		_, err := fmt.Sscanf(s, "%d,%d", &p.X, &p.Y)
		return p, err
	})
}

type pointIn struct {
	P point `query:"p"`
}

type metaKey struct{}

func filesController(dir string) dygo.Controller {
	return routesFunc(func(r dygo.Router) {
		r.Get("/file/{name}", dygo.Handle(func(_ context.Context, in struct {
			Name string `path:"name"`
		}) (dygo.Responder, error) {
			return dygo.File(filepath.Join(dir, in.Name)), nil
		}))
		r.Get("/go", dygo.HandleNoInput(func(context.Context) (dygo.Responder, error) {
			return dygo.RedirectTo("/there", http.StatusTemporaryRedirect), nil
		}))
		r.Get("/bytes", dygo.HandleNoInput(func(ctx context.Context) ([]byte, error) {
			dygo.SetStatus(ctx, http.StatusAccepted)
			dygo.SetHeader(ctx, "X-Status", fmt.Sprint(dygo.ResponseStatus(ctx)))
			return []byte{1, 2}, nil
		}))
		r.Get("/routes", dygo.HandleNoInput(func(ctx context.Context) (int, error) {
			return len(dygo.RoutesOf(ctx)), nil
		}))
		r.Get("/point", dygo.Handle(func(_ context.Context, in pointIn) (point, error) { return in.P, nil }))
		r.Post("/strict", dygo.Handle(func(_ context.Context, in CreateItemIn) (string, error) {
			return in.Body.Name, nil
		}), dygo.Options(dygo.StrictJSON(), dygo.BodyLimit(32), dygo.SetMetadata(metaKey{}, "admin")))
		r.Host("api.example.com").Get("/host", dygo.HandleNoInput(func(context.Context) (string, error) { return "api", nil }))
	})
}

func TestResponders(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	srv := dygotest.New(&dygo.Module{Name: "app", Controllers: []dygo.Provider{dygo.Value(filesController(dir))}}).CompileHTTP(t)
	srv.Client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	tests := []struct {
		name   string
		req    *dygotest.Req
		status int
		check  func(*dygotest.Res) bool
	}{
		{"file", srv.GET("/file/a.txt"), 200, func(r *dygotest.Res) bool { return r.Text() == "hello file" }},
		{"file range", srv.GET("/file/a.txt").Header("Range", "bytes=0-4"), 206, func(r *dygotest.Res) bool { return r.Text() == "hello" }},
		{"missing file", srv.GET("/file/nope.txt"), 404, nil},
		{"directory", srv.GET("/file/sub"), 404, nil},
		{"redirect", srv.GET("/go"), 307, func(r *dygotest.Res) bool { return r.Header.Get("Location") == "/there" }},
		{"bytes", srv.GET("/bytes"), 202, func(r *dygotest.Res) bool {
			return bytes.Equal(r.Body, []byte{1, 2}) && r.Header.Get("Content-Type") == "application/octet-stream" && r.Header.Get("X-Status") == "202"
		}},
		{"routes of", srv.GET("/routes"), 200, func(r *dygotest.Res) bool { return r.Text() != "0" }},
		{"converter", srv.GET("/point?p=3,4"), 200, func(r *dygotest.Res) bool { return strings.TrimSpace(r.Text()) == `{"X":3,"Y":4}` }},
		{"converter error", srv.GET("/point?p=x"), 400, nil},
		{"strict json", srv.POST("/strict").JSON(map[string]string{"name": "a"}), 201, nil},
		{"strict json unknown field", srv.POST("/strict").JSON(map[string]string{"name": "a", "extra": "b"}), 400, nil},
		{"body limit", srv.POST("/strict").JSON(map[string]string{"name": strings.Repeat("a", 64)}), 413, nil},
		{"host matches", srv.GET("/host").With(func(r *http.Request) { r.Host = "api.example.com" }), 200, nil},
		{"host does not match", srv.GET("/host"), 404, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := tt.req.Do(t).Status(t, tt.status)
			if tt.check != nil && !tt.check(res) {
				t.Fatalf("unexpected response %v %q", res.Header, res.Text())
			}
		})
	}

	var strict dygo.RouteInfo
	for _, ri := range srv.App.Routes() {
		if ri.Path == "/strict" {
			strict = ri
		}
	}
	if v, ok := strict.Metadata(metaKey{}); !ok || v != "admin" {
		t.Fatalf("Metadata = %v, %v", v, ok)
	}
	if _, ok := (dygo.RouteInfo{}).Metadata(metaKey{}); ok {
		t.Fatal("zero RouteInfo has metadata")
	}
}

func TestOutsideRequest(t *testing.T) {
	ctx := context.Background()
	if dygo.ResponseStatus(ctx) != 0 || dygo.RoutesOf(ctx) != nil {
		t.Fatal("request helpers return values outside a request")
	}
}

// --- application options and serving ----------------------------------------

func TestErrorWriterAndLogger(t *testing.T) {
	var logs bytes.Buffer
	srv := dygotest.New(itemsModule()).With(
		dygo.WithLogger(slog.New(slog.NewTextHandler(&logs, nil))),
		dygo.WithErrorWriter(func(w http.ResponseWriter, _ *http.Request, err error) {
			w.WriteHeader(http.StatusTeapot)
			_, _ = w.Write([]byte("custom: " + err.Error()))
		}),
	).CompileHTTP(t)
	res := srv.GET("/items/99").Do(t).Status(t, http.StatusTeapot)
	if !strings.HasPrefix(res.Text(), "custom: ") {
		t.Fatalf("body %q", res.Text())
	}
	if logs.Len() == 0 {
		t.Fatal("nothing logged with WithLogger")
	}
}

func TestListen(t *testing.T) {
	app, err := dygo.Create(context.Background(), itemsModule(),
		dygo.WithHTTPServer(func(s *http.Server) { s.ReadTimeout = time.Second }))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Listen(ctx, addr) }()
	var resp *http.Response
	for range 100 {
		if resp, err = http.Get("http://" + addr + "/items/1"); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Listen: %v", err)
	}
}

func TestListenBadAddress(t *testing.T) {
	app, err := dygo.Create(context.Background(), itemsModule())
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Listen(context.Background(), "256.0.0.1:bad"); err == nil {
		t.Fatal("no error for a bad address")
	}
}

func TestRunStopsOnSignal(t *testing.T) {
	app, err := dygo.CreateApplicationContext(context.Background(), &dygo.Module{Name: "app"})
	if err != nil {
		t.Fatal(err)
	}
	// Catch SIGTERM here too, so the test process never gets the default
	// action (exit) if the signal comes before Run starts listening.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM)
	defer signal.Stop(sigs)
	done := make(chan error, 1)
	go func() { done <- app.Run(context.Background()) }()
	for {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			return
		case <-time.After(20 * time.Millisecond):
			_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		}
	}
}
