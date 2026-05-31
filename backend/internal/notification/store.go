package notification

import (
	"context"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// Store is the central notifier runtime's durable interface. The lifecycle
// enqueuer writes notification rows; this interface routes them into durable
// delivery rows and lets AO-app/API code claim and complete desktop handoffs.
type Store interface {
	ListUnroutedNotifications(ctx context.Context, limit int) ([]domain.Notification, error)
	MarkNotificationRouted(ctx context.Context, id domain.NotificationID, at time.Time) error

	GetDelivery(ctx context.Context, id string) (DeliveryRow, bool, error)
	EnqueueDelivery(ctx context.Context, row DeliveryRow) (DeliveryRow, bool, error)
	ClaimDueDeliveries(ctx context.Context, sink string, owner string, now time.Time, limit int, lease time.Duration) ([]DeliveryRow, error)
	ReleaseExpiredDeliveryLeases(ctx context.Context, now time.Time) (int, error)
	MarkDeliverySent(ctx context.Context, id string, owner string, externalID string, at time.Time) error
	MarkDeliveryRetry(ctx context.Context, id string, owner string, errCode string, errMessage string, next time.Time, at time.Time) error
	MarkDeliveryFailed(ctx context.Context, id string, owner string, errCode string, errMessage string, at time.Time) error
	MarkDeliverySkipped(ctx context.Context, id string, reason string, at time.Time) error
}
