package main

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"text/template"
)

//go:embed templates
var templates embed.FS

// tmplData is what the templates see.
type tmplData struct {
	names
	Framework  string      // the dygo import path
	Name       string      // dygo new: the project name
	Module     string      // dygo new: the project's module path
	Replace    string      // dygo new: a local dygo checkout for a replace directive
	Fields     fieldSet    // entity and resource fields
	Routes     []testRoute // dygo g test
	Driver     string      // dygo db init: postgres or mysql
	DB         dbSettings  // dygo db init: the connection settings
	Migration  string      // dygo g migration: NNNN_name
	FlatPkg    string      // a simple project's root package: write flat files there, not a package
	IsEmail    bool        // the package already has isEmail, so the entity file must not repeat it
	UsesSQL    bool        // dygo g test: the module needs a *sql.DB
	ImportPath string      // dygo g security: the import path of the generated package
	Sec        *secInfo    // controller and CRUD in a security project
	OIDC       bool        // dygo g security -oidc
}

// Table is the entity's table name, as dygo g migration derives it.
func (d tmplData) Table() string { return tableName(nil, d.Entity) }

// Package is the package clause: the feature's own package, or the root
// package of a simple project.
func (d tmplData) Package() string {
	if d.FlatPkg != "" {
		return d.FlatPkg
	}
	return d.Pkg
}

// P prefixes type and function names. A feature package has Service and
// NewService; flat files in one package need ProductsService and
// NewProductsService so features do not clash.
func (d tmplData) P() string {
	if d.FlatPkg != "" {
		return d.Pascal
	}
	return ""
}

// Local names an unexported helper: notFound in a feature package,
// productsNotFound in flat files.
func (d tmplData) Local(name string) string {
	if d.FlatPkg == "" {
		return name
	}
	return strings.ToLower(d.Pascal[:1]) + d.Pascal[1:] + strings.ToUpper(name[:1]) + name[1:]
}

// Imports are the standard packages the entity file needs. net/mail is left
// out when isEmail is in another file of the package.
func (d tmplData) Imports() []string {
	if !d.IsEmail {
		return d.Fields.Imports()
	}
	return slices.DeleteFunc(d.Fields.Imports(), func(p string) bool { return p == "net/mail" })
}

// flatName is the file name for base in flat mode: module.go becomes
// products.module.go, and the entity file products.go stays as it is.
func flatName(n names, base string) string {
	if base == n.Pkg+".go" {
		return base
	}
	return n.Pkg + "." + base
}

// file is one file to create: the target path and its template.
type file struct {
	path, tmpl string
}

// render executes the templates. Go files are gofmt'ed, so a template mistake
// is caught here rather than by the user's build.
func render(files []file, data tmplData) (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, f := range files {
		t, err := template.ParseFS(templates, "templates/"+f.tmpl)
		if err != nil {
			return nil, err
		}
		var buf bytes.Buffer
		if err := t.Execute(&buf, data); err != nil {
			return nil, err
		}
		src := buf.Bytes()
		if strings.HasSuffix(f.path, ".go") {
			if src, err = format.Source(src); err != nil {
				return nil, fmt.Errorf("template %s: %w", f.tmpl, err)
			}
		}
		out[f.path] = src
	}
	return out, nil
}

