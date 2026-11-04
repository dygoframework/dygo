package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"go/format"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const dbUsage = `Usage: dygo db <command> [flags]

Set up a project:
  init [-driver postgres|mysql] [-url URL | -host -port -user -password -name] [-compose] [-force]
                       pick the database, write DATABASE_URL to .env and .env.example,
                       wire the driver module and the db:* commands into the app,
                       add migrations/ (-compose: a compose.yaml with the database)

Run in the app (dygo db <command> builds the app and runs it with db:<command>):
  create [--if-not-exists]          create the database named in DATABASE_URL
  drop --force [--if-exists]        drop it
  migrate [--to N] [--dry-run] [--out-of-order] [--contract]
                                    apply the pending migrations; it stops before a
                                    -- dygo:after-deploy migration (dygo g migration -safe)
                                    until --contract is given
  rollback [--steps N | --to N]     roll back the newest migration (or N, or down to version N)
  status [--check]                  list migrations; --check exits 1 when some are pending
  schema create                     apply every migration to an empty database
  schema drop --force [--all]       roll back every migration; --all drops every table instead

drop, rollback and schema drop refuse to run with APP_ENV=production unless
--allow-production is given. In production run the binary itself:
./app db:migrate. dygo g migration [name] writes one for the entity changes.
`

// dbAppCommands are the dygo db commands the app runs through migrate.Main.
var dbAppCommands = []string{"create", "drop", "migrate", "rollback", "status"}

func cmdDB(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, dbUsage)
		return exitError(2)
	}
	sub, rest := args[0], args[1:]
	switch {
	case sub == "help" || sub == "-h" || sub == "--help":
		fmt.Fprint(stdout, dbUsage)
		return nil
	case sub == "init":
		return cmdDBInit(rest, stdout)
	case sub == "schema":
		if len(rest) == 0 || (rest[0] != "create" && rest[0] != "drop") {
			return errors.New("dygo db schema: want create or drop")
		}
		return runDBCommand("db:schema:"+rest[0], rest[1:], stdout, stderr)
	case slices.Contains(dbAppCommands, sub):
		return runDBCommand("db:"+sub, rest, stdout, stderr)
	}
	return fmt.Errorf("dygo db: unknown command %q (run dygo db help)", sub)
}

// runDBCommand runs the app's main package with the db:* command, so the
// app's own driver, migrations and .env are used.
func runDBCommand(name string, args []string, stdout, stderr io.Writer) error {
	return runMainPackage("migrate.Main(", "database commands yet: run dygo db init first", append([]string{name}, args...), stdout, stderr)
}

// runMainPackage builds the project's main package and runs it with args from
// the project root, where the app reads .env. Building first (rather than go
// run) passes the exit status through unchanged. The main package must
// contain needle; otherwise the error ends with missing.
func runMainPackage(needle, missing string, args []string, stdout, stderr io.Writer) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	proj, err := findProject(cwd)
	if err != nil {
		return err
	}
	mainDir := filepath.Join(proj.Dir, filepath.FromSlash(cmp(proj.Config.Main, ".")))
	if !packageContains(mainDir, needle) {
		return fmt.Errorf("the main package (%s) does not run %s", rel(cwd, mainDir), missing)
	}
	tmp, err := os.MkdirTemp("", "dygo-run-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	bin := filepath.Join(tmp, "app")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = mainDir
	build.Stdout, build.Stderr = stderr, stderr
	if err := build.Run(); err != nil {
		return fmt.Errorf("building %s: %w", rel(cwd, mainDir), err)
	}
	run := exec.Command(bin, args...)
	run.Dir = proj.Dir
	run.Stdin, run.Stdout, run.Stderr = os.Stdin, stdout, stderr
	if err := run.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return exitError(ee.ExitCode())
		}
		return err
	}
	return nil
}

