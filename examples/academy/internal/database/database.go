// Package database owns the database connection. Module is global, so every
// feature can inject *database.DB.
//
// DB is an in-memory placeholder with the same shape as *sql.DB (open in a
// constructor, PingContext for health checks, Close on shutdown). To use a
// real database, return *sql.DB from Open instead:
//
//	db, err := sql.Open("pgx", cfg.DatabaseURL)
package database

import (
	"context"
	"log/slog"
	"sync"

	"github.com/dygoframework/dygo"

	"github.com/dygoframework/dygo/examples/academy/internal/config"
)

// Module provides *DB to the whole application.
var Module = &dygo.Module{
	Name:      "database",
	Global:    true,
	Providers: []dygo.Provider{dygo.Provide(Open)},
	Exports:   []dygo.Token{dygo.TypeOf[*DB]()},
}

// DB is the connection handle.
type DB struct {
	log  *slog.Logger
	mu   sync.RWMutex
	data map[string]any
}

// Open connects using the configured URL. A returned error stops the boot.
func Open(cfg *config.Config, log *slog.Logger) (*DB, error) {
	log.Info("database: connected", "url", cfg.DatabaseURL)
	return &DB{log: log, data: map[string]any{}}, nil
}

// PingContext reports whether the database answers. The health module uses it.
func (db *DB) PingContext(context.Context) error { return nil }

// Get reads a value.
func (db *DB) Get(key string) (any, bool) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	v, ok := db.data[key]
	return v, ok
}

// Set writes a value.
func (db *DB) Set(key string, v any) {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.data[key] = v
}

// OnApplicationShutdown closes the connection after in-flight requests finish.
func (db *DB) OnApplicationShutdown(context.Context, string) error {
	db.log.Info("database: closed")
	return nil
}
