package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"go/format"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// securityFiles are the files of dygo g security, relative to its package.
var securityFiles = []string{
	"module", "config", "rules", "rule", "guard", "principal", "roles", "authorize",
	"cors", "users", "password", "tokens", "tokencache", "refresh", "service", "dto", "controller",
}

// oidcFiles are the files of dygo g security -oidc: no users, passwords or
// own tokens, and a Verifier for the provider's tokens. Files named here
// have an oidc_ template of their own.
var oidcFiles = []string{
	"module", "config", "rules", "rule", "guard", "principal", "roles", "authorize",
	"cors", "verifier", "tokencache", "controller",
}

var oidcOwn = map[string]bool{"module": true, "config": true, "controller": true, "securitytest": true}

// securityTemplates lists the files of dygo g security (-oidc) in dir.
func securityTemplates(dir string, oidc bool) []file {
	list, tmpl := securityFiles, func(f string) string { return "security/" + f + ".go.tmpl" }
	if oidc {
		list = oidcFiles
		tmpl = func(f string) string {
			if oidcOwn[f] {
				return "security/oidc_" + f + ".go.tmpl"
			}
			return "security/" + f + ".go.tmpl"
		}
	}
	var files []file
	for _, f := range list {
		files = append(files, file{filepath.Join(dir, f+".go"), tmpl(f)})
	}
	return append(files,
		file{filepath.Join(dir, "security_test.go"), "security/security_test.go.tmpl"},
		file{filepath.Join(dir, "securitytest", "securitytest.go"), tmpl("securitytest")},
	)
}

// authPackage returns the directory of a package made by dygo g auth or dygo
// g security among the packages in base, or "". An app uses one of them.
func authPackage(base, marker string) string {
	entries, _ := os.ReadDir(base)
	for _, e := range entries {
		if e.IsDir() && packageContains(filepath.Join(base, e.Name()), marker) {
			return filepath.Join(base, e.Name())
		}
	}
	return ""
}

// Markers that identify the generated packages.
const (
	authMarker     = "func UserFrom(ctx context.Context) (Claims, bool)"
	securityMarker = "func CurrentUser(ctx context.Context) (Principal, bool)"
)

// switchFromAuth is the error when dygo g security finds a g auth package.
func switchFromAuth(cwd, dir string) error {
	return fmt.Errorf("%[1]s was made by dygo g auth; an app uses dygo g auth or dygo g security, not both.\n"+
		"To switch to dygo g security:\n"+
		"  1. delete %[1]s and its import in the root module\n"+
		"  2. run dygo g security again\n"+
		"  3. remove auth.Required() (routes are locked by default), and replace\n"+
		"     auth.Roles with security.Roles and auth.UserFrom with security.CurrentUser;\n"+
		"     go build points at every line to change", rel(cwd, dir))
}

// securityEnv is added to .env.example and .env by dygo g security.
const securityEnv = `
# --- security (dygo g security) ---
# Signs the access tokens: at least 32 random characters (openssl rand -hex 32).
# Required when APP_ENV=production; without it tokens stop working on restart.
AUTH_SECRET=
# How long an access token is valid. Keep it short: logout cannot end it early.
AUTH_TOKEN_TTL=15m
# How long a login lasts unless its refresh token is used: 720h is 30 days
AUTH_REFRESH_TTL=720h
# Browser origins allowed to call the API, comma separated; empty allows none
CORS_ALLOWED_ORIGINS=
`

// oidcEnv is added to .env.example and .env by dygo g security -oidc.
const oidcEnv = `
# --- security (dygo g security -oidc) ---
# The identity provider that signs the tokens: a Keycloak realm, an Auth0
# tenant, ... Required when APP_ENV=production, with https.
OAUTH_ISSUER_URL=
# This API's name at the provider; tokens must list it in "aud". Required in production.
OAUTH_AUDIENCE=
# The claim with the user's roles. Keycloak: realm_access.roles
OAUTH_ROLES_CLAIM=roles
# Browser origins allowed to call the API, comma separated; empty allows none
CORS_ALLOWED_ORIGINS=
`

