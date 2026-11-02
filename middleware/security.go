package middleware

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dygoframework/dygo/errs"
)

// CORSOptions configure CORS.
type CORSOptions struct {
	// AllowOrigins lists allowed origins, such as "https://app.example.com".
	// "*" allows any origin (not allowed together with AllowCredentials).
	// A function in AllowOriginFunc takes precedence.
	AllowOrigins    []string
	AllowOriginFunc func(origin string) bool
	// AllowMethods defaults to GET, HEAD, PUT, PATCH, POST, DELETE.
	AllowMethods []string
	// AllowHeaders defaults to the headers the preflight asks for.
	AllowHeaders []string
	// ExposeHeaders lists response headers the browser may read.
	ExposeHeaders    []string
	AllowCredentials bool
	// MaxAge is how long a preflight may be cached. Defaults to 5 minutes.
	MaxAge time.Duration
}

// CORS answers preflight requests and adds CORS headers. Use it as app
// middleware (dygo.WithMiddleware) so that preflights for any path are
// answered before routing.
func CORS(o CORSOptions) func(http.Handler) http.Handler {
	if len(o.AllowMethods) == 0 {
		o.AllowMethods = []string{"GET", "HEAD", "PUT", "PATCH", "POST", "DELETE"}
	}
	if o.MaxAge == 0 {
		o.MaxAge = 5 * time.Minute
	}
	anyOrigin := slices.Contains(o.AllowOrigins, "*") && !o.AllowCredentials
	methods := static("Access-Control-Allow-Methods", strings.Join(o.AllowMethods, ", "))
	allowHeaders := static("Access-Control-Allow-Headers", strings.Join(o.AllowHeaders, ", "))
	expose := static("Access-Control-Expose-Headers", strings.Join(o.ExposeHeaders, ", "))
	maxAge := static("Access-Control-Max-Age", strconv.Itoa(int(o.MaxAge.Seconds())))
	credentials := static("Access-Control-Allow-Credentials", "true")
	star := static("Access-Control-Allow-Origin", "*")
	// Prebuilt Allow-Origin values for the listed origins.
	origins := map[string]staticHeader{}
	for _, og := range o.AllowOrigins {
		origins[og] = static("Access-Control-Allow-Origin", og)
	}
	allowed := func(origin string) bool {
		if o.AllowOriginFunc != nil {
			return o.AllowOriginFunc(origin)
		}
		return anyOrigin || slices.Contains(o.AllowOrigins, origin)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			h := w.Header()
			addVary(h, varyOrigin)
			if origin == "" || !allowed(origin) {
				next.ServeHTTP(w, r)
				return
			}
			switch og, listed := origins[origin]; {
			case anyOrigin && o.AllowOriginFunc == nil:
				star.set(h)
			case listed:
				og.set(h)
			default:
				h.Set("Access-Control-Allow-Origin", origin)
			}
			if o.AllowCredentials {
				credentials.set(h)
			}
			preflight := r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
			if !preflight {
				if len(o.ExposeHeaders) > 0 {
					expose.set(h)
				}
				next.ServeHTTP(w, r)
				return
			}
			h.Add("Vary", "Access-Control-Request-Method")
			h.Add("Vary", "Access-Control-Request-Headers")
			methods.set(h)
			if len(o.AllowHeaders) > 0 {
				allowHeaders.set(h)
			} else if req := r.Header.Get("Access-Control-Request-Headers"); req != "" {
				h.Set("Access-Control-Allow-Headers", req)
			}
			maxAge.set(h)
			w.WriteHeader(http.StatusNoContent)
		})
	}
}

// SecureHeadersOptions configure SecureHeaders. Zero values give the Helmet
// defaults that make sense for a JSON API.
type SecureHeadersOptions struct {
	// ContentSecurityPolicy defaults to "default-src 'none'; frame-ancestors 'none'",
	// right for JSON APIs. Set it for routes that serve HTML (such as /docs),
	// or "-" to send none.
	ContentSecurityPolicy string
	// HSTSMaxAge is sent only over HTTPS (or X-Forwarded-Proto: https).
	// Defaults to 180 days; negative disables it.
	HSTSMaxAge time.Duration
	// ReferrerPolicy defaults to "no-referrer".
	ReferrerPolicy string
}

// SecureHeaders sets security response headers, like Helmet.
func SecureHeaders(o SecureHeadersOptions) func(http.Handler) http.Handler {
	if o.ContentSecurityPolicy == "" {
		o.ContentSecurityPolicy = "default-src 'none'; frame-ancestors 'none'"
	}
	if o.HSTSMaxAge == 0 {
		o.HSTSMaxAge = 180 * 24 * time.Hour
	}
	if o.ReferrerPolicy == "" {
		o.ReferrerPolicy = "no-referrer"
	}
	hsts := static("Strict-Transport-Security", "max-age="+strconv.Itoa(int(o.HSTSMaxAge.Seconds()))+"; includeSubDomains")
	headers := []staticHeader{
		static("X-Content-Type-Options", "nosniff"),
		static("X-Frame-Options", "DENY"),
		static("Referrer-Policy", o.ReferrerPolicy),
		static("Cross-Origin-Opener-Policy", "same-origin"),
		static("Cross-Origin-Resource-Policy", "same-origin"),
		static("X-DNS-Prefetch-Control", "off"),
	}
	if o.ContentSecurityPolicy != "-" {
		headers = append(headers, static("Content-Security-Policy", o.ContentSecurityPolicy))
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			for _, sh := range headers {
				sh.set(h)
			}
			if o.HSTSMaxAge > 0 && (r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https") {
				hsts.set(h)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// CSRFOptions configure CSRF.
type CSRFOptions struct {
	// CookieName defaults to "csrf_token"; HeaderName to "X-CSRF-Token".
	CookieName, HeaderName string
	// Secure marks the cookie Secure. Set it in production.
	Secure bool
}

// CSRF protects cookie authenticated apps with the double submit pattern: it
// sets a random token cookie readable by your frontend, and rejects unsafe
// requests (POST, PUT, PATCH, DELETE) unless the same token comes back in the
// header. APIs authenticated with bearer tokens do not need it.
func CSRF(o CSRFOptions) func(http.Handler) http.Handler {
	if o.CookieName == "" {
		o.CookieName = "csrf_token"
	}
	if o.HeaderName == "" {
		o.HeaderName = "X-CSRF-Token"
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie(o.CookieName)
			token := ""
			if err == nil {
				token = c.Value
			}
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
				if token == "" {
					var b [32]byte
					_, _ = rand.Read(b[:])
					// G124: the double submit cookie must be readable by the page's script, so no HttpOnly.
					http.SetCookie(w, &http.Cookie{ //nolint:gosec
						Name: o.CookieName, Value: base64.RawURLEncoding.EncodeToString(b[:]), Path: "/",
						Secure: o.Secure, SameSite: http.SameSiteLaxMode,
					})
				}
				next.ServeHTTP(w, r)
				return
			}
			sent := r.Header.Get(o.HeaderName)
			if token == "" || subtle.ConstantTimeCompare([]byte(sent), []byte(token)) != 1 {
				errs.WriteJSON(w, errs.ToBody(errs.Forbidden("invalid CSRF token", errs.Code("CSRF"))))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
