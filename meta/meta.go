// Package meta adds typed route metadata on top of [dygo.SetMetadata]:
//
//	var Roles = meta.NewKey[[]string]("roles")
//
//	r.Delete("/{id}", dygo.Handle(c.Delete), Roles.Set([]string{"admin"}))
//
//	func (g *RolesGuard) CanActivate(ec dygo.ExecutionContext) (bool, error) {
//		roles, ok := Roles.Get(ec)
//		...
//	}
//
// A value set through Router.Use applies to the whole controller; a value on
// the route itself applies to that route. Get prefers the route.
package meta

import "github.com/dygoframework/dygo"

// Key is a typed metadata key. Every NewKey call makes a distinct key, even
// for the same name.
type Key[T any] struct {
	id *keyID
}

type keyID struct{ name string }

// NewKey creates a key. name is only used in String.
func NewKey[T any](name string) Key[T] { return Key[T]{id: &keyID{name: name}} }

// String returns the key's name.
func (k Key[T]) String() string { return k.id.name }

// Set returns a route option that stores v under k.
func (k Key[T]) Set(v T) dygo.RouteOption { return dygo.SetMetadata(k.id, v) }

// Get returns the route's value, or else the controller's, like Reflector
// getAllAndOverride.
func (k Key[T]) Get(ec dygo.ExecutionContext) (T, bool) {
	if v, ok := k.Route(ec); ok {
		return v, true
	}
	return k.Controller(ec)
}

// Route returns the value set on the route only, like Reflector.get with
// context.getHandler().
func (k Key[T]) Route(ec dygo.ExecutionContext) (T, bool) {
	return as[T](ec.RouteMetadata(k.id))
}

// Controller returns the value set for the whole controller only.
func (k Key[T]) Controller(ec dygo.ExecutionContext) (T, bool) {
	return as[T](ec.ControllerMetadata(k.id))
}

func as[T any](v any, ok bool) (T, bool) {
	if !ok {
		var zero T
		return zero, false
	}
	t, ok := v.(T)
	return t, ok
}

// Set is k.Set(v).
func Set[T any](k Key[T], v T) dygo.RouteOption { return k.Set(v) }

// Get is k.Get(ec).
func Get[T any](ec dygo.ExecutionContext, k Key[T]) (T, bool) { return k.Get(ec) }

// GetAllAndMerge joins the controller's and the route's slices, controller
// first, like Reflector.getAllAndMerge. The joined slice is built once at
// startup and shared by every request: read it, do not modify it.
func GetAllAndMerge[T any](ec dygo.ExecutionContext, k Key[[]T]) []T {
	v, _ := as[[]T](ec.MergedMetadata(k.id))
	return v
}

// GetAllAndOverride is k.Get(ec).
func GetAllAndOverride[T any](ec dygo.ExecutionContext, k Key[T]) (T, bool) { return k.Get(ec) }
