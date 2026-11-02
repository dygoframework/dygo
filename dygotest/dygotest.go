// Package dygotest builds Dygo applications in tests:
//
//	app := dygotest.New(users.Module).
//		OverrideValue(dygo.TypeOf[users.Repo](), &fakeRepo{}).
//		Compile(t)
//	svc := dygo.MustGet[*users.Service](app)
//
// Compile fails the test on a boot error and closes the app when the test ends.
package dygotest

import (
	"context"
	"testing"

	"github.com/dygoframework/dygo"
)

// Builder collects overrides before compiling a test application.
type Builder struct {
	root *dygo.Module
	opts []dygo.Option
}

// New starts a test application from root.
func New(root *dygo.Module) *Builder {
	return &Builder{root: root}
}

// Module wraps providers in an anonymous root module, for testing a few
// providers without declaring a module. Imports are optional.
func Module(providers []dygo.Provider, imports ...*dygo.Module) *Builder {
	return New(&dygo.Module{Name: "test", Imports: imports, Providers: providers})
}

// OverrideProvider replaces the provider registered under tok with p.
func (b *Builder) OverrideProvider(tok dygo.Token, p dygo.Provider) *Builder {
	b.opts = append(b.opts, dygo.WithOverride(tok, p))
	return b
}

// OverrideValue replaces the provider registered under tok with a fixed value.
func (b *Builder) OverrideValue(tok dygo.Token, v any) *Builder {
	return b.OverrideProvider(tok, dygo.Value(v, dygo.For(tok)))
}

// With adds any other application option.
func (b *Builder) With(opts ...dygo.Option) *Builder {
	b.opts = append(b.opts, opts...)
	return b
}

// Build creates the application and returns any boot error.
func (b *Builder) Build(ctx context.Context) (*dygo.App, error) {
	return dygo.CreateApplicationContext(ctx, b.root, b.opts...)
}

// Compile creates the application, failing the test on error, and registers a
// cleanup that shuts it down.
func (b *Builder) Compile(t testing.TB) *dygo.App {
	t.Helper()
	app, err := b.Build(t.Context())
	if err != nil {
		t.Fatalf("dygotest: %v", err)
	}
	t.Cleanup(func() {
		if err := app.Close(context.Background()); err != nil {
			t.Errorf("dygotest: shutdown: %v", err)
		}
	})
	return app
}
