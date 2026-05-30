package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// These tests cover the composition-root adapters in cdc_wiring.go directly
// (package main otherwise has no test coverage): the outboxAdapter mapping the
// store's OutboxEvent to cdc.PendingEvent, and the snapshotSource rebuilding
// full-state events from the sessions table.

func newWiringStore(t *testing.T) *sqlite.Store {
	t.Helper()
	db, err := sqlite.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return sqlite.NewStore(db)
}

func wiringRec(id string) domain.SessionRecord {
	now := time.Now().UTC()
	return domain.SessionRecord{
		ID: domain.SessionID(id), ProjectID: "proj", Kind: domain.KindWorker, CreatedAt: now, UpdatedAt: now,
		Lifecycle: domain.CanonicalSessionLifecycle{
			Session:  domain.SessionSubstate{State: domain.SessionWorking, Reason: domain.ReasonTaskInProgress},
			PR:       domain.PRSubstate{State: domain.PRNone, Reason: domain.PRReasonNotCreated},
			Runtime:  domain.RuntimeSubstate{State: domain.RuntimeAlive, Reason: domain.RuntimeReasonProcessRunning},
			Activity: domain.ActivitySubstate{State: domain.ActivityActive, LastActivityAt: now, Source: domain.SourceNative},
		},
	}
}

func TestOutboxAdapterMapsPendingEvents(t *testing.T) {
	ctx := context.Background()
	store := newWiringStore(t)
	a := outboxAdapter{store}

	if err := store.Upsert(ctx, wiringRec("s1"), ports.EventSessionCreated); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	pending, err := a.ListUnsent(ctx, 10)
	if err != nil {
		t.Fatalf("list unsent: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("want 1 pending event, got %d", len(pending))
	}
	pe := pending[0]
	if pe.Seq != 1 || pe.SessionID != "s1" || pe.EventType != string(ports.EventSessionCreated) || pe.Revision != 1 {
		t.Fatalf("unexpected mapping: %+v", pe)
	}
	if pe.Payload == "" {
		t.Fatal("payload should carry the marshaled record")
	}

	// MarkSent must clear it from the unsent set.
	if err := a.MarkSent(ctx, pe.OutboxID, time.Now().UTC()); err != nil {
		t.Fatalf("mark sent: %v", err)
	}
	again, err := a.ListUnsent(ctx, 10)
	if err != nil {
		t.Fatalf("list unsent 2: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("sent event should not reappear, got %d", len(again))
	}
}

func TestSnapshotSourceRebuildsState(t *testing.T) {
	ctx := context.Background()
	store := newWiringStore(t)
	s := snapshotSource{store}

	// Empty store: no events, maxSeq 0.
	events, maxSeq, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatalf("empty snapshot: %v", err)
	}
	if len(events) != 0 || maxSeq != 0 {
		t.Fatalf("empty store should yield no events and maxSeq 0, got %d events maxSeq %d", len(events), maxSeq)
	}

	// Two canonical writes (seq 1,2) across two sessions.
	if err := store.Upsert(ctx, wiringRec("s1"), ports.EventSessionCreated); err != nil {
		t.Fatalf("upsert s1: %v", err)
	}
	if err := store.Upsert(ctx, wiringRec("s2"), ports.EventSessionCreated); err != nil {
		t.Fatalf("upsert s2: %v", err)
	}

	events, maxSeq, err = s.Snapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if maxSeq != 2 {
		t.Fatalf("maxSeq = %d, want 2 (change_log high-water)", maxSeq)
	}
	if len(events) != 2 {
		t.Fatalf("want one event per session (2), got %d", len(events))
	}
	for _, e := range events {
		if e.Seq != maxSeq {
			t.Errorf("snapshot event seq = %d, want resume watermark %d", e.Seq, maxSeq)
		}
		if e.EventType != "session_snapshot" {
			t.Errorf("event type = %q, want session_snapshot", e.EventType)
		}
		// Payload must be a parseable full record at the persisted revision with
		// metadata excluded and the schema version stamped.
		var rec domain.SessionRecord
		if err := json.Unmarshal([]byte(e.Payload), &rec); err != nil {
			t.Fatalf("payload not a SessionRecord: %v", err)
		}
		if rec.Lifecycle.Version != domain.LifecycleVersion {
			t.Errorf("payload version = %d, want %d", rec.Lifecycle.Version, domain.LifecycleVersion)
		}
		if rec.Lifecycle.Revision != 1 {
			t.Errorf("payload revision = %d, want 1", rec.Lifecycle.Revision)
		}
		if rec.Metadata != nil {
			t.Errorf("snapshot payload must exclude metadata, got %v", rec.Metadata)
		}
	}
}
