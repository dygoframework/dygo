package dygo_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dygoframework/dygo"
)

func TestDescribe(t *testing.T) {
	built := false
	root := usersModule(dbModule(&Config{DSN: "x"}))
	root.Providers = append(root.Providers, dygo.Provide(func() *Item { built = true; return &Item{} }))
	g, err := dygo.Describe(root)
	if err != nil {
		t.Fatal(err)
	}
	if built {
		t.Fatal("Describe must not build providers")
	}
	if len(g.Modules) != 2 || g.Modules[0].Name != "users" || g.Modules[0].Imports[0] != 1 {
		t.Fatalf("modules: %+v", g.Modules)
	}
	byType := map[string]dygo.GraphProvider{}
	for _, p := range g.Providers {
		byType[p.Type] = p
	}
	repo := byType["*dygo_test.PgRepo"]
	if repo.Tokens[1] != "dygo_test.Repo" || len(repo.Deps) != 1 || repo.Deps[0].Providers[0] != byType["*dygo_test.DB"].ID {
		t.Fatalf("repo: %+v", repo)
	}
	if c := byType["*dygo_test.UsersController"]; !c.Controller || c.Deps[0].Token != "*dygo_test.UserService" {
		t.Fatalf("controller: %+v", c)
	}

	broken := &dygo.Module{Name: "broken", Providers: []dygo.Provider{dygo.Provide(NewUserService)}}
	g, err = dygo.Describe(broken)
	var be *dygo.BootError
	if !errors.As(err, &be) || g == nil || len(g.Providers[0].Deps[0].Providers) != 0 {
		t.Fatalf("broken: %v %+v", err, g)
	}
}

func TestInspectEnv(t *testing.T) {
	out := filepath.Join(t.TempDir(), "report.json")
	t.Setenv("DYGO_INSPECT_OUT", out)
	read := func() inspectReport {
		t.Helper()
		data, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		var rep inspectReport
		if err := json.Unmarshal(data, &rep); err != nil {
			t.Fatal(err)
		}
		return rep
	}
	built := 0
	root := &dygo.Module{
		Name:        "app",
		Providers:   []dygo.Provider{dygo.Provide(func() *Config { built++; return &Config{} })},
		Controllers: []dygo.Provider{dygo.Provide(NewItemsController)},
	}

	// Graph only: nothing is built.
	_, err := dygo.Create(context.Background(), root)
	if !errors.Is(err, dygo.ErrInspected) || built != 0 {
		t.Fatalf("graph: %v, built %d", err, built)
	}
	if rep := read(); len(rep.Graph.Providers) != 2 || rep.Routes != nil {
		t.Fatalf("graph report: %+v", rep)
	}

	// Routes: providers are built, no startup hook runs, nothing is served.
	t.Setenv("DYGO_INSPECT", "routes")
	_, err = dygo.Create(context.Background(), root)
	if !errors.Is(err, dygo.ErrInspected) || built != 1 {
		t.Fatalf("routes: %v, built %d", err, built)
	}
	rep := read()
	if len(rep.Routes) != 5 || rep.Routes[0].Controller == "" {
		t.Fatalf("routes report: %+v", rep.Routes)
	}

	// A broken app reports its errors and still fails with the boot error.
	_, err = dygo.Create(context.Background(), &dygo.Module{Name: "bad", Providers: []dygo.Provider{dygo.Provide(NewDB)}})
	var be *dygo.BootError
	if !errors.As(err, &be) {
		t.Fatalf("broken: %v", err)
	}
	if rep := read(); len(rep.Errors) != 1 || !strings.Contains(rep.Errors[0], "*dygo_test.Config") {
		t.Fatalf("broken report: %+v", rep.Errors)
	}
}

// inspectReport is the part of the CLI report file these tests read.
type inspectReport struct {
	Graph  *dygo.GraphInfo `json:"graph"`
	Routes []struct {
		Method, Path, Controller, Handler, Input, Output string
		Status                                           int
	} `json:"routes"`
	Errors []string `json:"errors"`
}
