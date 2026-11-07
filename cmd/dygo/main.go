// Command dygo is the Dygo CLI.
//
//	dygo new <name> [-db postgres|mysql] create a project
//	dygo generate <kind> <name>          add a module, service, controller, CRUD resource, entity, command, test, auth, listener, cron, job, gateway or rpc (alias: g)
//	dygo routes [package]                print the route table
//	dygo graph [-format text|mermaid|dot] [package]
//	dygo check [package]                 validate the wiring without starting the app
//	dygo info [package]                  versions and counts
//	dygo gen [-check] [dir...]           generate Routes methods from //dygo: comments
//	dygo start [-watch] [package] [-- args]
//	dygo version
//
// Install it with go install github.com/dygoframework/dygo/cmd/dygo@latest.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"slices"
	"strings"
)

// errUsage makes main print the usage and exit with status 2.
var errUsage = errors.New("usage")

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, errUsage) {
			fmt.Fprint(os.Stderr, usage)
			os.Exit(2)
		}
		var exit exitError
		if errors.As(err, &exit) {
			os.Exit(int(exit))
		}
		if msg := err.Error(); strings.HasPrefix(msg, "dygo ") {
			fmt.Fprintln(os.Stderr, msg) // already names the command
		} else {
			fmt.Fprintln(os.Stderr, "dygo:", msg)
		}
		os.Exit(1)
	}
}

// exitError exits with a status after the command already printed why.
type exitError int

func (e exitError) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errUsage
	}
	cmd, args := args[0], args[1:]
	// db, ai and run read their own arguments: dygo run seed -h is the command's help.
	if cmd != "db" && cmd != "ai" && cmd != "run" && cmd != "help" && wantsHelp(args) {
		if err := printCommandHelp(cmd, stdout); err == nil {
			return nil
		}
	}
	switch cmd {
	case "new":
		return cmdNew(args, stdout)
	case "generate", "g":
		return cmdGenerate(args, stdout)
	case "routes":
		return cmdRoutes(args, stdout, stderr)
	case "graph":
		return cmdGraph(args, stdout, stderr)
	case "check":
		return cmdCheck(args, stdout, stderr)
	case "info":
		return cmdInfo(args, stdout, stderr)
	case "db":
		return cmdDB(args, stdout, stderr)
	case "ai":
		return cmdAI(args, stdout, stderr)
	case "gen":
		return cmdGen(args, stdout)
	case "start":
		return cmdStart(args, stdout, stderr)
	case "run":
		return cmdRun(args, stdout, stderr)
	case "version":
		fmt.Fprintln(stdout, "dygo", version())
		return nil
	case "help", "-h", "-help", "--help":
		return cmdHelp(args, stdout)
	}
	return fmt.Errorf("unknown command %q (run dygo help)", cmd)
}

func version() string {
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" {
		return bi.Main.Version
	}
	return "(devel)"
}

// newFlags returns a flag set that reports errors instead of exiting.
func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// parseFlags parses flags that may come before or after positional arguments
// (dygo graph . -format dot), and stops at "--".
func parseFlags(fs *flag.FlagSet, args []string) (pos, rest []string, err error) {
	if i := slices.Index(args, "--"); i >= 0 {
		args, rest = args[:i], args[i+1:]
	}
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			cmd, _, _ := strings.Cut(fs.Name(), " ")
			return nil, nil, fmt.Errorf("%s: %w (see dygo help %s)", fs.Name(), err, cmd)
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
	return pos, rest, nil
}
