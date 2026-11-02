package testgraph

// THIS CODE WILL BE UPDATED WITH SCHEMA CHANGES. PREVIOUS IMPLEMENTATION FOR SCHEMA CHANGES WILL BE KEPT IN THE COMMENT SECTION. IMPLEMENTATION FOR UNCHANGED SCHEMA WILL BE KEPT.

import (
	"context"
	"errors"

	"github.com/dygoframework/dygo/errs"
)

// Resolver is the root resolver of the test schema.
type Resolver struct {
	Users map[string]*User
	// Deleted records the ids given to deleteUser.
	Deleted []string
}

// DeleteUser is the resolver for the deleteUser field.
func (r *mutationResolver) DeleteUser(ctx context.Context, id string) (bool, error) {
	r.Deleted = append(r.Deleted, id)
	return true, nil
}

// User is the resolver for the user field.
func (r *queryResolver) User(ctx context.Context, id string) (*User, error) {
	switch id {
	case "panic":
		panic("boom")
	case "broken":
		return nil, errors.New("database password is hunter2")
	}
	u, ok := r.Users[id]
	if !ok {
		return nil, errs.NotFound("user not found")
	}
	return u, nil
}

// Me is the resolver for the me field.
func (r *queryResolver) Me(ctx context.Context) (*User, error) {
	return r.Users["1"], nil
}

// Mutation returns MutationResolver implementation.
func (r *Resolver) Mutation() MutationResolver { return &mutationResolver{r} }

// Query returns QueryResolver implementation.
func (r *Resolver) Query() QueryResolver { return &queryResolver{r} }

type mutationResolver struct{ *Resolver }
type queryResolver struct{ *Resolver }
