package dygo_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/dygotest"
	"github.com/dygoframework/dygo/errs"
	"github.com/dygoframework/dygo/interceptors"
	"github.com/dygoframework/dygo/meta"
	"github.com/dygoframework/dygo/validate"
)

// trace records the order in which enhancers ran.
type trace struct {
	mu  sync.Mutex
	got []string
}

func (tr *trace) add(s string) {
	tr.mu.Lock()
	tr.got = append(tr.got, s)
	tr.mu.Unlock()
}

func (tr *trace) take() string {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	s := strings.Join(tr.got, " ")
	tr.got = nil
	return s
}

func (tr *trace) guard(name string) dygo.GuardFunc {
	return func(dygo.ExecutionContext) (bool, error) { tr.add("g:" + name); return true, nil }
}

func (tr *trace) interceptor(name string) dygo.InterceptorFunc {
	return func(_ dygo.ExecutionContext, next dygo.CallHandler) (any, error) {
		tr.add("i:" + name)
		out, err := next.Handle()
		tr.add("/i:" + name)
		return out, err
	}
}

func (tr *trace) pipe(name string) dygo.PipeFunc {
	return func(dygo.ExecutionContext, any) error { tr.add("p:" + name); return nil }
}

func (tr *trace) filter(name string, handle bool) dygo.FilterFunc {
	return func(_ error, host dygo.ArgumentsHost) bool {
		tr.add("f:" + name)
		if handle {
			errs.WriteJSON(host.HTTP().Response(), errs.ToBody(errs.New(http.StatusTeapot, name)))
		}
		return handle
	}
}

type echoIn struct {
	Name string `query:"name"`
}

func TestPipelineOrder(t *testing.T) {
	tr := &trace{}
	ctrl := routesFunc(func(r dygo.Router) {
		r = r.Use(dygo.Guards(tr.guard("ctrl")), dygo.Interceptors(tr.interceptor("ctrl")),
			dygo.Pipes(tr.pipe("ctrl")), dygo.Filters(tr.filter("ctrl", false)))
		r.Get("/ok", dygo.Handle(func(_ context.Context, in echoIn) (string, error) {
			tr.add("handler")
			return in.Name, nil
		}), dygo.Guards(tr.guard("route")), dygo.Interceptors(tr.interceptor("route")),
			dygo.Pipes(tr.pipe("route")), dygo.Filters(tr.filter("route", false)))
		r.Get("/fail", dygo.HandleNoInput(func(context.Context) (string, error) {
			return "", errors.New("boom")
		}), dygo.Filters(tr.filter("route", false)))
	})
	srv := dygotest.New(&dygo.Module{Name: "app", Controllers: []dygo.Provider{dygo.Value(ctrl)}}).
		With(dygo.WithGlobalGuards(tr.guard("global")), dygo.WithGlobalInterceptors(tr.interceptor("global")),
			dygo.WithGlobalPipes(tr.pipe("global")), dygo.WithGlobalFilters(tr.filter("global", true))).
		CompileHTTP(t)

	if got := srv.GET("/ok?name=x").Do(t).Status(t, 200).Text(); got != "x" {
		t.Fatalf("body %q", got)
	}
	want := "g:global g:ctrl g:route i:global i:ctrl i:route p:global p:ctrl p:route handler /i:route /i:ctrl /i:global"
	if got := tr.take(); got != want {
		t.Fatalf("order\n got %s\nwant %s", got, want)
	}

	srv.GET("/fail").Do(t).Status(t, http.StatusTeapot)
	want = "g:global g:ctrl i:global i:ctrl p:global p:ctrl /i:ctrl /i:global f:route f:ctrl f:global"
	if got := tr.take(); got != want {
		t.Fatalf("filters\n got %s\nwant %s", got, want)
	}
}

type userKey struct{}

// sourceIn is filled by a custom source that reads the request context.
type sourceIn struct {
	User string `ctxuser:""`
}

func init() {
	dygo.RegisterSource("ctxuser", func(r *http.Request) (any, error) {
		u, _ := r.Context().Value(userKey{}).(string)
		return u, nil
	})
}

