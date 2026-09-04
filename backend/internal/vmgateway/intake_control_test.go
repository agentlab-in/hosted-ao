package vmgateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIntakeControlRoutesNeverReachDaemon(t *testing.T) {
	hosted := newTestGateway(t)
	pair := newTestPairGateway(t)
	for _, gateway := range []struct {
		name       string
		handler    http.Handler
		credential string
		calls      func() int
	}{
		{"hosted", hosted.handler, hosted.validToken(t), func() int { return len(hosted.daemonCalls) }},
		{"pair", pair.handler, pair.passcode, func() int { return len(pair.daemonCalls) }},
	} {
		for _, route := range []struct{ method, path string }{
			{http.MethodGet, "/api/v1/agents/codex/accounts"},
			{http.MethodGet, "/api/v1/agents/codex/accounts/events"},
			{http.MethodPost, "/api/v1/agents/codex/accounts/ensure"},
			{http.MethodPost, "/api/v1/agents/codex/accounts/account/reset-credit/consume"},
			{http.MethodPost, "/api/v1/agents/codex/accounts/account/login-terminal"},
			{http.MethodPost, "/api/v1/agents/codex/accounts/account/logout"},
			{http.MethodDelete, "/api/v1/agents/codex/accounts/account"},
			{http.MethodPost, "/api/v1/agents/codex/accounts/login-terminal"},
			{http.MethodPost, "/api/v1/agents/codex/accounts/login-operations/operation/verify"},
			{http.MethodPost, "/api/v1/agents/codex/accounts/login-operations/operation/cancel"},
			{http.MethodPost, "/api/v1/agents/codex/account-switches"},
			{http.MethodPost, "/api/v1/agents/codex/account-switches/switch/recover"},
			{http.MethodPost, "/api/v1/agents/claude-code/install"},
			{http.MethodPost, "/api/v1/agents/claude-code/install/"},
			{http.MethodPost, "/api/v1/agents/claude-code/verify"},
			{http.MethodGet, "/api/v1/desktop/sessions/session/workspace"},
		} {
			for _, credential := range []string{"", "invalid", gateway.credential} {
				t.Run(gateway.name+"/"+route.method+route.path, func(t *testing.T) {
					req := httptest.NewRequest(route.method, route.path, nil)
					req.Host = "localhost"
					if credential != "" {
						req.Header.Set("Authorization", "Bearer "+credential)
					}
					w := httptest.NewRecorder()
					gateway.handler.ServeHTTP(w, req)
					if w.Code != http.StatusNotFound || gateway.calls() != 0 {
						t.Fatalf("status=%d daemonCalls=%d, want 404 and zero calls", w.Code, gateway.calls())
					}
				})
			}
		}
	}
}

func TestIntakeReadOnlyInstallerRoutesRequireAuthentication(t *testing.T) {
	for _, path := range []string{"/api/v1/agents/installers", "/api/v1/agents/install-jobs", "/api/v1/agents/claude-code/install"} {
		t.Run(path, func(t *testing.T) {
			gateway := newTestGateway(t)
			for _, authenticated := range []bool{false, true} {
				req := httptest.NewRequest(http.MethodGet, path, nil)
				want := http.StatusUnauthorized
				if authenticated {
					req.Header.Set("Authorization", "Bearer "+gateway.validToken(t))
					want = http.StatusOK
				}
				w := httptest.NewRecorder()
				gateway.handler.ServeHTTP(w, req)
				if w.Code != want {
					t.Fatalf("status=%d, want %d", w.Code, want)
				}
				if !authenticated && len(gateway.daemonCalls) != 0 {
					t.Fatal("unauthenticated request reached daemon")
				}
			}
			if len(gateway.daemonCalls) != 1 {
				t.Fatalf("daemon calls=%d, want 1", len(gateway.daemonCalls))
			}
		})
	}
}
