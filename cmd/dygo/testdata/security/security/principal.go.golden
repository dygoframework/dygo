package security

import "context"

// Principal is the signed in user of a request. The guard puts it on the
// request context.
type Principal struct {
	Subject     string   `json:"sub"`         // the user ID
	Email       string   `json:"email"`       //
	Roles       []string `json:"roles"`       // as stored; HasRole also follows Hierarchy
	Authorities []string `json:"authorities"` // permissions such as "orders:write"
}

type principalKey struct{}

// CurrentUser returns the signed in user, like SecurityContextHolder. It
// reports false on a public route called without a token.
func CurrentUser(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// WithPrincipal returns ctx with p as the signed in user. The guard calls it;
// use it yourself to run code as a user, for example in a test or a job.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}
