package dygo

// Module groups providers. Declare modules as package
// level pointers so that importing the same module from several places shares
// one set of instances:
//
//	var Module = &dygo.Module{Name: "users", ...}
//
// A dynamic module (the ForRoot pattern) is a function that returns a
// new *Module:
//
//	func ForRoot(opts Options) *dygo.Module {
//		return &dygo.Module{
//			Name:      "config",
//			Global:    true,
//			Providers: []dygo.Provider{dygo.Value(opts), dygo.Provide(NewService)},
//			Exports:   []dygo.Token{dygo.TypeOf[*Service]()},
//		}
//	}
type Module struct {
	// Name identifies the module in error messages. Required.
	Name string

	// Global makes the module's exports visible to every module without an
	// import.
	Global bool

	// Imports are modules whose exports this module can inject.
	Imports []*Module

	// Providers are constructed by the container and visible inside the module.
	Providers []Provider

	// Controllers are built like singleton providers but cannot be injected or
	// exported. The HTTP layer registers their routes.
	Controllers []Provider

	// Exports lists the tokens other modules may inject. A module can export
	// its own providers or re-export tokens it imports.
	Exports []Token
}
