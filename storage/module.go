package storage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"

	"github.com/dygoframework/dygo"
	dygoconfig "github.com/dygoframework/dygo/config"
	"github.com/dygoframework/dygo/health"
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

// Reveal returns the real value. Only drivers call it.
func (s Secret) Reveal() string { return string(s) }

// Config is what [Module] reads from the environment and .env files. Each
// driver reads the fields it needs.
type Config struct {
	// Driver picks one of the drivers passed to Module, by name: "local",
	// "s3" or "rustfs".
	Driver string `env:"STORAGE_DRIVER" default:"local"`

	// LocalDir is the folder the local driver keeps files in.
	LocalDir string `env:"STORAGE_LOCAL_DIR" default:"storage"`
	// PublicURL is where the local folder is served from, such as a CDN.
	// When set, the local driver's URL returns PublicURL + key.
	PublicURL string `env:"STORAGE_PUBLIC_URL"`

	// Bucket, Region, Endpoint and PathStyle are for the s3 driver. Endpoint
	// is empty for AWS and set for other S3-compatible stores; the rustfs
	// driver defaults it to http://localhost:9000.
	Bucket    string `env:"STORAGE_BUCKET"`
	Region    string `env:"STORAGE_REGION"`
	Endpoint  string `env:"STORAGE_ENDPOINT"`
	PathStyle bool   `env:"STORAGE_PATH_STYLE"`
	// AccessKey and SecretKey go in .env only. Left empty, the s3 driver uses
	// the machine's own credentials, such as an IAM role.
	AccessKey string `env:"STORAGE_ACCESS_KEY"`
	SecretKey Secret `env:"STORAGE_SECRET_KEY"`
}

// Validate checks the settings every driver shares.
func (c Config) Validate() error {
	if c.Driver == "" {
		return errors.New("storage: STORAGE_DRIVER is required")
	}
	if c.Endpoint != "" {
		if err := checkURL("STORAGE_ENDPOINT", c.Endpoint, true); err != nil {
			return err
		}
	}
	if c.PublicURL != "" {
		if err := checkURL("STORAGE_PUBLIC_URL", c.PublicURL, false); err != nil {
			return err
		}
	}
	return nil
}

// checkURL refuses a URL holding a password, and, for an endpoint that gets
// the keys, plain http to another machine.
func checkURL(name, raw string, secret bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("storage: %s %q is not a URL", name, raw)
	}
	if u.User != nil {
		return fmt.Errorf("storage: %s must not hold a user or password", name)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if !secret || isLoopback(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("storage: %s %q must use https (http is only allowed for localhost)", name, raw)
	}
	return fmt.Errorf("storage: %s %q must use http or https", name, raw)
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Driver builds a store. Each driver package exports one, such as
// local.Driver.
type Driver struct {
	Name string
	New  func(Config) (Store, error)
}

// Checker is a store that can tell whether it works, such as a reachable
// bucket. [Module] adds it to the health checks.
type Checker interface {
	Check(ctx context.Context) error
}

// Module reads [Config] from the environment, .env.local and .env (real
// variables win), builds the driver STORAGE_DRIVER names and provides it as
// the app's [Store]. It is global: import it once, in the root module. A
// store that implements [Checker] also shows up in the health module's
// readiness check as "storage".
//
//	storage.Module(local.Driver, s3.Driver, s3.RustFS)
//
// Moving from a local folder to a bucket is then a change to .env.
func Module(drivers ...Driver) *dygo.Module {
	return module(func() (*Config, error) {
		return dygoconfig.Load[Config](dygoconfig.Files(".env.local", ".env"))
	}, drivers)
}

// ModuleWith is [Module] with settings from code instead of the environment.
func ModuleWith(cfg Config, drivers ...Driver) *dygo.Module {
	return module(func() (*Config, error) { return &cfg, nil }, drivers)
}

func module(load func() (*Config, error), drivers []Driver) *dygo.Module {
	build := func(l dygo.Optional[*slog.Logger]) (Store, error) {
		cfg, err := load()
		if err != nil {
			return nil, err
		}
		s, err := New(*cfg, drivers...)
		if err != nil {
			return nil, err
		}
		attrs := []any{slog.String("driver", cfg.Driver)}
		if cfg.Bucket != "" {
			attrs = append(attrs, slog.String("bucket", cfg.Bucket))
		}
		l.OrElse(slog.Default()).Info("storage: ready", attrs...)
		return s, nil
	}
	check := func(s Store) health.Indicator {
		return health.Func("storage", func(ctx context.Context) error {
			if c, ok := s.(Checker); ok {
				return c.Check(ctx)
			}
			return nil
		})
	}
	return &dygo.Module{
		Name:   "storage",
		Global: true,
		Providers: []dygo.Provider{
			dygo.Provide(build),
			dygo.Provide(check, dygo.Group(health.Indicators)),
		},
		Exports: []dygo.Token{dygo.TypeOf[Store]()},
	}
}

// New checks cfg and builds the store of the driver it names.
func New(cfg Config, drivers ...Driver) (Store, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(drivers))
	for _, d := range drivers {
		if d.Name == cfg.Driver {
			return d.New(cfg)
		}
		names = append(names, d.Name)
	}
	return nil, fmt.Errorf("storage: STORAGE_DRIVER %q is not one of the drivers passed to storage.Module (%s)", cfg.Driver, strings.Join(names, ", "))
}
