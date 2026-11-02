// Package users is a CRUD module: a repository, a service, and a controller
// with typed handlers, versioned routes, a request scoped value and an SSE
// stream.
package users

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/dygoframework/dygo"
)

// Module wires the users feature.
var Module = &dygo.Module{
	Name: "users",
	Providers: []dygo.Provider{
		dygo.Provide(NewMemoryRepo, dygo.As[Repo]()),
		dygo.Provide(NewService),
		dygo.Provide(NewRequestMeta, dygo.WithScope(dygo.RequestScoped)),
	},
	Controllers: []dygo.Provider{dygo.Provide(NewController)},
	Exports:     []dygo.Token{dygo.TypeOf[*Service]()},
}

// ID is a user ID. It implements encoding.TextUnmarshaler, so a path or query
// field of type ID is validated by the binder.
type ID string

// UnmarshalText accepts 16 lowercase hex characters.
func (id *ID) UnmarshalText(b []byte) error {
	if len(b) != 16 {
		return errors.New("must be 16 hex characters")
	}
	if _, err := hex.DecodeString(string(b)); err != nil {
		return errors.New("must be 16 hex characters")
	}
	*id = ID(b)
	return nil
}

func newID() ID {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return ID(hex.EncodeToString(b))
}

// User is the stored entity.
type User struct {
	ID        ID        `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

// Event is published on every change.
type Event struct {
	Type string `json:"type"`
	User User   `json:"user"`
}

// ErrNotFound is returned by the repository.
var ErrNotFound = errors.New("user not found")

// Repo stores users.
type Repo interface {
	List(ctx context.Context, offset, limit int) ([]User, int)
	Get(ctx context.Context, id ID) (User, error)
	Save(ctx context.Context, u User) error
	Delete(ctx context.Context, id ID) error
	Subscribe(ctx context.Context) <-chan Event
}

// MemoryRepo is an in-memory Repo.
type MemoryRepo struct {
	mu    sync.Mutex
	users map[ID]User
	subs  map[chan Event]struct{}
}

// NewMemoryRepo is the constructor.
func NewMemoryRepo() *MemoryRepo {
	return &MemoryRepo{users: map[ID]User{}, subs: map[chan Event]struct{}{}}
}

// List returns a page of users ordered by creation time, and the total.
func (r *MemoryRepo) List(_ context.Context, offset, limit int) ([]User, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	all := make([]User, 0, len(r.users))
	for _, u := range r.users {
		all = append(all, u)
	}
	slices.SortFunc(all, func(a, b User) int { return a.CreatedAt.Compare(b.CreatedAt) })
	total := len(all)
	if offset > total {
		offset = total
	}
	return all[offset:min(offset+limit, total)], total
}

// Get returns one user.
func (r *MemoryRepo) Get(_ context.Context, id ID) (User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.users[id]
	if !ok {
		return User{}, ErrNotFound
	}
	return u, nil
}

// Save creates or replaces a user and publishes an event.
func (r *MemoryRepo) Save(_ context.Context, u User) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, existed := r.users[u.ID]
	r.users[u.ID] = u
	typ := "created"
	if existed {
		typ = "updated"
	}
	r.publish(Event{Type: typ, User: u})
	return nil
}

// Delete removes a user.
func (r *MemoryRepo) Delete(_ context.Context, id ID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.users[id]
	if !ok {
		return ErrNotFound
	}
	delete(r.users, id)
	r.publish(Event{Type: "deleted", User: u})
	return nil
}

// Subscribe returns a channel of events that closes when ctx is done.
func (r *MemoryRepo) Subscribe(ctx context.Context) <-chan Event {
	ch := make(chan Event, 16)
	r.mu.Lock()
	r.subs[ch] = struct{}{}
	r.mu.Unlock()
	go func() {
		<-ctx.Done()
		r.mu.Lock()
		delete(r.subs, ch)
		close(ch)
		r.mu.Unlock()
	}()
	return ch
}

func (r *MemoryRepo) publish(e Event) {
	for ch := range r.subs {
		select {
		case ch <- e:
		default: // slow subscriber: drop rather than block writers
		}
	}
}

// Service holds the business rules.
type Service struct{ repo Repo }

// NewService is the constructor.
func NewService(repo Repo) *Service { return &Service{repo: repo} }

// ErrEmailTaken is returned when the email is already used.
var ErrEmailTaken = errors.New("email already registered")

// Create registers a user.
func (s *Service) Create(ctx context.Context, email, name string) (User, error) {
	all, _ := s.repo.List(ctx, 0, 1<<31-1)
	for _, u := range all {
		if u.Email == email {
			return User{}, ErrEmailTaken
		}
	}
	u := User{ID: newID(), Email: email, Name: name, CreatedAt: time.Now().UTC()}
	return u, s.repo.Save(ctx, u)
}

// Rename changes a user's name.
func (s *Service) Rename(ctx context.Context, id ID, name string) (User, error) {
	u, err := s.repo.Get(ctx, id)
	if err != nil {
		return User{}, err
	}
	u.Name = name
	return u, s.repo.Save(ctx, u)
}

// Get returns one user.
func (s *Service) Get(ctx context.Context, id ID) (User, error) { return s.repo.Get(ctx, id) }

// List returns a page of users and the total count.
func (s *Service) List(ctx context.Context, offset, limit int) ([]User, int) {
	return s.repo.List(ctx, offset, limit)
}

// Delete removes a user.
func (s *Service) Delete(ctx context.Context, id ID) error { return s.repo.Delete(ctx, id) }

// Subscribe streams changes until ctx is done.
func (s *Service) Subscribe(ctx context.Context) <-chan Event { return s.repo.Subscribe(ctx) }

// RequestMeta is request scoped: one per request, built from the request.
type RequestMeta struct {
	RequestID string
}

// NewRequestMeta reads X-Request-ID or creates one. *http.Request can be
// injected because the provider is request scoped.
func NewRequestMeta(r *http.Request) *RequestMeta {
	id := r.Header.Get("X-Request-ID")
	if id == "" {
		id = string(newID())
	}
	return &RequestMeta{RequestID: id}
}
