package vmgateway

import (
	"bytes"
	"crypto/ed25519"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testOrigin = "app://renderer"

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// testGateway wires a real fake-daemon httptest.Server behind a gateway
// handler built with a fixed, already-populated JWKS cache (bypassing any
// real network fetch) so tests only exercise proxy.go's own logic.
type testGateway struct {
	daemonCalls []*http.Request
	daemon      *httptest.Server
	handler     http.Handler
	pub         ed25519.PublicKey
	priv        ed25519.PrivateKey
	now         time.Time
}

func newTestGateway(t *testing.T) *testGateway {
	t.Helper()
	tg := &testGateway{now: time.Now()}
	tg.pub, tg.priv, _ = ed25519.GenerateKey(nil)

	tg.daemon = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clone := r.Clone(r.Context())
		tg.daemonCalls = append(tg.daemonCalls, clone)
		w.Header().Set("X-From", "daemon")
		// The real daemon runs its own corsMiddleware (internal/httpd/cors.go)
		// and app://renderer is in its allowed set, so a forwarded request
		// comes back carrying these. Modelled here because the gateway has to
		// strip them; see dropUpstreamCORS.
		if origin := r.Header.Get("Origin"); origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(tg.daemon.Close)

	daemonURL, err := url.Parse(tg.daemon.URL)
	if err != nil {
		t.Fatalf("parse daemon url: %v", err)
	}

	cache := &JWKSCache{
		ttl:       time.Hour,
		client:    http.DefaultClient,
		now:       func() time.Time { return tg.now },
		keys:      &KeySet{byKID: map[string]ed25519.PublicKey{testKid: tg.pub}},
		fetchedAt: tg.now,
	}

	verify := VerifyOptions{
		Issuer:   testIssuer,
		Audience: testAud,
		Subject:  testSub,
		Skew:     DefaultSkew,
		Now:      func() time.Time { return tg.now },
	}

	h, err := NewHandler(daemonURL.Host, nil, cache, verify, []string{testOrigin}, discardLogger())
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	tg.handler = h
	return tg
}

func (tg *testGateway) validToken(t *testing.T) string {
	t.Helper()
	return signToken(t, tg.priv, map[string]any{"alg": "EdDSA", "kid": testKid}, validClaims(tg.now))
}

func TestGateway_CORSPreflight_NoTokenRequired(t *testing.T) {
	tg := newTestGateway(t)
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/projects", nil)
	req.Header.Set("Origin", testOrigin)
	req.Header.Set("Access-Control-Request-Method", "POST")
	rec := httptest.NewRecorder()

	tg.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != testOrigin {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, testOrigin)
	}
	if len(tg.daemonCalls) != 0 {
		t.Errorf("preflight must never reach the daemon, got %d calls", len(tg.daemonCalls))
	}
}

