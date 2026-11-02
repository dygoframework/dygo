// Package config is the typed configuration, read from the environment and
// .env files. Every module can inject *config.Config.
package config

import (
	"github.com/dygoframework/dygo"
	dygoconfig "github.com/dygoframework/dygo/config"
)

// Config holds every setting. Add fields with env, default and required tags.
type Config struct {
	Port        int    `env:"PORT" default:"9900"`
	DatabaseURL string `env:"DATABASE_URL" default:"memory://"`
}

// Load reads the configuration from .env.local, .env and the environment
// (real variables win). Tests pass the variables to use instead.
func Load(env ...map[string]string) (*Config, error) {
	opts := []dygoconfig.Option{dygoconfig.Files(".env.local", ".env")}
	if len(env) > 0 {
		opts = []dygoconfig.Option{dygoconfig.Env(env[0])}
	}
	return dygoconfig.Load[Config](opts...)
}

// Module provides cfg to every module (it is global).
func Module(cfg *Config) *dygo.Module {
	return &dygo.Module{
		Name:      "config",
		Global:    true,
		Providers: []dygo.Provider{dygo.Value(cfg)},
		Exports:   []dygo.Token{dygo.TypeOf[*Config]()},
	}
}
