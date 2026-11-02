package migrate

import (
	"context"
	stdsql "database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/dygoframework/dygo/config"
	"github.com/dygoframework/dygo/sql"
)

// MainOption configures [Main].
type MainOption func(*mainOptions)

type mainOptions struct {
	prefix string
	files  []string
	cfg    *sql.Config
	env    map[string]string
	table  string
	stdout io.Writer
	stderr io.Writer
}

// Prefix reads REPORTS_DATABASE_URL and friends with Prefix("REPORTS_"), as
// sql.Prefix does for the module.
func Prefix(p string) MainOption { return func(o *mainOptions) { o.prefix = p } }

// Files sets the .env files to read, earlier ones winning. The default is
// .env.local then .env, as in projects made by dygo new.
func Files(paths ...string) MainOption { return func(o *mainOptions) { o.files = paths } }

// WithConfig uses cfg instead of reading the environment.
func WithConfig(cfg sql.Config) MainOption { return func(o *mainOptions) { o.cfg = &cfg } }

// WithTable records migrations in name instead of dygo_migrations.
func WithTable(name string) MainOption { return func(o *mainOptions) { o.table = name } }

// Output sends the command's output to stdout and its errors to stderr.
func Output(stdout, stderr io.Writer) MainOption {
	return func(o *mainOptions) { o.stdout, o.stderr = stdout, stderr }
}

// env replaces the process environment, for tests.
func env(vars map[string]string) MainOption { return func(o *mainOptions) { o.env = vars } }

// Commands are the names Main handles.
var Commands = []string{"db:create", "db:drop", "db:migrate", "db:rollback", "db:status", "db:schema:create", "db:schema:drop"}

const mainUsage = `Database commands:
  db:create [--if-not-exists]                  create the database named in DATABASE_URL
  db:drop --force [--if-exists]                drop it
  db:migrate [--to N] [--dry-run] [--out-of-order] [--contract]
                                               apply pending migrations; --contract also
                                               applies the -- dygo:after-deploy ones
  db:rollback [--steps N | --to N]             roll back the newest migration (or N, or down to version N)
  db:status [--check]                          list migrations; --check exits 1 when some are pending
  db:schema:create                             apply every migration to an empty database
  db:schema:drop --force [--all]               roll back every migration; --all drops every table instead

drop, rollback and schema:drop refuse to run with APP_ENV=production unless
--allow-production is given.
`

// Main runs the database command named by args[0] (db:migrate, db:status,
// ...) and reports handled=false, doing nothing, for any other argument, so
// main can call it first:
//
//	if code, ok := migrate.Main(ctx, postgres.Dialect(), migrations.FS, os.Args[1:]); ok {
//		os.Exit(code)
//	}
//
// It runs before the application is created: it reads DATABASE_URL from the
// environment and .env files itself, and opens one connection, so it works
// on a database the app cannot boot against yet (db:create) and needs no
// other service the app depends on. dygo db migrate runs it through go run.
func Main(ctx context.Context, d sql.Dialect, fsys fs.FS, args []string, opts ...MainOption) (code int, handled bool) {
	if len(args) == 0 || !strings.HasPrefix(args[0], "db:") {
		return 0, false
	}
	o := mainOptions{files: []string{".env.local", ".env"}, table: DefaultTable, stdout: os.Stdout, stderr: os.Stderr}
	for _, fn := range opts {
		fn(&o)
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runMain(ctx, d, fsys, args, &o); err != nil {
		var ex exitCode
		if errors.As(err, &ex) {
			return int(ex), true
		}
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(o.stdout, mainUsage)
			return 0, true
		}
		fmt.Fprintln(o.stderr, err)
		return 1, true
	}
	return 0, true
}

// exitCode ends Main with a status after the command printed why.
type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

