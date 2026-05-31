// Package httpd builds and runs the daemon's HTTP surface. Phase 1a is the
// skeleton: the middleware stack, liveness/readiness probes, and a graceful
// run loop. Route registration (/api/v1, /events, /mux, /) lands in later
// phases on top of the router this package builds.
package httpd

import (
	"log/slog"
	"net"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/daemonmeta"
	"github.com/aoagents/agent-orchestrator/backend/internal/terminal"
)

// NewRouter builds the root router with the standard middleware stack and the
// health probes mounted.
//
// Middleware order (outermost first):
//
//	Recoverer      → turn a handler panic into 500 instead of crashing the daemon
//	RequestID      → attach a request id for correlation
//	requestLogger  → slog-backed access log, stderr, carries the request id
//	RealIP         → normalise client IP (loopback proxy from the dev server)
//
// The per-request Timeout from the decision table is deliberately NOT applied
// globally: it must wrap only the /api/v1 REST surface, never the long-lived
// SSE (/events) or WebSocket (/mux) surfaces, nor the always-must-answer health
// probes. It is therefore applied per-surface when those subrouters are mounted
// in Phase 1b; cfg.RequestTimeout carries the value through to that point.
func NewRouter(cfg config.Config, log *slog.Logger, termMgr *terminal.Manager) chi.Router {
	return NewRouterWithAPI(cfg, log, termMgr, APIDeps{})
}

type ControlDeps struct {
	RequestShutdown func()
}

// NewRouterWithAPI is the dependency-injected variant. main.go calls it with
// real Managers when they exist; tests/dev wiring inject mocks explicitly.
// Missing Managers intentionally keep the route-shell 501 behavior.
func NewRouterWithAPI(cfg config.Config, log *slog.Logger, termMgr *terminal.Manager, deps APIDeps) chi.Router {
	return NewRouterWithControl(cfg, log, termMgr, deps, ControlDeps{})
}

func NewRouterWithControl(cfg config.Config, log *slog.Logger, termMgr *terminal.Manager, deps APIDeps, control ControlDeps) chi.Router {
	r := chi.NewRouter()

	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(requestLogger(log))
	r.Use(middleware.RealIP)

	// JSON envelopes for unmatched routes / methods — chi's defaults are
	// text/plain, which would break consumers that parse every response as
	// the locked APIError shape.
	r.NotFound(notFoundJSON)
	r.MethodNotAllowed(methodNotAllowedJSON)

	mountHealth(r)
	mountMux(r, termMgr, log)
	mountControl(r, control)
	NewAPI(cfg, deps).Register(r)

	return r
}

// mountHealth registers the liveness and readiness probes the Electron
// supervisor polls before letting the renderer connect.
func mountHealth(r chi.Router) {
	r.Get("/healthz", handleHealthz)
	r.Get("/readyz", handleReadyz)
}

// mountControl registers the loopback daemon-control endpoints. /shutdown is
// unauthenticated and state-changing, so it is gated by localControlRequest to
// keep a browser the user happens to have open (CSRF / DNS-rebinding) or a
// remote client from being able to kill the daemon.
func mountControl(r chi.Router, deps ControlDeps) {
	if deps.RequestShutdown == nil {
		return
	}
	r.Post("/shutdown", func(w http.ResponseWriter, req *http.Request) {
		if !localControlRequest(req) {
			writeJSON(w, http.StatusForbidden, map[string]any{
				"status":  "forbidden",
				"service": daemonmeta.ServiceName,
			})
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{
			"status":  "shutting_down",
			"service": daemonmeta.ServiceName,
			"pid":     os.Getpid(),
		})
		deps.RequestShutdown()
	})
}

// localControlRequest reports whether a control request is a trusted local
// caller. The Go CLI client addresses the daemon by its loopback host and
// never sets an Origin header; a cross-site browser fetch always carries an
// Origin, and a DNS-rebinding attempt resolves a non-loopback Host. Rejecting
// either closes the CSRF/rebinding vector while leaving the CLI unaffected.
func localControlRequest(r *http.Request) bool {
	if r.Header.Get("Origin") != "" {
		return false
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	switch host {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// handleHealthz is the liveness probe: it answers 200 as long as the process is
// up and serving. It does no dependency checks by design.
func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"service": daemonmeta.ServiceName,
		"pid":     os.Getpid(),
	})
}

// handleReadyz is the readiness probe. In the 1a skeleton the daemon is ready
// as soon as it is listening; later phases will gate this on dependency
// initialisation (e.g. store/event-bus warm-up).
func handleReadyz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ready",
		"service": daemonmeta.ServiceName,
		"pid":     os.Getpid(),
	})
}
