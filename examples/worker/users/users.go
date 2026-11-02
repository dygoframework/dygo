// Package users shows interface binding with dygo.As and a module that
// imports another module.
package users

import (
	"context"
	"fmt"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/examples/worker/report"
	"github.com/dygoframework/dygo/examples/worker/store"
)

// Module provides the user service and contributes a reporter to the
// application wide "reporters" group.
var Module = &dygo.Module{
	Name:    "users",
	Imports: []*dygo.Module{store.Module},
	Providers: []dygo.Provider{
		dygo.Provide(NewStoreRepo, dygo.As[Repo]()),
		dygo.Provide(NewService),
		dygo.Provide(NewReporter, dygo.Group(report.Group)),
	},
	Exports: []dygo.Token{dygo.TypeOf[*Service]()},
}

// Repo is what the service needs; tests can swap it.
type Repo interface {
	Save(ctx context.Context, id, name string) error
	Count(ctx context.Context) int
}

// StoreRepo implements Repo on the store.
type StoreRepo struct{ st *store.Store }

// NewStoreRepo is the constructor.
func NewStoreRepo(st *store.Store) *StoreRepo { return &StoreRepo{st: st} }

// Save stores a user.
func (r *StoreRepo) Save(_ context.Context, id, name string) error {
	r.st.Put("user:"+id, name)
	return nil
}

// Count returns how many keys are stored.
func (r *StoreRepo) Count(context.Context) int { return r.st.Len() }

// Service is the users business logic.
type Service struct{ repo Repo }

// NewService is the constructor.
func NewService(repo Repo) *Service { return &Service{repo: repo} }

// Register adds a user.
func (s *Service) Register(ctx context.Context, id, name string) error {
	if name == "" {
		return fmt.Errorf("user %s: name is required", id)
	}
	return s.repo.Save(ctx, id, name)
}

// OnApplicationBootstrap seeds demo data once everything is initialized.
func (s *Service) OnApplicationBootstrap(ctx context.Context) error {
	return s.Register(ctx, "1", "Dylan")
}

// Reporter reports user counts to the report module.
type Reporter struct{ repo Repo }

// NewReporter is the constructor.
func NewReporter(repo Repo) report.Reporter { return &Reporter{repo: repo} }

// Name implements report.Reporter.
func (r *Reporter) Name() string { return "users" }

// Report implements report.Reporter.
func (r *Reporter) Report(ctx context.Context) string {
	return fmt.Sprintf("%d users", r.repo.Count(ctx))
}