func runMain(ctx context.Context, d sql.Dialect, fsys fs.FS, args []string, o *mainOptions) error {
	name := args[0]
	fl := flag.NewFlagSet(name, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	force := fl.Bool("force", false, "")
	ifExists := fl.Bool("if-exists", false, "")
	ifNotExists := fl.Bool("if-not-exists", false, "")
	to := fl.Int64("to", -1, "")
	steps := fl.Int("steps", 0, "")
	dryRun := fl.Bool("dry-run", false, "")
	outOfOrder := fl.Bool("out-of-order", false, "")
	contract := fl.Bool("contract", false, "")
	check := fl.Bool("check", false, "")
	all := fl.Bool("all", false, "")
	allowProd := fl.Bool("allow-production", false, "")
	if err := fl.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return fmt.Errorf("%s: %w\n\n%s", name, err, mainUsage)
	}
	if fl.NArg() > 0 {
		return fmt.Errorf("%s: unexpected argument %q", name, fl.Arg(0))
	}
	if name == "db:help" {
		return flag.ErrHelp
	}
	known := false
	for _, c := range Commands {
		known = known || c == name
	}
	if !known {
		return fmt.Errorf("unknown command %q\n\n%s", name, mainUsage)
	}

	cfg, appEnv, err := o.load()
	if err != nil {
		return err
	}
	if sql.IsMemory(cfg.URL) {
		return fmt.Errorf("%s: DATABASE_URL is %s, so the data is kept in memory and there is no database to change; put your Postgres or MySQL URL in .env first", name, sql.MemoryURL)
	}
	destructive := name == "db:drop" || name == "db:rollback" || name == "db:schema:drop"
	if destructive && appEnv == "production" && !*allowProd {
		return fmt.Errorf("%s: APP_ENV is production; pass --allow-production to run it anyway", name)
	}
	if (name == "db:drop" || name == "db:schema:drop") && !*force {
		return fmt.Errorf("%s: this deletes data; pass --force to run it", name)
	}

	switch name {
	case "db:create":
		return createDatabase(ctx, d, cfg, *ifNotExists, o.stdout)
	case "db:drop":
		return dropDatabase(ctx, d, cfg, *ifExists, o.stdout)
	}

	cfg.MaxOpen, cfg.MaxIdle = 2, 1
	if cfg.ConnectTimeout == 0 {
		cfg.ConnectTimeout = 10 * time.Second
	}
	db, err := sql.Open(ctx, d, cfg)
	if err != nil {
		if msg := err.Error(); strings.Contains(msg, "3D000") || strings.Contains(msg, "Error 1049") {
			return fmt.Errorf("%w\nThe database does not exist yet: create it with dygo db create (db:create)", err)
		}
		return err
	}
	defer db.Close()
	mopts := []Option{Log(o.stdout), Table(o.table)}
	if *outOfOrder {
		mopts = append(mopts, OutOfOrder())
	}
	if *contract || name == "db:schema:create" { // an empty database has no old code to wait for
		mopts = append(mopts, Contract())
	}
	m, err := New(db, fsys, mopts...)
	if err != nil {
		return err
	}

	switch name {
	case "db:migrate":
		target := max(*to, 0)
		if *dryRun {
			pending, err := m.Pending(ctx, target)
			if err != nil {
				return err
			}
			for _, mig := range pending {
				fmt.Fprintf(o.stdout, "-- %s.up.sql\n%s\n", mig, strings.TrimSpace(mig.Up))
			}
			if len(pending) == 0 {
				fmt.Fprintln(o.stdout, "Nothing to migrate.")
			}
			printHeld(o.stdout, m)
			return nil
		}
		done, err := m.Up(ctx, target)
		if err == nil && len(done) == 0 {
			fmt.Fprintln(o.stdout, "Nothing to migrate.")
		}
		if err == nil {
			printHeld(o.stdout, m)
		}
		return err
	case "db:rollback":
		var done []Migration
		switch {
		case *to >= 0 && *steps > 0:
			return errors.New("db:rollback: give --steps or --to, not both")
		case *to >= 0:
			done, err = m.DownTo(ctx, *to)
		default:
			done, err = m.Down(ctx, max(*steps, 1))
		}
		if err == nil && len(done) == 0 {
			fmt.Fprintln(o.stdout, "Nothing to roll back.")
		}
		return err
	case "db:status":
		states, err := m.Status(ctx)
		if err != nil {
			return err
		}
		printStatus(o.stdout, states)
		if !*check {
			return nil
		}
		// --check fails when db:migrate would apply something, not for
		// after-deploy migrations that wait for --contract.
		ready, err := m.Pending(ctx, 0)
		if err != nil {
			return err
		}
		if len(ready) > 0 {
			return exitCode(1)
		}
		return nil
	case "db:schema:create":
		tables, err := listTables(ctx, db, m.kind)
		if err != nil {
			return err
		}
		tables = without(tables, o.table)
		if len(tables) > 0 {
			return fmt.Errorf("db:schema:create: the database already has tables (%s); use db:migrate, or db:schema:drop --force first", strings.Join(tables, ", "))
		}
		_, err = m.Up(ctx, 0)
		return err
	case "db:schema:drop":
		if !*all {
			done, err := m.DownTo(ctx, 0)
			if err == nil && len(done) == 0 {
				fmt.Fprintln(o.stdout, "Nothing to roll back.")
			}
			return err
		}
		return dropTables(ctx, db, m.kind, o.stdout)
	}
	return nil
}

