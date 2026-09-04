package vmgateway

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
)

// gatewayCookieName carries the JWT for the two routes a browser cannot
// attach an Authorization header to: the /mux WebSocket handshake and the
// EventSource stream on eventsPath. Set by the Electron main process, per
// TOKEN_CONTRACT.md's transport rule.
const gatewayCookieName = "ao_gw_token"

// muxPath is the terminal-mux WebSocket route: the one route whose token
// travels in gatewayCookieName instead of the Authorization header.
const muxPath = "/mux"

// eventsPath is the daemon's SSE stream. The renderer opens it with the
// browser EventSource API, which has no way to set a request header at all,
// so this is the second and last route where the cookie may authenticate.
const eventsPath = "/api/v1/events"

// notificationsStreamPath is the daemon's live notification EventSource; like
// eventsPath it can only carry the token in the gateway cookie.
const notificationsStreamPath = "/api/v1/notifications/stream"

// upstreamCORSHeaders are the response headers the daemon sets for itself
// (internal/httpd/cors.go) and that the gateway must own instead. See
// dropUpstreamCORS.
var upstreamCORSHeaders = []string{
	"Access-Control-Allow-Origin",
	"Access-Control-Allow-Credentials",
	"Access-Control-Allow-Methods",
	"Access-Control-Allow-Headers",
	"Access-Control-Max-Age",
	"Access-Control-Allow-Private-Network",
}

// blockedAPIPrefixes are never proxied even though they sit under the
// otherwise-allowed /api/v1 prefix: Connect Mobile control, developer
// maintenance, and host-mutating installer routes are loopback-only. This
// follows the same precedent as lanControlBlockedPrefixes in
// internal/httpd/lan_listener.go (a second, non-loopback listener that must
// never reach these routes).
var blockedAPIPrefixes = []string{
	"/api/v1/mobile",
	"/api/v1/dev",
	"/api/v1/system/install",
	"/api/v1/browser",
	"/api/v1/agents/codex",
	"/api/v1/desktop",
}

// maxRequestBodyBytes bounds a proxied request body before httputil.ReverseProxy
// ever streams it to the daemon; see limitBody. It mirrors maxSpawnBodyBytes in
// internal/httpd/controllers/sessions.go, the daemon's own cap and the largest
// legitimate payload the API accepts today (spawn/delegate/conversation bodies
// carrying base64-inlined file attachments): 25 MiB of attachments inflated by
// ~4/3 for base64, plus 2 MiB of headroom for the prompt and JSON envelope. The
// value is duplicated rather than imported: vmgateway is a separate process
// from the daemon, and importing internal/httpd/controllers would pull that
// package's whole service-layer dependency graph into the gateway binary for
// one constant. If the daemon's cap changes, this one must move with it.
//
// A var rather than a const only so a test can shrink it instead of
// generating a ~35 MiB body, mirroring cloneTimeout in
// internal/service/project/clone.go.
var maxRequestBodyBytes int64 = 25<<20*4/3 + (2 << 20) // ~37,049,685 bytes, ~35.3 MiB

// upstreamResponseHeaderTimeout bounds how long the gateway waits for the
// daemon to send response headers before treating it as wedged (as opposed to
// merely down, which the daemon refusing the connection already handles via
// ErrorHandler's 502) and returning 502 itself rather than blocking the
// serving goroutine forever. It must clear the daemon's own longest
// legitimate synchronous call: project.Add's clone-by-URL path runs `git
// clone` synchronously under a 5 minute cloneTimeout
// (internal/service/project/clone.go), deliberately decoupled from the
// daemon's own 60s REST request timeout so the clone can run that long, plus
// up to cloneWaitDelay (5s) waiting on a killed process's output pipes. This
// only bounds time to the first response header, never the response body, so
// it does not cut off the mux WebSocket or an SSE stream once the daemon has
// answered; see newReverseProxy.
const upstreamResponseHeaderTimeout = 6 * time.Minute

