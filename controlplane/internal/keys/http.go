package keys

import (
	"encoding/json"
	"net/http"
)

// Register adds the JWKS endpoint to mux. Called once from server.New so the
// keys package owns its own route instead of server.go growing a handler for
// it.
func Register(mux *http.ServeMux, m *Manager) {
	mux.HandleFunc("GET /.well-known/jwks.json", jwksHandler(m))
}

func jwksHandler(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Matches the "JWKS cache: 1 hour" clause of TOKEN_CONTRACT.md;
		// stale-if-error is the fetching verifier's responsibility, not
		// this server's.
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_ = json.NewEncoder(w).Encode(m.JWKS())
	}
}
