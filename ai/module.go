package ai

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/dygoframework/dygo"
	dygoconfig "github.com/dygoframework/dygo/config"
)

// Secret is a value that must never be printed: it formats, logs and encodes
// as "***".
type Secret string

func (Secret) String() string { return "***" }

// GoString hides the value from %#v too.
func (Secret) GoString() string { return `"***"` }

// LogValue hides the value from slog.
func (Secret) LogValue() slog.Value { return slog.StringValue("***") }

// MarshalText hides the value from encoders.
func (Secret) MarshalText() ([]byte, error) { return []byte("***"), nil }

// UnmarshalText reads the real value, for the config loader.
func (s *Secret) UnmarshalText(b []byte) error {
	*s = Secret(b)
	return nil
}

// Reveal returns the real value. Only providers call it, to set the header.
func (s Secret) Reveal() string { return string(s) }

// Config is what [Module] reads from the environment and .env files.
type Config struct {
	// Provider picks one of the providers passed to Module, by name.
	Provider string `env:"AI_PROVIDER" required:"true"`
	// Model is the default model id.
	Model  string `env:"AI_MODEL" required:"true"`
	APIKey Secret `env:"AI_API_KEY"`
	// BaseURL replaces the vendor's URL, for a local or compatible server. It
	// must be https unless it points at this machine.
	BaseURL    string        `env:"AI_BASE_URL"`
	Timeout    time.Duration `env:"AI_TIMEOUT" default:"60s"`
	MaxRetries int           `env:"AI_MAX_RETRIES" default:"2"`
}

// Validate checks the settings before any call is made.
func (c Config) Validate() error {
	if c.Provider == "" || c.Model == "" {
		return errors.New("ai: AI_PROVIDER and AI_MODEL are required")
	}
	if c.Timeout <= 0 {
		return errors.New("ai: AI_TIMEOUT must be positive")
	}
	if c.MaxRetries < 0 || c.MaxRetries > maxRetries {
		return fmt.Errorf("ai: AI_MAX_RETRIES must be 0 to %d", maxRetries)
	}
	if c.BaseURL != "" {
		return checkBaseURL(c.BaseURL)
	}
	return nil
}

// checkBaseURL refuses a plain http URL to another machine: the API key would
// travel in clear text.
func checkBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("ai: AI_BASE_URL %q is not a URL", raw)
	}
	if u.User != nil {
		return errors.New("ai: AI_BASE_URL must not hold a user or password; use AI_API_KEY")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopback(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("ai: AI_BASE_URL %q must use https (http is only allowed for localhost)", raw)
	}
	return fmt.Errorf("ai: AI_BASE_URL %q must use https", raw)
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Provider builds a model for one vendor. Each provider package exports one,
// such as openai.Provider.
type Provider struct {
	Name string
	New  func(Config) (Model, error)
}

// Module reads [Config] from the environment, .env.local and .env (real
// variables win), builds the provider AI_PROVIDER names and provides it as
// the app's [Model], wrapped in [Log], [Retry] and [Timeout]. It is global:
// import it once, in the root module.
//
//	ai.Module(openai.Provider, anthropic.Provider)
//
// Switching vendors is then a change to .env, not to code.
func Module(providers ...Provider) *dygo.Module {
	return module(func() (*Config, error) {
		return dygoconfig.Load[Config](dygoconfig.Files(".env.local", ".env"))
	}, providers)
}

// ModuleWith is [Module] with settings from code instead of the environment.
func ModuleWith(cfg Config, providers ...Provider) *dygo.Module {
	return module(func() (*Config, error) { return &cfg, nil }, providers)
}

func module(load func() (*Config, error), providers []Provider) *dygo.Module {
	build := func(l dygo.Optional[*slog.Logger]) (Model, error) {
		cfg, err := load()
		if err != nil {
			return nil, err
		}
		m, err := New(*cfg, providers...)
		if err != nil {
			return nil, err
		}
		log := l.OrElse(slog.Default())
		log.Info("ai: provider ready", slog.String("provider", cfg.Provider), slog.String("model", cfg.Model))
		return Chain(m, Log(log), Retry(cfg.MaxRetries), Timeout(cfg.Timeout)), nil
	}
	return &dygo.Module{
		Name:      "ai",
		Global:    true,
		Providers: []dygo.Provider{dygo.Provide(build)},
		Exports:   []dygo.Token{dygo.TypeOf[Model]()},
	}
}

// New checks cfg and builds the model of the provider it names, without
// middleware.
func New(cfg Config, providers ...Provider) (Model, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(providers))
	for _, p := range providers {
		if p.Name == cfg.Provider {
			return p.New(cfg)
		}
		names = append(names, p.Name)
	}
	return nil, fmt.Errorf("ai: AI_PROVIDER %q is not one of the providers passed to ai.Module (%s)", cfg.Provider, strings.Join(names, ", "))
}