func TestGateway_DisallowedOrigin_Rejected(t *testing.T) {
	tg := newTestGateway(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	req.Header.Set("Authorization", "Bearer "+tg.validToken(t))
	rec := httptest.NewRecorder()

	tg.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if len(tg.daemonCalls) != 0 {
		t.Errorf("disallowed origin must never reach the daemon, got %d calls", len(tg.daemonCalls))
	}
}

func TestGateway_MissingToken_Rejected(t *testing.T) {
	tg := newTestGateway(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	rec := httptest.NewRecorder()

	tg.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if len(tg.daemonCalls) != 0 {
		t.Errorf("unauthenticated request must never reach the daemon, got %d calls", len(tg.daemonCalls))
	}
}

func TestGateway_InvalidToken_Rejected(t *testing.T) {
	tg := newTestGateway(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-jwt")
	rec := httptest.NewRecorder()

	tg.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if len(tg.daemonCalls) != 0 {
		t.Errorf("invalid token must never reach the daemon, got %d calls", len(tg.daemonCalls))
	}
}

func TestGateway_ExpiredToken_Rejected(t *testing.T) {
	tg := newTestGateway(t)
	claims := map[string]any{"iss": testIssuer, "sub": testSub, "aud": testAud, "exp": tg.now.Add(-time.Hour).Unix()}
	token := signToken(t, tg.priv, map[string]any{"alg": "EdDSA", "kid": testKid}, claims)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	tg.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestGateway_ValidToken_ProxiesToDaemon(t *testing.T) {
	tg := newTestGateway(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	req.Header.Set("Authorization", "Bearer "+tg.validToken(t))
	rec := httptest.NewRecorder()

	tg.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-From") != "daemon" {
		t.Fatalf("response did not come from the daemon backend")
	}
	if len(tg.daemonCalls) != 1 {
		t.Fatalf("daemon calls = %d, want 1", len(tg.daemonCalls))
	}
	if got := tg.daemonCalls[0].Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization header leaked to the daemon: %q", got)
	}
}

func TestGateway_BlockedRoutes_NeverReachDaemon(t *testing.T) {
	tg := newTestGateway(t)
	token := tg.validToken(t)

	for _, path := range []string{
		"/shutdown",
		"/internal/telemetry/cli-invoked",
		"/api/v1/mobile/status",
		"/api/v1/mobile",
		"/api/v1/dev/import-projects",
		"/api/v1/dev",
		"/healthz",
		"/readyz",
		"/unknown-route",
	} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()

			tg.handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404 (even with a valid token)", rec.Code)
			}
		})
	}
	if len(tg.daemonCalls) != 0 {
		t.Errorf("a blocked route must never reach the daemon, got %d calls", len(tg.daemonCalls))
	}
}

func TestGateway_AllowsSiblingOfBlockedPrefix(t *testing.T) {
	tg := newTestGateway(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/devices", nil)
	req.Header.Set("Authorization", "Bearer "+tg.validToken(t))
	rec := httptest.NewRecorder()

	tg.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: an unrelated sibling of a blocked prefix must not be blocked", rec.Code)
	}
}

func TestGateway_Mux_AcceptsCookieOrHeader(t *testing.T) {
	tg := newTestGateway(t)
	token := tg.validToken(t)

	// A browser can only send the cookie, but the CLI and any other
	// non-browser client have nothing but the header.
	req := httptest.NewRequest(http.MethodGet, muxPath, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	tg.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/mux with only an Authorization header: status = %d, want 200", rec.Code)
	}
	tg.daemonCalls = nil

	req = httptest.NewRequest(http.MethodGet, muxPath, nil)
	req.Header.Set("Origin", testOrigin) // a WebSocket handshake always carries one
	req.AddCookie(&http.Cookie{Name: gatewayCookieName, Value: token})
	rec = httptest.NewRecorder()
	tg.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/mux with the gateway cookie: status = %d, want 200", rec.Code)
	}
	if len(tg.daemonCalls) != 1 {
		t.Fatalf("daemon calls = %d, want 1", len(tg.daemonCalls))
	}
	if _, err := tg.daemonCalls[0].Cookie(gatewayCookieName); err == nil {
		t.Error("the gateway auth cookie must not be forwarded to the daemon")
	}
}

