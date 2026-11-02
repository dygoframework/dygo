package security

import (
	"net/http"
	"slices"
	"strings"
)

// Rule is one line of the rule list in rules.go: which routes it matches and
// who may call them.
type Rule struct {
	methods  []string
	patterns [][]string // split on "/"; nil means any route
	access   access
	check    Check
	desc     string
}

type access int

const (
	permitAll access = iota
	authenticated
	denyAll
)

// Path starts a rule for the routes matching one of patterns. A pattern is a
// route path where * matches one segment and ** any number of segments:
// "/admin/**", "/orders/*/items".
func Path(patterns ...string) Rule { return Rule{}.Path(patterns...) }

// Method starts a rule for the routes of the HTTP methods, such as "GET".
// Chain Path to narrow it.
func Method(methods ...string) Rule { return Rule{methods: methods} }

// AnyRequest matches every route. Put it last.
func AnyRequest() Rule { return Rule{} }

// Path narrows the rule to patterns.
func (r Rule) Path(patterns ...string) Rule {
	for _, p := range patterns {
		r.patterns = append(r.patterns, split(p))
	}
	return r
}

// PermitAll lets anyone call the routes, with or without a token.
func (r Rule) PermitAll() Rule { return r.with(permitAll, nil, "public") }

// Authenticated needs a signed in user.
func (r Rule) Authenticated() Rule { return r.with(authenticated, Authenticated(), "authenticated") }

// HasRole needs a signed in user with role (or a role that includes it).
func (r Rule) HasRole(role string) Rule { return r.with(authenticated, HasRole(role), "role:"+role) }

// HasAnyRole needs one of roles.
func (r Rule) HasAnyRole(roles ...string) Rule {
	return r.with(authenticated, HasAnyRole(roles...), "role:"+strings.Join(roles, "|"))
}

// HasAuthority needs authority, such as "orders:write".
func (r Rule) HasAuthority(authority string) Rule {
	return r.with(authenticated, HasAuthority(authority), "authority:"+authority)
}

// HasAnyAuthority needs one of authorities.
func (r Rule) HasAnyAuthority(authorities ...string) Rule {
	return r.with(authenticated, HasAnyAuthority(authorities...), "authority:"+strings.Join(authorities, "|"))
}

// Access needs a signed in user for whom check returns true.
func (r Rule) Access(check Check) Rule { return r.with(authenticated, check, "custom") }

// DenyAll refuses every call.
func (r Rule) DenyAll() Rule { return r.with(denyAll, nil, "deny") }

func (r Rule) with(a access, c Check, desc string) Rule {
	r.access, r.check, r.desc = a, c, desc
	return r
}

// String describes the rule for the startup log.
func (r Rule) String() string {
	var b strings.Builder
	if len(r.methods) > 0 {
		b.WriteString(strings.Join(r.methods, ",") + " ")
	}
	if r.patterns == nil {
		b.WriteString("any request")
	}
	for i, p := range r.patterns {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("/" + strings.Join(p, "/"))
	}
	return b.String() + " -> " + r.desc
}

// matches reports whether the rule covers the route method and path, a
// ServeMux pattern such as "/orders/{id}". Rules match the route pattern,
// not the raw URL, so "/admin/../x" or "%2F" tricks cannot get past them.
func (r Rule) matches(method, path string) bool {
	if len(r.methods) > 0 && method != "" && !slices.Contains(r.methods, method) {
		return false
	}
	if r.patterns == nil {
		return true
	}
	segs := split(path)
	return slices.ContainsFunc(r.patterns, func(p []string) bool { return match(p, segs) })
}

func match(pattern, segs []string) bool {
	if len(pattern) == 0 {
		return len(segs) == 0
	}
	if pattern[0] == "**" {
		for i := 0; i <= len(segs); i++ {
			if match(pattern[1:], segs[i:]) {
				return true
			}
		}
		return false
	}
	if len(segs) == 0 || (pattern[0] != "*" && pattern[0] != segs[0]) {
		return false
	}
	return match(pattern[1:], segs[1:])
}

// split turns "/orders/{id}/{$}" into ["orders", "{id}"].
func split(path string) []string {
	var out []string
	for _, s := range strings.Split(path, "/") {
		if s != "" && s != "{$}" {
			out = append(out, s)
		}
	}
	return out
}

// methodOf returns the method of a route, "" for one registered with All.
func methodOf(m string) string {
	if m == http.MethodHead {
		return http.MethodGet
	}
	return m
}
