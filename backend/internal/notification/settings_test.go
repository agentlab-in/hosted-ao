package notification

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestSettingsFromConfigDefaultsWhenUnset(t *testing.T) {
	got := SettingsFromConfig(config.Config{Notifications: config.DefaultNotificationConfig()}).Settings(context.Background())
	if !got.Enabled || !got.Desktop.Enabled || !got.Dashboard.Enabled {
		t.Fatalf("default config should resolve safe enabled defaults: %+v", got)
	}
	if got.Retry.MaxAttempts != 5 || got.Retry.BatchSize != 50 {
		t.Fatalf("retry defaults = %+v", got.Retry)
	}
}

func TestSettingsFromConfigPreservesExplicitGlobalDisable(t *testing.T) {
	got := SettingsFromConfig(config.Config{Notifications: config.NotificationConfig{Enabled: false}}).Settings(context.Background())
	if got.Enabled {
		t.Fatalf("explicit disabled notifications should stay disabled: %+v", got)
	}
	if got.Retry.MaxAttempts != 5 || got.Routing.Priorities == nil {
		t.Fatalf("disabled config should still receive non-global defaults: %+v", got)
	}
}

func TestNormalizeSettingsPreservesExplicitSurfaceDisables(t *testing.T) {
	got := StaticSettings(config.NotificationConfig{
		Enabled:   true,
		Dashboard: config.DashboardNotificationConfig{Enabled: false},
		Desktop:   config.DesktopNotificationConfig{Enabled: false},
	}).Settings(context.Background())

	if !got.Enabled {
		t.Fatalf("global notifications should remain enabled: %+v", got)
	}
	if got.Dashboard.Enabled {
		t.Fatalf("explicit dashboard disable should stay disabled: %+v", got.Dashboard)
	}
	if got.Desktop.Enabled {
		t.Fatalf("explicit desktop disable should stay disabled: %+v", got.Desktop)
	}
	if got.Dashboard.Limit != 50 || len(got.Desktop.Priorities) == 0 || got.Retry.MaxAttempts != 5 {
		t.Fatalf("disabled surfaces should still receive non-bool defaults: %+v", got)
	}
}

func TestNormalizeSettingsPreservesExplicitEmptyRoute(t *testing.T) {
	cfg := config.DefaultNotificationConfig()
	cfg.Routing.Priorities[ports.PriorityUrgent] = []string{}

	got := StaticSettings(cfg).Settings(context.Background())
	if routes := got.Routing.Priorities[ports.PriorityUrgent]; len(routes) != 0 {
		t.Fatalf("explicit empty urgent route should be preserved, got %v", routes)
	}
}

func TestSettingsProviderReturnsClone(t *testing.T) {
	cfg := config.DefaultNotificationConfig()
	provider := StaticSettings(cfg)
	first := provider.Settings(context.Background())
	first.Desktop.Priorities[0] = ports.PriorityInfo
	first.Routing.Priorities[ports.PriorityUrgent][0] = "mutated"

	second := provider.Settings(context.Background())
	if second.Desktop.Priorities[0] != ports.PriorityUrgent {
		t.Fatalf("desktop priorities were mutated through clone: %v", second.Desktop.Priorities)
	}
	if second.Routing.Priorities[ports.PriorityUrgent][0] != RouteDashboard {
		t.Fatalf("routes were mutated through clone: %v", second.Routing.Priorities[ports.PriorityUrgent])
	}
}