// The daemon behind the gateway sets its own CORS headers and
// httputil.ReverseProxy merges upstream headers with Add, so without
// dropUpstreamCORS the client sees each value twice and every browser
// rejects the response outright.
func TestGateway_UpstreamCORSHeaders_NotDuplicated(t *testing.T) {
	tg := newTestGateway(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	req.Header.Set("Origin", testOrigin)
	req.Header.Set("Authorization", "Bearer "+tg.validToken(t))
	rec := httptest.NewRecorder()

	tg.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Values("Access-Control-Allow-Origin"); len(got) != 1 || got[0] != testOrigin {
		t.Errorf("Access-Control-Allow-Origin = %q, want exactly one value %q", got, testOrigin)
	}
	if got := rec.Header().Values("Access-Control-Allow-Credentials"); len(got) != 1 || got[0] != "true" {
		t.Errorf("Access-Control-Allow-Credentials = %q, want exactly one value \"true\"", got)
	}
	// The gateway does not answer a non-preflight with this header at all, so
	// the only way it could appear is the upstream's copy surviving.
	if got := rec.Header().Values("Access-Control-Allow-Methods"); len(got) != 0 {
		t.Errorf("Access-Control-Allow-Methods = %q, want the upstream's copy dropped", got)
	}
	if len(tg.daemonCalls) != 1 {
		t.Fatalf("daemon calls = %d, want 1", len(tg.daemonCalls))
	}
	if got := tg.daemonCalls[0].Header.Get("Origin"); got != testOrigin {
		t.Fatalf("Origin = %q, want the daemon to have seen the renderer origin (otherwise this test proves nothing)", got)
	}
}

// EventSource has no header API, so the SSE stream is unreachable from the
// renderer unless the cookie authenticates it.
func TestGateway_Events_AcceptsCookie(t *testing.T) {
	tg := newTestGateway(t)
	req := httptest.NewRequest(http.MethodGet, eventsPath, nil)
	req.Header.Set("Origin", testOrigin) // EventSource always sends one
	req.AddCookie(&http.Cookie{Name: gatewayCookieName, Value: tg.validToken(t)})
	rec := httptest.NewRecorder()

	tg.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s with the gateway cookie: status = %d, want 200", eventsPath, rec.Code)
	}
	if len(tg.daemonCalls) != 1 {
		t.Fatalf("daemon calls = %d, want 1", len(tg.daemonCalls))
	}
	if _, err := tg.daemonCalls[0].Cookie(gatewayCookieName); err == nil {
		t.Error("the gateway auth cookie must not be forwarded to the daemon")
	}
}

func TestGateway_EventSourceStreams_AcceptCookie(t *testing.T) {
	for _, path := range []string{
		notificationsStreamPath,
		"/api/v1/sessions/sess-1/workspace/events",
	} {
		tg := newTestGateway(t)
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Origin", testOrigin) // EventSource always sends one
		req.AddCookie(&http.Cookie{Name: gatewayCookieName, Value: tg.validToken(t)})
		rec := httptest.NewRecorder()

		tg.handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s with the gateway cookie: status = %d, want 200", path, rec.Code)
		}
	}
	// Nested-but-different session subpaths stay Bearer-only.
	tg := newTestGateway(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/sess-1/workspace/other", nil)
	req.Header.Set("Origin", testOrigin)
	req.AddCookie(&http.Cookie{Name: gatewayCookieName, Value: tg.validToken(t)})
	rec := httptest.NewRecorder()
	tg.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET workspace/other with only the cookie: status = %d, want 401", rec.Code)
	}
}

// The cookie is ambient, so widening it beyond the routes that cannot
// send a header would make corsGate the only CSRF defence.
func TestGateway_Cookie_RejectedOnEveryOtherRoute(t *testing.T) {
	tg := newTestGateway(t)
	token := tg.validToken(t)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/projects"},
		{http.MethodPost, "/api/v1/projects"},
		{http.MethodPost, eventsPath},
		{http.MethodDelete, eventsPath},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.AddCookie(&http.Cookie{Name: gatewayCookieName, Value: token})
			rec := httptest.NewRecorder()

			tg.handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401: only /mux and GET %s may use the cookie", rec.Code, eventsPath)
			}
		})
	}
	if len(tg.daemonCalls) != 0 {
		t.Errorf("a cookie-only request must never reach the daemon, got %d calls", len(tg.daemonCalls))
	}
}

