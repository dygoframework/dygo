// Package migrate runs SQL migrations for the Dygo sql module, on Postgres and
// MySQL, with no other dependency.
//
// Migrations are pairs of files named by a version number and a name:
//
//	migrations/0001_create_users.up.sql
//	migrations/0001_create_users.down.sql
//
// embedded in the binary with an embed.FS, so production needs nothing but
// the binary. dygo g migration writes the next pair. A dygo_migrations table
// records what ran, and a database lock (pg_advisory_lock on Postgres,
// GET_LOCK on MySQL) makes a second instance wait instead of migrating at
// the same time.
//
// Each migration runs in a transaction together with its dygo_migrations
// row, so on Postgres a failing migration leaves nothing behind. MySQL
// commits every DDL statement on its own, so a migration that fails half way
// leaves the statements before the failure applied: keep MySQL migrations to
// one change each. A file whose first line is
//
//	-- dygo:no-transaction
//
// runs outside a transaction, for statements that refuse one, such as
// CREATE INDEX CONCURRENTLY. An up file with
//
//	-- dygo:after-deploy
//
// among its first comment lines is the contract half of an expand and
// contract change (dygo g migration -safe): [Migrator.Up] stops before it,
// and before every later migration, unless the Migrator has [Contract]
// (dygo db migrate --contract). [Migrator.Held] reports where it stopped.
//
// Run migrations from the command line with [Main] (dygo db migrate, or
// ./app db:migrate in production), from code with [Migrator], or at boot
// with [OnBoot] for a small single instance service.
package migrate

import (
	"cmp"
	"context"
	stdsql "database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dygoframework/dygo/sql"
)

// DefaultTable records the applied migrations.
const DefaultTable = "dygo_migrations"

// noTxMarker on the first line of a file runs it outside a transaction.
const noTxMarker = "-- dygo:no-transaction"

// AfterDeployMarker, among the comment lines at the top of an up file, holds
// the migration back until the code that no longer needs the old schema is
// deployed: Up stops before it unless the Migrator was made with Contract
// (dygo db migrate --contract). dygo g migration -safe writes it on the
// contract half of an expand and contract change.
const AfterDeployMarker = "-- dygo:after-deploy"

// Migration is one version: its up SQL and, when there is a down file, its
// down SQL.
type Migration struct {
	Version int64
	Name    string
	Up      string
	Down    string
	HasDown bool
	// UpNoTx and DownNoTx are set by the -- dygo:no-transaction marker.
	UpNoTx, DownNoTx bool
	// AfterDeploy is set by AfterDeployMarker in the up file.
	AfterDeploy bool
}

// hasHeaderMarker reports whether marker is one of the comment lines at the
// top of src.
func hasHeaderMarker(src, marker string) bool {
	for _, line := range strings.Split(src, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == marker:
			return true
		case line == "" || strings.HasPrefix(line, "--"):
			continue
		}
		return false
	}
	return false
}

// String is the file name without its direction: 0001_create_users.
func (m Migration) String() string { return fmt.Sprintf("%04d_%s", m.Version, m.Name) }

var fileName = regexp.MustCompile(`^(\d+)_([A-Za-z0-9_\-]+)\.(up|down)\.sql$`)

// Load reads the migrations in the root of fsys, sorted by version. Files
// that are not NNNN_name.up.sql or NNNN_name.down.sql are ignored. Two
// migrations with the same version, or a down file without an up file, are
// errors.
func Load(fsys fs.FS) ([]Migration, error) {
	if fsys == nil {
		return nil, errors.New("migrate: no migrations (fs.FS is nil)")
	}
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	byVersion := map[int64]*Migration{}
	hasUp := map[int64]bool{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := fileName.FindStringSubmatch(e.Name())
		if m == nil {
			if strings.HasSuffix(e.Name(), ".sql") {
				return nil, fmt.Errorf("migrate: %s: want NNNN_name.up.sql or NNNN_name.down.sql", e.Name())
			}
			continue
		}
		v, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("migrate: %s: version must be a positive number", e.Name())
		}
		body, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, fmt.Errorf("migrate: %w", err)
		}
		mig := byVersion[v]
		if mig == nil {
			mig = &Migration{Version: v, Name: m[2]}
			byVersion[v] = mig
		} else if mig.Name != m[2] {
			return nil, fmt.Errorf("migrate: version %d is used twice: %s and %s", v, mig.Name, m[2])
		}
		src := string(body)
		noTx := strings.HasPrefix(strings.TrimSpace(src), noTxMarker)
		if m[3] == "up" {
			mig.Up, mig.UpNoTx, mig.AfterDeploy = src, noTx, hasHeaderMarker(src, AfterDeployMarker)
			hasUp[v] = true
		} else {
			mig.Down, mig.DownNoTx, mig.HasDown = src, noTx, true
		}
	}
	out := make([]Migration, 0, len(byVersion))
	for _, m := range byVersion {
		if !hasUp[m.Version] {
			return nil, fmt.Errorf("migrate: %s has a down file but no up file", m)
		}
		out = append(out, *m)
	}
	slices.SortFunc(out, func(a, b Migration) int { return cmp.Compare(a.Version, b.Version) })
	return out, nil
}

