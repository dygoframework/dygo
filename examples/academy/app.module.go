package main

import (
	"log/slog"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/events"
	"github.com/dygoframework/dygo/logger"
	"github.com/dygoframework/dygo/middleware"
	"github.com/dygoframework/dygo/openapi"
	"github.com/dygoframework/dygo/queue"
	"github.com/dygoframework/dygo/validate"

	"github.com/dygoframework/dygo/examples/academy/internal/config"
	"github.com/dygoframework/dygo/examples/academy/internal/database"
	"github.com/dygoframework/dygo/examples/academy/internal/health"
	"github.com/dygoframework/dygo/examples/academy/internal/modules/courses"
	"github.com/dygoframework/dygo/examples/academy/internal/modules/enrollments"
	"github.com/dygoframework/dygo/examples/academy/internal/modules/lessons"
	"github.com/dygoframework/dygo/examples/academy/internal/modules/notify"
	"github.com/dygoframework/dygo/examples/academy/internal/modules/security"
	"github.com/dygoframework/dygo/examples/academy/internal/modules/welcomeemail"
	"github.com/dygoframework/dygo/sql"
)

// AppModule is the root module: it imports the
// infrastructure modules and every feature module in internal/modules.
// dygo generate adds new features to Imports.
func AppModule(cfg *config.Config, log *slog.Logger) *dygo.Module {
	return &dygo.Module{
		Name: "app",
		Imports: []*dygo.Module{
			config.Module(cfg),
			logger.Module(log),
			database.Module,
			health.Module(),
			openapi.Module(openapi.Options{Info: openapi.Info{Title: "academy", Version: "0.1.0"}}),
			events.Module(),
			queue.Module(queue.Options{}),
			courses.Module,
			lessons.Module,
			enrollments.Module,
			security.Module,
			sql.Module(nil),
		},
		Providers: []dygo.Provider{dygo.Provide(notify.NewListener), dygo.Provide(welcomeemail.NewProcessor)},
	}
}

// AppOptions are the application-wide settings (middleware, global pipes),
// set up once for the whole app. main and the tests share them.
func AppOptions(cfg *config.Config, log *slog.Logger) []dygo.Option {
	return []dygo.Option{
		dygo.WithLogger(log),
		dygo.WithMiddleware(
			security.CORS(),
			middleware.RequestID(),
			middleware.Logger(log),
			middleware.SecureHeaders(middleware.SecureHeadersOptions{}),
		),
		validate.Use(validate.Methods()),
	}
}