// writeNew writes files that must not exist yet. It checks them all first,
// so nothing is written when one is in the way.
func writeNew(files []file, contents map[string][]byte, base string, stdout io.Writer, dryRun bool) error {
	var existing []string
	for _, f := range files {
		if _, err := os.Stat(f.path); err == nil {
			existing = append(existing, rel(base, f.path))
		}
	}
	if len(existing) > 0 {
		return fmt.Errorf("already exists: %s", strings.Join(existing, ", "))
	}
	for _, f := range files {
		fmt.Fprintln(stdout, "CREATE", rel(base, f.path))
		if dryRun {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(f.path, contents[f.path], 0o644); err != nil {
			return err
		}
	}
	return nil
}

func rel(base, path string) string {
	if r, err := filepath.Rel(base, path); err == nil {
		return r
	}
	return path
}

func cmdNew(args []string, stdout io.Writer) error {
	fs := newFlags("new")
	module := fs.String("module", "", "module path (default: the project name)")
	replace := fs.String("replace", "", "use a local dygo checkout through a replace directive")
	skipTidy := fs.Bool("skip-tidy", false, "do not run go mod tidy")
	layout := fs.String("template", "simple", "project layout: simple (everything in package main) or modular (main.go, app.module.go, src/modules, ...)")
	fs.StringVar(layout, "t", "simple", "short for -template")
	db := fs.String("db", "", "set up a database: postgres or mysql (DATABASE_URL in .env, migrations/, compose.yaml)")
	secure := fs.Bool("security", false, "secure the API: every route needs a token (runs dygo g security)")
	oidc := fs.Bool("oidc", false, "with -security: check the tokens of an identity provider (runs dygo g security -oidc)")
	pos, _, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	switch {
	case len(pos) == 0:
		return errors.New("dygo new: missing project name (example: dygo new shop, or dygo new shop -t modular -db postgres)")
	case len(pos) > 1:
		return fmt.Errorf("dygo new: unexpected argument %q (flags may come before or after the name)", pos[1])
	}
	if *oidc && !*secure {
		return errors.New("dygo new: -oidc goes with -security")
	}
	dir, err := filepath.Abs(pos[0])
	if err != nil {
		return err
	}
	name := filepath.Base(dir)
	if _, err := newNames(name); err != nil {
		return err
	}
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return fmt.Errorf("%s exists and is not empty", dir)
	}
	data := tmplData{Framework: frameworkPath, Name: name, Module: *module}
	if data.Module == "" {
		data.Module = name
	}
	if *replace != "" {
		abs, err := filepath.Abs(*replace)
		if err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(abs, "go.mod")); err != nil {
			return fmt.Errorf("-replace: %w", err)
		}
		data.Replace = abs
		// A nearby checkout stays relative, so the project can move with it.
		if r, err := filepath.Rel(dir, abs); err == nil && strings.Count(r, "..") <= 2 {
			data.Replace = filepath.ToSlash(r)
		}
	}
	layoutFiles, ok := layouts[*layout]
	if !ok {
		return fmt.Errorf("unknown -template %q: want simple or modular", *layout)
	}
	files := []file{{filepath.Join(dir, "go.mod"), "new/go.mod.tmpl"}}
	for _, f := range layoutFiles {
		files = append(files, file{filepath.Join(dir, filepath.FromSlash(f.path)), f.tmpl})
	}
	contents, err := render(files, data)
	if err != nil {
		return err
	}
	// .env is .env.example with its own header, so the project runs as soon
	// as it is created and the two files start out the same.
	example, env := filepath.Join(dir, ".env.example"), filepath.Join(dir, ".env")
	body := contents[example]
	contents[example] = append([]byte(envExampleHeader+"\n"), body...)
	contents[env] = append([]byte(envHeader+"\n"), body...)
	files = append(files, file{path: env})
	if err := writeNew(files, contents, filepath.Dir(dir), stdout, false); err != nil {
		return err
	}
	if *db != "" {
		proj, err := findProject(dir)
		if err != nil {
			return err
		}
		o := dbInitOptions{DB: dbSettings{Driver: *db}, Compose: true, SkipTidy: true}
		if err := dbInit(filepath.Dir(dir), proj, o, io.Discard); err != nil {
			return fmt.Errorf("-db: %w", err)
		}
		for _, f := range []string{"compose.yaml", "migrations/embed.go"} {
			fmt.Fprintln(stdout, "CREATE", filepath.Join(filepath.Base(dir), filepath.FromSlash(f)))
		}
	}
	if *secure {
		if err := generate(dir, "security", "security", genOptions{OIDC: *oidc}, io.Discard); err != nil {
			return fmt.Errorf("-security: %w", err)
		}
		fmt.Fprintln(stdout, "CREATE", filepath.Join(filepath.Base(dir), "security")+string(filepath.Separator), "(dygo g security: every route needs a token)")
	}
	if !*skipTidy {
		cmd := exec.Command("go", "mod", "tidy")
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintf(stdout, "\ngo mod tidy failed; fix the error and run it again in %s:\n%s", dir, out)
			return nil
		}
	}
	if *db != "" {
		fmt.Fprintf(stdout, nextStepsDB, name, pos[0], *db)
		return nil
	}
	fmt.Fprintf(stdout, nextSteps, name, pos[0])
	return nil
}

