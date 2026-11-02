// Package pattern joins and checks route paths written in Go's net/http
// pattern syntax ("/users/{id}", "/files/{path...}").
package pattern

import (
	"fmt"
	"strings"
)

// Join concatenates path parts with exactly one slash between them. Empty
// parts and "/" parts are skipped. A trailing slash on the last non-empty
// part is kept, and becomes "{$}" so that ServeMux matches it exactly
// instead of as a subtree. The root path "/" becomes "/{$}" for the same
// reason; use "/{path...}" for a catch-all.
func Join(parts ...string) string {
	var segs []string
	trailing := false
	for _, p := range parts {
		if strings.Trim(p, "/") == "" {
			continue // "" and "/" mean "the prefix itself"
		}
		trailing = strings.HasSuffix(p, "/")
		for _, s := range strings.Split(p, "/") {
			if s != "" {
				segs = append(segs, s)
			}
		}
	}
	if len(segs) == 0 {
		return "/{$}"
	}
	out := "/" + strings.Join(segs, "/")
	if trailing && !strings.HasSuffix(out, "...}") && !strings.HasSuffix(out, "{$}") {
		out += "/{$}"
	}
	return out
}

// Validate reports problems with a joined path: :name style segments, bad
// wildcard names, a "..." wildcard that is not last, and duplicate wildcard
// names.
func Validate(path string) error {
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("path %q must start with /", path)
	}
	seen := map[string]bool{}
	segs := strings.Split(path[1:], "/")
	for i, s := range segs {
		switch {
		case strings.HasPrefix(s, ":"):
			return fmt.Errorf("path %q: segment %q uses :name syntax; write {%s} instead", path, s, strings.TrimPrefix(s, ":"))
		case s == "*" || s == "**":
			return fmt.Errorf("path %q: use {name...} for a wildcard instead of %q", path, s)
		case strings.ContainsAny(s, "{}"):
			if !strings.HasPrefix(s, "{") || !strings.HasSuffix(s, "}") {
				return fmt.Errorf("path %q: segment %q must be a whole {name} wildcard", path, s)
			}
			name := s[1 : len(s)-1]
			if name == "$" {
				if i != len(segs)-1 {
					return fmt.Errorf("path %q: {$} must be last", path)
				}
				continue
			}
			if strings.HasSuffix(name, "...") {
				if i != len(segs)-1 {
					return fmt.Errorf("path %q: {%s} must be the last segment", path, name)
				}
				name = strings.TrimSuffix(name, "...")
			}
			if !isIdent(name) {
				return fmt.Errorf("path %q: %q is not a valid wildcard name", path, name)
			}
			if seen[name] {
				return fmt.Errorf("path %q: wildcard {%s} appears twice", path, name)
			}
			seen[name] = true
		}
	}
	return nil
}

// Wildcards returns the wildcard names in path, in order.
func Wildcards(path string) []string {
	var names []string
	for _, s := range strings.Split(path, "/") {
		if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") && s != "{$}" {
			names = append(names, strings.TrimSuffix(s[1:len(s)-1], "..."))
		}
	}
	return names
}

// Normalize replaces wildcard names so that two patterns matching the same
// requests compare equal: "/users/{id}" and "/users/{name}" both become
// "/users/{}".
func Normalize(path string) string {
	segs := strings.Split(path, "/")
	for i, s := range segs {
		if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") && s != "{$}" {
			if strings.HasSuffix(s, "...}") {
				segs[i] = "{...}"
			} else {
				segs[i] = "{}"
			}
		}
	}
	return strings.Join(segs, "/")
}

func isIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (i > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}
