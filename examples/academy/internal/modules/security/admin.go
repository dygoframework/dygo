package security

import "context"

// AdminSeed creates the configured admin when the app starts, so someone can
// publish the first course. It is not part of what dygo g security writes:
// it was added to this app, as you would add your own code.
type AdminSeed struct {
	cfg   *Config
	users UserStore
}

// NewAdminSeed is the constructor.
func NewAdminSeed(cfg *Config, users UserStore) *AdminSeed {
	return &AdminSeed{cfg: cfg, users: users}
}

// OnApplicationBootstrap adds the admin once; an existing email is left alone.
func (s *AdminSeed) OnApplicationBootstrap(ctx context.Context) error {
	if s.cfg.AdminEmail == "" || s.cfg.AdminPassword == "" {
		return nil
	}
	hash, err := HashPassword(s.cfg.AdminPassword)
	if err != nil {
		return err
	}
	_, _, err = s.users.Create(ctx, User{Email: s.cfg.AdminEmail, Roles: []string{"admin"}, PasswordHash: hash})
	return err
}
