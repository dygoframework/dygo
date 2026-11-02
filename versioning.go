package dygo

import (
	"mime"
	"net/http"
	"strings"
)

// VersionNeutral as a route version makes the route match every version, and
// requests without a version.
const VersionNeutral = "*"

type versionKind int

const (
	versionURI versionKind = iota
	versionHeader
	versionMediaType
	versionCustom
)

// VersioningOptions configures API versioning. Create one with
// [URIVersioning], [HeaderVersioning], [MediaTypeVersioning] or
// [CustomVersioning] and pass it to [WithVersioning].
type VersioningOptions struct {
	kind     versionKind
	header   string
	key      string
	extract  func(*http.Request) []string
	defaults []string
}

// URIVersioning puts the version in the path: /v1/users. Routes without a
// version get defaultVersion.
func URIVersioning(defaultVersion ...string) VersioningOptions {
	return VersioningOptions{kind: versionURI, defaults: defaultVersion}
}

// HeaderVersioning reads the version from a request header, for example
// "X-API-Version". Requests without the header get the default version.
func HeaderVersioning(header string, defaultVersion ...string) VersioningOptions {
	return VersioningOptions{kind: versionHeader, header: header, defaults: defaultVersion}
}

// MediaTypeVersioning reads the version from an Accept header parameter, for
// example key "v" in "Accept: application/json;v=2".
func MediaTypeVersioning(key string, defaultVersion ...string) VersioningOptions {
	return VersioningOptions{kind: versionMediaType, key: key, defaults: defaultVersion}
}

// CustomVersioning reads versions with extract, in order of preference.
func CustomVersioning(extract func(*http.Request) []string, defaultVersion ...string) VersioningOptions {
	return VersioningOptions{kind: versionCustom, extract: extract, defaults: defaultVersion}
}

// requestVersions returns the versions a request asks for, falling back to
// the default versions.
func (v *VersioningOptions) requestVersions(r *http.Request) []string {
	var out []string
	switch v.kind {
	case versionHeader:
		if h := strings.TrimSpace(r.Header.Get(v.header)); h != "" {
			out = []string{h}
		}
	case versionMediaType:
		for _, part := range strings.Split(r.Header.Get("Accept"), ",") {
			if _, params, err := mime.ParseMediaType(strings.TrimSpace(part)); err == nil && params[v.key] != "" {
				out = append(out, params[v.key])
			}
		}
	case versionCustom:
		out = v.extract(r)
	}
	if len(out) == 0 {
		return v.defaults
	}
	return out
}
