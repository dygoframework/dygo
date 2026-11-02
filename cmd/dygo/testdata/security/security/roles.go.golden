package security

import "slices"

// Hierarchy says which roles include others:
// an admin passes HasRole("user"). Edit it to match your app.
var Hierarchy = map[string][]string{
	"admin": {"user"},
}

// RoleAuthorities grants authorities through roles, so tokens stay small and
// the mapping lives in code. Edit it to match your app.
var RoleAuthorities = map[string][]string{
	// "admin": {"orders:write", "orders:cancel"},
}

// HasRole reports whether p has role, directly or through Hierarchy.
func (p Principal) HasRole(role string) bool {
	return slices.ContainsFunc(p.Roles, func(r string) bool { return includes(r, role, 0) })
}

// HasAuthority reports whether p has authority, directly or through a role
// in RoleAuthorities.
func (p Principal) HasAuthority(authority string) bool {
	if slices.Contains(p.Authorities, authority) {
		return true
	}
	for role, auths := range RoleAuthorities {
		if slices.Contains(auths, authority) && p.HasRole(role) {
			return true
		}
	}
	return false
}

// includes reports whether role have includes role want. depth stops a
// cycle in Hierarchy.
func includes(have, want string, depth int) bool {
	if have == want {
		return true
	}
	if depth > 10 {
		return false
	}
	return slices.ContainsFunc(Hierarchy[have], func(r string) bool { return includes(r, want, depth+1) })
}
