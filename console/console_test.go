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
