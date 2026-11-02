package main

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/dygotest"
	"github.com/dygoframework/dygo/examples/worker/report"
	"github.com/dygoframework/dygo/examples/worker/users"
)

type fakeRepo struct {
	mu    sync.Mutex
	saved map[string]string
}

func (f *fakeRepo) Save(_ context.Context, id, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saved[id] = name
	return nil
}

func (f *fakeRepo) Count(context.Context) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.saved)
}

func (f *fakeRepo) get(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.saved[id]
}

func TestAppBootsAndReports(t *testing.T) {
	fake := &fakeRepo{saved: map[string]string{}}
	app := dygotest.New(AppModule(slog.New(slog.NewTextHandler(io.Discard, nil)))).
		OverrideValue(dygo.TypeOf[users.Repo](), fake).
		Compile(t)

	// OnApplicationBootstrap seeded one user through the fake repo.
	if fake.get("1") != "Dylan" {
		t.Fatalf("seed data missing: %v", fake.saved)
	}
	svc := dygo.MustGet[*users.Service](app)
	if err := svc.Register(t.Context(), "2", ""); err == nil {
		t.Fatal("expected validation error")
	}
	runner := dygo.MustGet[*report.Runner](app)
	runner.RunOnce(t.Context())
	if runner.Runs() < 1 {
		t.Fatal("runner did not run")
	}
}