// envHeader starts .env, and envExampleHeader .env.example. dygo db init
// uses them too when it has to create one of the files.
const (
	envHeader = `# .env: settings for running this app on your machine.
# Not committed (git ignored). .env.example is the shared template.
# Real environment variables win over this file, and .env.local wins too.
`
	envExampleHeader = `# .env.example: every setting this app reads, with safe values.
# Commit this file. On a new machine: cp .env.example .env
# Never put real passwords or secrets here; they go in .env.
`
)

// nextSteps is printed after dygo new: only what gets the app running.
// dygo help lists the rest.
const nextSteps = `
Created %s. Next:

  cd %s
  dygo start               # http://localhost:9900, API docs at /docs

dygo help lists every command.
`

// nextStepsDB is printed after dygo new -db.
const nextStepsDB = `
Created %s with a %[3]s database. Next:

  cd %[2]s
  docker compose up -d     # start %[3]s
  dygo start               # http://localhost:9900, API docs at /docs

dygo help db shows the migration commands.
`

// layouts are the dygo new templates: target path and template file.
var layouts = map[string][]file{
	"simple": {
		{"main.go", "new/main.go.tmpl"},
		{"config.go", "new/config.go.tmpl"},
		{"app.go", "new/app.go.tmpl"},
		{"app.controller.go", "new/app_controller.go.tmpl"},
		{"dygo.json", "new/dygo.json.tmpl"},
		{"app_test.go", "new/app_test.go.tmpl"},
		{".env.example", "new/env.example.tmpl"},
		{".gitignore", "new/gitignore.tmpl"},
		{"Dockerfile", "new/Dockerfile.tmpl"},
		{"README.md", "new/README.md.tmpl"},
	},
	"modular": {
		{"main.go", "modular/main.go.tmpl"},
		{"app.module.go", "modular/app.module.go.tmpl"},
		{"app_test.go", "modular/app_test.go.tmpl"},
		{"src/config/config.go", "modular/config.go.tmpl"},
		{"src/database/database.go", "modular/database.go.tmpl"},
		{"src/health/health.go", "modular/health.go.tmpl"},
		{"src/common/pagination.go", "modular/common.go.tmpl"},
		{"src/modules/doc.go", "modular/modules.go.tmpl"},
		{"dygo.json", "modular/dygo.json.tmpl"},
		{".env.example", "modular/env.example.tmpl"},
		{".gitignore", "modular/gitignore.tmpl"},
		{"Dockerfile", "modular/Dockerfile.tmpl"},
		{"README.md", "modular/README.md.tmpl"},
	},
}

var kinds = map[string]string{
	"module": "module", "mo": "module",
	"service": "service", "s": "service",
	"controller": "controller", "co": "controller",
	"resource": "resource", "res": "resource", "crud": "crud",
	"entity": "entity", "en": "entity",
	"command": "command", "cmd": "command",
	"test":     "test",
	"auth":     "auth",
	"security": "security", "sec": "security",
	// Phase 6 packages.
	"listener": "listener", "li": "listener",
	"cron":    "cron",
	"job":     "job",
	"gateway": "gateway", "ga": "gateway",
	"rpc": "rpc",
	// Phase 8.
	"migration": "migration", "mig": "migration",
}

// kindList names the kinds in error messages.
const kindList = "module, service, controller, resource, entity, crud, command, test, auth, security, listener, cron, job, gateway, rpc or migration"