// afterSecurity adds the settings to .env files, marks the project as
// secured in dygo.json and adds CORS and security headers to the app's
// middleware.
func afterSecurity(cwd string, proj project, n names, dir string, oidc bool, stdout io.Writer) error {
	importPath := proj.importPath(dir)
	block, marker, keys := securityEnv, "AUTH_SECRET", "AUTH_SECRET, AUTH_TOKEN_TTL, AUTH_REFRESH_TTL, CORS_ALLOWED_ORIGINS"
	if oidc {
		block, marker, keys = oidcEnv, "OAUTH_ISSUER_URL", "OAUTH_ISSUER_URL, OAUTH_AUDIENCE, OAUTH_ROLES_CLAIM, CORS_ALLOWED_ORIGINS"
	}
	for _, name := range []string{".env.example", ".env"} {
		env := filepath.Join(proj.Dir, name)
		data, err := os.ReadFile(env)
		if err != nil || bytes.Contains(data, []byte(marker)) {
			continue
		}
		add := block
		if name == ".env" && !oidc {
			// .env is local and git-ignored: give it a real secret so tokens
			// survive restarts. .env.example keeps the empty line.
			key := make([]byte, 32)
			if _, err := rand.Read(key); err != nil {
				return err
			}
			add = strings.Replace(add, "\nAUTH_SECRET=\n", "\nAUTH_SECRET="+hex.EncodeToString(key)+"\n", 1)
		}
		if err := os.WriteFile(env, append(data, add...), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "UPDATE %s (%s)\n", rel(cwd, env), keys)
	}
	if err := markSecured(proj); err != nil {
		return err
	}
	if !oidc {
		// Users and refresh tokens are stored through dygo/sql, in memory
		// until DATABASE_URL names a database.
		if err := ensureDatabase(cwd, proj, stdout); err != nil {
			return err
		}
	}
	fmt.Fprintln(stdout, "UPDATE", rel(cwd, filepath.Join(proj.Dir, "dygo.json")), `("security": true)`)

	root := filepath.Join(proj.Dir, filepath.FromSlash(cmp(proj.Config.RootModuleDir, cmp(proj.Config.Main, "."))))
	cors := n.Pkg + ".CORS()"
	if f, err := addMiddleware(root, cors, importPath); err != nil {
		return err
	} else if f != "" {
		fmt.Fprintf(stdout, "UPDATE %s (dygo.WithMiddleware += %s)\n", rel(cwd, f), cors)
	} else if !packageContains(root, cors) {
		fmt.Fprintf(stdout, "\nAdd %s to your app's dygo.WithMiddleware, with the import %q, so browsers in CORS_ALLOWED_ORIGINS can call the API.\n", cors, importPath)
	}
	// Security headers (nosniff, frame denial, HSTS over HTTPS) on every
	// answer, as dygo new sets them; projects made without them get them now.
	if headers := "middleware.SecureHeaders(middleware.SecureHeadersOptions{})"; !packageContains(root, "middleware.SecureHeaders(") {
		if f, err := addMiddleware(root, headers, frameworkPath+"/middleware"); err != nil {
			return err
		} else if f != "" {
			fmt.Fprintf(stdout, "UPDATE %s (dygo.WithMiddleware += %s)\n", rel(cwd, f), headers)
		}
	}

	// The API docs get an Authorize button, so /docs can call protected
	// routes with a token; routes marked Public() stay open there too.
	if f, err := enableBearerAuth(root); err != nil {
		return err
	} else if f != "" {
		fmt.Fprintf(stdout, "UPDATE %s (openapi.Options += BearerAuth: true)\n", rel(cwd, f))
	}

	if oidc {
		fmt.Fprintf(stdout, "\nEvery route now needs a bearer token from your identity provider, unless %[1]s/rules.go or %[2]s.Public() opens it.\nNext:\n"+
			"  set OAUTH_ISSUER_URL and OAUTH_AUDIENCE in .env   # e.g. https://auth.example.com/realms/shop\n"+
			"  Keycloak roles:   OAUTH_ROLES_CLAIM=realm_access.roles\n"+
			"  GET /auth/me shows what the token says\n"+
			"  need a role:      r.Delete(\"/{id}\", h, %[2]s.Roles(\"admin\"))\n"+
			"  need a scope:     r.Put(\"/{id}\", h, %[2]s.Authorities(\"orders:write\"))\n"+
			"  read the user:    p, _ := %[2]s.CurrentUser(ctx)   // p.Claims has every claim\n"+
			"  in tests:         securitytest.Setup(t); srv.GET(\"/x\").With(securitytest.User(\"7\", \"user\"))\n",
			rel(cwd, dir), n.Pkg)
		return nil
	}
	fmt.Fprintf(stdout, "\nEvery route now needs a bearer token, unless %[1]s/rules.go or %[2]s.Public() opens it.\nNext:\n"+
		"  AUTH_SECRET:      .env has a random one; use your own in production   # openssl rand -hex 32\n"+
		"  POST /auth/register, POST /auth/login, POST /auth/refresh, POST /auth/logout, GET /auth/me\n"+
		"  with a database:  dygo g migration, add CREATE UNIQUE INDEX users_email ON users (email);\n"+
		"                    then dygo db migrate   # tables users, refresh_tokens, refresh_families\n"+
		"  open a route:     r.Get(\"/\", h, %[2]s.Public())\n"+
		"  need a role:      r.Delete(\"/{id}\", h, %[2]s.Roles(\"admin\"))\n"+
		"  in a service:     err := %[2]s.Require(ctx, %[2]s.HasAuthority(\"orders:cancel\"))\n"+
		"  read the user:    p, _ := %[2]s.CurrentUser(ctx)\n"+
		"  in tests:         securitytest.Setup(t); srv.GET(\"/x\").With(securitytest.User(\"7\", \"user\"))\n",
		rel(cwd, dir), n.Pkg)
	return nil
}

