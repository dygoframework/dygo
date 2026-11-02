package health_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/dygotest"
	"github.com/dygoframework/dygo/health"
)

type fakeDB struct{ down atomic.Bool }

func (d *fakeDB) PingContext(context.Context) error {
	if d.down.Load() {
		return errors.New("connection refused")
	}
	return nil
}

func TestHealth(t *testing.T) {
	db := &fakeDB{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	defer upstream.Close()
	srv := dygotest.New(&dygo.Module{
		Name:    "app",
		Imports: []*dygo.Module{health.Module(health.Options{})},
		Providers: []dygo.Provider{
			dygo.Provide(func() health.Indicator { return health.Ping("db", db) }, dygo.Group(health.Indicators)),
			dygo.Provide(func() health.Indicator { return health.HTTP("upstream", upstream.URL, nil) }, dygo.Group(health.Indicators)),
			dygo.Provide(func() health.Indicator {
				return health.Func("flaky", func(context.Context) error {
					if db.down.Load() {
						panic("indicator bug")
					}
					return nil
				})
			}, dygo.Group(health.Indicators)),
		},
	}).With(dygo.WithVersioning(dygo.URIVersioning("1")), dygo.WithGlobalPrefix("/api", "/health")).CompileHTTP(t)

	srv.GET("/health/live").Do(t).Status(t, 200)
	var res health.Result
	srv.GET("/health/ready").Do(t).Status(t, 200).JSON(t, &res)
	if res.Status != "ok" || res.Info["db"].Status != "up" || res.Info["upstream"].Status != "up" {
		t.Fatalf("%+v", res)
	}
	db.down.Store(true)
	res = health.Result{}
	srv.GET("/health/ready").Do(t).Status(t, 503).JSON(t, &res)
	// A panicking indicator is reported as down instead of crashing the process.
	if res.Status != "error" || res.Error["db"].Message != "connection refused" || res.Info["upstream"].Status != "up" ||
		res.Error["flaky"].Message != "panic: indicator bug" {
		t.Fatalf("%+v", res)
	}
}
