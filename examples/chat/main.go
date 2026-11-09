// Command chat is a small chat app on the ai module: a web page that streams
// the model's answer as it is written, and a route that turns an email into
// a typed support ticket.
//
//	cp .env.example .env   # pick the provider, model and key
//	go run .
//	open http://localhost:9900
//
// Switching from OpenAI to Anthropic, or to a local Ollama, is a change to
// .env; the code stays the same.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/ai"
	"github.com/dygoframework/dygo/ai/anthropic"
	"github.com/dygoframework/dygo/ai/openai"
	"github.com/dygoframework/dygo/validate"
)

// AppModule wires the model and the chat feature. ai.Module reads AI_* from
// the environment and .env, and stops the boot with a clear message when a
// setting is missing.
func AppModule() *dygo.Module {
	return &dygo.Module{
		Name: "app",
		Imports: []*dygo.Module{
			ai.Module(openai.Provider, anthropic.Provider),
			ChatModule,
		},
	}
}

// AppOptions are shared by main and the tests.
func AppOptions(log *slog.Logger) []dygo.Option {
	return []dygo.Option{dygo.WithLogger(log), validate.Use(validate.Methods())}
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	ctx := context.Background()
	app, err := dygo.Create(ctx, AppModule(), AppOptions(log)...)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := app.Listen(ctx, ":9900"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
