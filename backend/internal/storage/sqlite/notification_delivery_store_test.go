package sqlite

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/cdc"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/notification"
)

func TestNotificationDeliveryEnqueueIdempotentAndCDC(t *testing.T) {
	s, ntf := newDeliveryTestNotification(t, "delivery-dedupe")
	ctx := context.Background()
	startSeq, _ := s.MaxChangeLogSeq(ctx)

	row, created, err := s.EnqueueDelivery(ctx, sampleDelivery(ntf, "desktop"))
	if err != nil {
		t.Fatal(err)
	}
	if !created || row.ID == "" || row.Status != notification.DeliveryQueued {
		t.Fatalf("created=%v row=%+v", created, row)
	}
	dup, created, err := s.EnqueueDelivery(ctx, sampleDelivery(ntf, "desktop"))
	if err != nil {
		t.Fatal(err)
	}
	if created || dup.ID != row.ID {
		t.Fatalf("duplicate should return existing row created=false: created=%v dup=%+v row=%+v", created, dup, row)
	}
	evs, err := s.ReadChangeLogAfter(ctx, startSeq, 10)
	if err != nil {
		t.Fatal(err)
	}
	var createdEvents int
	for _, ev := range evs {
		if ev.EventType == string(cdc.EventNotificationDeliveryCreated) {
			createdEvents++
		}
	}
	if createdEvents != 1 {
		t.Fatalf("delivery created CDC count = %d, want 1 events=%+v", createdEvents, evs)
	}
}

func TestNotificationDeliveryEnqueueDefaultMaxAttempts(t *testing.T) {
	s, ntf := newDeliveryTestNotification(t, "delivery-default-max")
	ctx := context.Background()
	row := sampleDelivery(ntf, "desktop")
	row.MaxAttempts = 0
	got, _, err := s.EnqueueDelivery(ctx, row)
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxAttempts != 5 {
		t.Fatalf("default max attempts = %d, want 5", got.MaxAttempts)
	}
}

func TestNotificationDeliveryClaimDueStableOrder(t *testing.T) {
	s, ntf := newDeliveryTestNotification(t, "delivery-claim")
	ctx := context.Background()
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for i, d := range []time.Duration{2 * time.Second, time.Second, 3 * time.Second} {
		row := sampleDelivery(ntf, fmt.Sprintf("desktop-%d", i))
		row.DestinationKey = fmt.Sprintf("dest-%d", i)
		row.NextAttemptAt = base.Add(d)
		row.CreatedAt = base.Add(time.Duration(i) * time.Millisecond)
		row.UpdatedAt = row.CreatedAt
		if _, _, err := s.EnqueueDelivery(ctx, row); err != nil {
			t.Fatal(err)
		}
	}

	claimed, err := s.ClaimDueDeliveries(ctx, notification.SinkAOApp, "electron", base.Add(10*time.Second), 2, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 2 {
		t.Fatalf("claimed = %d, want 2", len(claimed))
	}
	if claimed[0].DestinationKey != "dest-1" || claimed[1].DestinationKey != "dest-0" {
		t.Fatalf("claim order = %s, %s; want dest-1, dest-0", claimed[0].DestinationKey, claimed[1].DestinationKey)
	}
	if claimed[0].Status != notification.DeliveryLeased || claimed[0].LeaseOwner != "electron" || claimed[0].LeaseExpiresAt.IsZero() {
		t.Fatalf("claimed row not leased: %+v", claimed[0])
	}
}

func TestNotificationDeliveryLeaseExpiryAndMaxAttempts(t *testing.T) {
	s, ntf := newDeliveryTestNotification(t, "delivery-expiry")
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	queued, _, err := s.EnqueueDelivery(ctx, sampleDueDelivery(ntf, "desktop", now))
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimDueDeliveries(ctx, notification.SinkAOApp, "owner", now, 1, time.Second)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim len=%d err=%v", len(claimed), err)
	}
	released, err := s.ReleaseExpiredDeliveryLeases(ctx, now.Add(2*time.Second))
	if err != nil || released != 1 {
		t.Fatalf("release = %d err=%v", released, err)
	}
	got, ok, _ := s.GetDelivery(ctx, queued.ID)
	if !ok || got.Status != notification.DeliveryQueued || got.Attempts != 1 || got.LeaseOwner != "" {
		t.Fatalf("expired lease should return queued with attempts=1: ok=%v row=%+v", ok, got)
	}

	maxOne := sampleDueDelivery(ntf, "desktop-max", now)
	maxOne.DestinationKey = "max"
	maxOne.MaxAttempts = 1
	maxOne, _, err = s.EnqueueDelivery(ctx, maxOne)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimDueDeliveries(ctx, notification.SinkAOApp, "owner", now, 1, time.Second); err != nil {
		t.Fatal(err)
	}
	released, err = s.ReleaseExpiredDeliveryLeases(ctx, now.Add(2*time.Second))
	if err != nil || released != 1 {
		t.Fatalf("release max = %d err=%v", released, err)
	}
	got, ok, _ = s.GetDelivery(ctx, maxOne.ID)
	if !ok || got.Status != notification.DeliveryFailed || got.Attempts != 1 {
		t.Fatalf("max attempts expired lease should fail: ok=%v row=%+v", ok, got)
	}
}