// Option configures a [Migrator].
type Option func(*Migrator)

// Table records the applied migrations in name instead of dygo_migrations.
func Table(name string) Option { return func(m *Migrator) { m.table = name } }

// Log writes one line per migration run to w.
func Log(w io.Writer) Option { return func(m *Migrator) { m.log = w } }

// OutOfOrder lets Up apply a pending migration older than the newest applied
// one, which happens when two branches each add a migration. Without it, Up
// refuses, so the order the database saw stays the order of the files.
func OutOfOrder() Option { return func(m *Migrator) { m.outOfOrder = true } }

// Contract lets Up apply migrations marked with AfterDeployMarker. Without
// it, Up stops before the first of them; Held reports which one.
func Contract() Option { return func(m *Migrator) { m.contract = true } }

// Migrator applies and rolls back the migrations of one database.
type Migrator struct {
	db         *sql.DB
	kind       string // "postgres" or "mysql"
	migrations []Migration
	table      string
	log        io.Writer
	outOfOrder bool
	contract   bool
	held       *Migration // the after-deploy migration the last Pending or Up stopped at
}

// New loads the migrations in fsys for db. It does not touch the database.
func New(db *sql.DB, fsys fs.FS, opts ...Option) (*Migrator, error) {
	if db == nil {
		return nil, errors.New("migrate: db is nil")
	}
	kind, err := dialectKind(db.Dialect())
	if err != nil {
		return nil, err
	}
	migs, err := Load(fsys)
	if err != nil {
		return nil, err
	}
	m := &Migrator{db: db, kind: kind, migrations: migs, table: DefaultTable, log: io.Discard}
	for _, o := range opts {
		o(m)
	}
	return m, nil
}

func dialectKind(d sql.Dialect) (string, error) {
	if d == nil {
		return "", errors.New("migrate: dialect is nil")
	}
	switch d.Name() {
	case "postgres", "mysql":
		return d.Name(), nil
	}
	return "", fmt.Errorf("migrate: dialect %q is not supported (want postgres or mysql)", d.Name())
}

// Migrations returns the loaded migrations, oldest first.
func (m *Migrator) Migrations() []Migration { return slices.Clone(m.migrations) }

// State is one migration's status.
type State struct {
	Version   int64
	Name      string
	Applied   bool
	AppliedAt time.Time
	// Missing means the database recorded the version but no file has it.
	Missing bool
	// AfterDeploy marks a migration that waits for Contract (see
	// AfterDeployMarker).
	AfterDeploy bool
}

// Status lists every migration, from the files and from the database,
// oldest first.
func (m *Migrator) Status(ctx context.Context) ([]State, error) {
	var states []State
	err := m.withConn(ctx, false, func(c *stdsql.Conn) error {
		applied, err := m.applied(ctx, c)
		if err != nil {
			return err
		}
		states = m.states(applied)
		return nil
	})
	return states, err
}

func (m *Migrator) states(applied map[int64]appliedRow) []State {
	var states []State
	for _, mig := range m.migrations {
		s := State{Version: mig.Version, Name: mig.Name, AfterDeploy: mig.AfterDeploy}
		if a, ok := applied[mig.Version]; ok {
			s.Applied, s.AppliedAt = true, a.at
		}
		states = append(states, s)
	}
	for v, a := range applied {
		if !slices.ContainsFunc(m.migrations, func(x Migration) bool { return x.Version == v }) {
			states = append(states, State{Version: v, Name: a.name, Applied: true, AppliedAt: a.at, Missing: true})
		}
	}
	slices.SortFunc(states, func(a, b State) int { return cmp.Compare(a.Version, b.Version) })
	return states
}

// Pending returns the migrations Up would apply, up to version to (0 for
// all), without applying them.
func (m *Migrator) Pending(ctx context.Context, to int64) ([]Migration, error) {
	var pending []Migration
	err := m.withConn(ctx, false, func(c *stdsql.Conn) error {
		applied, err := m.applied(ctx, c)
		if err != nil {
			return err
		}
		pending, err = m.pending(applied, to)
		return err
	})
	return pending, err
}

