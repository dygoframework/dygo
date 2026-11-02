package sql

import (
	"context"
	stdsql "database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/config"
	"github.com/dygoframework/dygo/health"
)

// Config is the connection and pool configuration. [Module] loads it from the
// environment (and .env files) with the config package, so every variable
// below can be set there; [Prefix] namespaces them for a second database.
type Config struct {
	// URL is the connection URL: postgres://user:pass@host:5432/db or
	// mysql://user:pass@host:3306/db. A driver native DSN works too.
	URL string `env:"DATABASE_URL" required:"true"`
	// MaxOpen limits open connections (in use plus idle).
	MaxOpen int `env:"DB_MAX_OPEN" default:"25"`
	// MaxIdle limits idle connections kept in the pool.
	MaxIdle int `env:"DB_MAX_IDLE" default:"25"`
	// ConnMaxLifetime closes connections older than this. Keep it below the
	// server's own idle timeout (MySQL wait_timeout is 8 hours by default).
	ConnMaxLifetime time.Duration `env:"DB_CONN_MAX_LIFETIME" default:"30m"`
	// ConnMaxIdleTime closes connections idle for longer than this.
	ConnMaxIdleTime time.Duration `env:"DB_CONN_MAX_IDLE_TIME" default:"5m"`
	// ConnectTimeout bounds the ping at boot.
	ConnectTimeout time.Duration `env:"DB_CONNECT_TIMEOUT" default:"5s"`
}

// Dialect adapts the module to one database. The sql/postgres and sql/mysql
// packages provide one each. The interface may still grow before v1.
type Dialect interface {
	// Name is the database name used in logs and errors, such as "postgres".
	Name() string
	// DriverName is the database/sql driver to open, such as "pgx".
	DriverName() string
	// DSN turns Config.URL into the driver's connection string, and reports
	// a malformed URL before anything is dialed.
	DSN(url string) (string, error)
	// Classify reports what kind of failure a driver error is, and the
	// constraint involved when the database names one.
	Classify(err error) (kind Kind, constraint string)
	// Placeholder is the n-th (from 1) bind parameter: "?" on MySQL, "$1" on
	// Postgres. Queries are always written with "?" and rewritten with it.
	Placeholder(n int) string
	// BackslashEscapes reports whether a backslash escapes a quote inside a
	// plain '...' string (MySQL), so the rewriter can find where it ends.
	BackslashEscapes() bool
	// Quote quotes an identifier, such as a table or column name: "name" on
	// Postgres, `name` on MySQL. A dotted name (schema.table) is quoted part
	// by part.
	Quote(ident string) string
	// Returning reports whether INSERT ... RETURNING gives back the columns
	// the database set (Postgres). Without it, [Repository.Create] reads the
	// one auto increment column from LastInsertId (MySQL).
	Returning() bool
}

// DB is a connection pool for one database. It is safe for concurrent use.
// Use [DB.SQL] for the underlying *database/sql.DB.
type DB struct {
	db      *stdsql.DB
	dialect Dialect
	name    string
	queries queryCache
	mem     *memStore // set for MemoryURL; db is nil then
}

// Open connects using cfg: it opens the pool, applies the pool settings and
// pings the database within cfg.ConnectTimeout, so a wrong URL or password is
// reported at boot. The error never contains the password.
func Open(ctx context.Context, d Dialect, cfg Config) (*DB, error) {
	return open(ctx, d, cfg, d.Name())
}

