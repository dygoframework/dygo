package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dygoframework/dygo/dygotest"
)

// fixtureMain is a tiny app. It has two routes and exits with status 3 after
// printing its arguments, unless FX_WAIT is set: then it waits for SIGTERM.
const fixtureMain = `package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/dygoframework/dygo"
)

type ctrl struct{}

func (ctrl) Routes(r dygo.Router) {
	r.Get("/hi", dygo.HandleNoInput(func(context.Context) (string, error) { return "hi", nil }))
	r.Host("api.example.com").All("/any", dygo.HandleNoInput(func(context.Context) (string, error) { return "", nil }))
}

func main() {
	_, err := dygo.Create(context.Background(), &dygo.Module{Name: "fx", Controllers: []dygo.Provider{dygo.Value(ctrl{})}})
	if errors.Is(err, dygo.ErrInspected) {
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if os.Getenv("FX_WAIT") != "" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
		defer stop()
		fmt.Println("ready MARK")
		<-ctx.Done()
		return
	}
	fmt.Println("args", os.Args[1:])
	os.Exit(3)
}
`

// brokenMain fails to wire: the provider needs an int nobody provides.
const brokenMain = `package main

import (
	"context"

	"github.com/dygoframework/dygo"
)

func main() {
	_, _ = dygo.Create(context.Background(), &dygo.Module{Name: "fx", Providers: []dygo.Provider{dygo.Provide(func(int) string { return "" })}})
}
`

// fixture writes a module using the local framework with main.go as given
// and makes it the working directory.
func fixture(t *testing.T, main string) string {
	t.Helper()
	dygotest.Integration(t) // runs go build and go run
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module example.com/fx\n\ngo 1.27.0\n\nrequire github.com/dygoframework/dygo v0.0.0\n\nreplace github.com/dygoframework/dygo => "+repo+"\n")
	write(t, filepath.Join(dir, "main.go"), main)
	t.Chdir(dir)
	return dir
}

func TestInspectCommands(t *testing.T) {
	fixture(t, fixtureMain)
	tests := []struct {
		args []string
		want []string
	}{
		{[]string{"routes"}, []string{"GET     /hi", "ALL     api.example.com/any", "2 routes"}},
		{[]string{"routes", "-json"}, []string{`"path": "/hi"`, `"host": "api.example.com"`}},
		{[]string{"graph"}, []string{"fx (level 0, root)", "controller  main.ctrl"}},
		{[]string{"graph", "-format", "dot"}, []string{"digraph dygo {"}},
		{[]string{"graph", "-format", "json"}, []string{`"modules": [`}},
		{[]string{"check"}, []string{"ok: 1 modules, 0 providers, 1 controllers"}},
		{[]string{"info"}, []string{"dygo CLI", "dygo framework  v0.0.0 => ", "Controllers     1"}},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if err := run(tt.args, &stdout, &stderr); err != nil {
				t.Fatalf("%v\n%s", err, stderr.String())
			}
			for _, w := range tt.want {
				if !strings.Contains(stdout.String(), w) {
					t.Errorf("missing %q in\n%s", w, stdout.String())
				}
			}
		})
	}
	var stdout bytes.Buffer
	if err := run([]string{"routes", "-json"}, &stdout, &stdout); err != nil {
		t.Fatal(err)
	}
	var routes []routeSummary
	if err := json.Unmarshal(stdout.Bytes(), &routes); err != nil || len(routes) != 2 {
		t.Fatalf("routes -json: %v %s", err, stdout.String())
	}
}

func TestInspectWiringErrors(t *testing.T) {
	fixture(t, brokenMain)
	for _, cmd := range []string{"routes", "graph", "check"} {
		t.Run(cmd, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := run([]string{cmd}, &stdout, &stderr)
			if code, ok := err.(exitError); !ok || code != 1 || !strings.Contains(stderr.String(), "1 wiring problem(s)") {
				t.Fatalf("err %v\n%s", err, stderr.String())
			}
		})
	}
	var stdout bytes.Buffer
	if err := run([]string{"info"}, &stdout, &stdout); err != nil || !strings.Contains(stdout.String(), "1 wiring problem(s), run dygo check") {
		t.Fatalf("info: %v\n%s", err, stdout.String())
	}
}

func TestInspectWithoutApp(t *testing.T) {
	tests := []struct {
		name, main, want string
	}{
		{"no dygo.Create", "package main\n\nfunc main() {}\n", "exited without calling dygo.Create"},
		{"does not compile", "package main\n\nfunc main() { x }\n", "failed before dygo.Create"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture(t, tt.main)
			var stdout bytes.Buffer
			if err := run([]string{"check"}, &stdout, &stdout); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want %q, got %v", tt.want, err)
			}
			stdout.Reset()
			if err := run([]string{"info"}, &stdout, &stdout); err != nil || !strings.Contains(stdout.String(), "not inspected: ") {
				t.Fatalf("info: %v\n%s", err, stdout.String())
			}
		})
	}
}

func TestStart(t *testing.T) {
	fixture(t, fixtureMain)
	var stdout, stderr bytes.Buffer
	err := run([]string{"start", "--", "a", "b"}, &stdout, &stderr)
	if code, ok := err.(exitError); !ok || code != 3 || !strings.Contains(stdout.String(), "args [a b]") {
		t.Fatalf("err %v\nstdout %s\nstderr %s", err, stdout.String(), stderr.String())
	}
	write(t, "main.go", "package main\n\nfunc main() { x }\n")
	if err := run([]string{"start"}, &stdout, &stderr); err != exitError(1) {
		t.Fatalf("build failure: %v", err)
	}
	if err := run([]string{"start", "a", "b"}, &stdout, &stderr); err == nil {
		t.Fatal("two packages accepted")
	}
}

// syncBuffer is a bytes.Buffer safe for the app and the test to share.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// waitFor waits until buf contains want n times.
func waitFor(t *testing.T, buf *syncBuffer, want string, n int) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for strings.Count(buf.String(), want) < n {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d x %q in\n%s", n, want, buf.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestWatch(t *testing.T) {
	dir := fixture(t, fixtureMain)
	t.Setenv("FX_WAIT", "1")
	var out syncBuffer
	r := &runner{pkg: ".", stdout: &out, stderr: &out, bin: filepath.Join(t.TempDir(), "app")}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.watch(ctx, dir, 20*time.Millisecond) }()

	waitFor(t, &out, "ready MARK", 1)
	// A change rebuilds and replaces the running app.
	write(t, "main.go", strings.Replace(fixtureMain, "ready MARK", "ready MARK again", 1))
	waitFor(t, &out, "change detected, rebuilding", 1)
	waitFor(t, &out, "ready MARK again", 1)
	// A broken change keeps the last good app running.
	write(t, "main.go", "package main\n\nfunc main() { x }\n")
	waitFor(t, &out, "build failed; waiting for changes", 1)
	// Tests and hidden files are not watched.
	write(t, "main_test.go", "package main\n")
	if err := os.Mkdir(".hidden", 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(".hidden", "x.go"), "package x\n")
	time.Sleep(100 * time.Millisecond)
	if n := strings.Count(out.String(), "change detected"); n != 2 {
		t.Fatalf("%d rebuilds, want 2:\n%s", n, out.String())
	}
	// An app that exits on its own is reported.
	t.Setenv("FX_WAIT", "")
	write(t, "main.go", fixtureMain)
	waitFor(t, &out, "app exited (exit status 3); waiting for changes", 1)

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
