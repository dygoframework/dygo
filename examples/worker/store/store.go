// Package store is a fake database with connection lifecycle hooks.
package store

import (
	"context"
	"log/slog"
	"sync"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/examples/worker/config"
)

// Module exports the store to modules that import it.
var Module = &dygo.Module{
	Name:      "store",
	Providers: []dygo.Provider{dygo.Provide(New)},
	Exports:   []dygo.Token{dygo.TypeOf[*Store]()},
}

// Store is an in-memory key value store standing in for a database.
type Store struct {
	dsn  string
	log  *slog.Logger
	mu   sync.Mutex
	data map[string]string
}

// New is the constructor. *config.Config comes from the global config module
// and *slog.Logger from the app module.
func New(cfg *config.Config, log *slog.Logger) *Store {
	return &Store{dsn: cfg.StoreDSN, log: log}
}

// OnModuleInit opens the "connection".
func (s *Store) OnModuleInit(context.Context) error {
	s.data = map[string]string{}
	s.log.Info("store connected", "dsn", s.dsn)
	return nil
}

// OnApplicationShutdown closes it, after every module has stopped using it.
func (s *Store) OnApplicationShutdown(_ context.Context, signal string) error {
	s.log.Info("store closed", "signal", signal)
	return nil
}

// Put stores a value.
func (s *Store) Put(k, v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[k] = v
}

// Len returns the number of keys.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.data)
}