func open(ctx context.Context, d Dialect, cfg Config, name string) (*DB, error) {
	if IsMemory(cfg.URL) {
		return memoryDB(name), nil
	}
	where := redact(cfg.URL)
	if d == nil {
		if scheme, _, ok := strings.Cut(cfg.URL, "://"); ok && (scheme == "postgres" || scheme == "postgresql" || scheme == "mysql") {
			return nil, fmt.Errorf("sql %s: DATABASE_URL is %s, but the app has no %s driver yet: run dygo db init -driver %s", name, where, scheme, strings.TrimSuffix(scheme, "ql"))
		}
		return nil, fmt.Errorf("sql %s: DATABASE_URL is %s, but the app has no database driver: set it to %s, or run dygo db init", name, where, MemoryURL)
	}
	if cfg.URL == "" {
		return nil, fmt.Errorf("sql %s: URL is empty", name)
	}
	dsn, err := d.DSN(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("sql %s: bad URL %s: %w", name, where, redactErr(err, cfg.URL))
	}
	db, err := stdsql.Open(d.DriverName(), dsn)
	if err != nil {
		return nil, fmt.Errorf("sql %s: open %s: %w", name, where, redactErr(err, cfg.URL))
	}
	if cfg.MaxOpen > 0 {
		db.SetMaxOpenConns(cfg.MaxOpen)
	}
	if cfg.MaxIdle > 0 {
		db.SetMaxIdleConns(cfg.MaxIdle)
	}
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	db.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)

	pingCtx := ctx
	if cfg.ConnectTimeout > 0 {
		var cancel context.CancelFunc
		pingCtx, cancel = context.WithTimeout(ctx, cfg.ConnectTimeout)
		defer cancel()
	}
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sql %s: connect to %s: %w", name, where, redactErr(err, cfg.URL))
	}
	return &DB{db: db, dialect: d, name: name}, nil
}

// SQL returns the underlying pool, for libraries that take a *database/sql.DB
// (sqlc, migration tools, GORM). It is nil for a [MemoryURL] database.
func (db *DB) SQL() *stdsql.DB { return db.db }

// IsMemory reports whether the data is kept in memory ([MemoryURL]).
func (db *DB) IsMemory() bool { return db.mem != nil }

// Dialect returns the database dialect.
func (db *DB) Dialect() Dialect { return db.dialect }

// Name is the dialect name, or the name given with [Named].
func (db *DB) Name() string { return db.name }

// Stats returns the pool statistics.
func (db *DB) Stats() stdsql.DBStats {
	if db.db == nil {
		return stdsql.DBStats{}
	}
	return db.db.Stats()
}

// PingContext checks that the database answers. The health indicator uses it.
func (db *DB) PingContext(ctx context.Context) error {
	if db.db == nil {
		return nil
	}
	return db.db.PingContext(ctx)
}

// Close closes the pool. [Module] calls it on shutdown; call it yourself only
// for a DB made with [Open].
func (db *DB) Close() error {
	if db.db == nil {
		return nil
	}
	return db.db.Close()
}

// OnApplicationShutdown closes the pool. It runs after the HTTP server has
// drained in-flight requests, so no handler loses its connection mid-query.
func (db *DB) OnApplicationShutdown(context.Context, string) error {
	if db == nil || db.db == nil {
		return nil
	}
	return db.Close()
}

// Option configures [Module].
type Option func(*moduleOptions)

type moduleOptions struct {
	prefix string
	name   string
	cfg    *Config
	files  []string
	health bool
}

// Prefix puts p in front of every variable name: with Prefix("REPORTS_") the
// URL is read from REPORTS_DATABASE_URL and the pool size from
// REPORTS_DB_MAX_OPEN.
func Prefix(p string) Option { return func(o *moduleOptions) { o.prefix = p } }

// Named provides the pool under the named token [Token](name) instead of
// *sql.DB, so an app can have several databases. Inject it with a field
// tagged name:"name" in a dygo.In struct, or with dygo.GetNamed.
func Named(name string) Option { return func(o *moduleOptions) { o.name = name } }

// WithConfig uses cfg instead of reading the environment.
func WithConfig(cfg Config) Option { return func(o *moduleOptions) { o.cfg = &cfg } }

// Files sets the .env files to read, earlier ones winning. The default is
// ".env.local" then ".env", the files the app generated by dygo new reads, so
// the app and the pool see the same DATABASE_URL; missing files are skipped.
func Files(paths ...string) Option { return func(o *moduleOptions) { o.files = paths } }

// NoHealth leaves out the readiness indicator.
func NoHealth() Option { return func(o *moduleOptions) { o.health = false } }

// Token is the named token [Named] provides the pool under.
func Token(name string) dygo.NamedToken[*DB] { return dygo.NewToken[*DB](name) }

