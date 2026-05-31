package notification

import (
	"slices"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type RouteDecision struct {
	RouteName      string
	Sink           string
	DestinationKey string
	Status         DeliveryStatus
	Reason         string
	CreateDelivery bool
}

// ResolveRoutes resolves the configured built-in routes for one notification.
// Dashboard is a read model over the notifications table, so it is represented
// in the decision list but never creates a delivery row. Unknown explicitly
// configured routes become skipped delivery rows for operator visibility.
func ResolveRoutes(settings config.NotificationConfig, priority ports.Priority) []RouteDecision {
	settings = NormalizeSettings(settings)
	if !settings.Enabled {
		return nil
	}

	routes, ok := settings.Routing.Priorities[priority]
	if !ok {
		return nil
	}
	out := make([]RouteDecision, 0, len(routes))
	for _, name := range routes {
		switch name {
		case RouteDashboard:
			if settings.Dashboard.Enabled {
				out = append(out, RouteDecision{RouteName: RouteDashboard})
			}
		case RouteDesktop:
			if settings.Desktop.Enabled && priorityAllowed(priority, settings.Desktop.Priorities) {
				out = append(out, RouteDecision{
					RouteName:      RouteDesktop,
					Sink:           SinkAOApp,
					Status:         DeliveryQueued,
					CreateDelivery: true,
				})
			}
		case "":
			// Ignore empty route names so a stray trailing separator in future
			// config parsing does not create a permanent skipped delivery.
		default:
			out = append(out, RouteDecision{
				RouteName:      name,
				Sink:           SinkUnknown,
				Status:         DeliverySkipped,
				Reason:         "unknown route",
				CreateDelivery: true,
			})
		}
	}
	return out
}

func DesktopEligible(settings config.NotificationConfig, priority ports.Priority) bool {
	settings = NormalizeSettings(settings)
	return settings.Enabled && settings.Desktop.Enabled && priorityAllowed(priority, settings.Desktop.Priorities)
}

func priorityAllowed(p ports.Priority, allowed []ports.Priority) bool {
	return slices.Contains(allowed, p)
}