// kindExamples are sample names for the missing-name error.
var kindExamples = map[string]string{
	"module": "billing", "service": "mailer", "controller": "health", "resource": "products",
	"entity": "Product", "crud": "Product", "command": "seed", "test": "products", "auth": "auth", "security": "security",
	"listener": "notify", "cron": "reports", "job": "welcome-email", "gateway": "chat",
	"rpc": "users-rpc", "migration": "create_users",
}

// pkgKinds describes the generators of the background work packages: the files, the
// constructor registered as a provider, and the module the root must import.
var pkgKinds = map[string]struct {
	file, ctor, pkg, module, next string
}{
	"listener": {"listener", "NewListener", "events", "events.Module()", "emit {pkg}.Event from a service that injects *events.Emitter"},
	"cron":     {"jobs", "NewJobs", "schedule", "schedule.Module()", "the job runs every hour; change the schedule in {pkg}/jobs.go"},
	"job":      {"processor", "NewProcessor", "queue", "queue.Module(queue.Options{})", "add jobs with q.Add(ctx, {pkg}.JobName, {pkg}.Payload{...}) from a service that injects *queue.Queue"},
	"gateway":  {"gateway", "NewGateway", "ws", "ws.Module(ws.Options{})", "connect to /ws and send {\"event\": \"{kebab}.ping\", \"data\": {\"text\": \"hi\"}}; run go get " + frameworkPath + "/ws"},
	"rpc":      {"handlers", "NewHandlers", "micro", "", "serve the handlers with app.ConnectMicroservice(nats.NewServer(...)) from " + frameworkPath + "/micro/nats"},
}

// genOptions are the dygo generate flags.
type genOptions struct {
	DryRun bool
	Fields []entityField // entity and resource; none means ask or use name:string
	Prompt io.Reader     // when set and no fields are given, ask for them

	Public     bool   // security project: every new route is public (-public)
	PublicRead bool   // security project, crud: list and get are public (-public-read)
	WriteRole  string // security project, crud: role for writes (-write-role, default admin, "-" for any signed in user)
	OIDC       bool   // security: check the tokens of an identity provider (-oidc)

	Renames map[string]string // migration: old column to new column (-rename)
	Safe    bool              // migration: expand now, contract after the deploy (-safe)
}

func cmdGenerate(args []string, stdout io.Writer) error {
	fs := newFlags("generate")
	dryRun := fs.Bool("dry-run", false, "print what would change without writing")
	renameSpec := fs.String("rename", "", "migration: renamed columns, old:new (comma separated)")
	safe := fs.Bool("safe", false, "migration: expand now, contract after the deploy")
	public := fs.Bool("public", false, "security project: make the new routes public")
	publicRead := fs.Bool("public-read", false, "security project, crud: make list and get public")
	oidc := fs.Bool("oidc", false, "security: check the tokens of an identity provider (Keycloak, Auth0, ...) instead of issuing tokens")
	writeRole := fs.String("write-role", "", `security project, crud: role for create, update and delete (default admin; "-" for any signed in user)`)
	fieldSpec := fs.String("fields", "", `entity fields: "name:string email:email age:int?" (types: `+strings.Join(fieldTypeNames, ", ")+`; ? = optional)`)
	pos, _, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) == 1 && (kinds[pos[0]] == "auth" || kinds[pos[0]] == "security") {
		pos = append(pos, kinds[pos[0]])
	}
	if len(pos) == 1 && kinds[pos[0]] == "crud" {
		pos = append(pos, "") // the entity is asked for
	}
	if len(pos) == 0 {
		return fmt.Errorf("dygo generate: missing kind and name (example: dygo g resource products)\n  kinds: %s", kindList)
	}
	kind, ok := kinds[pos[0]]
	if !ok {
		return fmt.Errorf("unknown kind %q: want %s", pos[0], kindList)
	}
	switch {
	case len(pos) == 1 && kind == "migration":
		pos = append(pos, "") // named after its changes
	case len(pos) == 1:
		return fmt.Errorf("dygo g %s: missing name (example: dygo g %s %s)", pos[0], pos[0], kindExamples[kind])
	case len(pos) > 2:
		return fmt.Errorf("dygo g %s: unexpected argument %q (one name per command)", pos[0], pos[2])
	}
	o := genOptions{DryRun: *dryRun, Public: *public, PublicRead: *publicRead, WriteRole: *writeRole, OIDC: *oidc}
	if o.OIDC && kind != "security" {
		return fmt.Errorf("-oidc is for dygo g security")
	}
	if (o.Public || o.PublicRead || o.WriteRole != "") && kind != "controller" && kind != "crud" && kind != "resource" {
		return fmt.Errorf("-public, -public-read and -write-role are for dygo g controller, crud and resource")
	}
	if o.Fields, err = parseFields(*fieldSpec); err != nil {
		return err
	}
	if *safe {
		if kind != "migration" {
			return fmt.Errorf("-safe is for dygo g migration")
		}
		o.Safe = true
	}
	if *renameSpec != "" {
		if kind != "migration" {
			return fmt.Errorf("-rename is for dygo g migration")
		}
		if o.Renames, err = parseRenames(*renameSpec); err != nil {
			return err
		}
	}
	if isTerminal(os.Stdin) && !*dryRun {
		o.Prompt = os.Stdin
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	return generate(cwd, kind, pos[1], o, stdout)
}

