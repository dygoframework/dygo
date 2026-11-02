// Package security secures the API: every route needs a
// valid bearer token unless Rules (rules.go) or Public() opens it.
//
//	POST /auth/register, POST /auth/login, POST /auth/refresh, POST /auth/logout, GET /auth/me
//	r.Delete("/{id}", dygo.Handle(c.Delete), security.Roles("admin"))
//	err := security.Require(ctx, security.HasAuthority("orders:cancel"))
//	p, _ := security.CurrentUser(ctx)
//
// Users and refresh tokens are kept where DATABASE_URL in .env says: in
// memory, or in the users, refresh_tokens and refresh_families tables (dygo g
// migration writes them). Set AUTH_SECRET in .env (it is required when
// APP_ENV=production).
package security

import (
	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/config"
)

// Module is global, so every module can use Public, Roles, Require and
// CurrentUser, and inject *Service. Its guard protects every route.
var Module = &dygo.Module{
	Name:    "security",
	Global:  true,
	Imports: []*dygo.Module{config.ForFeature[Config](config.Files(".env.local", ".env"))},
	Providers: []dygo.Provider{
		dygo.Provide(NewUsers),
		dygo.Provide(NewTokens),
		dygo.Provide(NewRefreshTokens),
		dygo.Provide(NewService),
		dygo.Provide(NewGuard, dygo.Group(dygo.AppGuards)),
		dygo.Provide(NewAdminSeed),
	},
	Controllers: []dygo.Provider{dygo.Provide(NewController)},
	Exports:     []dygo.Token{dygo.TypeOf[*Service](), dygo.TypeOf[*Tokens]()},
}
