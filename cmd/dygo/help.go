package main

import (
	"fmt"
	"io"
	"slices"
	"strings"
)

// usage is the short overview: one line per command. The details are in
// commandHelp, shown by dygo help <command> and dygo <command> -h.
const usage = `dygo is the Dygo CLI.

Usage: dygo <command> [arguments]

Commands:
  new        create a project
  generate   add code to the project (alias: g)
  db         set up the database and run migrations
  routes     print the route table
  graph      print the module and provider graph
  check      validate the wiring without starting the app
  info       Go and Dygo versions, module and route counts
  gen        write Routes methods from //dygo:get (and other) comments
  start      build and run; -watch restarts on change
  run        run one of the app's console commands
  version    print the dygo version

Run dygo help <command> (or dygo <command> -h) for its arguments and flags.
`

// inspectNote is shared by the commands that inspect the app.
const inspectNote = `
It runs the application's main package (default ".", or "main" in
dygo.json) with DYGO_INSPECT_OUT set: dygo.Create writes its graph and routes
there and returns dygo.ErrInspected instead of starting. Configuration that
main loads before dygo.Create must still be valid.
`

// commandHelp is the detailed help of each command, by its main name.
var commandHelp = map[string]string{
	"new": `Usage: dygo new <name> [-db postgres|mysql] [-module path] [-replace dir] [-skip-tidy]

Create a project in the directory <name>: main.go and app.module.go at the
root, src/config, src/database and src/health, and src/modules for the
features that dygo g adds.

  -db             set up a database: postgres or mysql (DATABASE_URL in .env,
                  migrations/, compose.yaml); see dygo help db
  -module         the module path (default: the project name)
  -replace        use a local dygo checkout through a replace directive
  -skip-tidy      do not run go mod tidy

Example: dygo new shop -db postgres
`,
	"generate": `Usage: dygo generate <kind> <name> [-fields spec] [-dry-run]   (alias: dygo g)

Add code to the current project and register it in the nearest module.

Kinds:
  module (mo)        a module                      dygo g mo billing
  service (s)        a service and its test        dygo g s mailer
  controller (co)    a controller and its test     dygo g co health
  entity (en)        an entity with its DTOs       dygo g en Product -fields "title:string price:float"
  crud               CRUD around an entity         dygo g crud Product
  resource (res)     entity + crud in one step     dygo g res products
  command (cmd)      a console command             dygo g cmd seed
  test               route tests for a package     dygo g test products
  auth               register, login, guard        dygo g auth
  listener (li)      an event listener             dygo g li notify
  cron               a scheduled job               dygo g cron reports
  job                a queue processor             dygo g job welcome-email
  gateway (ga)       a WebSocket gateway           dygo g ga chat
  rpc                micro message handlers        dygo g rpc users-rpc
  migration (mig)    the next .up.sql/.down.sql    dygo g mig create_products

Flags:
  -fields "title:string price:float note:text? owner:email"
           entity and resource fields. Types: string, text, email, int,
           int64, float, bool, time; ? = optional. Without -fields, a
           terminal asks for them. On an existing entity, dygo g entity
           adds the fields.
  -dry-run print what would change without writing
  -rename  migration only: renamed columns, old:new
  -safe    migration only: an expand migration now and a contract migration
           (-- dygo:after-deploy) for dygo db migrate --contract after the
           deploy, with a trigger keeping renamed columns in sync

A migration named after an entity's table (create_products, update_products,
add_price_to_products) is filled with CREATE TABLE, or ALTER TABLE for the
fields added, removed, renamed or retyped since the earlier migrations. In a
terminal it asks whether a dropped column was renamed; -rename old:new
answers it in advance (comma separated).
`,
	"db":     dbUsage,
	"routes": "Usage: dygo routes [-json] [package]\n\nPrint the route table: method, path, handler, input and output types.\n" + inspectNote,
	"graph":  "Usage: dygo graph [-format text|mermaid|dot|json] [package]\n\nPrint the module and provider graph.\n" + inspectNote,
	"check":  "Usage: dygo check [package]\n\nValidate the module wiring (missing providers, visibility, cycles, scopes)\nwithout starting the app. Exits 1 with the errors when it is broken.\n" + inspectNote,
	"info":   "Usage: dygo info [package]\n\nPrint the Go and Dygo versions and the module, provider and route counts.\n" + inspectNote,
	"gen": `Usage: dygo gen [-check] [dir...]

Write Routes methods (dygo_gen.go) from //dygo:get, //dygo:post, ...
comments on controller methods, in the given directories (default: the
current one).

  -check   fail if a generated file is missing or out of date, without writing
`,
	"start": `Usage: dygo start [-watch] [-poll 500ms] [package] [-- args]

Build and run the app (default package ".", or "main" in dygo.json).
Arguments after -- go to the app.

  -watch   rebuild and restart when a Go file, go.mod or .env file changes
  -poll    how often -watch looks for changes
`,
	"run": `Usage: dygo run [command] [arguments]

Build the app and run one of its console commands (see dygo g command),
from the project root so .env is read. The arguments go to the command, so
dygo run seed -h is the command's own help. With no command, it lists the
app's commands.

Example: dygo run seed --users 10
`,
	"version": "Usage: dygo version\n\nPrint the dygo version.\n",
}

// commandAliases map short names to the commands in commandHelp.
var commandAliases = map[string]string{"g": "generate"}

// isHelpFlag reports whether a is -h, -help or --help.
func isHelpFlag(a string) bool { return a == "-h" || a == "-help" || a == "--help" }

// wantsHelp reports whether args (after the command) ask for help: a help
// flag before any "--".
func wantsHelp(args []string) bool {
	if i := slices.Index(args, "--"); i >= 0 {
		args = args[:i]
	}
	return slices.ContainsFunc(args, isHelpFlag)
}

// cmdHelp prints the overview, or the help of the command in args.
func cmdHelp(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stdout, usage)
		return nil
	}
	return printCommandHelp(args[0], stdout)
}

func printCommandHelp(name string, stdout io.Writer) error {
	if a, ok := commandAliases[name]; ok {
		name = a
	}
	text, ok := commandHelp[name]
	if !ok {
		var names []string
		for n := range commandHelp {
			names = append(names, n)
		}
		slices.Sort(names)
		return fmt.Errorf("dygo help: unknown command %q (commands: %s)", name, strings.Join(names, ", "))
	}
	fmt.Fprint(stdout, text)
	return nil
}
