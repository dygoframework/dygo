package dygo_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/interceptors"
	"github.com/dygoframework/dygo/meta"
	"github.com/dygoframework/dygo/validate"
)

type benchIn struct {
	ID    int `path:"id"`
	Limit int `query:"limit" default:"10"`
	Body  struct {
		Name string `json:"name"`
	} `body:"json"`
}

type benchOut struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type benchController struct{}

func (benchController) Routes(r dygo.Router) {
	r.Post("/typed/{id}", dygo.Handle(func(_ context.Context, in benchIn) (benchOut, error) {
		return benchOut{ID: in.ID, Name: in.Body.Name}, nil
	}))
	r.Post("/raw/{id}", dygo.HandleRaw(func(w http.ResponseWriter, r *http.Request) error {
		_, _ = io.WriteString(w, r.PathValue("id"))
		return nil
	}))
}

func benchServe(b *testing.B, h http.Handler, path string) {
	b.ReportAllocs()
	for b.Loop() {
		req := httptest.NewRequest("POST", path+"?limit=5", strings.NewReader(`{"name":"bench"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code >= 300 {
			b.Fatal(w.Code, w.Body.String())
		}
	}
}

func BenchmarkBareServeMux(b *testing.B) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /raw/{id}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.PathValue("id"))
	})
	benchServe(b, mux, "/raw/42")
}

func benchApp(b *testing.B) http.Handler {
	app, err := dygo.Create(context.Background(), &dygo.Module{Name: "bench", Controllers: []dygo.Provider{dygo.Value(benchController{})}})
	if err != nil {
		b.Fatal(err)
	}
	return app.Handler()
}

func BenchmarkHandleRaw(b *testing.B)   { benchServe(b, benchApp(b), "/raw/42") }
func BenchmarkHandleTyped(b *testing.B) { benchServe(b, benchApp(b), "/typed/42") }

// BenchmarkHandWritten does by hand what HandleTyped does: parse the path and
// query, decode the JSON body, encode the JSON result.
func BenchmarkHandWritten(b *testing.B) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /typed/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.Error(w, "bad id", 400)
			return
		}
		limit := 10
		if l := r.URL.Query().Get("limit"); l != "" {
			if limit, err = strconv.Atoi(l); err != nil {
				http.Error(w, "bad limit", 400)
				return
			}
		}
		_ = limit
		var body struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
			http.Error(w, "bad body", 400)
			return
		}
		out, _ := json.Marshal(benchOut{ID: id, Name: body.Name})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = w.Write(out)
	})
	benchServe(b, mux, "/typed/42")
}

// BenchmarkPipeline is HandleTyped behind 2 guards, 2 interceptors and 1 pipe,
// the phase 3 exit criterion.
func BenchmarkPipeline(b *testing.B) {
	pass := dygo.GuardFunc(func(dygo.ExecutionContext) (bool, error) { return true, nil })
	through := dygo.InterceptorFunc(func(_ dygo.ExecutionContext, next dygo.CallHandler) (any, error) { return next.Handle() })
	noop := dygo.PipeFunc(func(dygo.ExecutionContext, any) error { return nil })
	app, err := dygo.Create(context.Background(), &dygo.Module{Name: "bench", Controllers: []dygo.Provider{dygo.Value(benchController{})}},
		dygo.WithGlobalGuards(pass, pass), dygo.WithGlobalInterceptors(through, through), dygo.WithGlobalPipes(noop))
	if err != nil {
		b.Fatal(err)
	}
	benchServe(b, app.Handler(), "/typed/42")
}

type realisticBody struct {
	Name string `json:"name"`
}

func (b *realisticBody) Validate(context.Context) error {
	if b.Name == "" {
		return validate.Errors{{Source: "body", Name: "name", Reason: "is required"}}.Err()
	}
	return nil
}

type realisticIn struct {
	ID    int           `path:"id"`
	Limit int           `query:"limit" default:"10"`
	Body  realisticBody `body:"json"`
}

type benchUserKey struct{}

var benchRoles = meta.NewKey[[]string]("roles")

// BenchmarkRealistic is a route like the example's: an auth guard that puts
// the user in the context and checks merged roles, validation with Validate
// methods, and a logging interceptor whose level is off.
func BenchmarkRealistic(b *testing.B) {
	auth := dygo.GuardFunc(func(ec dygo.ExecutionContext) (bool, error) {
		ec.SetContext(context.WithValue(ec.Context(), benchUserKey{}, "ann"))
		for _, r := range meta.GetAllAndMerge(ec, benchRoles) {
			if r == "admin" {
				return true, nil
			}
		}
		return false, nil
	})
	ctrl := routesFunc(func(r dygo.Router) {
		r = r.Use(benchRoles.Set([]string{"user"}))
		r.Post("/typed/{id}", dygo.Handle(func(_ context.Context, in realisticIn) (benchOut, error) {
			return benchOut{ID: in.ID, Name: in.Body.Name}, nil
		}), benchRoles.Set([]string{"admin"}))
	})
	app, err := dygo.Create(context.Background(), &dygo.Module{Name: "bench", Controllers: []dygo.Provider{dygo.Value(ctrl)}},
		dygo.WithGlobalGuards(auth), validate.Use(validate.Methods()),
		dygo.WithGlobalInterceptors(interceptors.Logging(slog.New(slog.DiscardHandler))))
	if err != nil {
		b.Fatal(err)
	}
	benchServe(b, app.Handler(), "/typed/42")
}

// BenchmarkDurableParallel is a request to a durable (per tenant) provider
// tree, from many goroutines.
func BenchmarkDurableParallel(b *testing.B) {
	n := &durableCounts{}
	app, err := dygo.Create(context.Background(), tenantModule(n), dygo.WithContextIDStrategy(tenantStrategy))
	if err != nil {
		b.Fatal(err)
	}
	h := app.Handler()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			req := httptest.NewRequest("GET", "/whoami", nil)
			req.Header.Set("X-Tenant", "acme")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != 200 {
				b.Fatal(w.Code)
			}
		}
	})
}
