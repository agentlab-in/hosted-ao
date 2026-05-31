package integration

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/notification"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

func TestNotificationRuntimeRoutesDesktopEligiblePriorities(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := sqlite.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedProject(t, store, "mer")
	rec, err := store.CreateSession(ctx, durableRecord("mer", "MER-55", "feat/notifier"))
	if err != nil {
		t.Fatal(err)
	}

	urgent := enqueueRuntimeNotification(t, store, rec, "urgent", "urgent")
	action := enqueueRuntimeNotification(t, store, rec, "action", "action")
	info := enqueueRuntimeNotification(t, store, rec, "info", "info")

	mgr := notification.NewManager(store, notification.StaticSettings(config.DefaultNotificationConfig()), slog.New(slog.NewTextHandler(io.Discard, nil)))
	routed, err := mgr.RoutePending(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if routed != 3 {
		t.Fatalf("routed = %d, want 3", routed)
	}

	for _, ntf := range []domain.Notification{urgent, action} {
		rows, err := store.ListDeliveries(ctx, sqlite.DeliveryFilter{NotificationID: string(ntf.ID), Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].Sink != notification.SinkAOApp || rows[0].RouteName != notification.RouteDesktop {
			t.Fatalf("%s should have one AO-app desktop delivery, got %+v", ntf.Priority, rows)
		}
	}
	rows, err := store.ListDeliveries(ctx, sqlite.DeliveryFilter{NotificationID: string(info.ID), Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("info should remain dashboard/read-model only, got deliveries %+v", rows)
	}
}

func enqueueRuntimeNotification(t *testing.T, store *sqlite.Store, rec domain.SessionRecord, priority, dedupe string) domain.Notification {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	row, _, err := store.EnqueueNotification(context.Background(), domain.Notification{
		ProjectID:    rec.ProjectID,
		SessionID:    rec.ID,
		Source:       "lifecycle",
		EventType:    "reaction.test",
		SemanticType: "test." + priority,
		Priority:     priority,
		Message:      "test " + priority,
		Payload:      json.RawMessage(`{}`),
		DedupeKey:    "runtime-" + dedupe,
		CreatedAt:    now,
		UpdatedAt:    now,
	})
	if err != nil {
		t.Fatalf("enqueue notification: %v", err)
	}
	return row
}
