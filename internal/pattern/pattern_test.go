package pattern

import (
	"strings"
	"testing"
)

func TestJoin(t *testing.T) {
	cases := []struct {
		parts []string
		want  string
	}{
		{[]string{"/api", "/users", "/"}, "/api/users"},
		{[]string{"api/", "users", ""}, "/api/users"},
		{[]string{"/users", "/{id}"}, "/users/{id}"},
		{[]string{"", "/"}, "/{$}"},
		{[]string{"/api", "/users/"}, "/api/users/{$}"},
		{[]string{"/files", "/{path...}"}, "/files/{path...}"},
		{[]string{"//a//", "b"}, "/a/b"},
	}
	for _, c := range cases {
		if got := Join(c.parts...); got != c.want {
			t.Errorf("Join(%q) = %q, want %q", c.parts, got, c.want)
		}
	}
}

func TestValidate(t *testing.T) {
	ok := []string{"/users/{id}", "/f/{path...}", "/{$}", "/a/{x}/b/{y}", "/users/{id_2}"}
	for _, p := range ok {
		if err := Validate(p); err != nil {
			t.Errorf("Validate(%q): %v", p, err)
		}
	}
	bad := map[string]string{
		"/users/:id":  "write {id} instead",
		"/files/*":    "{name...}",
		"/a/{p...}/b": "must be the last",
		"/a/{x}/{x}":  "appears twice",
		"/a/x{y}":     "whole {name}",
		"/a/{1x}":     "not a valid",
		"users":       "must start with /",
		"/a/{$}/b":    "must be last",
	}
	for p, want := range bad {
		err := Validate(p)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Validate(%q) = %v, want error containing %q", p, err, want)
		}
	}
}

func TestWildcardsAndNormalize(t *testing.T) {
	if got := strings.Join(Wildcards("/a/{id}/b/{rest...}/{$}"), ","); got != "id,rest" {
		t.Fatal(got)
	}
	if Normalize("/users/{id}") != Normalize("/users/{name}") || Normalize("/f/{p...}") != "/f/{...}" {
		t.Fatal("normalize")
	}
}
