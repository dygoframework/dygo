// Package courses is the courses resource: a service and a CRUD controller.
package courses

import "github.com/dygoframework/dygo"

// Module wires the courses resource. Other modules that import it can
// inject *Service.
var Module = &dygo.Module{
	Name:        "courses",
	Providers:   []dygo.Provider{dygo.Provide(NewService)},
	Controllers: []dygo.Provider{dygo.Provide(NewController)},
	Exports:     []dygo.Token{dygo.TypeOf[*Service]()},
}