func TestGuards(t *testing.T) {
	auth := func(ec dygo.ExecutionContext) (bool, error) {
		tok := ec.HTTP().Request().Header.Get("Authorization")
		switch tok {
		case "":
			return false, errs.Unauthorized("")
		case "Bearer guest":
			return false, nil
		}
		ec.SetContext(context.WithValue(ec.Context(), userKey{}, strings.TrimPrefix(tok, "Bearer ")))
		return true, nil
	}
	ctrl := routesFunc(func(r dygo.Router) {
		r.Get("/me", dygo.HandleNoInput(func(ctx context.Context) (string, error) {
			// dygo.Request must see the context the guard set, as the handler does.
			if got := dygo.Request(ctx).Context().Value(userKey{}); got != ctx.Value(userKey{}) {
				return "", fmt.Errorf("request context has %v", got)
			}
			return ctx.Value(userKey{}).(string), nil
		}), dygo.Guards(auth))
		r.Get("/source", dygo.Handle(func(_ context.Context, in sourceIn) (string, error) { return in.User, nil }), dygo.Guards(auth))
		r.Get("/raw", dygo.HandleRaw(func(w http.ResponseWriter, r *http.Request) error {
			_, err := w.Write([]byte(r.Context().Value(userKey{}).(string)))
			return err
		}), dygo.Guards(auth))
	})
	srv := dygotest.New(&dygo.Module{Name: "app", Controllers: []dygo.Provider{dygo.Value(ctrl)}}).CompileHTTP(t)

	var b errorBody
	srv.GET("/me").Do(t).Status(t, 401).JSON(t, &b)
	srv.GET("/me").Header("Authorization", "Bearer guest").Do(t).Status(t, 403).JSON(t, &b)
	if b.Message != "Forbidden resource" {
		t.Fatalf("403 body %+v", b)
	}
	if got := srv.GET("/me").Header("Authorization", "Bearer ann").Do(t).Status(t, 200).Text(); got != "ann" {
		t.Fatalf("me = %q", got)
	}
	if got := srv.GET("/source").Header("Authorization", "Bearer cat").Do(t).Status(t, 200).Text(); got != "cat" {
		t.Fatalf("source = %q", got)
	}
	if got := srv.GET("/raw").Header("Authorization", "Bearer bob").Do(t).Status(t, 200).Text(); got != "bob" {
		t.Fatalf("raw = %q", got)
	}
}

func TestInterceptors(t *testing.T) {
	calls := 0
	envelope := func(_ dygo.ExecutionContext, next dygo.CallHandler) (any, error) {
		out, err := next.Handle()
		if err != nil {
			return nil, errs.New(http.StatusBadGateway, "", errs.Cause(err))
		}
		return map[string]any{"data": out}, nil
	}
	cached := func(dygo.ExecutionContext, dygo.CallHandler) (any, error) { return "from cache", nil }
	retry := func(_ dygo.ExecutionContext, next dygo.CallHandler) (any, error) {
		out, err := next.Handle()
		if err != nil {
			return next.Handle()
		}
		return out, nil
	}
	ctrl := routesFunc(func(r dygo.Router) {
		r.Get("/wrap", dygo.HandleNoInput(func(context.Context) (int, error) { return 7, nil }), dygo.Interceptors(envelope))
		r.Get("/err", dygo.HandleNoInput(func(context.Context) (int, error) { return 0, errors.New("x") }), dygo.Interceptors(envelope))
		r.Get("/cached", dygo.HandleNoInput(func(context.Context) (string, error) {
			t.Error("handler ran")
			return "", nil
		}), dygo.Interceptors(cached))
		r.Get("/flaky", dygo.HandleNoInput(func(context.Context) (int, error) {
			calls++
			if calls == 1 {
				return 0, errors.New("first try fails")
			}
			return calls, nil
		}), dygo.Interceptors(retry))
		// More interceptors than reqState holds inline.
		many := make([]any, 6)
		for i := range many {
			many[i] = dygo.InterceptorFunc(func(_ dygo.ExecutionContext, next dygo.CallHandler) (any, error) {
				out, err := next.Handle()
				return out.(int) + 1, err
			})
		}
		r.Get("/many", dygo.HandleNoInput(func(context.Context) (int, error) { return 0, nil }), dygo.Interceptors(many...))
	})
	srv := dygotest.New(&dygo.Module{Name: "app", Controllers: []dygo.Provider{dygo.Value(ctrl)}}).CompileHTTP(t)

	if got := srv.GET("/wrap").Do(t).Status(t, 200).Text(); got != "{\"data\":7}\n" {
		t.Fatalf("wrap = %q", got)
	}
	srv.GET("/err").Do(t).Status(t, 502)
	if got := srv.GET("/cached").Do(t).Status(t, 200).Text(); got != "from cache" {
		t.Fatalf("cached = %q", got)
	}
	if got := srv.GET("/flaky").Do(t).Status(t, 200).Text(); got != "2\n" {
		t.Fatalf("flaky = %q", got)
	}
	if got := srv.GET("/many").Do(t).Status(t, 200).Text(); got != "6\n" {
		t.Fatalf("many = %q", got)
	}
}

