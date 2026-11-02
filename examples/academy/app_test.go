package main

import (
	"log/slog"
	"testing"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/dygotest"

	"github.com/dygoframework/dygo/examples/academy/internal/config"
)

// TestWiring validates the whole module graph without building anything.
func TestWiring(t *testing.T) {
	if _, err := dygo.Describe(AppModule(&config.Config{}, slog.New(slog.DiscardHandler))); err != nil {
		t.Fatal(err)
	}
}

// TestApp boots the whole application, end to end.
func TestApp(t *testing.T) {
	cfg, err := config.Load(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	srv := dygotest.New(AppModule(cfg, log)).With(AppOptions(cfg, log)...).CompileHTTP(t)
	srv.GET("/health/ready").Do(t).Status(t, 200)
	srv.GET("/openapi.json").Do(t).Status(t, 200)
}
