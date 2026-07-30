package vmgateway

import (
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
)

// gatewayCookieName carries the JWT for the /mux WebSocket upgrade, since
// browsers cannot set an Authorization header on a WebSocket handshake. Set
// by the Electron main process, per TOKEN_CONTRACT.md's transport rule.
const gatewayCookieName = "ao_gw_token"

// muxPath is the terminal-mux WebSocket route: the one route whose token
// travels in gatewayCookieName instead of the Authorization header.
const muxPath = "/mux"

// blockedAPIPrefixes are never proxied even though they sit under the
// otherwise-allowed /api/v1 prefix: Connect Mobile control and developer
// maintenance routes are loopback-only. This mirrors lanControlBlockedPrefixes
// in internal/httpd/lan_listener.go, the daemon's own precedent for the same
// problem (a second, non-loopback listener that must never reach these
// routes) applied to the gateway's public listener.
var blockedAPIPrefixes = []string{
	"/api/v1/mobile",
	"/api/v1/dev",
}

// NewHandler assembles the gateway's HTTP handler: CORS preflight, a
// deny-by-default path allowlist, AO token verification, then a reverse
// proxy onto the loopback daemon at daemonAddr. Middleware order (outermost
// first): CORS answers preflights and gates disallowed origins before
// anything else runs — this also is the only Origin check a WebSocket
// upgrade to /mux ever gets, since browsers do not enforce same-origin on
// WebSocket connections themselves and /mux's cookie is ambient
// (browser-attached) credential; deny-by-default rejects any path outside
// the proxyable set with 404, so an unrecognised or loopback-only route
// never reaches the daemon regardless of auth; token verification rejects an
// unauthenticated or invalid request with 401 before the daemon ever sees
// it.
func NewHandler(daemonAddr string, jwks *JWKSCache, verify VerifyOptions, allowedOrigins []string, log *slog.Logger) (http.Handler, error) {
	target, err := url.Parse("http://" + daemonAddr)
	if err != nil {
		return nil, err
	}

	h := http.Handler(newReverseProxy(target, log))
	h = requireToken(jwks, verify, log)(h)
	h = denyByDefault(h)
	h = corsGate(allowedOrigins)(h)
	return h, nil
}

// newReverseProxy proxies to target, flushing immediately (required for SSE
// and for responsive WebSocket framing on /mux) and stripping the
// credentials the gateway itself consumed so they are never forwarded to,
// logged by, or otherwise exposed on the loopback daemon side, which has no
// use for them.
func newReverseProxy(target *url.URL, log *slog.Logger) *httputil.ReverseProxy {
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.FlushInterval = -1

	director := proxy.Director
	proxy.Director = func(r *http.Request) {
		director(r)
		stripCredentials(r)
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Warn("vm gateway: proxy error", "err", err, "path", r.URL.Path)
		envelope.WriteAPIError(w, r, http.StatusBadGateway, "bad_gateway", "DAEMON_UNREACHABLE",
			"the local daemon is not reachable", nil)
	}
	return proxy
}

func stripCredentials(r *http.Request) {
	r.Header.Del("Authorization")
	cookies := r.Cookies()
	if len(cookies) == 0 {
		return
	}
	r.Header.Del("Cookie")
	for _, c := range cookies {
		if c.Name == gatewayCookieName {
			continue
		}
		r.AddCookie(c)
	}
}

// denyByDefault 404s any request outside the proxyable path set before auth
// or the daemon ever see it, answering as if the route were never mounted at
// all (no 401/403 that would confirm a blocked route exists).
func denyByDefault(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isProxyablePath(r.URL.Path) {
			notFoundJSON(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isProxyablePath(path string) bool {
	if path == muxPath {
		return true
	}
	if !hasPathPrefix(path, "/api/v1") {
		return false
	}
	for _, blocked := range blockedAPIPrefixes {
		if hasPathPrefix(path, blocked) {
			return false
		}
	}
	return true
}

// hasPathPrefix reports whether path is prefix, or nested under it, on a
// segment boundary, so "/api/v1/mobile" matches itself and everything
// beneath it without also catching an unrelated sibling like
// "/api/v1/mobileapp". Mirrors isLANControlBlockedPath in
// internal/httpd/lan_listener.go.
func hasPathPrefix(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

func notFoundJSON(w http.ResponseWriter, r *http.Request) {
	envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found", "NOT_FOUND", "not found", nil)
}

// requireToken rejects any request without a valid AO access token with 401
// before next (the proxy) ever runs. A JWKS fetch failure with nothing
// cached fails closed: every request is rejected, never treated as
// authenticated.
func requireToken(jwks *JWKSCache, verify VerifyOptions, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := extractToken(r)
			if !ok {
				unauthorized(w, r)
				return
			}
			ks, err := jwks.Get(r.Context())
			if err != nil {
				log.Warn("vm gateway: jwks unavailable, failing closed", "err", err)
				unauthorized(w, r)
				return
			}
			if _, err := VerifyToken(token, ks, verify); err != nil {
				log.Warn("vm gateway: token rejected", "reason", err, "path", r.URL.Path)
				unauthorized(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// extractToken reads the bearer token per TOKEN_CONTRACT.md's transport
// rule: Authorization: Bearer <jwt> everywhere except /mux, whose WebSocket
// handshake a browser cannot attach a header to, so it travels in
// gatewayCookieName instead. The raw token is returned only to be handed
// straight to VerifyToken; it must never be logged.
func extractToken(r *http.Request) (string, bool) {
	if r.URL.Path == muxPath {
		c, err := r.Cookie(gatewayCookieName)
		if err != nil || c.Value == "" {
			return "", false
		}
		return c.Value, true
	}
	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, prefix) {
		return "", false
	}
	tok := strings.TrimSpace(strings.TrimPrefix(auth, prefix))
	if tok == "" {
		return "", false
	}
	return tok, true
}

func unauthorized(w http.ResponseWriter, r *http.Request) {
	envelope.WriteAPIError(w, r, http.StatusUnauthorized, "unauthorized", "UNAUTHORIZED",
		"missing or invalid access token", nil)
}

// corsGate answers CORS preflights without requiring a token and rejects any
// actual request bearing a disallowed Origin with 403 before it reaches the
// path allowlist or auth. This is not just browser-fetch hygiene: a
// WebSocket upgrade to /mux is not subject to the browser's same-origin
// policy the way fetch() is, and /mux's cookie is an ambient credential the
// browser attaches automatically to any request to this host — so this gate
// is the only thing that stops a hostile page's cross-origin WebSocket
// connection from riding the user's own cookie. Mirrors corsMiddleware in
// internal/httpd/cors.go.
func corsGate(allowedOrigins []string) func(http.Handler) http.Handler {
	allowed := make(map[string]struct{}, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		origin = strings.TrimSpace(origin)
		if origin == "" || origin == "null" || origin == "*" {
			continue
		}
		allowed[origin] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Add("Vary", "Origin")
			if _, ok := allowed[origin]; !ok {
				envelope.WriteAPIError(w, r, http.StatusForbidden, "forbidden", "ORIGIN_FORBIDDEN",
					"origin is not allowed to access this gateway", nil)
				return
			}

			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")

			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
				if reqHeaders := r.Header.Get("Access-Control-Request-Headers"); reqHeaders != "" {
					h.Set("Access-Control-Allow-Headers", reqHeaders)
				}
				h.Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
