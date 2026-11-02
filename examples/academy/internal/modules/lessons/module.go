// Package lessons is the lessons resource: a service and a CRUD controller.
package lessons

import "github.com/dygoframework/dygo"

// Module wires the lessons resource. Other modules that import it can
// inject *Service.
var Module = &dygo.Module{
	Name:        "lessons",
	Providers:   []dygo.Provider{dygo.Provide(NewService)},
	Controllers: []dygo.Provider{dygo.Provide(NewController)},
	Exports:     []dygo.Token{dygo.TypeOf[*Service]()},
}
