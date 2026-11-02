package benchmarks

import "net/http"

// User is the JSON result of the param and JSON scenarios.
type User struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Limit int    `json:"limit,omitempty"`
}

// CreateUser is the JSON request body of the JSON scenario.
type CreateUser struct {
	Name string `json:"name"`
}

// Server is one framework's implementation of every scenario:
//
//	GET  /ping                      static text "pong"
//	GET  /users/{id}                path param, JSON result
//	POST /users/{id}?limit=5        path + query + JSON body in, JSON out, 201
//	GET  /r{n}/items/{id}           routing: one of RouteCount param routes
type Server struct {
	Name    string
	Handler http.Handler
}

// RouteCount is the number of routes in the routing scenario's table.
const RouteCount = 100

// LastRoute is the routing scenario's target: the last route registered.
var LastRoute = routePath(RouteCount - 1)

func routePath(n int) string { return "/r" + itoa(n) + "/items/" }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
