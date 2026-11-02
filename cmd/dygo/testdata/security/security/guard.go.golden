package security

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/errs"
	"github.com/dygoframework/dygo/meta"
	"github.com/dygoframework/dygo/openapi"
)

var (
	publicKey      = meta.NewKey[bool]("security.public")
	rolesKey       = meta.NewKey[[]string]("security.roles")
	authoritiesKey = meta.NewKey[[]string]("security.authorities")
)

// Public opens a route (or, on Router.Use, a controller) to callers without
// a token, whatever the rules say. A valid token still sets CurrentUser.
func Public() dygo.RouteOption { return dygo.Options(publicKey.Set(true), openapi.Public()) }

// Roles needs one of roles on top of the rules, like @PreAuthorize on a
// controller method. Controller and route roles are joined.
func Roles(roles ...string) dygo.RouteOption { return rolesKey.Set(roles) }

// Authorities needs one of authorities on top of the rules.
func Authorities(authorities ...string) dygo.RouteOption { return authoritiesKey.Set(authorities) }

// Guard applies the rules to every HTTP route. It is registered in
// dygo.AppGuards, so it runs before the controllers' own guards.
type Guard struct {
	rules  []Rule
	tokens *Tokens
	log    *slog.Logger
	cache  atomic.Pointer[map[routeKey]int] // rule index per route, copied on write
	mu     sync.Mutex                       // serializes writers of cache
}

// NewGuard is the constructor.
func NewGuard(cfg *Config, tokens *Tokens, log dygo.Optional[*slog.Logger]) *Guard {
	rules := Rules(cfg)
	if n := len(rules); n == 0 || rules[n-1].patterns != nil || len(rules[n-1].methods) > 0 {
		rules = append(rules, AnyRequest().Authenticated()) // deny by default, whatever Rules returns
	}
	return &Guard{rules: rules, tokens: tokens, log: log.OrElse(slog.Default())}
}

// OnApplicationBootstrap logs the rules, so the startup log shows what is
// protected.
func (g *Guard) OnApplicationBootstrap(ctx context.Context) error {
	for i, r := range g.rules {
		g.log.DebugContext(ctx, "security rule", "order", i+1, "rule", r.String())
	}
	g.log.InfoContext(ctx, "security on: routes need a token unless a rule or Public() opens them", "rules", len(g.rules))
	return nil
}

// CanActivate answers 401 without a valid token and 403 without access.
func (g *Guard) CanActivate(ec dygo.ExecutionContext) (bool, error) {
	if ec.Kind() != dygo.KindHTTP {
		return true, nil // microservice, queue and event handlers are internal traffic
	}
	req := ec.HTTP().Request()
	rule := g.rule(methodOf(req.Method), ec.Route().Path)
	roles := meta.GetAllAndMerge(ec, rolesKey)
	authorities := meta.GetAllAndMerge(ec, authoritiesKey)
	public, _ := publicKey.Get(ec)
	open := public || (rule.access == permitAll && len(roles) == 0 && len(authorities) == 0)

	p, err := g.authenticate(ec.Context(), req.Header.Get("Authorization"))
	if err == nil {
		ec.SetContext(WithPrincipal(ec.Context(), p))
	}
	switch {
	case open:
		return true, nil
	case err != nil:
		ec.HTTP().Response().Header().Set("WWW-Authenticate", `Bearer realm="api"`)
		return false, err
	case rule.access == denyAll,
		rule.check != nil && !rule.check(p),
		len(roles) > 0 && !slices.ContainsFunc(roles, p.HasRole),
		len(authorities) > 0 && !slices.ContainsFunc(authorities, p.HasAuthority):
		g.log.InfoContext(ec.Context(), "access denied", "route", ec.Route().Path, "method", req.Method, "sub", p.Subject)
		return false, errDenied
	}
	return true, nil
}

// rule returns the first rule matching the route. The answer is cached per
// route, so a request does one map lookup.
func (g *Guard) rule(method, path string) Rule {
	key := routeKey{method, path}
	if m := g.cache.Load(); m != nil {
		if i, ok := (*m)[key]; ok {
			return g.rules[i]
		}
	}
	i := slices.IndexFunc(g.rules, func(r Rule) bool { return r.matches(method, path) })
	g.mu.Lock()
	defer g.mu.Unlock()
	next := map[routeKey]int{key: i}
	if m := g.cache.Load(); m != nil {
		for k, v := range *m {
			next[k] = v
		}
	}
	g.cache.Store(&next)
	return g.rules[i]
}

// routeKey names a route for the rule cache. Each route is added once, so
// the copy on write happens only while the app warms up.
type routeKey struct{ method, path string }

var errBadToken = errs.Unauthorized("invalid token")

// authenticate reads the bearer token, only from the Authorization header.
func (g *Guard) authenticate(_ context.Context, header string) (Principal, error) {
	token, ok := strings.CutPrefix(header, "Bearer ")
	if !ok || token == "" {
		return Principal{}, errUnauthenticated
	}
	p, err := g.tokens.Verify(token)
	if err != nil {
		g.log.Debug("bad token", "err", err) // the reason stays in the log, never in the answer
		return Principal{}, errBadToken
	}
	return p, nil
}