type signupBody struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

// Validate is found on the Body field by validate.Methods.
func (b *signupBody) Validate(context.Context) error {
	var v validate.Errors
	if !strings.Contains(b.Email, "@") {
		v.Add("body", "email", "must be a valid email")
	}
	if b.Name == "" {
		v.Add("body", "name", "is required")
	}
	return v.Err()
}

type signupIn struct {
	Body signupBody `body:"json"`
}

type pageIn struct {
	Limit int `query:"limit" default:"10"`
}

func (p pageIn) Validate(context.Context) error {
	if p.Limit > 100 {
		return validate.Errors{{Source: "query", Name: "limit", Reason: "must be at most 100"}}.Err()
	}
	return nil
}

// searchIn gets Validate from the embedded pageIn.
type searchIn struct {
	pageIn
	Q string `query:"q"`
}

func TestPipesAndValidation(t *testing.T) {
	trim := func(_ dygo.ExecutionContext, in any) error {
		if s, ok := in.(*signupIn); ok {
			s.Body.Name = strings.TrimSpace(s.Body.Name)
		}
		return nil
	}
	ctrl := routesFunc(func(r dygo.Router) {
		r.Post("/signup", dygo.Handle(func(_ context.Context, in signupIn) (string, error) {
			return in.Body.Name, nil
		}), dygo.Pipes(trim))
		r.Get("/search", dygo.Handle(func(_ context.Context, in searchIn) (int, error) { return in.Limit, nil }))
	})
	mod := &dygo.Module{Name: "app", Controllers: []dygo.Provider{dygo.Value(ctrl)}}
	srv := dygotest.New(mod).With(validate.Use(validate.Methods())).CompileHTTP(t)

	// The global validation pipe runs before the route's trim pipe.
	var b errorBody
	srv.POST("/signup").JSON(map[string]string{"email": "nope", "name": ""}).Do(t).Status(t, 400).JSON(t, &b)
	if b.Message != "validation failed" || len(b.Details) != 2 || b.Details[1].Name != "name" {
		t.Fatalf("body %+v", b)
	}
	if got := srv.POST("/signup").JSON(map[string]string{"email": "a@b", "name": " ann "}).Do(t).Status(t, 201).Text(); got != "ann" {
		t.Fatalf("name %q", got)
	}
	srv.GET("/search?limit=500").Do(t).Status(t, 400).JSON(t, &b)
	if len(b.Details) != 1 || b.Details[0].Source != "query" {
		t.Fatalf("body %+v", b)
	}
	if got := srv.GET("/search").Do(t).Status(t, 200).Text(); got != "10\n" {
		t.Fatalf("search %q", got)
	}

	// A custom error shape: a list of messages.
	shaped := dygotest.New(mod).With(validate.Use(validate.Methods(), validate.WithErrorShape(func(fs []validate.FieldError) any {
		out := make([]string, len(fs))
		for i, f := range fs {
			out[i] = f.Name + " " + f.Reason
		}
		return out
	}))).CompileHTTP(t)
	res := shaped.POST("/signup").JSON(map[string]string{"email": "a@b"}).Do(t).Status(t, 400).Text()
	if !strings.Contains(res, `"details":["name is required"]`) {
		t.Fatalf("shaped = %s", res)
	}
}

func TestValidateResponses(t *testing.T) {
	ctrl := routesFunc(func(r dygo.Router) {
		r.Get("/bad", dygo.HandleNoInput(func(context.Context) (*signupBody, error) { return &signupBody{}, nil }),
			dygo.Interceptors(validate.Responses(validate.Methods())))
	})
	srv := dygotest.New(&dygo.Module{Name: "app", Controllers: []dygo.Provider{dygo.Value(ctrl)}}).CompileHTTP(t)
	srv.GET("/bad").Do(t).Status(t, 500)
}

