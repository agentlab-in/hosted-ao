package notification

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestResolveRoutes_Defaults(t *testing.T) {
	cfg := config.DefaultNotificationConfig()
	tests := []struct {
		priority ports.Priority
		want     []string
	}{
		{ports.PriorityUrgent, []string{RouteDashboard, RouteDesktop}},
		{ports.PriorityAction, []string{RouteDashboard, RouteDesktop}},
		{ports.PriorityWarning, []string{RouteDashboard}},
		{ports.PriorityInfo, []string{RouteDashboard}},
	}
	for _, tc := range tests {
		t.Run(string(tc.priority), func(t *testing.T) {
			got := routeNames(ResolveRoutes(cfg, tc.priority))
			if len(got) != len(tc.want) {
				t.Fatalf("routes = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("routes = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestResolveRoutes_DesktopDisabledOrIneligible(t *testing.T) {
	cfg := config.DefaultNotificationConfig()
	cfg.Desktop.Enabled = false
	got := routeNames(ResolveRoutes(cfg, ports.PriorityUrgent))
	if len(got) != 1 || got[0] != RouteDashboard {
		t.Fatalf("desktop disabled routes = %v, want dashboard only", got)
	}

	cfg = config.DefaultNotificationConfig()
	cfg.Routing.Priorities[ports.PriorityInfo] = []string{RouteDashboard, RouteDesktop}
	got = routeNames(ResolveRoutes(cfg, ports.PriorityInfo))
	if len(got) != 1 || got[0] != RouteDashboard {
		t.Fatalf("info desktop ineligible routes = %v, want dashboard only", got)
	}
}

func TestResolveRoutes_GlobalDisabled(t *testing.T) {
	cfg := config.DefaultNotificationConfig()
	cfg.Enabled = false
	if got := ResolveRoutes(cfg, ports.PriorityUrgent); len(got) != 0 {
		t.Fatalf("globally disabled routes = %+v, want none", got)
	}
}

func TestResolveRoutes_ExplicitEmptySuppressesPriority(t *testing.T) {
	cfg := config.DefaultNotificationConfig()
	cfg.Routing.Priorities[ports.PriorityUrgent] = []string{}
	if got := ResolveRoutes(cfg, ports.PriorityUrgent); len(got) != 0 {
		t.Fatalf("explicit empty routes = %+v, want none", got)
	}
}

func TestResolveRoutes_UnknownExplicitRouteSkipped(t *testing.T) {
	cfg := config.DefaultNotificationConfig()
	cfg.Routing.Priorities[ports.PriorityUrgent] = []string{RouteDashboard, "pager"}
	got := ResolveRoutes(cfg, ports.PriorityUrgent)
	if len(got) != 2 {
		t.Fatalf("routes = %+v, want dashboard + skipped unknown", got)
	}
	unknown := got[1]
	if unknown.RouteName != "pager" || unknown.Status != DeliverySkipped || !unknown.CreateDelivery || unknown.Sink != SinkUnknown {
		t.Fatalf("unknown route decision = %+v", unknown)
	}
}

func routeNames(routes []RouteDecision) []string {
	out := make([]string, len(routes))
	for i, r := range routes {
		out[i] = r.RouteName
	}
	return out
}
