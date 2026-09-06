package vmgateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Codex account events remain inside the local account-control boundary.
// A renderer choosing EventSource must not implicitly widen route or cookie access.
func TestIntakeCodexStreamDoesNotExpandCookieBoundary(t *testing.T) {
	routes := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/agents/codex/accounts/events"},
		{http.MethodGet, "/api/v1/agents/codex/accounts/events/"},
		{http.MethodGet, "/api/v1/agents/codex/accounts/events/child"},
		{http.MethodGet, "/api/v1/agents/codex/accounts/events-other"},
		{http.MethodGet, "/api/v1/agents/codex/accounts"},
		{http.MethodPost, "/api/v1/agents/codex/accounts/events"},
		{http.MethodPost, "/api/v1/agents/codex/accounts/a/logout"},
		{http.MethodDelete, "/api/v1/agents/codex/accounts/a"},
	}
	for _, mode := range []string{"hosted", "pair"} {
		t.Run(mode, func(t *testing.T) {
			var handler http.Handler
			var credential string
			var calls func() int
			if mode == "hosted" {
				gateway := newTestGateway(t)
				handler, credential = gateway.handler, gateway.validToken(t)
				calls = func() int { return len(gateway.daemonCalls) }
			} else {
				gateway := newTestPairGateway(t)
				handler, credential = gateway.handler, gateway.passcode
				calls = func() int { return len(gateway.daemonCalls) }
			}
			for _, route := range routes {
				for _, origin := range []string{testOrigin, "", "https://hostile.example"} {
					for _, cookie := range []string{"", "invalid", credential} {
						req := httptest.NewRequest(route.method, route.path, nil)
						req.Host = "127.0.0.1"
						if origin != "" {
							req.Header.Set("Origin", origin)
						}
						if cookie != "" {
							req.AddCookie(&http.Cookie{Name: gatewayCookieName, Value: cookie})
						}
						if cookieAuthAllowed(req) {
							t.Fatalf("cookie unexpectedly eligible: %s %s", route.method, route.path)
						}
						if _, ok := extractToken(req); ok {
							t.Fatalf("cookie became a credential: %s %s", route.method, route.path)
						}
						w := httptest.NewRecorder()
						handler.ServeHTTP(w, req)
						want := http.StatusNotFound
						if origin == "https://hostile.example" {
							want = http.StatusForbidden
						}
						if w.Code != want {
							t.Errorf("%s %s origin=%q: status=%d, want %d", route.method, route.path, origin, w.Code, want)
						}
					}
				}
			}
			if calls() != 0 {
				t.Fatalf("Codex account stream/control reached daemon %d times", calls())
			}
		})
	}
}
