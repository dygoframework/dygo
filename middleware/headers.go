package middleware

import (
	"net/http"
	"slices"
)

// Header values built once and shared by every response, so setting them
// allocates nothing. Each slice has len == cap: a later Header.Add appends
// into a new array and never writes into the shared one.

type staticHeader struct {
	key   string
	value []string
}

func static(key, value string) staticHeader {
	return staticHeader{key: http.CanonicalHeaderKey(key), value: slices.Clip([]string{value})}
}

func (s staticHeader) set(h http.Header) { h[s.key] = s.value }

var (
	varyOrigin         = static("Vary", "Origin")
	varyAcceptEncoding = static("Vary", "Accept-Encoding")
)

// addVary adds a Vary value without allocating when Vary is not set yet.
func addVary(h http.Header, v staticHeader) {
	if len(h[v.key]) == 0 {
		v.set(h)
		return
	}
	h[v.key] = append(h[v.key], v.value[0])
}
