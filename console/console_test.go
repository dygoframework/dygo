package console

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dygoframework/dygo"
)

type greet struct{ got []string }

func (g *greet) Name() string        { return "greet" }
func (g *greet) Description() string { return "say hello" }
func (g *greet) Run(_ context.Context, args []string) error {
	g.got = args
	return nil
}

func TestRun(t *testing.T) {
	g := &greet{}
	root := &dygo.Module{Name: "app", Providers: []dygo.Provider{dygo.Value(g)}}
	var out bytes.Buffer
	if err := run(context.Background(), root, []string{"greet", "a", "b"}, &out, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Join(g.got, ",") != "a,b" {
		t.Fatalf("args %q", g.got)
	}
	if err := run(context.Background(), root, []string{"help"}, &out, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "greet  say hello") {
		t.Fatalf("help:\n%s", out.String())
	}
	if err := run(context.Background(), root, []string{"nope"}, &out, nil); !errors.Is(err, ErrUnknown) {
		t.Fatalf("err = %v", err)
	}
}

type bye struct{}

func (bye) Name() string                        { return "bye" }
func (bye) Run(context.Context, []string) error { return errors.New("bye failed") }

func TestUsage(t *testing.T) {
	tests := []struct {
		name      string
		providers []dygo.Provider
		want      string
	}{
		{"no commands", nil, "No commands."},
		{"sorted, description optional", []dygo.Provider{dygo.Value(bye{}), dygo.Value(&greet{})}, "  bye    \n  greet  say hello"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			root := &dygo.Module{Name: "app", Providers: tt.providers}
			if err := run(context.Background(), root, nil, &out, nil); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), tt.want) {
				t.Fatalf("help:\n%q", out.String())
			}
		})
	}
}

func TestMainExitCode(t *testing.T) {
	ok := &dygo.Module{Name: "app", Providers: []dygo.Provider{dygo.Value(&greet{}), dygo.Value(bye{})}}
	broken := &dygo.Module{Name: "app", Providers: []dygo.Provider{dygo.Provide(func(*bytes.Buffer) int { return 1 })}}
	tests := []struct {
		name string
		root *dygo.Module
		args []string
		want int
	}{
		{"success", ok, []string{"greet"}, 0},
		{"command error", ok, []string{"bye"}, 1},
		{"boot error", broken, []string{"greet"}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Main(context.Background(), tt.root, tt.args); got != tt.want {
				t.Fatalf("Main = %d, want %d", got, tt.want)
			}
		})
	}
}
