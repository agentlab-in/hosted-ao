// Package server builds the control plane's HTTP handler.
package server

import (
	"database/sql"
	"encoding/json"
	"net/http"

	"github.com/agentlab-in/hosted-ao/controlplane/internal/keys"
)

// New builds the control plane's HTTP handler, registering /healthz and the
// JWKS endpoint. Later batches add the OAuth and device-flow routes to the
// same mux.
func New(db *sql.DB, km *keys.Manager) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthHandler(db))
	keys.Register(mux, km)
	return mux
}

func healthHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := db.PingContext(r.Context()); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "unavailable"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}
