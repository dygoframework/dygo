package security

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/sql"
)

// User is a registered user, stored in the users table. The password hash
// never leaves the server.
type User struct {
	ID           int      `db:"id,pk,auto" json:"id"`
	Email        string   `db:"email" json:"email"`
	Roles        []string `db:"-" json:"roles"`
	RoleList     string   `db:"roles" json:"-"` // Roles, comma separated, as stored
	PasswordHash string   `db:"password_hash" json:"-"`
}

// UserStore loads and saves users.
type UserStore interface {
	// Create stores u with a new ID. It reports false when the email is taken.
	Create(ctx context.Context, u User) (User, bool, error)
	// ByEmail finds a user by email, in any case.
	ByEmail(ctx context.Context, email string) (User, bool, error)
	// ByID finds a user by ID.
	ByID(ctx context.Context, id int) (User, bool, error)
	// SetPasswordHash replaces a user's password hash.
	SetPasswordHash(ctx context.Context, id int, hash string) error
}

// NewUsers keeps users where DATABASE_URL in .env says: in memory for
// memory://, else in the users table (create it with dygo g migration and
// dygo db migrate).
func NewUsers(db dygo.Optional[*sql.DB], log dygo.Optional[*slog.Logger]) (UserStore, error) {
	l := log.OrElse(slog.Default())
	if d, ok := db.Get(); ok && !d.IsMemory() {
		repo, err := sql.NewRepository[User](d, "users")
		if err != nil {
			return nil, err
		}
		return &SQLUsers{repo: repo, log: l}, nil
	}
	return &MemoryUsers{byID: map[int]User{}, byEmail: map[string]int{}, log: l}, nil
}

// SQLUsers keeps users in the database. Emails are stored in lower case;
// add a unique index on users.email in the migration.
type SQLUsers struct {
	repo *sql.Repository[User]
	log  *slog.Logger
}

// OnApplicationBootstrap says at startup where the users are kept.
func (s *SQLUsers) OnApplicationBootstrap(ctx context.Context) error {
	s.log.InfoContext(ctx, "users stored in the database", "table", "users")
	return nil
}

// Create implements UserStore.
func (s *SQLUsers) Create(ctx context.Context, u User) (User, bool, error) {
	if _, taken, err := s.ByEmail(ctx, u.Email); err != nil || taken {
		return User{}, false, err
	}
	u.Email, u.RoleList = strings.ToLower(u.Email), strings.Join(u.Roles, ",")
	if err := s.repo.Create(ctx, &u); err != nil {
		if errors.Is(err, sql.ErrDuplicate) {
			return User{}, false, nil
		}
		return User{}, false, err
	}
	return u, true, nil
}

// ByEmail implements UserStore.
func (s *SQLUsers) ByEmail(ctx context.Context, email string) (User, bool, error) {
	return found(s.repo.FindBy(ctx, sql.Where("email = ?", strings.ToLower(email))))
}

// ByID implements UserStore.
func (s *SQLUsers) ByID(ctx context.Context, id int) (User, bool, error) {
	return found(s.repo.Find(ctx, id))
}

// SetPasswordHash implements UserStore.
func (s *SQLUsers) SetPasswordHash(ctx context.Context, id int, hash string) error {
	return s.repo.Patch(ctx, id, sql.Set{"password_hash": hash})
}

func found(u User, err error) (User, bool, error) {
	switch {
	case errors.Is(err, sql.ErrNotFound):
		return User{}, false, nil
	case err != nil:
		return User{}, false, err
	}
	if u.RoleList != "" {
		u.Roles = strings.Split(u.RoleList, ",")
	}
	return u, true, nil
}

// MemoryUsers keeps users in memory, for DATABASE_URL=memory:// and tests:
// accounts are lost when the app stops.
type MemoryUsers struct {
	mu      sync.RWMutex
	byID    map[int]User
	byEmail map[string]int // lower case email -> ID
	log     *slog.Logger
}

// OnApplicationBootstrap says at startup where the users are kept.
func (s *MemoryUsers) OnApplicationBootstrap(ctx context.Context) error {
	s.log.InfoContext(ctx, "users stored in memory", "service", "MemoryUsers")
	return nil
}

// Create implements UserStore.
func (s *MemoryUsers) Create(_ context.Context, u User) (User, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strings.ToLower(u.Email)
	if _, taken := s.byEmail[key]; taken {
		return User{}, false, nil
	}
	u.ID = len(s.byID) + 1
	s.byID[u.ID], s.byEmail[key] = u, u.ID
	return u, true, nil
}

// ByEmail implements UserStore.
func (s *MemoryUsers) ByEmail(_ context.Context, email string) (User, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.byEmail[strings.ToLower(email)]
	return s.byID[id], ok, nil
}

// ByID implements UserStore.
func (s *MemoryUsers) ByID(_ context.Context, id int) (User, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.byID[id]
	return u, ok, nil
}

// SetPasswordHash implements UserStore.
func (s *MemoryUsers) SetPasswordHash(_ context.Context, id int, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u, ok := s.byID[id]; ok {
		u.PasswordHash = hash
		s.byID[id] = u
	}
	return nil
}
