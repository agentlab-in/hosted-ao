package notification

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type fakeRuntimeStore struct {
	mu          sync.Mutex
	unrouted    []domain.Notification
	deliveries  []DeliveryRow
	routed      []domain.NotificationID
	releases    int
	failEnqueue map[domain.NotificationID]error
}

func (f *fakeRuntimeStore) ListUnroutedNotifications(context.Context, int) ([]domain.Notification, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.Notification(nil), f.unrouted...), nil
}

func (f *fakeRuntimeStore) MarkNotificationRouted(_ context.Context, id domain.NotificationID, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routed = append(f.routed, id)
	return nil
}

func (f *fakeRuntimeStore) EnqueueDelivery(_ context.Context, row DeliveryRow) (DeliveryRow, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failEnqueue[row.NotificationID]; err != nil {
		return DeliveryRow{}, false, err
	}
	f.deliveries = append(f.deliveries, row)
	return row, true, nil
}

func (f *fakeRuntimeStore) ClaimDueDeliveries(context.Context, string, string, time.Time, int, time.Duration) ([]DeliveryRow, error) {
	return nil, nil
}
func (f *fakeRuntimeStore) ReleaseExpiredDeliveryLeases(context.Context, time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases++
	return 0, nil
}
func (f *fakeRuntimeStore) MarkDeliverySent(context.Context, string, string, time.Time) error {
	return nil
}
func (f *fakeRuntimeStore) MarkDeliveryRetry(context.Context, string, string, string, time.Time) error {
	return nil
}
func (f *fakeRuntimeStore) MarkDeliveryFailed(context.Context, string, string, string, time.Time) error {
	return nil
}
func (f *fakeRuntimeStore) MarkDeliverySkipped(context.Context, string, string, time.Time) error {
	return nil
}

func TestDispatcherStartReleasesAndStops(t *testing.T) {
	store := &fakeRuntimeStore{}
	mgr := NewManager(store, StaticSettings(config.DefaultNotificationConfig()), discardLogger())
	mgr.interval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := mgr.Start(ctx)

	deadline := time.After(time.Second)
	for {
		store.mu.Lock()
		released := store.releases
		store.mu.Unlock()
		if released > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("dispatcher did not run initial release")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("dispatcher did not stop after context cancel")
	}
}

func TestRoutePendingDeliveryFailureDoesNotBlockOtherNotifications(t *testing.T) {
	n1 := sampleDomainNotification("ntf_1", "urgent")
	n2 := sampleDomainNotification("ntf_2", "urgent")
	store := &fakeRuntimeStore{
		unrouted:    []domain.Notification{n1, n2},
		failEnqueue: map[domain.NotificationID]error{n1.ID: errors.New("boom")},
	}
	mgr := NewManager(store, StaticSettings(config.DefaultNotificationConfig()), discardLogger())

	routed, err := mgr.RoutePending(context.Background(), 10)
	if err == nil {
		t.Fatal("RoutePending should return the first routing error")
	}
	if routed != 1 {
		t.Fatalf("routed = %d, want one successful notification", routed)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.routed) != 1 || store.routed[0] != n2.ID {
		t.Fatalf("routed IDs = %v, want only %s", store.routed, n2.ID)
	}
}

func sampleDomainNotification(id domain.NotificationID, priority string) domain.Notification {
	return domain.Notification{
		Seq:       1,
		ID:        id,
		ProjectID: "ao",
		SessionID: "ao-1",
		Priority:  priority,
		Message:   "hello",
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
