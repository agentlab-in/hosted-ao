package notification

import (
	"context"
	"log/slog"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Store is the durable write-side used by the enqueuer. *sqlite.Store satisfies
// this interface.
type Store interface {
	EnqueueNotification(ctx context.Context, row domain.Notification) (domain.Notification, bool, error)
}

// Enqueuer is a store-backed ports.Notifier. It does not deliver to external
// sinks; it renders and persists the notification for later dashboard/app sinks.
type Enqueuer struct {
	store    Store
	renderer *Renderer
	logger   *slog.Logger
}

var _ ports.Notifier = (*Enqueuer)(nil)

func NewEnqueuer(store Store, renderer *Renderer, logger *slog.Logger) *Enqueuer {
	if logger == nil {
		logger = slog.Default()
	}
	return &Enqueuer{store: store, renderer: renderer, logger: logger}
}

func (e *Enqueuer) Notify(ctx context.Context, event ports.Event) error {
	row, err := e.renderer.Render(ctx, event)
	if err != nil {
		return err
	}
	saved, created, err := e.store.EnqueueNotification(ctx, row)
	if err != nil {
		return err
	}
	e.logger.DebugContext(ctx, "notification enqueued",
		"id", saved.ID,
		"session", saved.SessionID,
		"semantic_type", saved.SemanticType,
		"created", created,
	)
	return nil
}