// load reads the database config and APP_ENV.
func (o *mainOptions) load() (sql.Config, string, error) {
	copts := []config.Option{config.Files(o.files...)}
	if o.env != nil {
		copts = []config.Option{config.Env(o.env)}
	}
	type appEnv struct {
		Env string `env:"APP_ENV"`
	}
	ae, err := config.Load[appEnv](copts...)
	if err != nil {
		return sql.Config{}, "", err
	}
	if o.cfg != nil {
		return *o.cfg, ae.Env, nil
	}
	cfg, err := config.Load[sql.Config](append(copts, config.Prefix(o.prefix))...)
	if err != nil {
		return sql.Config{}, "", err
	}
	return *cfg, ae.Env, nil
}

func printStatus(w io.Writer, states []State) (pending int) {
	if len(states) == 0 {
		fmt.Fprintln(w, "No migrations. Add one with: dygo g migration <name>")
		return 0
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "VERSION\tNAME\tSTATUS")
	for _, s := range states {
		status := "pending"
		switch {
		case s.Missing:
			status = "applied " + s.AppliedAt.Local().Format(time.DateTime) + ", file missing"
		case s.Applied:
			status = "applied " + s.AppliedAt.Local().Format(time.DateTime)
		default:
			pending++
			if s.AfterDeploy {
				status = "pending, after deploy (--contract)"
			}
		}
		fmt.Fprintf(tw, "%04d\t%s\t%s\n", s.Version, s.Name, status)
	}
	_ = tw.Flush()
	fmt.Fprintf(w, "\n%d applied, %d pending\n", len(states)-pending, pending)
	return pending
}

// printHeld says which after-deploy migration db:migrate stopped before.
func printHeld(w io.Writer, m *Migrator) {
	if held, ok := m.Held(); ok {
		fmt.Fprintf(w, "Waiting: %s runs after the deploy (%s). Once the new code is live everywhere, run it with dygo db migrate --contract (./app db:migrate --contract).\n", held, AfterDeployMarker)
	}
}

func without(list []string, s string) []string {
	out := list[:0:0]
	for _, x := range list {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}

// listTables lists the tables of the current schema (Postgres) or database
// (MySQL).
func listTables(ctx context.Context, db *sql.DB, kind string) ([]string, error) {
	q := "SELECT tablename FROM pg_tables WHERE schemaname = current_schema() ORDER BY tablename"
	if kind == "mysql" {
		q = "SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_TYPE = 'BASE TABLE' ORDER BY TABLE_NAME"
	}
	rows, err := db.SQL().QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// dropTables drops every table, foreign keys or not.
func dropTables(ctx context.Context, db *sql.DB, kind string, w io.Writer) (err error) {
	tables, err := listTables(ctx, db, kind)
	if err != nil {
		return err
	}
	if len(tables) == 0 {
		fmt.Fprintln(w, "No tables.")
		return nil
	}
	quoted := make([]string, len(tables))
	for i, t := range tables {
		quoted[i] = db.Dialect().Quote(t)
	}
	c, err := db.SQL().Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, c.Close()) }()
	if kind == "postgres" {
		_, err = c.ExecContext(ctx, "DROP TABLE IF EXISTS "+strings.Join(quoted, ", ")+" CASCADE")
	} else {
		// The session setting only lasts on this connection.
		if _, err = c.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 0"); err == nil {
			_, err = c.ExecContext(ctx, "DROP TABLE IF EXISTS "+strings.Join(quoted, ", "))
			_, rerr := c.ExecContext(context.WithoutCancel(ctx), "SET FOREIGN_KEY_CHECKS = 1")
			err = errors.Join(err, rerr)
		}
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "Dropped %d tables: %s\n", len(tables), strings.Join(tables, ", "))
	return nil
}

