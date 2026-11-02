// Package securitytest signs in as a user in tests, without a real login:
//
//	securitytest.Setup(t)
//	srv := dygotest.New(app).CompileHTTP(t)
//	srv.GET("/orders").With(securitytest.User("7", "user")).Do(t).Status(t, 200)
//	srv.DELETE("/orders/1").With(securitytest.Admin()).Do(t).Status(t, 204)
package securitytest

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/dygoframework/dygo/examples/academy/internal/modules/security"
)

// Secret signs the test tokens. Setup gives it to the app under test.
const Secret = "securitytest-secret-at-least-32-characters-long"

// Setup makes the app under test accept the tokens of User and Admin. Call
// it before building the app.
func Setup(t testing.TB) {
	t.Setenv("AUTH_SECRET", Secret)
	t.Setenv("AUTH_ISSUER", "dygo")
}

// User signs in as subject with roles. Pass it to dygotest's Req.With.
func User(subject string, roles ...string) func(*http.Request) {
	return As(security.Principal{Subject: subject, Email: subject + "@example.com", Roles: roles})
}

// Admin signs in as user "1" with the admin role.
func Admin() func(*http.Request) { return User("1", "admin") }

// As signs in as p.
func As(p security.Principal) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+Token(p)) }
}

// Token returns an access token for p, valid for an hour.
func Token(p security.Principal) string {
	tokens, err := security.NewTokens(&security.Config{Secret: Secret, TokenTTL: time.Hour, Issuer: "dygo"})
	if err != nil {
		panic(err)
	}
	token, _, err := tokens.Issue(p)
	if err != nil {
		panic(err)
	}
	return token
}

// Context returns ctx signed in as p, for testing services that call
// security.Require or security.CurrentUser.
func Context(ctx context.Context, p security.Principal) context.Context {
	return security.WithPrincipal(ctx, p)
}