// Module returns a global module that provides *DB (or [Token](name) with
// [Named]) to the whole app. The pool is opened
// and pinged at boot, closed after the HTTP drain on shutdown, and added to
// GET /health/ready as "db" (or the name). The sql/postgres and sql/mysql
// packages wrap it with their dialect:
//
//	Imports: []*dygo.Module{postgres.Module()}
//
// Module(nil), which an app made by dygo new imports, has no driver: it
// opens DATABASE_URL=memory:// ([MemoryURL], also the default when the
// variable is not set) and stops the boot with a hint to run dygo db init
// for any other URL.
func Module(d Dialect, opts ...Option) *dygo.Module {
	o := moduleOptions{files: []string{".env.local", ".env"}, health: true}
	for _, fn := range opts {
		fn(&o)
	}
	name, label := "db", "sql"
	if d != nil {
		label = "sql." + d.Name()
	}
	if o.name != "" {
		name = o.name
		label += "." + o.name
	}

	// The indicator needs the pool, which only exists once the constructor
	// has run; it reads it through this holder at check time.
	h := &holder{}
	ctor := func(ctx context.Context, log dygo.Optional[*slog.Logger]) (*DB, error) {
		cfg, err := o.load(d == nil)
		if err != nil {
			return nil, err
		}
		db, err := open(ctx, d, *cfg, name)
		if err != nil {
			return nil, err
		}
		h.db = db
		// Say which database the app uses, so a wrong DATABASE_URL shows at
		// startup. The password is redacted.
		if db.IsMemory() {
			log.OrElse(slog.Default()).InfoContext(ctx, "database in memory: the data is lost when the app stops", "name", name, "url", MemoryURL)
		} else {
			log.OrElse(slog.Default()).InfoContext(ctx, "database connected", "name", name, "url", redact(cfg.URL))
		}
		return db, nil
	}

	m := &dygo.Module{Name: label, Global: true}
	if o.name != "" {
		tok := Token(o.name)
		m.Providers = append(m.Providers, dygo.Provide(ctor, dygo.For(tok)))
		m.Exports = []dygo.Token{tok}
	} else {
		m.Providers = append(m.Providers, dygo.Provide(ctor))
		m.Exports = []dygo.Token{dygo.TypeOf[*DB]()}
	}
	if o.health {
		m.Providers = append(m.Providers, dygo.Value(health.Func(name, h.ping), dygo.Group(health.Indicators)))
	}
	return m
}

func (o *moduleOptions) load(memoryDefault bool) (*Config, error) {
	if o.cfg != nil {
		c := *o.cfg
		return &c, nil
	}
	if memoryDefault {
		// Module(nil) has no driver, so memory:// is the only database it
		// can open: it is the default when DATABASE_URL is not set.
		u, err := config.Load[struct {
			URL string `env:"DATABASE_URL" default:"memory://"`
		}](config.Prefix(o.prefix), config.Files(o.files...))
		if err != nil {
			return nil, err
		}
		if IsMemory(u.URL) {
			return &Config{URL: u.URL}, nil
		}
	}
	return config.Load[Config](config.Prefix(o.prefix), config.Files(o.files...))
}

type holder struct{ db *DB }

func (h *holder) ping(ctx context.Context) error {
	if h.db == nil {
		return errors.New("not connected")
	}
	return h.db.PingContext(ctx)
}

// dsnPassword matches the password of a driver native MySQL DSN,
// user:password@tcp(host)/db.
var dsnPassword = regexp.MustCompile(`^([^:@/]*):([^@]*)@`)

// redact returns the URL or DSN with its password replaced by "xxxxx".
func redact(s string) string {
	if u, err := url.Parse(s); err == nil && u.Scheme != "" && u.User != nil {
		return u.Redacted()
	}
	if dsnPassword.MatchString(s) {
		return dsnPassword.ReplaceAllString(s, "${1}:xxxxx@")
	}
	return s
}

// redactErr wraps err so its message has any copy of the password removed,
// in case the driver echoed the connection string. errors.Is and errors.As
// still reach the original error.
func redactErr(err error, rawURL string) error {
	msg := err.Error()
	if pw := password(rawURL); len(pw) >= 3 {
		msg = strings.ReplaceAll(msg, pw, "xxxxx")
		if esc := url.QueryEscape(pw); esc != pw {
			msg = strings.ReplaceAll(msg, esc, "xxxxx")
		}
	}
	return &redactedError{msg: msg, err: err}
}

type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }

func password(s string) string {
	if u, err := url.Parse(s); err == nil && u.User != nil {
		if pw, ok := u.User.Password(); ok {
			return pw
		}
	}
	if m := dsnPassword.FindStringSubmatch(s); m != nil {
		return m[2]
	}
	return ""
}
