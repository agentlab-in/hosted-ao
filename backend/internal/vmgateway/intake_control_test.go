package vmgateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Pin every new sensitive route against both real gateway authentication modes.
func TestIntakeControlRoutesNeverReachDaemon(t *testing.T) {
	routes := []struct{ method, path string }{
		{"GET", "/api/v1/agents/codex/accounts"},
		{"GET", "/api/v1/agents/codex/accounts/events"},
		{"POST", "/api/v1/agents/codex/accounts/ensure"},
		{"POST", "/api/v1/agents/codex/accounts/a/reset-credit/consume"},
		{"POST", "/api/v1/agents/codex/accounts/a/login-terminal"},
		{"POST", "/api/v1/agents/codex/accounts/a/logout"},
		{"DELETE", "/api/v1/agents/codex/accounts/a"},
		{"POST", "/api/v1/agents/codex/accounts/login-terminal"},
		{"POST", "/api/v1/agents/codex/accounts/login-operations/op/verify"},
		{"POST", "/api/v1/agents/codex/accounts/login-operations/op/cancel"},
		{"POST", "/api/v1/agents/codex/account-switches"},
		{"POST", "/api/v1/agents/codex/account-switches/sw/recover"},
		{"POST", "/api/v1/agents/claude-code/install"},
		{"POST", "/api/v1/agents/claude-code/install/"},
		{"GET", "/api/v1/agents/installers"},
		{"GET", "/api/v1/agents/install-jobs"},
		{"GET", "/api/v1/agents/claude-code/install"},
		{"GET", "/api/v1/agents/claude-code/install/history"},
		{"POST", "/api/v1/agents/claude-code/verify"},
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
				for _, auth := range []string{"", "Bearer invalid", "Bearer " + credential} {
					req := httptest.NewRequest(route.method, route.path, nil)
					req.Host = "127.0.0.1"
					req.Header.Set("Authorization", auth)
					w := httptest.NewRecorder()
					handler.ServeHTTP(w, req)
					if w.Code != http.StatusNotFound {
						t.Errorf("%s %s: status=%d, want 404", route.method, route.path, w.Code)
					}
				}
				req := httptest.NewRequest(http.MethodOptions, route.path, nil)
				req.Header.Set("Origin", testOrigin)
				req.Header.Set("Access-Control-Request-Method", route.method)
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, req)
				if w.Code != http.StatusNotFound {
					t.Errorf("preflight %s %s: status=%d, want 404", route.method, route.path, w.Code)
				}
			}
			if calls() != 0 {
				t.Fatalf("blocked controls reached daemon %d times", calls())
			}
			// Ordinary API and segment-boundary siblings still require authentication.
			for _, route := range []struct{ method, path string }{
				{"GET", "/api/v1/agents/codexapp/accounts"},
				{"GET", "/api/v1/identity"},
			} {
				req := httptest.NewRequest(route.method, route.path, nil)
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, req)
				if w.Code != http.StatusUnauthorized {
					t.Errorf("anonymous %s %s: status=%d, want 401", route.method, route.path, w.Code)
				}
				req.Header.Set("Authorization", "Bearer "+credential)
				w = httptest.NewRecorder()
				handler.ServeHTTP(w, req)
				if w.Code != http.StatusOK {
					t.Errorf("authenticated %s %s: status=%d, want 200", route.method, route.path, w.Code)
				}
			}
		})
	}
}