// generate creates the files for kind in cwd/name and registers them in the
// nearest module.
func generate(cwd, kind, name string, o genOptions, stdout io.Writer) error {
	proj, err := findProject(cwd)
	if err != nil {
		return err
	}
	// The last element names the package, and the directory is named after
	// the package, as Go expects: order-items goes to orderitems/. From the
	// root of a modular project, features go to its features directory.
	base := cwd
	if proj.Config.FeaturesDir != "" && cwd == proj.Dir {
		base = filepath.Join(proj.Dir, filepath.FromSlash(proj.Config.FeaturesDir))
	}
	if kind == "migration" {
		return generateMigration(cwd, proj, name, o, stdout)
	}
	if kind == "crud" {
		return generateCRUD(cwd, proj, base, name, o, stdout)
	}
	// A simple project keeps everything in its root package, so from the
	// root, features become flat files there (products.service.go) instead
	// of a package. auth and the background kinds stay packages: other code uses
	// them by package name (auth.Required(), jobs.JobName).
	flatPkg := ""
	if proj.flat(cwd) && flatKinds[kind] {
		if flatPkg, err = packageName(cwd); err != nil {
			return err
		}
	}
	dir := filepath.Join(base, filepath.FromSlash(name))
	nameFor := newNames
	if kind == "entity" || kind == "resource" || (kind == "test" && flatPkg != "") {
		nameFor = entityNames // g test Product finds ProductsModule, like g resource Product made it
	}
	n, err := nameFor(filepath.Base(dir))
	if err != nil {
		return err
	}
	dir = filepath.Join(filepath.Dir(dir), n.Pkg)
	if flatPkg != "" {
		dir = cwd
	}
	data := tmplData{names: n, Framework: frameworkPath, FlatPkg: flatPkg, IsEmail: packageContains(dir, "func isEmail("), ImportPath: proj.importPath(dir)}
	if kind == "controller" || kind == "resource" {
		if data.Sec, err = findSecurity(proj, o); err != nil {
			return err
		}
	}
	at := func(path, tmpl string) file {
		if flatPkg != "" {
			path = flatName(n, path)
		}
		return file{filepath.Join(dir, path), "gen/" + tmpl}
	}

	fields := func(existing []string) error {
		adding := existing != nil
		switch {
		case len(o.Fields) > 0:
		case o.Prompt != nil:
			if o.Fields, err = promptFields(o.Prompt, stdout, n.Entity, existing); err != nil {
				return err
			}
			if len(o.Fields) == 0 && adding {
				fmt.Fprintln(stdout, "\nNo new fields.")
				return nil
			}
		case adding:
			return fmt.Errorf("%s already exists: pass -fields to add fields to it", n.Entity)
		}
		if len(o.Fields) == 0 && !adding {
			o.Fields = []entityField{{Name: "Name", JSON: "name", Type: "string"}}
		}
		data.Fields = o.Fields
		return nil
	}

	var files []file
	var reg moduleEdit
	regFrom := dir // where the search for the module to register in starts
	switch kind {
	case "test":
		return generateTest(cwd, proj, dir, data, o.DryRun, stdout)
	case "entity":
		if existing, err := findEntity(dir, n.Entity); err != nil {
			return err
		} else if existing != "" {
			names, err := entityFieldNames(existing, n.Entity)
			if err != nil {
				return err
			}
			if err := fields(names); err != nil {
				return err
			}
			if len(o.Fields) == 0 {
				return nil
			}
			if err := addFields(cwd, dir, existing, n.Entity, o.Fields, o.DryRun, stdout); err != nil {
				return err
			}
			migrationHint(proj, stdout)
			return nil
		}
		if err := fields(nil); err != nil {
			return err
		}
		contents, err := render([]file{at(n.Pkg+".go", "entity.go.tmpl")}, data)
		if err != nil {
			return err
		}
		if err := writeNew([]file{at(n.Pkg+".go", "entity.go.tmpl")}, contents, cwd, stdout, o.DryRun); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "\nNext: run dygo g entity %s again to add fields, or dygo g crud %s to build the CRUD around it.\n", n.Entity, n.Entity)
		migrationHint(proj, stdout)
		return nil
	case "module", "resource", "auth", "security":
		switch other := ""; kind {
		case "auth":
			if other = authPackage(filepath.Dir(dir), securityMarker); other != "" {
				return fmt.Errorf("%s was made by dygo g security, which includes login; an app uses dygo g auth or dygo g security, not both", rel(cwd, other))
			}
		case "security":
			if other = authPackage(filepath.Dir(dir), authMarker); other != "" {
				return switchFromAuth(cwd, other)
			}
		}
		if sites, err := featureModules(dir, n, flatPkg != ""); err != nil {
			return err
		} else if len(sites) > 0 {
			hint := ""
			if kind == "resource" {
				hint = fmt.Sprintf("; to add fields run dygo g entity %s", n.Entity)
			}
			return fmt.Errorf("%s already has a module (%s)%s", rel(cwd, dir), rel(cwd, sites[0].File), hint)
		}
		switch kind {
		case "module":
			files = []file{at("module.go", "module.go.tmpl")}
		case "resource":
			if err := fields(nil); err != nil {
				return err
			}
			files = []file{
				at("module.go", "resource_module.go.tmpl"),
				at(n.Pkg+".go", "entity.go.tmpl"),
				at("service.go", "resource_service.go.tmpl"),
				at("controller.go", "resource_controller.go.tmpl"),
				at("controller_test.go", "resource_controller_test.go.tmpl"),
			}
		case "auth":
			for _, f := range []string{"module", "config", "users", "password", "tokens", "service", "dto", "guard", "controller", "controller_test"} {
				files = append(files, file{filepath.Join(dir, f+".go"), "auth/" + f + ".go.tmpl"})
			}
		case "security":
			files = securityTemplates(dir, o.OIDC)
			data.OIDC = o.OIDC
		}
		reg = moduleEdit{Field: "Imports", Expr: n.Pkg + ".Module", ImportPath: proj.importPath(dir)}
		regFrom = filepath.Dir(dir)
		if flatPkg != "" {
			reg = moduleEdit{Field: "Imports", Expr: data.P() + "Module"}
			regFrom = dir
		}
	case "service":
		files = []file{at("service.go", "service.go.tmpl"), at("service_test.go", "service_test.go.tmpl")}
		reg = moduleEdit{Field: "Providers", Expr: "dygo.Provide(New" + data.P() + "Service)"}
	case "controller":
		files = []file{at("controller.go", "controller.go.tmpl"), at("controller_test.go", "controller_test.go.tmpl")}
		reg = moduleEdit{Field: "Controllers", Expr: "dygo.Provide(New" + data.P() + "Controller)"}
	case "command":
		files = []file{at("command.go", "command.go.tmpl"), at("command_test.go", "command_test.go.tmpl")}
		reg = moduleEdit{Field: "Providers", Expr: "dygo.Provide(New" + data.P() + "Command)"}
	case "listener", "cron", "job", "gateway", "rpc":
		g := pkgKinds[kind]
		files = []file{at(g.file+".go", kind+".go.tmpl"), at(g.file+"_test.go", kind+"_test.go.tmpl")}
		reg = moduleEdit{Field: "Providers", Expr: "dygo.Provide(" + g.ctor + ")"}
	}
	return writeAndRegister(cwd, proj, kind, n, dir, regFrom, files, data, reg, o.DryRun, stdout)
}