// dbSettings are the connection settings dygo db init asks for.
type dbSettings struct {
	Driver   string // postgres or mysql
	URL      string // when given, the rest is ignored
	Host     string
	Port     int
	User     string
	Password string
	Name     string
}

func (s *dbSettings) defaults(project string) {
	if s.Host == "" {
		s.Host = "localhost"
	}
	if s.Port == 0 {
		s.Port = map[string]int{"postgres": 5432, "mysql": 3306}[s.Driver]
	}
	if s.User == "" {
		s.User = "app"
	}
	if s.Password == "" {
		s.Password = "app"
	}
	if s.Name == "" {
		s.Name = dbName(project)
	}
}

// dbName turns a project name into a database name: my-shop to my_shop.
func dbName(project string) string {
	words := splitWords(project)
	if len(words) == 0 {
		return "app"
	}
	return strings.Join(words, "_")
}

// url builds DATABASE_URL, with the password replaced when password is set.
func (s dbSettings) url(password string) string {
	if s.URL != "" {
		if password == "" {
			return s.URL
		}
		if u, err := url.Parse(s.URL); err == nil && u.User != nil {
			if _, ok := u.User.Password(); ok {
				u.User = url.UserPassword(u.User.Username(), password)
				return u.String()
			}
		}
		return s.URL
	}
	pw := s.Password
	if password != "" {
		pw = password
	}
	u := url.URL{
		Scheme: s.Driver,
		User:   url.UserPassword(s.User, pw),
		Host:   s.Host + ":" + strconv.Itoa(s.Port),
		Path:   "/" + s.Name,
	}
	if s.Driver == "postgres" && (s.Host == "localhost" || s.Host == "127.0.0.1") {
		u.RawQuery = "sslmode=disable"
	}
	return u.String()
}

// dbInitOptions are the dygo db init flags.
type dbInitOptions struct {
	DB       dbSettings
	Compose  bool
	Force    bool
	SkipTidy bool
	Prompt   io.Reader // ask for the settings that were not given
}

func cmdDBInit(args []string, stdout io.Writer) error {
	fs := newFlags("db init")
	var o dbInitOptions
	fs.StringVar(&o.DB.Driver, "driver", "", "postgres or mysql")
	fs.StringVar(&o.DB.URL, "url", "", "the full DATABASE_URL (instead of -host, -port, ...)")
	fs.StringVar(&o.DB.Host, "host", "", "database host (default localhost)")
	fs.IntVar(&o.DB.Port, "port", 0, "database port (default 5432 or 3306)")
	fs.StringVar(&o.DB.User, "user", "", "database user (default app)")
	fs.StringVar(&o.DB.Password, "password", "", "database password (default app)")
	fs.StringVar(&o.DB.Name, "name", "", "database name (default: the project name)")
	fs.BoolVar(&o.Compose, "compose", false, "write compose.yaml with the database")
	fs.BoolVar(&o.Force, "force", false, "replace a DATABASE_URL already set in .env")
	fs.BoolVar(&o.SkipTidy, "skip-tidy", false, "do not run go mod tidy")
	pos, _, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return fmt.Errorf("dygo db init: unexpected argument %q", pos[0])
	}
	if o.DB.Driver == "" && isTerminal(os.Stdin) {
		o.Prompt = os.Stdin
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	proj, err := findProject(cwd)
	if err != nil {
		return err
	}
	return dbInit(cwd, proj, o, stdout)
}

var driverAliases = map[string]string{"postgres": "postgres", "postgresql": "postgres", "pg": "postgres", "mysql": "mysql"}