func TestNotificationDeliveryMarkSentRetryFailedAndSkipped(t *testing.T) {
	s, ntf := newDeliveryTestNotification(t, "delivery-mark")
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	sent, _, _ := s.EnqueueDelivery(ctx, sampleDueDelivery(ntf, "desktop-sent", now))
	claimed, _ := s.ClaimDueDeliveries(ctx, notification.SinkAOApp, "owner", now, 1, time.Minute)
	if len(claimed) != 1 {
		t.Fatalf("claim sent row len=%d", len(claimed))
	}
	if err := s.MarkDeliverySent(ctx, sent.ID, "owner", "native-1", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.GetDelivery(ctx, sent.ID)
	if got.Status != notification.DeliverySent || got.ExternalID != "native-1" || got.Attempts != 1 || got.DeliveredAt.IsZero() {
		t.Fatalf("sent row = %+v", got)
	}

	retry := sampleDueDelivery(ntf, "desktop-retry", now)
	retry.DestinationKey = "retry"
	retry, _, _ = s.EnqueueDelivery(ctx, retry)
	claimed, _ = s.ClaimDueDeliveries(ctx, notification.SinkAOApp, "owner", now, 1, time.Minute)
	if len(claimed) != 1 {
		t.Fatalf("claim retry row len=%d", len(claimed))
	}
	next := now.Add(30 * time.Second)
	if err := s.MarkDeliveryRetry(ctx, retry.ID, "owner", "timeout", "timed out", next, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.GetDelivery(ctx, retry.ID)
	if got.Status != notification.DeliveryRetryWait || got.Attempts != 1 || !got.NextAttemptAt.Equal(next) {
		t.Fatalf("retry row = %+v", got)
	}

	fail := sampleDueDelivery(ntf, "desktop-fail", now)
	fail.DestinationKey = "fail"
	fail.MaxAttempts = 1
	fail, _, _ = s.EnqueueDelivery(ctx, fail)
	claimed, _ = s.ClaimDueDeliveries(ctx, notification.SinkAOApp, "owner", now, 1, time.Minute)
	if len(claimed) != 1 {
		t.Fatalf("claim fail row len=%d", len(claimed))
	}
	if err := s.MarkDeliveryRetry(ctx, fail.ID, "owner", "timeout", "timed out", next, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.GetDelivery(ctx, fail.ID)
	if got.Status != notification.DeliveryFailed || got.Attempts != 1 {
		t.Fatalf("retry at max should fail: %+v", got)
	}

	skipped := sampleDueDelivery(ntf, "desktop-skip", now)
	skipped.DestinationKey = "skip"
	skipped.Status = notification.DeliverySkipped
	skipped, _, _ = s.EnqueueDelivery(ctx, skipped)
	claimed, err := s.ClaimDueDeliveries(ctx, notification.SinkAOApp, "owner", now, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range claimed {
		if row.ID == skipped.ID {
			t.Fatalf("skipped row should not be claimable: %+v", claimed)
		}
	}
	if err := s.MarkDeliveryRetry(ctx, skipped.ID, "owner", "timeout", "timed out", next, now.Add(time.Second)); !errors.Is(err, notification.ErrDeliveryUpdateConflict) {
		t.Fatalf("retry skipped row err = %v, want update conflict", err)
	}
	got, _, _ = s.GetDelivery(ctx, skipped.ID)
	if got.Status != notification.DeliverySkipped || got.Attempts != 0 {
		t.Fatalf("skipped row should be terminal: %+v", got)
	}
}

func TestNotificationDeliveryCompletionFencedByLeaseOwner(t *testing.T) {
	s, ntf := newDeliveryTestNotification(t, "delivery-owner-fence")
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	row, _, err := s.EnqueueDelivery(ctx, sampleDueDelivery(ntf, "desktop", now))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimDueDeliveries(ctx, notification.SinkAOApp, "owner-1", now, 1, time.Second); err != nil {
		t.Fatal(err)
	}
	if released, err := s.ReleaseExpiredDeliveryLeases(ctx, now.Add(2*time.Second)); err != nil || released != 1 {
		t.Fatalf("release = %d err=%v", released, err)
	}
	if _, err := s.ClaimDueDeliveries(ctx, notification.SinkAOApp, "owner-2", now.Add(2*time.Second), 1, time.Second); err != nil {
		t.Fatal(err)
	}

	if err := s.MarkDeliverySent(ctx, row.ID, "owner-1", "stale", now.Add(2500*time.Millisecond)); !errors.Is(err, notification.ErrDeliveryUpdateConflict) {
		t.Fatalf("stale owner MarkDeliverySent err = %v, want update conflict", err)
	}
	got, _, _ := s.GetDelivery(ctx, row.ID)
	if got.Status != notification.DeliveryLeased || got.LeaseOwner != "owner-2" || got.ExternalID != "" {
		t.Fatalf("stale owner should not change active lease: %+v", got)
	}
	if err := s.MarkDeliverySent(ctx, row.ID, "owner-2", "native-2", now.Add(2500*time.Millisecond)); err != nil {
		t.Fatalf("current owner sent: %v", err)
	}
	got, _, _ = s.GetDelivery(ctx, row.ID)
	if got.Status != notification.DeliverySent || got.ExternalID != "native-2" {
		t.Fatalf("current owner should complete delivery: %+v", got)
	}
}

func TestNotificationDeliveryUpdateCDC(t *testing.T) {
	s, ntf := newDeliveryTestNotification(t, "delivery-cdc-update")
	ctx := context.Background()
	row, _, err := s.EnqueueDelivery(ctx, sampleDelivery(ntf, "desktop"))
	if err != nil {
		t.Fatal(err)
	}
	startSeq, _ := s.MaxChangeLogSeq(ctx)
	if _, err := s.ClaimDueDeliveries(ctx, notification.SinkAOApp, "owner", time.Now().UTC(), 1, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkDeliveryFailed(ctx, row.ID, "owner", "permanent", "bad route", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	evs, err := s.ReadChangeLogAfter(ctx, startSeq, 10)
	if err != nil {
		t.Fatal(err)
	}
	var updates int
	for _, ev := range evs {
		if ev.EventType == string(cdc.EventNotificationDeliveryUpdated) {
			updates++
		}
	}
	if updates < 2 {
		t.Fatalf("expected claim + failed update CDC events, got %d in %+v", updates, evs)
	}
}

func newDeliveryTestNotification(t *testing.T, dedupe string) (*Store, domain.Notification) {
	t.Helper()
	s, rec := newNotificationTestSession(t)
	row, _, err := s.EnqueueNotification(context.Background(), sampleNotification(rec, dedupe))
	if err != nil {
		t.Fatalf("enqueue notification: %v", err)
	}
	return s, row
}

func sampleDelivery(ntf domain.Notification, route string) notification.DeliveryRow {
	now := time.Now().UTC().Truncate(time.Second)
	return notification.DeliveryRow{
		NotificationID:  ntf.ID,
		NotificationSeq: ntf.Seq,
		ProjectID:       ntf.ProjectID,
		SessionID:       ntf.SessionID,
		RouteName:       route,
		Sink:            notification.SinkAOApp,
		Status:          notification.DeliveryQueued,
		MaxAttempts:     5,
		NextAttemptAt:   now,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

func sampleDueDelivery(ntf domain.Notification, route string, due time.Time) notification.DeliveryRow {
	row := sampleDelivery(ntf, route)
	row.NextAttemptAt = due
	row.CreatedAt = due
	row.UpdatedAt = due
	return row
}