type conflictError struct{ what string }

func (e *conflictError) Error() string { return e.what + " already exists" }

func TestFilters(t *testing.T) {
	conflict := dygo.FilterFor(func(e *conflictError, host dygo.ArgumentsHost) {
		errs.WriteJSON(host.HTTP().Response(), errs.ToBody(errs.Conflict(e.Error())))
	})
	var panicked any
	panics := dygo.FilterFor(func(e *dygo.PanicError, host dygo.ArgumentsHost) {
		panicked = e.Value
		host.HTTP().Response().WriteHeader(http.StatusServiceUnavailable)
	})
	ctrl := routesFunc(func(r dygo.Router) {
		r = r.Use(dygo.Filters(conflict))
		r.Post("/users", dygo.HandleNoInput(func(context.Context) (string, error) {
			return "", fmt.Errorf("saving: %w", &conflictError{what: "ann"})
		}))
		r.Get("/panic", dygo.HandleNoInput(func(context.Context) (string, error) { panic("oops") }), dygo.Filters(panics))
		r.Get("/other", dygo.HandleNoInput(func(context.Context) (string, error) { return "", errs.NotFound("") }))
	})
	srv := dygotest.New(&dygo.Module{Name: "app", Controllers: []dygo.Provider{dygo.Value(ctrl)}}).CompileHTTP(t)

	var b errorBody
	srv.POST("/users").Do(t).Status(t, 409).JSON(t, &b)
	if b.Message != "ann already exists" {
		t.Fatalf("body %+v", b)
	}
	srv.GET("/panic").Do(t).Status(t, 503)
	if panicked != "oops" {
		t.Fatalf("panicked = %v", panicked)
	}
	srv.GET("/other").Do(t).Status(t, 404)
}

// --- dependency injection ----------------------------------------------------

type Tokens struct{ valid map[string]bool }

func NewTokens() *Tokens { return &Tokens{valid: map[string]bool{"good": true}} }

type TokenGuard struct {
	tokens *Tokens
}

var tokenGuards sync.Map

func NewTokenGuard(t *Tokens) *TokenGuard {
	g := &TokenGuard{tokens: t}
	tokenGuards.Store(g, true)
	return g
}

func (g *TokenGuard) CanActivate(ec dygo.ExecutionContext) (bool, error) {
	return g.tokens.valid[ec.HTTP().Request().Header.Get("X-Token")], nil
}

type AuditInterceptor struct{ log *trace }

func (a *AuditInterceptor) Intercept(ec dygo.ExecutionContext, next dygo.CallHandler) (any, error) {
	a.log.add("audit " + ec.Route().Path)
	return next.Handle()
}

func TestEnhancersFromDI(t *testing.T) {
	tr := &trace{}
	ctrl := routesFunc(func(r dygo.Router) {
		r = r.Use(dygo.Guards(NewTokenGuard)) // constructor, deps from this module
		r.Get("/a", dygo.HandleNoInput(func(context.Context) (string, error) { return "a", nil }))
		r.Get("/b", dygo.HandleNoInput(func(context.Context) (string, error) { return "b", nil }),
			dygo.Guards(dygo.Ref[*TokenGuard]()))
	})
	mod := &dygo.Module{
		Name: "app",
		Providers: []dygo.Provider{
			dygo.Provide(NewTokens),
			dygo.Provide(NewTokenGuard),
			dygo.Provide(func() *AuditInterceptor { return &AuditInterceptor{log: tr} }, dygo.Group(dygo.AppInterceptors)),
		},
		Controllers: []dygo.Provider{dygo.Value(ctrl)},
	}
	tokenGuards.Clear()
	srv := dygotest.New(mod).CompileHTTP(t)
	srv.GET("/a").Do(t).Status(t, 403)
	srv.GET("/a").Header("X-Token", "good").Do(t).Status(t, 200)
	srv.GET("/b").Header("X-Token", "good").Do(t).Status(t, 200)
	if got := tr.take(); got != "audit /a audit /b" {
		t.Fatalf("audit = %q", got)
	}
	// One provider instance plus one built from the constructor, shared by both routes.
	n := 0
	tokenGuards.Range(func(any, any) bool { n++; return true })
	if n != 2 {
		t.Fatalf("%d guards built, want 2", n)
	}
}