// writeAndRegister renders and writes files, registers reg in the nearest
// module above regFrom, and finishes the kind.
func writeAndRegister(cwd string, proj project, kind string, n names, dir, regFrom string, files []file, data tmplData, reg moduleEdit, dryRun bool, stdout io.Writer) error {
	contents, err := render(files, data)
	if err != nil {
		return err
	}

	site, err := nearestModule(regFrom, proj.Dir)
	if data.FlatPkg != "" {
		// Flat files share the root directory with AppModule: a service of
		// products goes into ProductsModule when there is one.
		if sites, _ := featureModules(regFrom, n, true); len(sites) == 1 {
			site, err = sites[0], nil
		}
	}
	if errors.Is(err, errNoModule) && proj.Config.RootModuleDir != "" {
		var sites []moduleSite
		sites, err = findModules(filepath.Join(proj.Dir, filepath.FromSlash(proj.Config.RootModuleDir)))
		if s, ok := pickModule(sites); ok {
			site, err = s, nil
		}
	}
	if err != nil && !errors.Is(err, errNoModule) {
		return err
	}
	if err := writeNew(files, contents, cwd, stdout, dryRun); err != nil {
		return err
	}
	if site.File == "" {
		fmt.Fprintf(stdout, "\nNo module found in %s or above: register %s yourself.\n", rel(cwd, regFrom), reg.Expr)
		return nil
	}
	if filepath.Dir(site.File) != dir && reg.ImportPath == "" {
		// A service or controller registered in a module of another package.
		reg.Expr = strings.Replace(reg.Expr, "(New", "("+n.Pkg+".New", 1)
		reg.ImportPath = proj.importPath(dir)
	}
	if dryRun {
		fmt.Fprintf(stdout, "UPDATE %s (%s += %s)\n", rel(cwd, site.File), reg.Field, reg.Expr)
		return nil
	}
	changed, err := editModule(site, reg)
	if err != nil {
		return fmt.Errorf("files created, but registering failed: %w", err)
	}
	if changed {
		fmt.Fprintf(stdout, "UPDATE %s (%s += %s)\n", rel(cwd, site.File), reg.Field, reg.Expr)
	}
	if kind == "security" {
		return afterSecurity(cwd, proj, n, dir, data.OIDC, stdout)
	}
	if err := afterGenerate(cwd, proj, kind, n, stdout); err != nil {
		return err
	}
	securityHint(data.Sec, kind, stdout)
	return nil
}

