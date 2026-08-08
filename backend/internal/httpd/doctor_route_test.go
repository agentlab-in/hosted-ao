package httpd

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/vmgateway"
)

// mountedDoctorRoutes returns the doctor routes the daemon actually mounts,
// so the assertions below are pinned to the router rather than to a path
// literal that could drift from it.
func mountedDoctorRoutes(t *testing.T) []string {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := NewRouterWithControl(config.Config{}, log, nil, APIDeps{}, ControlDeps{})

	var routes []string
	err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.Contains(route, "doctor") {
			routes = append(routes, strings.ToUpper(method)+" "+route)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk routes: %v", err)
	}
	return routes
}

func TestDoctorRouteIsMounted(t *testing.T) {
	routes := mountedDoctorRoutes(t)
	if len(routes) != 1 || routes[0] != "GET /api/v1/doctor" {
		t.Fatalf("doctor routes = %v, want exactly [GET /api/v1/doctor]", routes)
	}
}

// TestDoctorRouteIsGatewayProxyable covers the one failure mode no other test
// would catch. The desktop reaches a machine through the VM gateway, which
// forwards /api/v1 and below except the loopback-only mobile and dev
// prefixes; a doctor route mounted anywhere else would pass every daemon test
// and still be unreachable from the app, making the whole route pointless.
//
// The gateway answers 404 for a path outside its allowlist, before auth runs,
// and 401 for one inside it, so an unauthenticated probe tells the two apart
// without needing a signed token or a JWKS endpoint.
func TestDoctorRouteIsGatewayProxyable(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	// The daemon address is never dialled: every request here stops at the
	// gateway's path allowlist or its token check, well before the proxy.
	gateway, err := vmgateway.NewHandler("127.0.0.1:1", nil, nil, vmgateway.VerifyOptions{}, nil, log)
	if err != nil {
		t.Fatalf("build gateway handler: %v", err)
	}

	for _, route := range mountedDoctorRoutes(t) {
		method, path, _ := strings.Cut(route, " ")
		rec := httptest.NewRecorder()
		gateway.ServeHTTP(rec, httptest.NewRequest(method, path, http.NoBody))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("gateway %s = %d, want 401 (404 means the gateway refuses to forward this path)", route, rec.Code)
		}
	}

	// Control: a route the gateway deliberately blocks answers 404, so the
	// assertion above is really distinguishing allowed from blocked.
	rec := httptest.NewRecorder()
	gateway.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/mobile/status", http.NoBody))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("gateway GET /api/v1/mobile/status = %d, want 404", rec.Code)
	}
}