// TestGateway_Cookie_RejectedWithoutOrigin closes corsGate's one hole: it lets
// a request with no Origin through untouched, and the gateway cookie is
// SameSite=None, so the browser attaches it cross-site. Without this, a
// hostile page's `new Image().src = "https://vm.example.com/api/v1/events"`
// (an image load sends no Origin) is an authenticated SSE stream held open on
// the daemon, and the same shape reaches /mux. Every browser API that can
// legitimately reach these two routes sends an Origin; a non-browser client
// uses the Authorization header, which this does not touch.
func TestGateway_Cookie_RejectedWithoutOrigin(t *testing.T) {
	tg := newTestGateway(t)
	token := tg.validToken(t)

	for _, path := range []string{eventsPath, muxPath} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.AddCookie(&http.Cookie{Name: gatewayCookieName, Value: token})
			rec := httptest.NewRecorder()

			tg.handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401: the ambient cookie must not authenticate a request with no Origin", rec.Code)
			}
		})
	}
	if len(tg.daemonCalls) != 0 {
		t.Errorf("an originless cookie request must never reach the daemon, got %d calls", len(tg.daemonCalls))
	}

	// The header path is what non-browser clients use, and it must be
	// unaffected: no Origin, no cookie, still 200.
	req := httptest.NewRequest(http.MethodGet, eventsPath, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	tg.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s with only an Authorization header: status = %d, want 200", eventsPath, rec.Code)
	}
}

// The daemon runs middleware.RealIP and gates its loopback-only routes on
// what it believes the peer is, so a client must not be able to write its own
// answer.
func TestGateway_ClientForwardingHeaders_NotForwarded(t *testing.T) {
	tg := newTestGateway(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	req.Header.Set("Authorization", "Bearer "+tg.validToken(t))
	req.Header.Set("X-Real-IP", "127.0.0.1")
	req.Header.Set("X-Forwarded-For", "127.0.0.1")
	rec := httptest.NewRecorder()

	tg.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(tg.daemonCalls) != 1 {
		t.Fatalf("daemon calls = %d, want 1", len(tg.daemonCalls))
	}
	forwarded := tg.daemonCalls[0].Header
	if got := forwarded.Get("X-Real-IP"); got != "" {
		t.Errorf("X-Real-IP = %q, want the client's own claim dropped", got)
	}
	// httptest.NewRequest's RemoteAddr, i.e. the real peer and nothing else.
	if got := forwarded.Get("X-Forwarded-For"); got != "192.0.2.1" {
		t.Errorf("X-Forwarded-For = %q, want only the true peer address", got)
	}
}

func TestGateway_PreflightOnBlockedPath_Is404(t *testing.T) {
	tg := newTestGateway(t)
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/mobile/status", nil)
	req.Header.Set("Origin", testOrigin)
	req.Header.Set("Access-Control-Request-Method", "GET")
	rec := httptest.NewRecorder()

	tg.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("preflight status = %d, want 404: a 204 confirms a route every real method 404s", rec.Code)
	}
}

// unauthenticatedHandler builds a handler that only exercises requireToken:
// the daemon address is never dialed because an unauthenticated request must
// never reach the proxy.
func unauthenticatedHandler(t *testing.T, log *slog.Logger) http.Handler {
	t.Helper()
	pub, _, _ := ed25519.GenerateKey(nil)
	cache := &JWKSCache{
		ttl: time.Hour, client: http.DefaultClient, now: time.Now,
		keys: &KeySet{byKID: map[string]ed25519.PublicKey{testKid: pub}}, fetchedAt: time.Now(),
	}
	verify := VerifyOptions{Issuer: testIssuer, Audience: testAud, Subject: testSub, Skew: DefaultSkew}
	h, err := NewHandler("127.0.0.1:1", nil, cache, verify, []string{testOrigin}, log)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return h
}

