// Package config is a dynamic, global module: ForRoot returns a new module
// configured for the caller.
package config

import (
	"os"
	"time"

	"github.com/dygoframework/dygo"
)

// Config is the typed application configuration.
type Config struct {
	StoreDSN string
	Interval time.Duration
}

// Options configure ForRoot.
type Options struct {
	Defaults Config
}

// ForRoot loads configuration from the environment over the given defaults.
func ForRoot(opts Options) *dygo.Module {
	return &dygo.Module{
		Name:   "config",
		Global: true,
		Providers: []dygo.Provider{
			dygo.Value(opts),
			dygo.Provide(load),
		},
		Exports: []dygo.Token{dygo.TypeOf[*Config]()},
	}
}

func load(opts Options) (*Config, error) {
	cfg := opts.Defaults
	if v := os.Getenv("STORE_DSN"); v != "" {
		cfg.StoreDSN = v
	}
	if v := os.Getenv("REPORT_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, err
		}
		cfg.Interval = d
	}
	return &cfg, nil
}