// NewHandler assembles the gateway's HTTP handler for hosted mode: panic
// recovery, CORS preflight, a deny-by-default path allowlist, AO token
// verification, a request body size cap, then a reverse proxy onto the
// loopback daemon, initially at daemonAddr. Middleware order (outermost
// first): panic recovery guards every layer below it so a panic anywhere in
// the chain logs and returns a clean 500 instead of dropping the connection
// with nothing in slog; CORS answers preflights and gates disallowed origins
// before anything else runs — this also is the only Origin check a WebSocket
// upgrade to /mux ever gets, since browsers do not enforce same-origin on
// WebSocket connections themselves and /mux's cookie is ambient
// (browser-attached) credential; deny-by-default rejects any path outside the
// proxyable set with 404, so an unrecognised or loopback-only route never
// reaches the daemon regardless of auth; token verification rejects an
// unauthenticated or invalid request with 401 before the daemon ever sees it;
// the body size cap applies only once a request is authenticated, so an
// anonymous flood cannot use it to force allocation.
//
// See NewPairHandler for pair mode's equivalent, which shares this exact
// middleware chain via newHandler and swaps only the credential check
// (requirePasscode instead of requireToken), per
// docs/adr/0003-pair-mode-gateway.md's "Add a branch, do not rewrite the
// existing path" framing: this function and its signature are unchanged by
// pair mode's existence.
//
// resolveDaemonAddr, when non-nil, is consulted by the reverse proxy's
// ErrorHandler after a failed round trip to re-read the daemon's current
// address (see internal/cli/vm.go's discoverDaemonAddr) so the gateway
// recovers on its own if the daemon was not up yet at gateway boot or later
// restarted onto a different port. Pass nil when daemonAddr was pinned
// explicitly (a flag or environment variable), so a re-resolve never
// silently overrides an operator's explicit choice.
func NewHandler(daemonAddr string, resolveDaemonAddr func() (string, bool), jwks *JWKSCache, verify VerifyOptions, allowedOrigins []string, log *slog.Logger) (http.Handler, error) {
	return newHandler(requireToken(jwks, verify, log), daemonAddr, resolveDaemonAddr, allowedOrigins, log)
}

// NewPairHandler assembles the gateway's HTTP handler for pair mode: the
// same middleware chain NewHandler builds for hosted mode (panic recovery,
// CORS preflight, the deny-by-default path allowlist, a request body size
// cap, and the reverse proxy), with requirePasscode against passcodes as the
// credential check instead of requireToken against a JWKS. See
// docs/adr/0003-pair-mode-gateway.md. passcodes must be a store already
// loaded by LoadPasscodeStore; a nil store is refused here rather than
// starting a gateway with no usable passcode.
func NewPairHandler(daemonAddr string, resolveDaemonAddr func() (string, bool), passcodes *PasscodeStore, allowedOrigins []string, log *slog.Logger) (http.Handler, error) {
	if passcodes == nil {
		return nil, errors.New("vm gateway: pair mode requires a loaded passcode store")
	}
	lock := newPasscodeLockout(passcodeLockoutLimit, passcodeLockoutCooldown, time.Now)
	return newHandler(requirePasscode(passcodes, lock, log), daemonAddr, resolveDaemonAddr, allowedOrigins, log)
}

// newHandler builds the middleware chain NewHandler and NewPairHandler both
// use, parameterized only by which credential check (authMW) sits in the
// requireToken/requirePasscode slot; every other layer, and their order, is
// identical between hosted and pair mode.
func newHandler(authMW func(http.Handler) http.Handler, daemonAddr string, resolveDaemonAddr func() (string, bool), allowedOrigins []string, log *slog.Logger) (http.Handler, error) {
	if _, err := url.Parse("http://" + daemonAddr); err != nil {
		return nil, err
	}

	h := http.Handler(newReverseProxy(daemonAddr, resolveDaemonAddr, log))
	h = limitBody(h)
	h = authMW(h)
	h = denyByDefault(h)
	h = corsGate(allowedOrigins)(h)
	h = recoverAndLog(log)(h)
	return h, nil
}

// daemonTarget holds the reverse proxy's current upstream host:port, updated
// in place when ErrorHandler's resolveDaemonAddr callback finds a new one.
// Reads happen on every proxied request; writes happen only on a proxy
// error, so a mutex (rather than atomic.Value, which would need a wrapper
// struct anyway to swap a string) is the plain, adequate tool here.
type daemonTarget struct {
	mu   sync.RWMutex
	host string
}