// dbInit sets up the database in proj; dygo new --db uses it too.
func dbInit(cwd string, proj project, o dbInitOptions, stdout io.Writer) error {
	projectName := filepath.Base(proj.Dir)
	if o.Prompt != nil {
		if err := promptDB(o.Prompt, stdout, &o.DB, projectName); err != nil {
			return err
		}
	}
	if o.DB.URL != "" && o.DB.Driver == "" {
		scheme, _, _ := strings.Cut(o.DB.URL, "://")
		o.DB.Driver = scheme
	}
	driver, ok := driverAliases[strings.ToLower(o.DB.Driver)]
	if !ok {
		if o.DB.Driver == "" {
			return errors.New("dygo db init: pass -driver postgres or -driver mysql")
		}
		return fmt.Errorf("unknown database %q: want postgres or mysql", o.DB.Driver)
	}
	o.DB.Driver = driver
	o.DB.defaults(projectName)

	// Check everything before writing anything.
	envFile := filepath.Join(proj.Dir, ".env")
	if !o.Force {
		if v, ok := envValue(envFile, "DATABASE_URL"); ok && v != "" && v != "memory://" && v != o.DB.url("") {
			return fmt.Errorf("DATABASE_URL is already set in %s; pass -force to replace it", rel(cwd, envFile))
		}
	}
	mainDir := filepath.Join(proj.Dir, filepath.FromSlash(cmp(proj.Config.Main, ".")))
	mainFile, err := findMainFile(mainDir)
	if err != nil {
		return err
	}

	data := tmplData{Framework: frameworkPath, Name: projectName, Driver: driver, DB: o.DB}
	write := func(path string, content []byte, what string) error {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			return err
		}
		fmt.Fprintln(stdout, what, rel(cwd, path))
		return nil
	}

	// 1. .env and .env.example.
	if err := setEnv(envFile, "DATABASE_URL", o.DB.url(""), envHeader+"\n"); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "UPDATE", rel(cwd, envFile), "(DATABASE_URL)")
	example := filepath.Join(proj.Dir, ".env.example")
	if err := setEnv(example, "DATABASE_URL", o.DB.url("change-me"), envExampleHeader+"\n"); err != nil {
		return err
	}
	if src, _ := os.ReadFile(example); !bytes.Contains(src, []byte("DB_MAX_OPEN")) {
		pool := "\n# Database connection pool. Uncomment to change; these are the defaults.\n# DB_MAX_OPEN=25\n# DB_MAX_IDLE=25\n# DB_CONN_MAX_LIFETIME=30m\n# DB_CONNECT_TIMEOUT=5s\n"
		if err := os.WriteFile(example, append(src, pool...), 0o644); err != nil {
			return err
		}
	}
	fmt.Fprintln(stdout, "UPDATE", rel(cwd, example), "(DATABASE_URL)")

	// 2. migrations/.
	migDir := migrationsDir(proj)
	embedFile := filepath.Join(migDir, "embed.go")
	if _, err := os.Stat(embedFile); errors.Is(err, os.ErrNotExist) {
		out, err := render([]file{{embedFile, "db/embed.go.tmpl"}}, data)
		if err != nil {
			return err
		}
		if err := write(embedFile, out[embedFile], "CREATE"); err != nil {
			return err
		}
	}

	// 3. The driver module in the app.
	dbFile := filepath.Join(proj.Dir, "src", "database", "database.go")
	if _, err := os.Stat(dbFile); err != nil {
		dbFile = filepath.Join(proj.Dir, "internal", "database", "database.go") // older modular projects
	}
	src, err := os.ReadFile(dbFile)
	switch {
	case err == nil && (bytes.Contains(src, []byte("in-memory placeholder")) || bytes.Contains(src, []byte("sql.Module(nil"))):
		out, err := render([]file{{dbFile, "db/database.go.tmpl"}}, data)
		if err != nil {
			return err
		}
		if err := write(dbFile, out[dbFile], "UPDATE"); err != nil {
			return err
		}
	case err == nil && bytes.Contains(src, []byte(driver+".Module(")):
		// database.go already opens this driver: a second init must not add
		// another module to the app.
	default:
		if err := wireDriverModule(cwd, proj, driver, stdout); err != nil {
			return err
		}
	}

	// 4. The db:* commands in main.
	if err := wireMigrateMain(cwd, proj, mainFile, migDir, driver, stdout); err != nil {
		return err
	}

	// 5. compose.yaml.
	if o.Compose {
		compose := filepath.Join(proj.Dir, "compose.yaml")
		if _, err := os.Stat(compose); err == nil {
			fmt.Fprintf(stdout, "%s exists: add a %s service yourself.\n", rel(cwd, compose), driver)
		} else {
			out, err := render([]file{{compose, "db/compose.yaml.tmpl"}}, data)
			if err != nil {
				return err
			}
			if err := write(compose, out[compose], "CREATE"); err != nil {
				return err
			}
		}
	}

	// 6. go.mod: the sql module, from the same checkout as dygo when it is
	// replaced.
	if err := addSQLModule(proj); err != nil {
		return err
	}
	if !o.SkipTidy {
		cmd := exec.Command("go", "mod", "tidy")
		cmd.Dir = proj.Dir
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintf(stdout, "\ngo mod tidy failed; fix the error and run it again in %s:\n%s", proj.Dir, out)
			return nil
		}
	}
	up := "start the database"
	if o.Compose {
		up = "docker compose up -d"
	}
	fmt.Fprintf(stdout, "\nNext:\n"+
		"  %-32s # %s on %s:%d\n"+
		"  dygo db create                   # create the database %s\n"+
		"  dygo g migration                 # write the SQL for your entities in migrations/\n"+
		"  dygo db migrate                  # apply them; dygo db status lists them\n",
		up, driver, o.DB.Host, o.DB.Port, o.DB.Name)
	return nil
}