// markSecured sets "security": true in dygo.json, creating the file when
// the project has none. Later generators read it.
func markSecured(proj project) error {
	path := filepath.Join(proj.Dir, "dygo.json")
	data, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		data = []byte("{\n  \"security\": true\n}\n")
	case err != nil:
		return err
	case bytes.Contains(data, []byte(`"security"`)):
		return nil
	default:
		end := bytes.LastIndexByte(data, '}')
		if end < 0 {
			return fmt.Errorf("%s: not a JSON object", path)
		}
		body := bytes.TrimRight(data[:end], " \t\n")
		sep := ","
		if bytes.HasSuffix(body, []byte("{")) {
			sep = ""
		}
		data = append(append([]byte{}, body...), sep+"\n  \"security\": true\n}\n"...)
	}
	return os.WriteFile(path, data, 0o644)
}

// addMiddleware inserts expr as the first argument of the dygo.WithMiddleware
// call in the root package, and imports importPath. It returns the edited
// file, or "" when there is no such call or expr is already there.
func addMiddleware(dir, expr, importPath string) (string, error) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		src, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		const call = "dygo.WithMiddleware("
		at := bytes.Index(src, []byte(call))
		if at < 0 {
			continue
		}
		if bytes.Contains(src, []byte(expr)) {
			return "", nil
		}
		at += len(call)
		out := append(append(append([]byte{}, src[:at]...), "\n"+expr+","...), src[at:]...)
		if out, err = format.Source(out); err != nil {
			return "", fmt.Errorf("%s: adding %s: %w", path, expr, err)
		}
		if out, err = addImport(path, out, importPath); err != nil {
			return "", err
		}
		return path, os.WriteFile(path, out, 0o644)
	}
	return "", nil
}

// enableBearerAuth sets BearerAuth: true in the openapi.Options literal of
// the root package. It returns the edited file, or "" when there is no such
// literal or it already sets BearerAuth.
func enableBearerAuth(dir string) (string, error) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		src, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		const lit = "openapi.Options{"
		at := bytes.Index(src, []byte(lit))
		if at < 0 {
			continue
		}
		if bytes.Contains(src, []byte("BearerAuth")) {
			return "", nil
		}
		at += len(lit)
		out := append(append(append([]byte{}, src[:at]...), "BearerAuth: true, "...), src[at:]...)
		if out, err = format.Source(out); err != nil {
			return "", fmt.Errorf("%s: adding BearerAuth: %w", path, err)
		}
		return path, os.WriteFile(path, out, 0o644)
	}
	return "", nil
}

