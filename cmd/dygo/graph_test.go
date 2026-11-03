package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dygoframework/dygo"
)

type gStore struct{}
type gMailer struct{}
type gService struct{}
type gController struct{}

type gParams struct {
	dygo.In
	Store   *gStore
	Mailers []*gMailer `group:"mailers"`
}

func (gController) Routes(dygo.Router) {}

// testGraph describes a small app: a global module, a group, an optional
// dependency and a request scoped provider.
func testGraph(t *testing.T) *dygo.GraphInfo {
	t.Helper()
	shared := &dygo.Module{
		Name:      "shared",
		Global:    true,
		Providers: []dygo.Provider{dygo.Provide(func() *gStore { return &gStore{} })},
		Exports:   []dygo.Token{dygo.TypeOf[*gStore]()},
	}
	users := &dygo.Module{
		Name: "users",
		Providers: []dygo.Provider{
			dygo.Provide(func() *gMailer { return &gMailer{} }, dygo.Group("mailers")),
			dygo.Provide(func(gParams, dygo.Optional[*gMailer]) *gService { return &gService{} },
				dygo.WithScope(dygo.RequestScoped)),
		},
		Controllers: []dygo.Provider{dygo.Provide(func(dygo.Scoped[*gService]) *gController { return &gController{} })},
	}
	g, err := dygo.Describe(&dygo.Module{Name: "app", Imports: []*dygo.Module{shared, users}})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestGraphFormats(t *testing.T) {
	g := testGraph(t)
	tests := []struct {
		name   string
		render func(*bytes.Buffer)
		want   []string
	}{
		{"text", func(b *bytes.Buffer) { graphText(b, g) }, []string{
			"app (level 2, root)", "shared (level 0, global)", "imports     shared, users",
			"exports     *main.gStore", "controller  *main.gController", "[request]",
			"<- *main.gStore", "<- optional *main.gMailer", `<- group "mailers"`, "<- scoped *main.gService",
		}},
		{"dot", func(b *bytes.Buffer) { graphDOT(b, g) }, []string{
			"digraph dygo {", `label="users";`, "style=rounded", `p2 -> p1 [style=dashed, label="group"];`,
		}},
		{"mermaid", func(b *bytes.Buffer) { graphMermaid(b, g) }, []string{"flowchart LR", `subgraph m2["users"]`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b bytes.Buffer
			tt.render(&b)
			for _, w := range tt.want {
				if !strings.Contains(b.String(), w) {
					t.Errorf("missing %q in\n%s", w, b.String())
				}
			}
		})
	}
}

func TestGraphArgs(t *testing.T) {
	var out bytes.Buffer
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"-format", "svg"}, `unknown format "svg"`},
		{[]string{"-nope"}, "flag provided but not defined"},
		{[]string{"a", "b"}, `unexpected argument "b"`},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			if err := cmdGraph(tt.args, &out, &out); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want %q, got %v", tt.want, err)
			}
		})
	}
}