func (m *Migrator) pending(applied map[int64]appliedRow, to int64) ([]Migration, error) {
	var newest int64
	for v := range applied {
		newest = max(newest, v)
	}
	var out []Migration
	m.held = nil
	for _, mig := range m.migrations {
		if _, ok := applied[mig.Version]; ok || (to > 0 && mig.Version > to) {
			continue
		}
		if mig.Version < newest && !m.outOfOrder {
			return nil, fmt.Errorf("migrate: %s is pending but the newer version %d is already applied; renumber it, or allow it with OutOfOrder (--out-of-order)", mig, newest)
		}
		if mig.AfterDeploy && !m.contract {
			held := mig
			m.held = &held
			break // later migrations wait too, so the order stays the order of the files
		}
		out = append(out, mig)
	}
	return out, nil
}

// Held returns the after-deploy migration that the last Pending or Up
// stopped before, and false when it stopped at none (see Contract).
func (m *Migrator) Held() (Migration, bool) {
	if m.held == nil {
		return Migration{}, false
	}
	return *m.held, true
}

// Up applies the pending migrations, oldest first, up to and including
// version to (0 for all). It returns the migrations it applied, also when a
// later one failed.
func (m *Migrator) Up(ctx context.Context, to int64) ([]Migration, error) {
	var done []Migration
	err := m.withConn(ctx, true, func(c *stdsql.Conn) error {
		applied, err := m.applied(ctx, c)
		if err != nil {
			return err
		}
		pending, err := m.pending(applied, to)
		if err != nil {
			return err
		}
		for _, mig := range pending {
			if err := m.run(ctx, c, mig, true); err != nil {
				return err
			}
			done = append(done, mig)
		}
		return nil
	})
	return done, err
}

// Down rolls back the newest steps applied migrations, newest first.
func (m *Migrator) Down(ctx context.Context, steps int) ([]Migration, error) {
	if steps < 1 {
		return nil, errors.New("migrate: steps must be at least 1")
	}
	return m.down(ctx, func(i int, _ int64) bool { return i < steps })
}

// DownTo rolls back every applied migration newer than version, newest
// first. DownTo(ctx, 0) rolls back everything.
func (m *Migrator) DownTo(ctx context.Context, version int64) ([]Migration, error) {
	if version < 0 {
		return nil, errors.New("migrate: version must not be negative")
	}
	return m.down(ctx, func(_ int, v int64) bool { return v > version })
}

func (m *Migrator) down(ctx context.Context, want func(i int, v int64) bool) ([]Migration, error) {
	var done []Migration
	err := m.withConn(ctx, true, func(c *stdsql.Conn) error {
		applied, err := m.applied(ctx, c)
		if err != nil {
			return err
		}
		versions := make([]int64, 0, len(applied))
		for v := range applied {
			versions = append(versions, v)
		}
		slices.Sort(versions)
		slices.Reverse(versions)
		// Check every file first, so a missing down file stops the
		// rollback before anything ran.
		var todo []Migration
		for i, v := range versions {
			if !want(i, v) {
				break
			}
			idx := slices.IndexFunc(m.migrations, func(x Migration) bool { return x.Version == v })
			if idx < 0 {
				return fmt.Errorf("migrate: version %d (%s) is applied but its files are missing", v, applied[v].name)
			}
			mig := m.migrations[idx]
			if !mig.HasDown {
				return fmt.Errorf("migrate: %s has no down file", mig)
			}
			todo = append(todo, mig)
		}
		for _, mig := range todo {
			if err := m.run(ctx, c, mig, false); err != nil {
				return err
			}
			done = append(done, mig)
		}
		return nil
	})
	return done, err
}

// run applies one migration in one direction and records it.
func (m *Migrator) run(ctx context.Context, c *stdsql.Conn, mig Migration, up bool) error {
	src, noTx, dir := mig.Up, mig.UpNoTx, "up"
	// G201: only the quoted table name and placeholders are formatted in; values are arguments.
	record := fmt.Sprintf("INSERT INTO %s (version, name, applied_at) VALUES (%s, %s, %s)", //nolint:gosec
		m.quote(m.table), m.ph(1), m.ph(2), m.ph(3))
	args := []any{mig.Version, mig.Name, time.Now().UTC()}
	if !up {
		src, noTx, dir = mig.Down, mig.DownNoTx, "down"
		record = fmt.Sprintf("DELETE FROM %s WHERE version = %s", m.quote(m.table), m.ph(1))
		args = args[:1]
	}
	start := time.Now()
	fail := func(err error) error {
		return fmt.Errorf("migrate: %s (%s): %w", mig, dir, sql.MapError(m.db.Dialect(), err))
	}
	if noTx {
		if err := m.exec(ctx, c, src); err != nil {
			return fail(err)
		}
		if _, err := c.ExecContext(ctx, record, args...); err != nil {
			return fail(err)
		}
	} else {
		tx, err := c.BeginTx(ctx, nil)
		if err != nil {
			return fail(err)
		}
		if err := m.exec(ctx, tx, src); err != nil {
			_ = tx.Rollback()
			return fail(err)
		}
		if _, err := tx.ExecContext(ctx, record, args...); err != nil {
			_ = tx.Rollback()
			return fail(err)
		}
		if err := tx.Commit(); err != nil {
			return fail(err)
		}
	}
	fmt.Fprintf(m.log, "%-4s %s (%s)\n", strings.ToUpper(dir), mig, time.Since(start).Round(time.Millisecond))
	return nil
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (stdsql.Result, error)
}

