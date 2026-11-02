// academy starts here: load the config, create the
// application from AppModule (app.module.go) and listen.
//
//	go run .            # or: dygo start -watch
//	go run . help       # list the console commands
//	open http://localhost:9900/docs
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/console"
	"github.com/dygoframework/dygo/logger"

	"github.com/dygoframework/dygo/examples/academy/internal/config"
)

func main() {
	log := logger.New(logger.Options{}) // JSON when APP_ENV=production, level from LOG_LEVEL
	cfg, err := config.Load()
	if err != nil {
		log.Error("bad configuration", "err", err)
		os.Exit(1)
	}
	ctx := context.Background()
	if len(os.Args) > 1 { // go run . <command>: a console command (dygo g command)
		os.Exit(console.Main(ctx, AppModule(cfg, log), os.Args[1:], dygo.WithLogger(log)))
	}
	app, err := dygo.Create(ctx, AppModule(cfg, log), AppOptions(cfg, log)...)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := app.Listen(ctx, ":"+strconv.Itoa(cfg.Port)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
