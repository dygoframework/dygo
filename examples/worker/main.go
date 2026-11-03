// Command worker shows the core without HTTP: modules, a dynamic global
// module, interface binding, a provider group, lifecycle hooks and graceful
// shutdown, with no HTTP.
//
//	go run ./examples/worker
//
// Press Ctrl+C to see the shutdown hooks run.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/examples/worker/config"
	"github.com/dygoframework/dygo/examples/worker/report"
	"github.com/dygoframework/dygo/examples/worker/users"
)

// AppModule is the root module.
func AppModule(log *slog.Logger) *dygo.Module {
	return &dygo.Module{
		Name: "app",
		Imports: []*dygo.Module{
			// A global module so every module can inject *slog.Logger.
			{
				Name:      "logger",
				Global:    true,
				Providers: []dygo.Provider{dygo.Value(log)},
				Exports:   []dygo.Token{dygo.TypeOf[*slog.Logger]()},
			},
			config.ForRoot(config.Options{Defaults: config.Config{
				StoreDSN: "memory://local",
				Interval: 2 * time.Second,
			}}),
			users.Module,
			report.Module,
		},
	}
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	ctx := context.Background()

	app, err := dygo.CreateApplicationContext(ctx, AppModule(log), dygo.WithLogger(log))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := app.Run(ctx); err != nil {
		log.Error("shutdown failed", "err", err)
		os.Exit(1)
	}
}