func TestEnhancerBootErrors(t *testing.T) {
	ctrl := routesFunc(func(r dygo.Router) {
		r.Get("/a", dygo.HandleNoInput(func(context.Context) (string, error) { return "", nil }),
			dygo.Guards(NewTokenGuard, dygo.Ref[*AuditInterceptor](), 42))
	})
	_, err := dygotest.New(&dygo.Module{Name: "app", Controllers: []dygo.Provider{dygo.Value(ctrl)}}).
		With(dygo.WithGlobalInterceptors("nope")).BuildHTTP(context.Background())
	if err == nil {
		t.Fatal("expected a boot error")
	}
	for _, want := range []string{"*dygo_test.Tokens", "*dygo_test.AuditInterceptor", "int is not a dygo.Guard", "global enhancers: string is not a dygo.Interceptor"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
}

// --- metadata ----------------------------------------------------------------

var (
	rolesKey  = meta.NewKey[[]string]("roles")
	publicKey = meta.NewKey[bool]("public")
)

func TestMetadata(t *testing.T) {
	var seen []string
	var mu sync.Mutex
	rolesGuard := func(ec dygo.ExecutionContext) (bool, error) {
		if public, _ := publicKey.Get(ec); public {
			return true, nil
		}
		roles := meta.GetAllAndMerge(ec, rolesKey)
		mu.Lock()
		seen = roles
		mu.Unlock()
		return strings.Contains(ec.HTTP().Request().Header.Get("X-Roles"), roles[len(roles)-1]), nil
	}
	ctrl := routesFunc(func(r dygo.Router) {
		r = r.Use(rolesKey.Set([]string{"user"}), dygo.Guards(rolesGuard))
		r.Get("/profile", dygo.HandleNoInput(func(context.Context) (string, error) { return "p", nil }))
		r.Delete("/users", dygo.HandleNoInput(func(context.Context) (dygo.NoContent, error) { return dygo.NoContent{}, nil }),
			meta.Set(rolesKey, []string{"admin"}))
		r.Get("/health", dygo.HandleNoInput(func(context.Context) (string, error) { return "ok", nil }), publicKey.Set(true))
	})
	srv := dygotest.New(&dygo.Module{Name: "app", Controllers: []dygo.Provider{dygo.Value(ctrl)}}).CompileHTTP(t)

	srv.GET("/profile").Header("X-Roles", "user").Do(t).Status(t, 200)
	srv.DELETE("/users").Header("X-Roles", "user").Do(t).Status(t, 403)
	if strings.Join(seen, ",") != "user,admin" {
		t.Fatalf("merged roles %v", seen)
	}
	srv.DELETE("/users").Header("X-Roles", "admin").Do(t).Status(t, 204)
	srv.GET("/health").Do(t).Status(t, 200)
}

// --- built-in interceptors ---------------------------------------------------

func TestTimeoutAndLoggingInterceptors(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	log := slog.New(slog.NewTextHandler(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	}), nil))
	ctrl := routesFunc(func(r dygo.Router) {
		r.Get("/slow", dygo.HandleNoInput(func(ctx context.Context) (string, error) {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(5 * time.Second):
				return "late", nil
			}
		}), dygo.Interceptors(interceptors.Timeout(20*time.Millisecond)))
		r.Get("/fast", dygo.HandleNoInput(func(context.Context) (string, error) { return "ok", nil }),
			dygo.Interceptors(interceptors.Timeout(time.Second)))
	})
	srv := dygotest.New(&dygo.Module{Name: "app", Controllers: []dygo.Provider{dygo.Value(ctrl)}}).
		With(dygo.WithGlobalInterceptors(interceptors.Logging(log))).CompileHTTP(t)

	var b errorBody
	srv.GET("/slow").Do(t).Status(t, 408).JSON(t, &b)
	if b.Message != "Request Timeout" {
		t.Fatalf("body %+v", b)
	}
	srv.GET("/fast").Do(t).Status(t, 200)
	mu.Lock()
	out := buf.String()
	mu.Unlock()
	if !strings.Contains(out, "request failed") || !strings.Contains(out, "route=/slow") || !strings.Contains(out, "request handled") {
		t.Fatalf("log:\n%s", out)
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
