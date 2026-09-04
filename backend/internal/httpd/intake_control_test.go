package httpd

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIntakeLANControlMethods(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		blocked      bool
	}{
		{http.MethodGet, "/api/v1/agents/codex/accounts", true},
		{http.MethodGet, "/api/v1/agents/codex/accounts/events", true},
		{http.MethodPost, "/api/v1/agents/codex/account-switches", true},
		{http.MethodPost, "/api/v1/agents/claude-code/install", true},
		{http.MethodPost, "/api/v1/agents/claude-code/verify", true},
		{http.MethodPost, "/api/v1/agents/claude-code/verify/", true},
		{http.MethodGet, "/api/v1/agents/installers", true},
		{http.MethodGet, "/api/v1/agents/install-jobs", true},
		{http.MethodGet, "/api/v1/agents/claude-code/install", true},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			calls := 0
			h := lanControlBlock(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.WriteHeader(http.StatusUnauthorized)
			}))
			r := httptest.NewRequest(tc.method, tc.path, nil)
			r.Host = "localhost"
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if tc.blocked {
				if w.Code != http.StatusNotFound || calls != 0 {
					t.Fatalf("status=%d calls=%d, want 404 and no inner calls", w.Code, calls)
				}
			} else if w.Code != http.StatusUnauthorized || calls != 1 {
				t.Fatalf("status=%d calls=%d, want inner authentication", w.Code, calls)
			}
		})
	}
}