func promptDB(in io.Reader, out io.Writer, s *dbSettings, project string) error {
	r := bufio.NewReader(in)
	ask := func(q, def string) (string, error) {
		fmt.Fprintf(out, "%s [%s]: ", q, def)
		line, err := r.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		if line = strings.TrimSpace(line); line == "" {
			return def, nil
		}
		return line, nil
	}
	var err error
	for {
		if s.Driver, err = ask("Database (postgres or mysql)", "postgres"); err != nil {
			return err
		}
		if _, ok := driverAliases[strings.ToLower(s.Driver)]; ok {
			s.Driver = driverAliases[strings.ToLower(s.Driver)]
			break
		}
		fmt.Fprintln(out, "Please answer postgres or mysql.")
	}
	s.defaults(project)
	if s.Host, err = ask("Host", s.Host); err != nil {
		return err
	}
	port, err := ask("Port", strconv.Itoa(s.Port))
	if err != nil {
		return err
	}
	if s.Port, err = strconv.Atoi(port); err != nil {
		return fmt.Errorf("port %q is not a number", port)
	}
	for _, f := range []struct {
		q string
		v *string
	}{{"User", &s.User}, {"Password", &s.Password}, {"Database name", &s.Name}} {
		if *f.v, err = ask(f.q, *f.v); err != nil {
			return err
		}
	}
	return nil
}

// envLine matches KEY=value in a .env file.
var envLine = regexp.MustCompile(`^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=(.*)$`)

func envValue(path, key string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if m := envLine.FindStringSubmatch(line); m != nil && m[1] == key {
			return strings.Trim(strings.TrimSpace(m[2]), `"'`), true
		}
	}
	return "", false
}

// setEnv sets key=value in a .env file, replacing the existing line or
// appending one, and creates the file with header when it is missing.
// envComments explain the variables setEnv adds, like the comments in the
// .env.example that dygo new writes.
var envComments = map[string]string{
	"DATABASE_URL": `# Database connection, written by dygo db init. Formats:
#   postgres://USER:PASSWORD@HOST:5432/NAME?sslmode=disable
#   mysql://USER:PASSWORD@HOST:3306/NAME
# Services that dygo g crud wrote in memory do not use it.`,
}