// TestRequireToken_MissingToken_Logged pins the fix for issue #82: a storm of
// bare requests (no Authorization header, no cookie) used to reject with 401
// and log nothing, leaving a clean journal indistinguishable from a healthy,
// idle gateway. The rejection must now log at Warn, like its two sibling
// rejection paths, distinguishing "no header at all" from "sent something
// malformed" without ever logging token material.
func TestRequireToken_MissingToken_Logged(t *testing.T) {
	cases := []struct {
		name      string
		setHeader func(r *http.Request)
		wantIn    string
	}{
		{name: "no header at all", setHeader: func(*http.Request) {}, wantIn: "no authorization header"},
		{name: "malformed header", setHeader: func(r *http.Request) { r.Header.Set("Authorization", "Basic dXNlcjpwYXNz") }, wantIn: "malformed authorization header"},
		{name: "empty bearer token", setHeader: func(r *http.Request) { r.Header.Set("Authorization", "Bearer ") }, wantIn: "empty bearer token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, nil))
			h := unauthenticatedHandler(t, log)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
			tc.setHeader(req)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			got := buf.String()
			if !strings.Contains(got, "vm gateway: token rejected") {
				t.Fatalf("log does not contain the rejection line:\n%s", got)
			}
			if !strings.Contains(got, tc.wantIn) {
				t.Errorf("log = %q, want it to contain %q", got, tc.wantIn)
			}
			if !strings.Contains(got, "/api/v1/projects") {
				t.Errorf("log = %q, want the request path", got)
			}
		})
	}
}