func (d *daemonTarget) get() string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.host
}

func (d *daemonTarget) set(host string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.host = host
}

// newReverseProxy proxies to daemonAddr, flushing immediately (required for
// SSE and for responsive WebSocket framing on /mux) and stripping the
// credentials the gateway itself consumed so they are never forwarded to,
// logged by, or otherwise exposed on the loopback daemon side, which has no
// use for them.
func newReverseProxy(daemonAddr string, resolveDaemonAddr func() (string, bool), log *slog.Logger) *httputil.ReverseProxy {
	target := &daemonTarget{host: daemonAddr}
	proxy := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "http", Host: daemonAddr})
	proxy.FlushInterval = -1

	// An explicit Transport, cloned from the default so dial/keep-alive/idle
	// behavior is unchanged, adds ResponseHeaderTimeout: NewSingleHostReverseProxy
	// leaves Transport nil, which falls back to http.DefaultTransport with no
	// such bound, so a daemon that accepts the connection but then wedges
	// mid-handler (as opposed to one that is simply down, already handled by
	// ErrorHandler's 502 below) would block this goroutine forever.
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		// net/http always builds DefaultTransport as a *http.Transport, so this
		// is unreachable in practice. Fall back to a zero Transport rather than
		// panicking, since the timeout below is the only thing being added.
		base = &http.Transport{}
	}
	transport := base.Clone()
	transport.ResponseHeaderTimeout = upstreamResponseHeaderTimeout
	proxy.Transport = transport

	director := proxy.Director
	proxy.Director = func(r *http.Request) {
		director(r)
		// Overwrite with the live target: the base director above baked in
		// daemonAddr as it was at construction time, but target.host may have
		// moved since, via ErrorHandler's resolveDaemonAddr call below.
		r.URL.Host = target.get()
		stripCredentials(r)
		stripForwardedFor(r)
	}
	proxy.ModifyResponse = dropUpstreamCORS
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		if isBodyTooLarge(err) {
			// limitBody's http.MaxBytesReader cut the body short; this is the
			// client's fault, not the daemon's, so it must not be reported (or
			// treated, by re-resolving below) as a daemon connectivity failure.
			log.Warn("vm gateway: request body too large", "path", r.URL.Path, "limit", maxRequestBodyBytes)
			envelope.WriteAPIError(w, r, http.StatusRequestEntityTooLarge, "payload_too_large", "PAYLOAD_TOO_LARGE",
				"request body exceeds the maximum allowed size", nil)
			return
		}
		log.Warn("vm gateway: proxy error", "err", err, "path", r.URL.Path)
		// A lazy re-read on failure, not a polling loop: the next request (not
		// this one, which still 502s) picks up whatever address running.json
		// names now. If the daemon was simply down, discoverDaemonAddr fails the
		// same way it did at gateway boot and target is left unchanged, so this
		// never turns a real outage into a wrong-but-different dead address.
		if resolveDaemonAddr != nil {
			if addr, ok := resolveDaemonAddr(); ok && addr != "" {
				target.set(addr)
			}
		}
		envelope.WriteAPIError(w, r, http.StatusBadGateway, "bad_gateway", "DAEMON_UNREACHABLE",
			"the local daemon is not reachable", nil)
	}
	return proxy
}

// isBodyTooLarge reports whether err, surfaced from the reverse proxy's round
// trip to the daemon, stems from limitBody's http.MaxBytesReader cutting off
// an oversized request body rather than a real daemon-connectivity failure.
func isBodyTooLarge(err error) bool {
	var mbe *http.MaxBytesError
	return errors.As(err, &mbe)
}

// limitBody caps a request body at maxRequestBodyBytes before the reverse
// proxy streams it on to the daemon. http.MaxBytesReader stops the read past
// the limit (and tells the ResponseWriter to close the connection) rather
// than buffering an oversized body in full first; see isBodyTooLarge for how
// the resulting error is turned into a clean 413.
func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		next.ServeHTTP(w, r)
	})
}