func setEnv(path, key, value, header string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		data, err = []byte(header), nil
	}
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	found := false
	for i, line := range lines {
		if m := envLine.FindStringSubmatch(line); m != nil && m[1] == key {
			lines[i], found = key+"="+value, true
		}
	}
	out := strings.Join(lines, "\n")
	if !found {
		if out != "" && !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		if c := envComments[key]; c != "" {
			out += "\n" + c + "\n"
		}
		out += key + "=" + value + "\n"
	}
	return os.WriteFile(path, []byte(out), 0o644)
}

func migrationsDir(proj project) string {
	return filepath.Join(proj.Dir, filepath.FromSlash(cmp(proj.Config.Migrations, "migrations")))
}

// findMainFile returns the file with func main in dir.
func findMainFile(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if src, err := os.ReadFile(path); err == nil && bytes.Contains(src, []byte("\nfunc main() {")) {
			return path, nil
		}
	}
	return "", fmt.Errorf("no func main in %s (set \"main\" in dygo.json)", dir)
}

// wireDriverModule imports postgres.Module() or mysql.Module() in the root
// module, unless a driver module is there already.
func wireDriverModule(cwd string, proj project, driver string, stdout io.Writer) error {
	root := filepath.Join(proj.Dir, filepath.FromSlash(cmp(proj.Config.RootModuleDir, cmp(proj.Config.Main, "."))))
	if packageContains(root, "postgres.Module(") || packageContains(root, "mysql.Module(") {
		return nil
	}
	expr := driver + ".Module()"
	if done, err := replaceMemoryModule(cwd, root, driver, stdout); done || err != nil {
		return err
	}
	sites, err := findModules(root)
	if err != nil {
		return err
	}
	site, ok := pickModule(sites)
	if !ok {
		fmt.Fprintf(stdout, "\nNo root module found in %s: import %s (%q) in it yourself.\n", rel(cwd, root), expr, frameworkPath+"/sql/"+driver)
		return nil
	}
	changed, err := editModule(site, moduleEdit{Field: "Imports", Expr: expr, ImportPath: frameworkPath + "/sql/" + driver})
	if err != nil {
		return err
	}
	if changed {
		fmt.Fprintf(stdout, "UPDATE %s (Imports += %s)\n", rel(cwd, site.File), expr)
	}
	return nil
}

// replaceMemoryModule turns the sql.Module(nil) that dygo new puts in the
// root module into the driver's module, so the app keeps one database.
func replaceMemoryModule(cwd, root, driver string, stdout io.Writer) (bool, error) {
	files, err := filepath.Glob(filepath.Join(root, "*.go"))
	if err != nil {
		return false, err
	}
	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil || !bytes.Contains(src, []byte("sql.Module(nil)")) {
			continue
		}
		out := bytes.Replace(src, []byte("sql.Module(nil)"), []byte(driver+".Module()"), 1)
		if out, err = addImport(path, out, frameworkPath+"/sql/"+driver); err != nil {
			return false, err
		}
		if !regexp.MustCompile(`\bsql\.`).Match(out) {
			out = bytes.Replace(out, []byte("\t\""+frameworkPath+"/sql\"\n"), nil, 1)
		}
		if out, err = format.Source(out); err != nil {
			return false, err
		}
		if err := os.WriteFile(path, out, 0o644); err != nil {
			return false, err
		}
		fmt.Fprintf(stdout, "UPDATE %s (sql.Module(nil) -> %s.Module())\n", rel(cwd, path), driver)
		return true, nil
	}
	return false, nil
}

// mainAnchor is where dygo new's main creates its context; the db:*
// commands go right after it, before the console commands.
var mainAnchor = regexp.MustCompile(`(?m)^(\s*)ctx := context\.Background\(\)\n`)

