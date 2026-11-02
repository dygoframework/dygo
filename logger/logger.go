// Package logger builds the application's *slog.Logger and provides it
// through DI:
//
//	log := logger.New(logger.Options{Format: logger.JSON, Level: slog.LevelInfo})
//	app, err := dygo.Create(ctx, AppModule, dygo.WithLogger(log))
//	// and import logger.Module(log) so providers can take a *slog.Logger
//
// Request logging is middleware.Logger.
package logger

import (
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/dygoframework/dygo"
)

// Format is the output format.
type Format string

const (
	// Text is slog's key=value format, for development.
	Text Format = "text"
	// JSON is one JSON object per line, for log collectors.
	JSON Format = "json"
)

// Options configure New.
type Options struct {
	// Format defaults to JSON when APP_ENV or GO_ENV is "production", else Text.
	Format Format
	// Level is the minimum level. Defaults to the LOG_LEVEL variable (debug,
	// info, warn, error), else Info.
	Level slog.Leveler
	// Output defaults to os.Stdout.
	Output io.Writer
	// AddSource adds the source file and line.
	AddSource bool
}

// New returns a logger configured by o.
func New(o Options) *slog.Logger {
	if o.Output == nil {
		o.Output = os.Stdout
	}
	if o.Format == "" {
		o.Format = Text
		if env := firstEnv("APP_ENV", "GO_ENV"); env == "production" || env == "prod" {
			o.Format = JSON
		}
	}
	if o.Level == nil {
		o.Level = ParseLevel(os.Getenv("LOG_LEVEL"))
	}
	ho := &slog.HandlerOptions{Level: o.Level, AddSource: o.AddSource}
	if o.Format == JSON {
		return slog.New(slog.NewJSONHandler(o.Output, ho))
	}
	return slog.New(slog.NewTextHandler(o.Output, ho))
}

// ParseLevel reads "debug", "info", "warn" or "error"; anything else is Info.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return strings.ToLower(v)
		}
	}
	return ""
}

// Module returns a global module that provides log as *slog.Logger.
func Module(log *slog.Logger) *dygo.Module {
	return &dygo.Module{
		Name:      "logger",
		Global:    true,
		Providers: []dygo.Provider{dygo.Value(log)},
		Exports:   []dygo.Token{dygo.TypeOf[*slog.Logger]()},
	}
}
