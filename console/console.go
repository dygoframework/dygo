// Package console runs one-off commands inside the application context: a
// seed script, a data fix or a report that needs the same
// services as the app, without starting the HTTP server.
//
// A command is any provider with a Name and a Run method:
//
//	type SeedCommand struct{ users *users.Service }
//
//	func (c *SeedCommand) Name() string { return "seed" }
//	func (c *SeedCommand) Run(ctx context.Context, args []string) error { ... }
//
// and main hands its arguments over before it starts the server:
//
//	if len(os.Args) > 1 { // go run . seed
//		os.Exit(console.Main(ctx, AppModule(cfg, log), os.Args[1:], dygo.WithLogger(log)))
//	}
//
// dygo generate command writes both.
package console

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/dygoframework/dygo"
)

// Command is a provider that can run from the command line.
type Command interface {
	// Name is the first argument that selects the command: "seed".
	Name() string
	// Run gets the remaining arguments. ctx is cancelled on SIGINT or SIGTERM.
	Run(ctx context.Context, args []string) error
}

// Describer is an optional one-line description shown by help.
type Describer interface {
	Description() string
}

// ErrUnknown is returned for a name no command has.
var ErrUnknown = errors.New("unknown command")

// Run builds the application context from root, runs the command named by
// args[0] with the rest of args, and shuts the context down, so lifecycle
// hooks run as they do for the server. "help" lists the commands.
func Run(ctx context.Context, root *dygo.Module, args []string, opts ...dygo.Option) error {
	return run(ctx, root, args, os.Stdout, opts)
}

func run(ctx context.Context, root *dygo.Module, args []string, out io.Writer, opts []dygo.Option) (err error) {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	app, err := dygo.CreateApplicationContext(ctx, root, opts...)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, app.Close(context.WithoutCancel(ctx))) }()
	cmds := dygo.ProvidersImplementing[Command](app)
	slices.SortFunc(cmds, func(a, b Command) int { return strings.Compare(a.Name(), b.Name()) })
	name := ""
	if len(args) > 0 {
		name = args[0]
	}
	if name == "" || name == "help" || name == "-h" || name == "--help" {
		return usage(out, cmds)
	}
	for _, c := range cmds {
		if c.Name() == name {
			return c.Run(ctx, args[1:])
		}
	}
	_ = usage(out, cmds)
	return fmt.Errorf("%w %q", ErrUnknown, name)
}

func usage(out io.Writer, cmds []Command) error {
	if len(cmds) == 0 {
		_, err := fmt.Fprintln(out, "No commands. Add one with: dygo generate command <name>")
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "Commands:")
	for _, c := range cmds {
		desc := ""
		if d, ok := c.(Describer); ok {
			desc = d.Description()
		}
		_, _ = fmt.Fprintf(tw, "  %s\t%s\n", c.Name(), desc)
	}
	return tw.Flush()
}

// Main is Run for main: it prints an error and returns the exit status.
func Main(ctx context.Context, root *dygo.Module, args []string, opts ...dygo.Option) int {
	if err := Run(ctx, root, args, opts...); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