// recoverAndLog is the outermost layer of the gateway's middleware chain, so
// a panic anywhere below it (in corsGate, denyByDefault, requireToken,
// extractToken/VerifyToken, or the reverse proxy's Director/ModifyResponse)
// is caught before it can drop the connection with nothing in slog. Mirrors
// recoverTelemetry in internal/httpd/recover.go, minus the telemetry sink:
// the gateway is its own process (docs/adr/0002-hosted-public-gateway.md)
// with no ports.EventSink wired to it, and adding one here would be a new
// coupling this package does not already establish.
func recoverAndLog(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log.Error("vm gateway: handler panic",
						"method", r.Method,
						"path", r.URL.Path,
						"panic", fmt.Sprint(rec),
						"stack", string(debug.Stack()),
					)
					envelope.WriteAPIError(w, r, http.StatusInternalServerError, "internal_error", "INTERNAL_ERROR",
						"internal server error", nil)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// dropUpstreamCORS removes the CORS headers the daemon set for itself before
// they are merged into the gateway's own response. The forwarded request
// still carries the renderer's Origin, which the daemon also allows, so
// without this both sides answer: httputil.ReverseProxy copies upstream
// headers with Add, not Set, so the client would receive
// "Access-Control-Allow-Origin: app://renderer, app://renderer" and every
// browser rejects a multi-valued one outright. corsGate is the single owner
// of these headers on the public listener.
func dropUpstreamCORS(res *http.Response) error {
	for _, h := range upstreamCORSHeaders {
		res.Header.Del(h)
	}
	return nil
}

// stripForwardedFor drops the client's own claim about who it is, so that
// httputil.ReverseProxy's own X-Forwarded-For append (which runs after the
// director) starts from the real peer address rather than extending an
// attacker-supplied chain. The daemon runs middleware.RealIP behind us and
// trusts both headers.
func stripForwardedFor(r *http.Request) {
	r.Header.Del("X-Real-IP")
	r.Header.Del("X-Forwarded-For")
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
		if !isProxyablePath(r.URL.Path) || isAgentControlRequest(r.Method, r.URL.Path) {
			notFoundJSON(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isAgentControlRequest keeps harness installation and verification on loopback.
func isAgentControlRequest(method, path string) bool {
	path = strings.TrimSuffix(path, "/")
	return method == http.MethodPost && strings.HasPrefix(path, "/api/v1/agents/") && (strings.HasSuffix(path, "/install") || strings.HasSuffix(path, "/verify"))
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
	// One limiter per handler chain (built once by NewHandler, not per
	// request), so it throttles across the gateway's whole lifetime rather
	// than resetting on every call.
	limiter := &bareRequestLogLimiter{}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := extractToken(r)
			if !ok {
				// This branch used to reject silently: a storm of bare requests
				// (issue #82) left a completely clean journal, with nothing to
				// tell it apart from a quiet, healthy gateway. Log it at the same
				// level and shape as the two sibling rejections below, minus the
				// token itself (never logged, here or anywhere in this package).
				if n, ok := limiter.allow(time.Now()); ok {
					log.Warn("vm gateway: token rejected", "reason", missingTokenReason(r), "path", r.URL.Path, "suppressed", n)
				}
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

// missingTokenReason classifies why extractToken found nothing usable,
// without ever inspecting header or cookie content beyond a presence/shape
// check: issue #82 needed exactly this distinction; "no header at all"
// (a scanner or a client that never attaches credentials) reads very
// differently from "sent something, but not a bearer token" (a client with a
// real, fixable bug) or "sent an empty bearer token" (a client bug of a
// different shape).
func missingTokenReason(r *http.Request) string {
	if cookieAuthAllowed(r) {
		if c, err := r.Cookie(gatewayCookieName); err == nil {
			if c.Value == "" {
				return "empty gateway cookie, no authorization header"
			}
			// A non-empty cookie means extractToken only failed because this
			// request also lacked a usable Authorization header; fall through
			// to classify that instead of double-reporting the cookie as fine.
		}
	}
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return "no authorization header"
	}
	if !strings.HasPrefix(auth, "Bearer ") {
		return "malformed authorization header"
	}
	return "empty bearer token"
}

// bareRequestLogWindow bounds how often requireToken logs a missing-token
// rejection; see bareRequestLogLimiter. A var rather than a const only so a
// test can shrink it instead of sleeping out a full second, mirroring
// cloneTimeout in internal/service/project/clone.go.
var bareRequestLogWindow = time.Second

// bareRequestLogLimiter throttles the missing-token log line to at most once
// per bareRequestLogWindow, carrying forward how many rejections it
// suppressed since the last line. The gateway sits on the open internet,
// where a constant trickle of scanners and misconfigured clients send bare
// requests; logging every single one at Warn would trade the silence issue
// #82 found (a request storm with nothing in the journal at all) for the
// opposite failure, a journal so noisy during a real storm that the signal
// drowns in volume. One line per window, carrying a suppressed count, keeps
// the signal issue #82 needed (that bare requests are happening, and
// roughly how many) without either extreme.
type bareRequestLogLimiter struct {
	mu         sync.Mutex
	windowEnd  time.Time
	suppressed int
}

// allow reports whether the caller should log now, and if so, how many prior
// rejections were suppressed since the last logged line.
func (l *bareRequestLogLimiter) allow(now time.Time) (suppressed int, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Before(l.windowEnd) {
		l.suppressed++
		return 0, false
	}
	suppressed = l.suppressed
	l.suppressed = 0
	l.windowEnd = now.Add(bareRequestLogWindow)
	return suppressed, true
}

// extractToken reads the token per TOKEN_CONTRACT.md's transport rule:
// Authorization: Bearer <jwt> everywhere, plus the gatewayCookieName cookie
// on the two routes whose browser API cannot send a header (see
// cookieAuthAllowed). The cookie is tried first on those routes and the
// header is still accepted there, so a non-browser client (the CLI, a test
// harness) can open /mux or the event stream too. The raw token is returned
// only to be handed straight to VerifyToken; it must never be logged.
func extractToken(r *http.Request) (string, bool) {
	if cookieAuthAllowed(r) {
		if c, err := r.Cookie(gatewayCookieName); err == nil && c.Value != "" {
			return c.Value, true
		}
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

// cookieAuthAllowed reports whether the ambient gatewayCookieName cookie may
// authenticate this request. Exactly two routes qualify, both because the
// browser API the renderer must use cannot carry a header: the /mux
// WebSocket handshake, and GET on the SSE stream, which the renderer opens
// with EventSource. Everything else stays header-only deliberately. The
// cookie is attached by the browser to any request to this host, so
// accepting it on a state-changing method would leave corsGate as the sole
// CSRF defence.
//
// An Origin header is required on both, which is what makes corsGate a real
// gate rather than one a request can opt out of: corsGate passes a request
// with no Origin straight through, and the cookie is SameSite=None
// (TOKEN_CONTRACT.md), so a hostile page's `new Image().src =
// "https://vm.example.com/api/v1/events"` would otherwise be an authenticated
// SSE stream held open on the daemon. Every browser API that can reach these
// two routes sends an Origin (fetch, EventSource, and the WebSocket
// handshake all do), and a non-browser client authenticates with the
// Authorization header, which this does not touch. So a cookie with no Origin
// is only ever the shape no legitimate caller has.
func cookieAuthAllowed(r *http.Request) bool {
	if r.Header.Get("Origin") == "" {
		return false
	}
	if r.URL.Path == muxPath {
		return true
	}
	return r.Method == http.MethodGet && isCookieAuthStreamPath(r.URL.Path)
}

// isCookieAuthStreamPath names the EventSource routes the renderer opens.
// EventSource cannot send an Authorization header, so these and only these
// GET paths may authenticate with the gateway cookie. Extend this when the
// renderer grows a new EventSource stream; everything else keeps requiring
// the Bearer header.
func isCookieAuthStreamPath(path string) bool {
	if path == eventsPath || path == notificationsStreamPath {
		return true
	}
	rest, ok := strings.CutPrefix(path, "/api/v1/sessions/")
	if !ok {
		return false
	}
	sessionID, tail, ok := strings.Cut(rest, "/")
	return ok && sessionID != "" && tail == "workspace/events"
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
				// Answer a preflight only for a route that actually exists
				// here. corsGate runs before denyByDefault, so without this
				// a 204 would confirm the existence of a route every real
				// method 404s.
				if !isProxyablePath(r.URL.Path) {
					notFoundJSON(w, r)
					return
				}
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