// wireMigrateMain makes main run migrate.Main before anything else.
func wireMigrateMain(cwd string, proj project, mainFile, migDir, driver string, stdout io.Writer) error {
	src, err := os.ReadFile(mainFile)
	if err != nil {
		return err
	}
	if bytes.Contains(src, []byte("migrate.Main(")) {
		return nil
	}
	call := fmt.Sprintf("if code, ok := migrate.Main(ctx, %s.Dialect(), migrations.FS, os.Args[1:]); ok { // go run . db:migrate (dygo db)\n\tos.Exit(code)\n}\n", driver)
	imports := []string{frameworkPath + "/sql/migrate", frameworkPath + "/sql/" + driver, proj.importPath(migDir)}
	loc := mainAnchor.FindSubmatchIndex(src)
	if loc == nil {
		fmt.Fprintf(stdout, "\nAdd this to main in %s, before the app is created:\n\n\t%s\nwith the imports %q.\n",
			rel(cwd, mainFile), strings.ReplaceAll(call, "\n", "\n\t"), imports)
		return nil
	}
	indent := string(src[loc[2]:loc[3]])
	call = indent + strings.ReplaceAll(strings.TrimSuffix(call, "\n"), "\n", "\n"+indent) + "\n"
	out := slices.Concat(src[:loc[1]], []byte(call), src[loc[1]:])
	for _, imp := range imports {
		if out, err = addImport(mainFile, out, imp); err != nil {
			return err
		}
	}
	if out, err = format.Source(out); err != nil {
		return err
	}
	if err := os.WriteFile(mainFile, out, 0o644); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "UPDATE", rel(cwd, mainFile), "(db:* commands)")
	return nil
}

// addSQLModule adds a replace for the sql module when dygo itself is
// replaced by a local checkout, as dygo new -replace does; go mod tidy then
// adds the require.
func addSQLModule(proj project) error {
	path := filepath.Join(proj.Dir, "go.mod")
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if bytes.Contains(src, []byte(frameworkPath+"/sql ")) {
		return nil
	}
	m := regexp.MustCompile(`(?m)^replace\s+` + regexp.QuoteMeta(frameworkPath) + `\s+=>\s+(\S+)\s*$`).FindSubmatch(src)
	if m == nil {
		return nil
	}
	dir := string(m[1])
	if !strings.HasPrefix(dir, ".") && !filepath.IsAbs(dir) {
		return nil // replaced by another module version, not a directory
	}
	line := fmt.Sprintf("\nreplace %s/sql => %s/sql\n", frameworkPath, filepath.ToSlash(dir))
	return os.WriteFile(path, append(src, line...), 0o644)
}