// afterGenerate finishes a kind that touches more than its own files, and
// prints what to do next.
func afterGenerate(cwd string, proj project, kind string, n names, stdout io.Writer) error {
	switch kind {
	case "resource", "crud":
		if err := ensureDatabase(cwd, proj, stdout); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "\n Success! CRUD for %s:\n\n"+
			"   GET    /%[2]s          List    (index)\n"+
			"   POST   /%[2]s          Create  (new)\n"+
			"   GET    /%[2]s/{id}     Get     (show)\n"+
			"   PATCH  /%[2]s/{id}     Update  (edit)\n"+
			"   DELETE /%[2]s/{id}     Delete  (delete)\n\n"+
			" Next: go test ./... and dygo routes; try them at /docs.\n", n.Entity, n.Kebab)
	case "command":
		main := filepath.Join(proj.Dir, filepath.FromSlash(cmp(proj.Config.Main, ".")))
		if !packageContains(main, "console.") {
			fmt.Fprintf(stdout, "\nThe main package (%s) does not run console commands yet. Add this to main before the server starts:\n\n"+
				"\tif len(os.Args) > 1 { // go run . <command>\n"+
				"\t\tos.Exit(console.Main(ctx, AppModule(cfg, log), os.Args[1:], dygo.WithLogger(log)))\n"+
				"\t}\n\nwith the import %q.\n", rel(cwd, main), frameworkPath+"/console")
		}
		fmt.Fprintf(stdout, "\nNext: go run . %s, and go run . help lists every command.\n", n.Kebab)
	case "listener", "cron", "job", "gateway", "rpc":
		g := pkgKinds[kind]
		root := filepath.Join(proj.Dir, filepath.FromSlash(cmp(proj.Config.RootModuleDir, cmp(proj.Config.Main, "."))))
		if g.module != "" && !packageContains(root, g.pkg+".Module(") {
			fmt.Fprintf(stdout, "\nImport %s in the root module (Imports), with the import %q, so the %s runs.\n", g.module, frameworkPath+"/"+g.pkg, kind)
		}
		fmt.Fprintf(stdout, "\nNext: %s.\n", strings.NewReplacer("{pkg}", n.Pkg, "{kebab}", n.Kebab).Replace(g.next))
	case "auth":
		env := filepath.Join(proj.Dir, ".env.example")
		if data, err := os.ReadFile(env); err == nil && !strings.Contains(string(data), "AUTH_SECRET") {
			add := "\n# Signs the access tokens: at least 32 random characters (openssl rand -hex 32).\nAUTH_SECRET=\nAUTH_TOKEN_TTL=24h\n"
			if err := os.WriteFile(env, append(data, add...), 0o644); err != nil {
				return err
			}
			fmt.Fprintln(stdout, "UPDATE", rel(cwd, env), "(AUTH_SECRET, AUTH_TOKEN_TTL)")
		}
		fmt.Fprintf(stdout, "\nNext:\n"+
			"  set AUTH_SECRET in .env               # openssl rand -hex 32\n"+
			"  POST /%[1]s/register, POST /%[1]s/login, GET /%[1]s/me\n"+
			"  protect a route:   r.Get(\"/\", h, %[2]s.Required())  or  %[2]s.Roles(\"admin\")\n"+
			"  read the user:     claims, _ := %[2]s.UserFrom(ctx)\n", n.Kebab, n.Pkg)
	}
	return nil
}

// flatKinds are the generators that write flat files in a simple project.
var flatKinds = map[string]bool{"module": true, "service": true, "controller": true, "resource": true, "entity": true, "command": true, "test": true}

// featureModules returns the modules of the feature n in dir. In a feature
// package that is every module there; among flat files it is the one named
// like the feature, so AppModule next to it does not count.
func featureModules(dir string, n names, flat bool) ([]moduleSite, error) {
	sites, err := findModules(dir)
	if err != nil || !flat {
		return sites, err
	}
	var out []moduleSite
	for _, s := range sites {
		if s.Name == n.Kebab {
			out = append(out, s)
		}
	}
	return out, nil
}

// packageName returns the package clause of the non-test Go files in dir.
func packageName(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), "_test.go") {
			f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, e.Name()), nil, parser.PackageClauseOnly)
			if err != nil {
				return "", err
			}
			return f.Name.Name, nil
		}
	}
	return "main", nil
}

func cmp(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// packageContains reports whether a non-test Go file in dir contains s.
func packageContains(dir, s string) bool {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), "_test.go") {
			if data, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil && strings.Contains(string(data), s) {
				return true
			}
		}
	}
	return false
}
