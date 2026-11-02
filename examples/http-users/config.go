package main

import "time"

// Config is the app's typed configuration, read from the environment and .env
// by package config.
type Config struct {
	Port        int           `env:"PORT" default:"9900"`
	CORSOrigins []string      `env:"CORS_ORIGINS" default:"http://localhost:5173"`
	RateLimit   int           `env:"RATE_LIMIT" default:"100"`
	RateWindow  time.Duration `env:"RATE_WINDOW" default:"1m"`
	// TrustProxy reads client IPs from X-Forwarded-For, for rate limiting.
	TrustProxy bool `env:"TRUST_PROXY"`
}