// secInfo is what generators need to write code for a security project.
type secInfo struct {
	Pkg        string // the package name, security
	ImportPath string // its import path
	WriteRole  string // role for create, update and delete in g crud ("" for any signed in user)
	PublicRead bool   // g crud: list and get are public
	Public     bool   // g controller and g crud: every route is public
	Rules      string // rules.go, relative to the project root
}

// WriteOption returns the option for create, update and delete routes, or "".
func (s *secInfo) WriteOption() string {
	if s.Public || s.WriteRole == "" {
		return ""
	}
	return fmt.Sprintf(", %s.Roles(%q)", s.Pkg, s.WriteRole)
}

// ReadOption returns the option for list and get routes, or "".
func (s *secInfo) ReadOption() string {
	if s.Public || s.PublicRead {
		return ", " + s.Pkg + ".Public()"
	}
	return ""
}

// findSecurity returns the security package of the project, or nil when the
// project has none. dygo.json "security": true says so; without it, a
// package made by dygo g security in the root or features directory counts.
func findSecurity(proj project, o genOptions) (*secInfo, error) {
	dirs := []string{proj.Dir}
	if proj.Config.FeaturesDir != "" {
		dirs = append(dirs, filepath.Join(proj.Dir, filepath.FromSlash(proj.Config.FeaturesDir)))
	}
	var dir string
	for _, d := range dirs {
		if dir = authPackage(d, securityMarker); dir != "" {
			break
		}
	}
	if dir == "" {
		if proj.Config.Security {
			return nil, fmt.Errorf(`dygo.json says "security": true, but no package made by dygo g security was found; run dygo g security, or remove the setting`)
		}
		if o.Public || o.PublicRead || o.WriteRole != "" {
			return nil, fmt.Errorf("-public, -public-read and -write-role need a security project (dygo g security)")
		}
		return nil, nil
	}
	pkg, err := packageName(dir)
	if err != nil {
		return nil, err
	}
	role := o.WriteRole
	if role == "" {
		role = "admin"
	}
	if role == "-" {
		role = ""
	}
	return &secInfo{Pkg: pkg, ImportPath: proj.importPath(dir), WriteRole: role, PublicRead: o.PublicRead, Public: o.Public,
		Rules: filepath.ToSlash(rel(proj.Dir, filepath.Join(dir, "rules.go")))}, nil
}

// securityHint is printed after a generator wrote routes in a security project.
func securityHint(s *secInfo, kind string, stdout io.Writer) {
	switch {
	case s == nil:
	case s.Public:
		fmt.Fprintf(stdout, "\nSecurity: these routes are public (%s.Public()).\n", s.Pkg)
	case kind == "controller":
		fmt.Fprintf(stdout, "\nSecurity: these routes need a signed in user, unless %s opens them; -public makes them public.\n", s.Rules)
	default:
		read, write := "need a signed in user", "a signed in user"
		if s.PublicRead {
			read = "are public"
		}
		if s.WriteRole != "" {
			write = "the " + s.WriteRole + " role"
		}
		fmt.Fprintf(stdout, "\nSecurity: list and get %s; create, update and delete need %s. Change it with -public-read, -write-role or %s.\n", read, write, s.Rules)
	}
}

// AsWriter signs a generated test request in as a user who may write.
func (d tmplData) AsWriter() string {
	if d.Sec == nil || d.Sec.Public {
		return ""
	}
	return ".With(writer)"
}

// AsReader signs a generated test request in as a user who may read.
func (d tmplData) AsReader() string {
	if d.Sec == nil || d.Sec.Public || d.Sec.PublicRead {
		return ""
	}
	return ".With(reader)"
}

// UsesSec reports whether the generated controller imports the security package.
func (s *secInfo) UsesSec() bool { return s != nil && (s.ReadOption() != "" || s.WriteOption() != "") }
