package ratelimit_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/dygotest"
	"github.com/dygoframework/dygo/ratelimit"
)

type ctrl struct{}

func (ctrl) Routes(r dygo.Router) {
	ok := dygo.HandleNoInput(func(context.Context) (string, error) { return "ok", nil })
	r.Get("/a", ok)
	r.Get("/login", ok, ratelimit.Limit(1, time.Minute))
	r.Get("/free", ok, ratelimit.Skip())
}

func TestGuard(t *testing.T) {
	srv := dygotest.New(&dygo.Module{Name: "app", Controllers: []dygo.Provider{dygo.Value(ctrl{})}}).
		With(dygo.WithGlobalGuards(ratelimit.Guard(ratelimit.Options{Limit: 2, Window: time.Minute}))).CompileHTTP(t)

	res := srv.GET("/a").Do(t).Status(t, 200)
	if res.Header.Get("X-RateLimit-Limit") != "2" || res.Header.Get("X-RateLimit-Remaining") != "1" || res.Header.Get("X-RateLimit-Reset") != "30" {
		t.Fatal(res.Header)
	}
	srv.GET("/a").Do(t).Status(t, 200)
	res = srv.GET("/a").Do(t).Status(t, 429)
	if res.Header.Get("Retry-After") == "" {
		t.Fatal(res.Header)
	}
	// Routes count separately; /login has its own limit of 1.
	srv.GET("/login").Do(t).Status(t, 200)
	srv.GET("/login").Do(t).Status(t, 429)
	for range 5 {
		srv.GET("/free").Do(t).Status(t, 200)
	}
	// Another client is not limited.
	if got := srv.GET("/a").Header("X-Forwarded-For", "10.0.0.9").Do(t).StatusCode; got != 429 {
		t.Fatalf("X-Forwarded-For trusted without TrustProxy: %d", got)
	}
}

func TestMemoryStoreRefills(t *testing.T) {
	s := ratelimit.NewMemoryStore()
	ctx := context.Background()
	for i := range 3 {
		r, _ := s.Take(ctx, "k", 2, 40*time.Millisecond)
		if r.Allowed != (i < 2) || (i < 2 && r.Remaining != 1-i) {
			t.Fatalf("take %d: %+v", i, r)
		}
	}
	// One request's worth refills after window/limit, not a whole window.
	time.Sleep(25 * time.Millisecond)
	if r, _ := s.Take(ctx, "k", 2, 40*time.Millisecond); !r.Allowed {
		t.Fatalf("after refill: %+v", r)
	}
	if r, _ := s.Take(ctx, "k", 2, 40*time.Millisecond); r.Allowed {
		t.Fatalf("burst past the limit: %+v", r)
	}
}

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Add("X-Forwarded-For", "6.6.6.6, 1.2.3.4")
	r.Header.Add("X-Forwarded-For", "10.0.0.2")
	for hops, want := range map[int]string{0: "10.0.0.1", 1: "10.0.0.2", 2: "1.2.3.4", 9: "6.6.6.6"} {
		if got := ratelimit.ClientIP(r, hops); got != want {
			t.Errorf("hops %d: %s, want %s", hops, got, want)
		}
	}
}
