package cache_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/cache"
)

type product struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func TestGetSetAndExpiry(t *testing.T) {
	ctx := context.Background()
	c := cache.New(cache.Options{TTL: 20 * time.Millisecond, Prefix: "app:"})
	if err := cache.Set(ctx, c, "p1", product{ID: "1", Name: "pen"}, 0); err != nil {
		t.Fatal(err)
	}
	p, ok, err := cache.Get[product](ctx, c, "p1")
	if err != nil || !ok || p.Name != "pen" {
		t.Fatalf("Get = %+v %v %v", p, ok, err)
	}
	if err := cache.Set(ctx, c, "forever", 1, time.Hour); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if _, ok, _ := cache.Get[product](ctx, c, "p1"); ok {
		t.Error("p1 should have expired")
	}
	if _, ok, _ := cache.Get[int](ctx, c, "forever"); !ok {
		t.Error("forever expired")
	}
	if err := c.Delete(ctx, "forever"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := cache.Get[int](ctx, c, "forever"); ok {
		t.Error("forever not deleted")
	}
	if _, _, err := cache.Get[int](ctx, c, "missing"); err != nil {
		t.Error(err)
	}
}

func TestMemoryEvictsLeastRecentlyUsed(t *testing.T) {
	ctx := context.Background()
	m := cache.NewMemory(2)
	_ = m.Set(ctx, "a", []byte("1"), 0)
	_ = m.Set(ctx, "b", []byte("2"), 0)
	_, _, _ = m.Get(ctx, "a")
	_ = m.Set(ctx, "c", []byte("3"), 0)
	if _, ok, _ := m.Get(ctx, "b"); ok {
		t.Error("b should be evicted")
	}
	if _, ok, _ := m.Get(ctx, "a"); !ok {
		t.Error("a was used recently")
	}
	if m.Len() != 2 {
		t.Errorf("len = %d", m.Len())
	}
}

func TestGetOrSetCallsOnce(t *testing.T) {
	ctx := context.Background()
	c := cache.New(cache.Options{})
	var calls atomic.Int32
	release := make(chan struct{})
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := cache.GetOrSet(ctx, c, "k", 0, func(context.Context) (int, error) {
				calls.Add(1)
				<-release
				return 42, nil
			})
			if err != nil || v != 42 {
				t.Errorf("v = %d, %v", v, err)
			}
		}()
	}
	time.Sleep(10 * time.Millisecond)
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Errorf("fn ran %d times", calls.Load())
	}
	boom := errors.New("boom")
	if _, err := cache.GetOrSet(ctx, c, "e", 0, func(context.Context) (int, error) { return 0, boom }); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
	if _, ok, _ := cache.Get[int](ctx, c, "e"); ok {
		t.Error("errors must not be cached")
	}
}

func TestWrap(t *testing.T) {
	c := cache.New(cache.Options{})
	calls := 0
	byID := cache.Wrap(c, "product", time.Minute, func(_ context.Context, id string) (*product, error) {
		calls++
		return &product{ID: id}, nil
	})
	for range 3 {
		p, err := byID(context.Background(), "7")
		if err != nil || p.ID != "7" {
			t.Fatalf("p = %v, %v", p, err)
		}
	}
	if calls != 1 {
		t.Errorf("calls = %d", calls)
	}
	if _, ok, _ := c.GetRaw(context.Background(), "product:7"); !ok {
		t.Error("key product:7 missing")
	}
}

type catalog struct{ calls atomic.Int32 }

func (c *catalog) Routes(r dygo.Router) {
	r = r.Use(dygo.Interceptors(dygo.Ref[*cache.Interceptor]()))
	r.Get("/products", dygo.HandleNoInput(func(context.Context) ([]product, error) {
		c.calls.Add(1)
		return []product{{ID: "1", Name: "pen"}}, nil
	}), cache.TTL(time.Hour))
	r.Get("/text", dygo.HandleNoInput(func(context.Context) (string, error) {
		c.calls.Add(1)
		return "hi", nil
	}), cache.Key("text"))
	r.Get("/live", dygo.HandleNoInput(func(context.Context) (string, error) {
		c.calls.Add(1)
		return "now", nil
	}), cache.NoCache())
	r.Post("/products", dygo.HandleNoInput(func(context.Context) (string, error) {
		c.calls.Add(1)
		return "ok", nil
	}))
}

func TestInterceptor(t *testing.T) {
	ctl := &catalog{}
	app, err := dygo.Create(context.Background(), &dygo.Module{
		Name:        "app",
		Imports:     []*dygo.Module{cache.Module(cache.Options{TTL: time.Minute})},
		Controllers: []dygo.Provider{dygo.Value(ctl)},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close(context.Background())
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()
	get := func(method, path string) (string, string) {
		req, _ := http.NewRequest(method, srv.URL+path, nil)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return string(b), res.Header.Get("Content-Type")
	}
	for range 2 {
		body, ct := get("GET", "/products")
		if body != `[{"id":"1","name":"pen"}]`+"\n" || ct != "application/json; charset=utf-8" {
			t.Errorf("body = %q, %q", body, ct)
		}
		if body, ct := get("GET", "/text"); body != "hi" || ct != "text/plain; charset=utf-8" {
			t.Errorf("text = %q, %q", body, ct)
		}
		get("GET", "/live")
		get("POST", "/products")
	}
	// products and text once each, live and POST twice each.
	if n := ctl.calls.Load(); n != 6 {
		t.Errorf("handler calls = %d", n)
	}
	c := dygo.MustGet[*cache.Cache](app)
	if _, ok, _ := c.GetRaw(context.Background(), "http:/products"); !ok {
		t.Error("key http:/products missing")
	}
}
