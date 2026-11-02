package meta_test

import (
	"context"
	"testing"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/dygotest"
	"github.com/dygoframework/dygo/meta"
)

// fakeEC answers every metadata lookup with fixed values, whatever the key.
type fakeEC struct {
	dygo.ExecutionContext
	route, ctrl, merged any
}

func lookup(v any) (any, bool) { return v, v != nil }

func (f fakeEC) RouteMetadata(any) (any, bool)      { return lookup(f.route) }
func (f fakeEC) ControllerMetadata(any) (any, bool) { return lookup(f.ctrl) }
func (f fakeEC) MergedMetadata(any) (any, bool)     { return lookup(f.merged) }

func TestGetPrefersRoute(t *testing.T) {
	k := meta.NewKey[string]("role")
	if k.String() != "role" {
		t.Fatalf("String() = %q", k.String())
	}
	cases := []struct {
		ec   fakeEC
		want string
		ok   bool
	}{
		{fakeEC{route: "r", ctrl: "c"}, "r", true},
		{fakeEC{ctrl: "c"}, "c", true},
		{fakeEC{}, "", false},
		{fakeEC{route: 3}, "", false}, // wrong type reads as missing
	}
	for _, c := range cases {
		got, ok := meta.Get(c.ec, k)
		if got != c.want || ok != c.ok {
			t.Errorf("%+v: got %q %v", c.ec, got, ok)
		}
		if got2, ok2 := meta.GetAllAndOverride(c.ec, k); got2 != got || ok2 != ok {
			t.Errorf("GetAllAndOverride differs from Get")
		}
	}
	if v, ok := k.Controller(fakeEC{route: "r"}); ok || v != "" {
		t.Error("Controller read the route value")
	}
	if v, ok := k.Route(fakeEC{ctrl: "c"}); ok || v != "" {
		t.Error("Route read the controller value")
	}
}

func TestGetAllAndMerge(t *testing.T) {
	k := meta.NewKey[[]string]("roles")
	if got := meta.GetAllAndMerge(fakeEC{merged: []string{"a", "b"}}, k); len(got) != 2 {
		t.Fatalf("got %v", got)
	}
	if got := meta.GetAllAndMerge(fakeEC{merged: []int{1}}, k); got != nil {
		t.Fatalf("wrong type should be nil, got %v", got)
	}
}

type routes func(dygo.Router)

func (f routes) Routes(r dygo.Router) { f(r) }

// Two keys with the same name and type must not see each other's values.
func TestKeysAreDistinct(t *testing.T) {
	a, b := meta.NewKey[string]("tag"), meta.NewKey[string]("tag")
	var gotA, gotB string
	var okB bool
	ctrl := routes(func(r dygo.Router) {
		r.Get("/", dygo.HandleNoInput(func(context.Context) (string, error) { return "ok", nil }),
			meta.Set(a, "from a"),
			dygo.Guards(dygo.GuardFunc(func(ec dygo.ExecutionContext) (bool, error) {
				gotA, _ = a.Get(ec)
				gotB, okB = b.Get(ec)
				return true, nil
			})))
	})
	srv := dygotest.New(&dygo.Module{Name: "app", Controllers: []dygo.Provider{dygo.Value(ctrl)}}).CompileHTTP(t)
	srv.GET("/").Do(t).Status(t, 200)
	if gotA != "from a" || okB || gotB != "" {
		t.Fatalf("a=%q b=%q %v", gotA, gotB, okB)
	}
}