// generateMigration writes the next NNNN_name.up.sql and .down.sql pair,
// and migrations/embed.go when it is missing.
func generateMigration(cwd string, proj project, name string, o genOptions, stdout io.Writer) error {
	dryRun := o.DryRun
	auto := name == "" // no name: write files only when an entity changed
	if auto {
		name = "update_schema"
	}
	words := splitWords(name)
	if len(words) == 0 || !isLetter(words[0][0]) {
		return fmt.Errorf("invalid migration name %q: use words like create_users", name)
	}
	snake := strings.Join(words, "_")
	dir := migrationsDir(proj)
	next, width := 1, 4
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		m := migrationFile.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		if m[2] == snake && !auto {
			return fmt.Errorf("a migration named %s already exists: %s", snake, e.Name())
		}
		v, _ := strconv.Atoi(m[1])
		next, width = max(next, v+1), max(width, len(m[1]))
	}
	base := fmt.Sprintf("%0*d_%s", width, next, snake)
	data := tmplData{Framework: frameworkPath, Name: filepath.Base(proj.Dir), Migration: base}
	files := []file{
		{filepath.Join(dir, base+".up.sql"), "db/migration.up.sql.tmpl"},
		{filepath.Join(dir, base+".down.sql"), "db/migration.down.sql.tmpl"},
	}
	embedMissing := false
	if _, err := os.Stat(filepath.Join(dir, "embed.go")); errors.Is(err, os.ErrNotExist) {
		files = append(files, file{filepath.Join(dir, "embed.go"), "db/embed.go.tmpl"})
		embedMissing = true
	}
	contents, err := render(files, data)
	if err != nil {
		return err
	}
	mo := migrationOptions{Renames: o.Renames, Safe: o.Safe}
	if o.Prompt != nil {
		sc := bufio.NewScanner(o.Prompt)
		mo.Ask = func(q string) bool {
			fmt.Fprint(stdout, q)
			if !sc.Scan() {
				fmt.Fprintln(stdout)
				return false
			}
			a := strings.ToLower(strings.TrimSpace(sc.Text()))
			return a == "y" || a == "yes"
		}
	}
	filled, err := fillMigration(proj, dir, words, mo)
	if err != nil {
		return err
	}
	if auto && filled.Up == "" {
		fmt.Fprintln(stdout, "No changes: every table matches its entity in the migrations.")
		return nil
	}
	if filled.Up != "" {
		contents[files[0].path] = append(contents[files[0].path], filled.Up...)
		contents[files[1].path] = append(contents[files[1].path], filled.Down...)
	}
	if filled.ContractUp != "" {
		// The contract half is the next version, held back until --contract.
		cbase := fmt.Sprintf("%0*d_%s_contract", width, next+1, snake)
		cfiles := []file{
			{filepath.Join(dir, cbase+".up.sql"), "db/migration.up.sql.tmpl"},
			{filepath.Join(dir, cbase+".down.sql"), "db/migration.down.sql.tmpl"},
		}
		cc, err := render(cfiles, tmplData{Framework: frameworkPath, Name: filepath.Base(proj.Dir), Migration: cbase})
		if err != nil {
			return err
		}
		contents[cfiles[0].path] = append([]byte(afterDeployHeader), append(cc[cfiles[0].path], filled.ContractUp...)...)
		contents[cfiles[1].path] = append(cc[cfiles[1].path], filled.ContractDown...)
		files = append(files[:2:2], append(cfiles, files[2:]...)...)
	}
	if err := writeNew(files, contents, cwd, stdout, dryRun); err != nil {
		return err
	}
	if filled.Message != "" {
		fmt.Fprintln(stdout, "\n"+filled.Message)
	}
	for i, w := range filled.Warnings {
		if i == 0 {
			fmt.Fprintln(stdout)
		}
		fmt.Fprintln(stdout, "WARNING:", w)
	}
	mainDir := filepath.Join(proj.Dir, filepath.FromSlash(cmp(proj.Config.Main, ".")))
	if embedMissing || !packageContains(mainDir, "migrate.Main(") {
		fmt.Fprintln(stdout, "\nThe app does not run migrations yet: dygo db init wires them (and DATABASE_URL).")
	}
	if filled.ContractUp != "" {
		fmt.Fprintf(stdout, "Next: review the four files, then:\n"+
			"  1. dygo db migrate             applies %s (expand); old code keeps working\n"+
			"  2. deploy the new code everywhere\n"+
			"  3. dygo db migrate --contract  applies %s (drops what only the old code used)\n",
			filepath.Base(strings.TrimSuffix(files[0].path, ".up.sql")), filepath.Base(strings.TrimSuffix(files[2].path, ".up.sql")))
		return nil
	}
	if filled.Up != "" {
		fmt.Fprintf(stdout, "Next: review %s and %s, then dygo db migrate.\n", rel(cwd, files[0].path), rel(cwd, files[1].path))
		return nil
	}
	fmt.Fprintf(stdout, "\nNext: write the SQL in %s and its undo in %s, then dygo db migrate.\n",
		rel(cwd, files[0].path), rel(cwd, files[1].path))
	return nil
}

// afterDeployHeader starts a contract migration: dygo db migrate stops
// before it until --contract is given (migrate.AfterDeployMarker).
const afterDeployHeader = "-- dygo:after-deploy\n-- Runs with dygo db migrate --contract, once the new code is deployed everywhere.\n"

var migrationFile = regexp.MustCompile(`^(\d+)_(.+)\.(?:up|down)\.sql$`)

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
