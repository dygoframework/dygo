// Package dygo is a modular application framework for Go.
//
// An application is a tree of modules. Each module declares providers
// (constructor functions or values), the tokens it exports, and the modules it
// imports. At startup the container reads constructor signatures, checks the
// whole dependency graph, builds every singleton in dependency order, and runs
// lifecycle hooks module level by module level.
//
//	var Module = &dygo.Module{
//		Name:      "users",
//		Imports:   []*dygo.Module{database.Module},
//		Providers: []dygo.Provider{
//			dygo.Provide(NewUserService),
//			dygo.Provide(NewPgRepo, dygo.As[Repo]()),
//		},
//		Exports: []dygo.Token{dygo.TypeOf[*UserService]()},
//	}
//
//	app, err := dygo.CreateApplicationContext(ctx, AppModule)
//
// Go has no decorators, so providers, injection and modules are declared
// with plain function calls and struct literals.
// Reflection is used only while the application boots.
//
// # Writing a transport
//
// [Transport], [Endpoint], [Pipeline], [Enhancers], [NewEnhancers] and
// [ProvidersImplementing] are for packages that add a way to receive work,
// such as the ws, graphql and micro modules. Applications do not need them.
//
// # Stability
//
// From v1.0.0 the module follows semantic versioning. The promise covers the
// exported Go API, the JSON error body written by errs.DefaultWriter, and the
// struct tag syntax read by the binder (path, query, header, cookie, form,
// file, body, default, required). It does not cover the output format of the
// dygo CLI, the layout of generated code, or log messages.
package dygo
