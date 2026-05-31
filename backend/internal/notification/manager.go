package notification

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type Manager struct {
	store    Store
	settings SettingsProvider
	clock    func() time.Time
	logger   *slog.Logger

	interval time.Duration
}

func NewManager(store Store, settings SettingsProvider, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	if settings == nil {
		settings = StaticSettings(config.DefaultNotificationConfig())
	}
	return &Manager{
		store:    store,
		settings: settings,
		clock:    time.Now,
		logger:   logger,
		interval: time.Second,
	}
}

func (m *Manager) Start(ctx context.Context) <-chan struct{} {
	return startDispatcher(ctx, m)
}

// RunOnce performs one maintenance/routing pass. It is exposed for tests and
// for future API-triggered nudges; Start calls it on every dispatcher tick.
func (m *Manager) RunOnce(ctx context.Context) error {
	settings := m.settings.Settings(ctx)
	policy := RetryPolicyFromConfig(settings.Retry)
	now := m.clock().UTC()

	if released, err := m.store.ReleaseExpiredDeliveryLeases(ctx, now); err != nil {
		return err
	} else if released > 0 {
		m.logger.DebugContext(ctx, "notification delivery leases released", "count", released)
	}

	_, err := m.RoutePending(ctx, policy.BatchSize)
	return err
}

func (m *Manager) RoutePending(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = RetryPolicyFromConfig(m.settings.Settings(ctx).Retry).BatchSize
	}
	rows, err := m.store.ListUnroutedNotifications(ctx, limit)
	if err != nil {
		return 0, err
	}
	var firstErr error
	var routed int
	for _, row := range rows {
		if err := m.RouteNotification(ctx, row); err != nil {
			m.logger.ErrorContext(ctx, "route notification", "notification", row.ID, "err", err)
			if firstErr == nil {
				firstErr = err
			} else {
				firstErr = errors.Join(firstErr, err)
			}
			continue
		}
		routed++
	}
	return routed, firstErr
}

func (m *Manager) RouteNotification(ctx context.Context, row domain.Notification) error {
	settings := m.settings.Settings(ctx)
	now := m.clock().UTC()
	if !settings.Enabled || !row.ArchivedAt.IsZero() {
		return m.store.MarkNotificationRouted(ctx, row.ID, now)
	}

	decisions := ResolveRoutes(settings, ports.Priority(row.Priority))
	maxAttempts := RetryPolicyFromConfig(settings.Retry).MaxAttempts
	for _, decision := range decisions {
		if !decision.CreateDelivery {
			continue
		}
		delivery := DeliveryRow{
			NotificationID:  row.ID,
			NotificationSeq: row.Seq,
			ProjectID:       row.ProjectID,
			SessionID:       row.SessionID,
			RouteName:       decision.RouteName,
			Sink:            decision.Sink,
			DestinationKey:  decision.DestinationKey,
			Status:          decision.Status,
			MaxAttempts:     maxAttempts,
			NextAttemptAt:   now,
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		if delivery.Status == "" {
			delivery.Status = DeliveryQueued
		}
		if decision.Reason != "" {
			delivery.LastErrorCode = "route_skipped"
			delivery.LastError = decision.Reason
		}
		if _, _, err := m.store.EnqueueDelivery(ctx, delivery); err != nil {
			return err
		}
	}
	return m.store.MarkNotificationRouted(ctx, row.ID, now)
}

func (m *Manager) ClaimDesktopDeliveries(ctx context.Context, owner string, limit int) ([]DeliveryRow, error) {
	settings := m.settings.Settings(ctx)
	policy := RetryPolicyFromConfig(settings.Retry)
	return m.store.ClaimDueDeliveries(ctx, SinkAOApp, owner, m.clock().UTC(), limit, policy.LeaseTTL)
}

func (m *Manager) MarkDeliverySent(ctx context.Context, id, owner, externalID string) error {
	return m.store.MarkDeliverySent(ctx, id, owner, externalID, m.clock().UTC())
}

func (m *Manager) MarkDeliveryError(ctx context.Context, id, owner, code, message string) error {
	settings := m.settings.Settings(ctx)
	policy := RetryPolicyFromConfig(settings.Retry)
	now := m.clock().UTC()
	// The store is the source of truth for attempts/max-attempt terminal
	// handling. Permanent classification short-circuits to failed; otherwise we
	// fetch the current delivery attempts and provide the attempt-aware next
	// retry timestamp for retry_wait rows.
	if ClassifyError(code) == ErrorPermanent {
		return m.store.MarkDeliveryFailed(ctx, id, owner, code, message, now)
	}
	row, ok, err := m.store.GetDelivery(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("notification delivery %s not found", id)
	}
	nextAttemptNo := row.Attempts + 1
	return m.store.MarkDeliveryRetry(ctx, id, owner, code, message, policy.NextAttemptAt(now, nextAttemptNo), now)
}
