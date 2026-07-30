// Package server builds the control plane's HTTP handler.
package server

import (
	"database/sql"
	"encoding/json"
	"net/http"
)

// New builds the control plane's HTTP handler, registering /healthz. Later
// batches add the OAuth, JWKS, and device-flow routes to the same mux.
func New(db *sql.DB) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthHandler(db))
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
