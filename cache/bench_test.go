package cache_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/cache"
)

type bigCatalog struct{}

func (bigCatalog) Routes(r dygo.Router) {
	r = r.Use(dygo.Interceptors(dygo.Ref[*cache.Interceptor]()))
	ps := make([]product, 200)
	for i := range ps {
		ps[i] = product{ID: "12345", Name: "a fountain pen with a long name"}
	}
	r.Get("/products", dygo.HandleNoInput(func(context.Context) ([]product, error) { return ps, nil }), cache.TTL(time.Hour))
}

func BenchmarkInterceptorHit(b *testing.B) {
	app, err := dygo.Create(context.Background(), &dygo.Module{
		Name:        "app",
		Imports:     []*dygo.Module{cache.Module(cache.Options{TTL: time.Minute})},
		Controllers: []dygo.Provider{dygo.Value(bigCatalog{})},
	})
	if err != nil {
		b.Fatal(err)
	}
	h := app.Handler()
	b.ReportAllocs()
	for b.Loop() {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/products", nil))
		if w.Code != 200 {
			b.Fatal(w.Code)
		}
	}
}
