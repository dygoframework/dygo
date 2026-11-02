// Package enrollments lets signed in users enroll in published courses.
package enrollments

import (
	"github.com/dygoframework/dygo"

	"github.com/dygoframework/dygo/examples/academy/internal/modules/courses"
)

// Module wires enrollments. It needs courses (to check the course) and the
// global auth and events modules.
var Module = &dygo.Module{
	Name:        "enrollments",
	Imports:     []*dygo.Module{courses.Module},
	Providers:   []dygo.Provider{dygo.Provide(NewService)},
	Controllers: []dygo.Provider{dygo.Provide(NewController)},
	Exports:     []dygo.Token{dygo.TypeOf[*Service]()},
}