// createDatabase connects to the server without the database in the URL and
// creates it.
func createDatabase(ctx context.Context, d sql.Dialect, cfg sql.Config, ifNotExists bool, w io.Writer) error {
	return onServer(ctx, d, cfg, func(db *stdsql.DB, kind, name string) error {
		exists, err := databaseExists(ctx, db, kind, name)
		if err != nil {
			return err
		}
		if exists {
			if ifNotExists {
				fmt.Fprintf(w, "Database %s already exists.\n", name)
				return nil
			}
			return fmt.Errorf("db:create: database %s already exists (pass --if-not-exists to accept that)", name)
		}
		if _, err := db.ExecContext(ctx, "CREATE DATABASE "+quoteOne(d, name)); err != nil {
			return fmt.Errorf("db:create: %w", err)
		}
		fmt.Fprintf(w, "Created database %s.\n", name)
		return nil
	})
}

func dropDatabase(ctx context.Context, d sql.Dialect, cfg sql.Config, ifExists bool, w io.Writer) error {
	return onServer(ctx, d, cfg, func(db *stdsql.DB, kind, name string) error {
		exists, err := databaseExists(ctx, db, kind, name)
		if err != nil {
			return err
		}
		if !exists {
			if ifExists {
				fmt.Fprintf(w, "Database %s does not exist.\n", name)
				return nil
			}
			return fmt.Errorf("db:drop: database %s does not exist (pass --if-exists to accept that)", name)
		}
		if _, err := db.ExecContext(ctx, "DROP DATABASE "+quoteOne(d, name)); err != nil {
			return fmt.Errorf("db:drop: %w", err)
		}
		fmt.Fprintf(w, "Dropped database %s.\n", name)
		return nil
	})
}

func databaseExists(ctx context.Context, db *stdsql.DB, kind, name string) (bool, error) {
	q := "SELECT 1 FROM pg_database WHERE datname = $1"
	if kind == "mysql" {
		q = "SELECT 1 FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = ?"
	}
	var one int
	err := db.QueryRowContext(ctx, q, name).Scan(&one)
	if errors.Is(err, stdsql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// quoteOne quotes a database name, which may contain a dot.
func quoteOne(d sql.Dialect, name string) string {
	q := d.Quote("x")
	open, close := q[:1], q[len(q)-1:]
	return open + strings.ReplaceAll(name, close, close+close) + close
}

func onServer(ctx context.Context, d sql.Dialect, cfg sql.Config, fn func(db *stdsql.DB, kind, name string) error) error {
	kind, err := dialectKind(d)
	if err != nil {
		return err
	}
	serverURL, name, err := ServerURL(kind, cfg.URL)
	if err != nil {
		return err
	}
	cfg.URL, cfg.MaxOpen, cfg.MaxIdle = serverURL, 1, 1
	if cfg.ConnectTimeout == 0 {
		cfg.ConnectTimeout = 10 * time.Second
	}
	db, err := sql.Open(ctx, d, cfg)
	if err != nil {
		return err
	}
	defer db.Close()
	return fn(db.SQL(), kind, name)
}

// mysqlDSN splits a driver native MySQL DSN at the database name:
// user:pass@tcp(host:3306)/name?params.
var mysqlDSN = regexp.MustCompile(`^(.*/)([^/?]*)(\?.*)?$`)

// ServerURL returns the URL of the server without the database (Postgres:
// the postgres maintenance database) and the database name, so the database
// itself can be created or dropped. kind is "postgres" or "mysql".
func ServerURL(kind, raw string) (server, name string, err error) {
	if kind == "mysql" && !strings.HasPrefix(raw, "mysql://") {
		m := mysqlDSN.FindStringSubmatch(raw)
		if m == nil || m[2] == "" {
			return "", "", errors.New("no database name in DATABASE_URL")
		}
		return m[1] + m[3], m[2], nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return "", "", fmt.Errorf("DATABASE_URL must be a %s:// URL to create or drop the database", kind)
	}
	name = strings.TrimPrefix(u.Path, "/")
	if name == "" {
		return "", "", errors.New("no database name in DATABASE_URL")
	}
	if kind == "postgres" {
		if name == "postgres" {
			return "", "", errors.New("refusing to create or drop the postgres maintenance database")
		}
		u.Path = "/postgres"
	} else {
		u.Path = "/"
	}
	return u.String(), name, nil
}