// TestRequireToken_MissingToken_LogSampling proves the log line added for
// issue #82 cannot itself become a new incident: a gateway on the open
// internet gets a constant trickle of bare requests from scanners, and
// logging every single one at Warn would make a real storm just as hard to
// read as the silence it replaces. Rapid repeats within one window must
// collapse into a single line carrying a suppressed count.
func TestRequireToken_MissingToken_LogSampling(t *testing.T) {
	original := bareRequestLogWindow
	bareRequestLogWindow = 50 * time.Millisecond
	t.Cleanup(func() { bareRequestLogWindow = original })

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	h := unauthenticatedHandler(t, log)

	const burst = 5
	for range burst {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
	if n := strings.Count(buf.String(), "vm gateway: token rejected"); n != 1 {
		t.Fatalf("log lines after a %d-request burst = %d, want 1 (the rest must be throttled within one window):\n%s", burst, n, buf.String())
	}

	// Once the window has passed, the next rejection logs again, and its
	// suppressed count must account for the throttled requests in between.
	time.Sleep(bareRequestLogWindow + 20*time.Millisecond)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	got := buf.String()
	if n := strings.Count(got, "vm gateway: token rejected"); n != 2 {
		t.Fatalf("log lines after the window elapsed = %d, want 2:\n%s", n, got)
	}
	if !strings.Contains(got, "suppressed=4") {
		t.Errorf("log = %q, want the second line to report the 4 requests suppressed during the burst", got)
	}
}

// TestMissingTokenReason pins the classification requireToken logs, in the
// exact shape issue #82 needed: distinguishing an absent header from a
// malformed one, without ever reading header or cookie content beyond a
// presence/shape check.
func TestMissingTokenReason(t *testing.T) {
	cases := []struct {
		name  string
		build func(r *http.Request)
		want  string
	}{
		{name: "nothing at all", build: func(*http.Request) {}, want: "no authorization header"},
		{name: "wrong auth scheme", build: func(r *http.Request) { r.Header.Set("Authorization", "Token abc") }, want: "malformed authorization header"},
		{name: "empty bearer token", build: func(r *http.Request) { r.Header.Set("Authorization", "Bearer ") }, want: "empty bearer token"},
		{name: "empty gateway cookie on mux", build: func(r *http.Request) {
			r.Header.Set("Origin", testOrigin)
			r.URL.Path = muxPath
			r.AddCookie(&http.Cookie{Name: gatewayCookieName, Value: ""})
		}, want: "empty gateway cookie, no authorization header"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
			tc.build(req)
			if got := missingTokenReason(req); got != tc.want {
				t.Errorf("missingTokenReason = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRecoverAndLog_PanicYields500AndLogs pins the gateway's own panic
// recovery: today, a panic anywhere in the middleware chain drops the
// connection with nothing in slog, unlike the daemon it fronts (see
// recoverTelemetry in internal/httpd/recover.go). recoverAndLog must catch
// it, answer with a clean 500 instead of a dropped connection, and log the
// panic.
func TestRecoverAndLog_PanicYields500AndLogs(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	panicky := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})
	h := recoverAndLog(log)(panicky)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "INTERNAL_ERROR") {
		t.Errorf("body = %q, want a clean INTERNAL_ERROR envelope", rec.Body.String())
	}
	got := buf.String()
	if !strings.Contains(got, "vm gateway: handler panic") || !strings.Contains(got, "boom") {
		t.Fatalf("log does not contain the panic line:\n%s", got)
	}
}

// TestGateway_OversizedBody_Rejected pins the request body cap: without it,
// an oversized proxied body is read in full before anything rejects it.
// maxRequestBodyBytes is shrunk for the test so it does not have to build a
// ~35 MiB body to exercise the limit.
func TestGateway_OversizedBody_Rejected(t *testing.T) {
	original := maxRequestBodyBytes
	maxRequestBodyBytes = 16
	t.Cleanup(func() { maxRequestBodyBytes = original })

	tg := newTestGateway(t)
	body := bytes.Repeat([]byte("a"), 64)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.Header.Set("Authorization", "Bearer "+tg.validToken(t))
	rec := httptest.NewRecorder()

	tg.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413, body: %s", rec.Code, rec.Body.String())
	}
}

// TestGateway_ReresolvesDaemonAddrAfterFailure pins the self-healing fix: a
// gateway proxying to a dead daemon address (the daemon was not up yet at
// gateway boot, or has since restarted onto a different port) must not stay
// broken until an operator restarts it. On a failed round trip, the next
// request re-reads the daemon address via resolveDaemonAddr and succeeds.
func TestGateway_ReresolvesDaemonAddrAfterFailure(t *testing.T) {
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(daemon.Close)
	daemonURL, err := url.Parse(daemon.URL)
	if err != nil {
		t.Fatalf("parse daemon url: %v", err)
	}

	pub, priv, _ := ed25519.GenerateKey(nil)
	now := time.Now()
	cache := &JWKSCache{
		ttl: time.Hour, client: http.DefaultClient, now: func() time.Time { return now },
		keys: &KeySet{byKID: map[string]ed25519.PublicKey{testKid: pub}}, fetchedAt: now,
	}
	verify := VerifyOptions{Issuer: testIssuer, Audience: testAud, Subject: testSub, Skew: DefaultSkew, Now: func() time.Time { return now }}
	token := signToken(t, priv, map[string]any{"alg": "EdDSA", "kid": testKid}, validClaims(now))

	var resolveCalls int32
	resolve := func() (string, bool) {
		atomic.AddInt32(&resolveCalls, 1)
		return daemonURL.Host, true
	}

	// 127.0.0.1:1 refuses the connection immediately (nothing listens there
	// without root), standing in for a daemon that has not started yet.
	h, err := NewHandler("127.0.0.1:1", resolve, cache, verify, nil, discardLogger())
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("first request status = %d, want 502 against a dead daemon address, body: %s", rec.Code, rec.Body.String())
	}
	if got := atomic.LoadInt32(&resolveCalls); got != 1 {
		t.Fatalf("resolveDaemonAddr calls = %d, want 1 after the failed round trip", got)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	req2.Header.Set("Authorization", "Bearer "+token)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second request status = %d, want 200 once re-resolved to the live daemon, body: %s", rec2.Code, rec2.Body.String())
	}
}

func TestIsProxyablePath(t *testing.T) {
	cases := map[string]bool{
		"/mux":                        true,
		"/muxed":                      false,
		"/api/v1":                     true,
		"/api/v1/projects":            true,
		"/api/v1/mobile":              false,
		"/api/v1/mobile/status":       false,
		"/api/v1/mobileapp":           true,
		"/api/v1/dev":                 false,
		"/api/v1/dev/import-projects": false,
		"/api/v1/devices":             true,
		"/shutdown":                   false,
		"/internal/telemetry":         false,
		"/healthz":                    false,
		"":                            false,
	}
	for path, want := range cases {
		if got := isProxyablePath(path); got != want {
			t.Errorf("isProxyablePath(%q) = %v, want %v", path, got, want)
		}
	}
}
