package notification

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type SettingsProvider interface {
	Settings(ctx context.Context) config.NotificationConfig
}

type staticSettings struct {
	cfg config.NotificationConfig
}

func SettingsFromConfig(cfg config.Config) SettingsProvider {
	return staticSettings{cfg: NormalizeSettings(cfg.Notifications)}
}

func StaticSettings(cfg config.NotificationConfig) SettingsProvider {
	return staticSettings{cfg: NormalizeSettings(cfg)}
}

func (s staticSettings) Settings(context.Context) config.NotificationConfig {
	return cloneSettings(s.cfg)
}

// NormalizeSettings fills unset settings with safe defaults while preserving
// explicit route overrides, including an explicit empty route list.
func NormalizeSettings(in config.NotificationConfig) config.NotificationConfig {
	def := config.DefaultNotificationConfig()
	out := in

	if out.Dashboard.Limit == 0 {
		out.Dashboard.Limit = def.Dashboard.Limit
	}
	if out.Desktop.Priorities == nil {
		out.Desktop.Priorities = append([]ports.Priority(nil), def.Desktop.Priorities...)
	}
	if out.Desktop.SoundPriorities == nil {
		out.Desktop.SoundPriorities = append([]ports.Priority(nil), def.Desktop.SoundPriorities...)
	}
	if out.Routing.Priorities == nil {
		out.Routing.Priorities = cloneRoutes(def.Routing.Priorities)
	} else {
		merged := cloneRoutes(def.Routing.Priorities)
		for p, routes := range out.Routing.Priorities {
			merged[p] = append([]string(nil), routes...)
		}
		out.Routing.Priorities = merged
	}
	if out.Retry.MaxAttempts == 0 {
		out.Retry.MaxAttempts = def.Retry.MaxAttempts
	}
	if out.Retry.BaseDelay == 0 {
		out.Retry.BaseDelay = def.Retry.BaseDelay
	}
	if out.Retry.MaxDelay == 0 {
		out.Retry.MaxDelay = def.Retry.MaxDelay
	}
	if out.Retry.LeaseTTL == 0 {
		out.Retry.LeaseTTL = def.Retry.LeaseTTL
	}
	if out.Retry.BatchSize == 0 {
		out.Retry.BatchSize = def.Retry.BatchSize
	}
	return cloneSettings(out)
}

func cloneSettings(in config.NotificationConfig) config.NotificationConfig {
	out := in
	out.Desktop.Priorities = append([]ports.Priority(nil), in.Desktop.Priorities...)
	out.Desktop.SoundPriorities = append([]ports.Priority(nil), in.Desktop.SoundPriorities...)
	out.Routing.Priorities = cloneRoutes(in.Routing.Priorities)
	return out
}

func cloneRoutes(in map[ports.Priority][]string) map[ports.Priority][]string {
	if in == nil {
		return nil
	}
	out := make(map[ports.Priority][]string, len(in))
	for p, routes := range in {
		out[p] = append([]string(nil), routes...)
	}
	return out
}
