package security

import (
	"context"
	"fmt"
	"slices"

	"github.com/dygoframework/dygo/errs"
)

// Check is an access rule on the signed in user. Rules (rules.go) and
// Require use them.
type Check func(p Principal) bool

// Authenticated lets any signed in user through.
func Authenticated() Check { return func(Principal) bool { return true } }

// HasRole needs role (or a role that includes it in Hierarchy).
func HasRole(role string) Check { return func(p Principal) bool { return p.HasRole(role) } }

// HasAnyRole needs one of roles.
func HasAnyRole(roles ...string) Check {
	return func(p Principal) bool { return slices.ContainsFunc(roles, p.HasRole) }
}

// HasAuthority needs authority, such as "orders:write".
func HasAuthority(authority string) Check {
	return func(p Principal) bool { return p.HasAuthority(authority) }
}

// HasAnyAuthority needs one of authorities.
func HasAnyAuthority(authorities ...string) Check {
	return func(p Principal) bool { return slices.ContainsFunc(authorities, p.HasAuthority) }
}

// OwnerBypassRole may pass RequireOwner for any owner. Set it to "" to make
// RequireOwner strict.
var OwnerBypassRole = "admin"

// Require checks the signed in user inside a service method, like
// @PreAuthorize. It returns a 401 without a user and a 403 when check fails,
// so the caller just returns the error:
//
//	if err := security.Require(ctx, security.HasAuthority("orders:cancel")); err != nil {
//		return err
//	}
func Require(ctx context.Context, check Check) error {
	p, ok := CurrentUser(ctx)
	if !ok {
		return errUnauthenticated
	}
	if !check(p) {
		return errDenied
	}
	return nil
}

// RequireOwner checks that the signed in user owns a record, like
// @PostAuthorize("returnObject.owner == authentication.name"). ownerID is
// the record's owner, compared with Principal.Subject. Users with
// OwnerBypassRole pass for any record.
func RequireOwner[ID comparable](ctx context.Context, ownerID ID) error {
	p, ok := CurrentUser(ctx)
	if !ok {
		return errUnauthenticated
	}
	if fmt.Sprint(ownerID) == p.Subject || (OwnerBypassRole != "" && p.HasRole(OwnerBypassRole)) {
		return nil
	}
	return errDenied
}

var (
	errUnauthenticated = errs.Unauthorized("missing bearer token")
	errDenied          = errs.Forbidden("access denied")
)
