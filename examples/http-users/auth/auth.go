// Package auth is the example's authentication: a global guard that reads a
// bearer token, plus Roles and Public metadata.
package auth

import (
	"context"
	"slices"
	"strings"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/errs"
	"github.com/dygoframework/dygo/meta"
	"github.com/dygoframework/dygo/openapi"
)

// User is the authenticated caller.
type User struct {
	Name  string
	Roles []string
}

// Tokens maps bearer tokens to users. A real app would verify a JWT here.
type Tokens struct{ users map[string]User }

// NewTokens returns the demo tokens.
func NewTokens() *Tokens {
	return &Tokens{users: map[string]User{
		"admin-token": {Name: "ada", Roles: []string{"admin", "user"}},
		"user-token":  {Name: "bob", Roles: []string{"user"}},
	}}
}

// Lookup returns the user for a token.
func (t *Tokens) Lookup(token string) (User, bool) {
	u, ok := t.users[token]
	return u, ok
}

var (
	rolesKey  = meta.NewKey[[]string]("roles")
	publicKey = meta.NewKey[bool]("public")
)

// Roles requires the caller to have one of roles. On Router.Use it applies to
// every route of the controller; route roles add to the controller's.
func Roles(roles ...string) dygo.RouteOption { return rolesKey.Set(roles) }

// Public lets requests through without a token, and marks the route as
// needing no auth in the OpenAPI document.
func Public() dygo.RouteOption { return dygo.Options(publicKey.Set(true), openapi.Public()) }

// Guard authenticates every request. It is registered in the dygo.AppGuards
// group, so it is global and gets its dependencies injected.
type Guard struct{ tokens *Tokens }

// NewGuard is the constructor.
func NewGuard(t *Tokens) *Guard { return &Guard{tokens: t} }

type userKey struct{}

// CanActivate answers 401 without a valid token and 403 without a required role.
func (g *Guard) CanActivate(ec dygo.ExecutionContext) (bool, error) {
	if public, _ := publicKey.Get(ec); public {
		return true, nil
	}
	token, ok := strings.CutPrefix(ec.HTTP().Request().Header.Get("Authorization"), "Bearer ")
	if !ok {
		return false, errs.Unauthorized("missing bearer token")
	}
	u, ok := g.tokens.Lookup(token)
	if !ok {
		return false, errs.Unauthorized("invalid token")
	}
	ec.SetContext(context.WithValue(ec.Context(), userKey{}, u))
	needsRole := false
	for _, r := range meta.GetAllAndMerge(ec, rolesKey) { // joined at startup, no allocation
		if slices.Contains(u.Roles, r) {
			return true, nil
		}
		needsRole = true
	}
	return !needsRole, nil
}

// UserFrom returns the authenticated user.
func UserFrom(ctx context.Context) (User, bool) {
	u, ok := ctx.Value(userKey{}).(User)
	return u, ok
}

// Module provides the tokens and the global guard.
var Module = &dygo.Module{
	Name: "auth",
	Providers: []dygo.Provider{
		dygo.Provide(NewTokens),
		dygo.Provide(NewGuard, dygo.Group(dygo.AppGuards)),
	},
}