// exec runs a migration file. Postgres takes the whole file in one call (the
// simple protocol allows several statements); MySQL gets one statement at a
// time, as its driver refuses several unless the DSN sets multiStatements.
func (m *Migrator) exec(ctx context.Context, e execer, src string) error {
	stmts := Split(src, m.kind == "mysql")
	if len(stmts) == 0 {
		return nil
	}
	if m.kind == "postgres" {
		_, err := e.ExecContext(ctx, src)
		return err
	}
	for _, s := range stmts {
		if _, err := e.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

type appliedRow struct {
	name string
	at   time.Time
}

// applied reads the dygo_migrations table, creating it first.
func (m *Migrator) applied(ctx context.Context, c *stdsql.Conn) (map[int64]appliedRow, error) {
	ts := "TIMESTAMPTZ"
	if m.kind == "mysql" {
		ts = "DATETIME(6)"
	}
	ddl := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (version BIGINT NOT NULL PRIMARY KEY, name VARCHAR(255) NOT NULL, applied_at %s NOT NULL)",
		m.quote(m.table), ts)
	if _, err := c.ExecContext(ctx, ddl); err != nil {
		return nil, fmt.Errorf("migrate: create %s: %w", m.table, err)
	}
	rows, err := c.QueryContext(ctx, fmt.Sprintf("SELECT version, name, applied_at FROM %s", m.quote(m.table)))
	if err != nil {
		return nil, fmt.Errorf("migrate: read %s: %w", m.table, err)
	}
	defer rows.Close()
	out := map[int64]appliedRow{}
	for rows.Next() {
		var v int64
		var r appliedRow
		if err := rows.Scan(&v, &r.name, &r.at); err != nil {
			return nil, fmt.Errorf("migrate: read %s: %w", m.table, err)
		}
		out[v] = r
	}
	return out, rows.Err()
}

// lockKey is the Postgres advisory lock key: "dygo" in ASCII.
const lockKey = 0x6479676f

// withConn runs fn on one connection, holding the migration lock when lock
// is set. Everything runs on that connection, so a pool of one is enough.
func (m *Migrator) withConn(ctx context.Context, lock bool, fn func(*stdsql.Conn) error) (err error) {
	c, err := m.db.SQL().Conn(ctx)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	defer func() { err = errors.Join(err, c.Close()) }()
	if !lock {
		return fn(c)
	}
	if m.kind == "postgres" {
		if _, err := c.ExecContext(ctx, "SELECT pg_advisory_lock($1)", lockKey); err != nil {
			return fmt.Errorf("migrate: lock: %w", err)
		}
		defer func() {
			if _, uerr := c.ExecContext(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", lockKey); uerr != nil {
				err = errors.Join(err, fmt.Errorf("migrate: unlock: %w", uerr))
			}
		}()
	} else {
		name := m.table + "_lock"
		var got stdsql.NullInt64
		// Wait up to ten minutes for another instance to finish.
		if err := c.QueryRowContext(ctx, "SELECT GET_LOCK(?, 600)", name).Scan(&got); err != nil {
			return fmt.Errorf("migrate: lock: %w", err)
		}
		if got.Int64 != 1 {
			return errors.New("migrate: lock: another instance held the migration lock for 10 minutes")
		}
		defer func() {
			if _, uerr := c.ExecContext(context.WithoutCancel(ctx), "SELECT RELEASE_LOCK(?)", name); uerr != nil {
				err = errors.Join(err, fmt.Errorf("migrate: unlock: %w", uerr))
			}
		}()
	}
	return fn(c)
}

func (m *Migrator) quote(ident string) string { return m.db.Dialect().Quote(ident) }
func (m *Migrator) ph(n int) string           { return m.db.Dialect().Placeholder(n) }
