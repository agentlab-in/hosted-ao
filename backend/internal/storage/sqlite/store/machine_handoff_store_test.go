package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestMachineHandoffCDCContainsOnlySessionInvalidation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "handoff")
	session, err := s.CreateSession(ctx, sampleRecord("handoff"))
	if err != nil {
		t.Fatal(err)
	}
	seq, err := s.LatestSeq(ctx)
	if err != nil {
		t.Fatal(err)
	}
	h := sourceHandoff()
	h.SessionID = session.ID
	if _, _, err := s.CreateMachineHandoff(ctx, h); err != nil {
		t.Fatal(err)
	}
	h.State, h.Revision, h.CheckpointHash = domain.HandoffCheckpointReady, 1, strings.Repeat("b", 64)
	h.UpdatedAt = h.UpdatedAt.Add(time.Second)
	if ok, err := s.AdvanceMachineHandoff(ctx, h, domain.HandoffFreezing, 0); err != nil || !ok {
		t.Fatal(ok, err)
	}
	events, err := s.EventsAfter(ctx, seq, 10)
	if err != nil || len(events) != 2 {
		t.Fatalf("CDC events: %d %v", len(events), err)
	}
	for _, event := range events {
		var payload map[string]string
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload) != 1 || payload["id"] != string(session.ID) || string(event.Type) != "session_updated" {
			t.Fatalf("unexpected handoff CDC: %s", event.Payload)
		}
	}
}

func sourceHandoff() domain.MachineHandoff {
	now := time.Now().UTC()
	secret := strings.Repeat("a", 64)
	return domain.MachineHandoff{ID: "transfer", SessionID: "session", SourceMachine: "A", DestinationMachine: "B",
		Role: domain.HandoffSource, State: domain.HandoffFreezing, ActivationSecret: secret,
		ActivationHash: domain.HandoffTokenHash(secret), CreatedAt: now, UpdatedAt: now}
}

func TestMachineHandoffRejectsStaleAndReboundWrites(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	h := sourceHandoff()
	if _, created, err := s.CreateMachineHandoff(ctx, h); err != nil || !created {
		t.Fatal(created, err)
	}
	h.State, h.Revision, h.CheckpointHash = domain.HandoffCheckpointReady, 1, strings.Repeat("b", 64)
	h.UpdatedAt = h.UpdatedAt.Add(time.Second)
	if changed, err := s.AdvanceMachineHandoff(ctx, h, domain.HandoffFreezing, 0); err != nil || !changed {
		t.Fatal(changed, err)
	}
	if changed, err := s.AdvanceMachineHandoff(ctx, h, domain.HandoffFreezing, 0); err != nil || changed {
		t.Fatal("stale CAS mutated ownership", changed, err)
	}
	for _, mutate := range []func(*domain.MachineHandoff){
		func(h *domain.MachineHandoff) { h.DestinationMachine = "C" },
		func(h *domain.MachineHandoff) { h.SessionID = "other" },
		func(h *domain.MachineHandoff) { h.CheckpointHash = strings.Repeat("c", 64) },
		func(h *domain.MachineHandoff) {
			h.ActivationSecret = strings.Repeat("d", 64)
			h.ActivationHash = domain.HandoffTokenHash(h.ActivationSecret)
		},
	} {
		next := h
		next.State, next.Revision, next.UpdatedAt = domain.HandoffRelinquished, 2, h.UpdatedAt.Add(time.Second)
		mutate(&next)
		if changed, err := s.AdvanceMachineHandoff(ctx, next, h.State, h.Revision); changed || !errors.Is(err, domain.ErrMachineHandoffConflict) {
			t.Fatal("rebound ownership", changed, err)
		}
	}
}

func TestMachineHandoffTombstoneSurvivesDatabaseReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := sqlitetest.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	h := sourceHandoff()
	if _, _, err := s.CreateMachineHandoff(ctx, h); err != nil {
		_ = s.Close()
		t.Fatal(err)
	}
	for _, next := range []domain.HandoffState{domain.HandoffCheckpointReady, domain.HandoffRelinquished} {
		previous, revision := h.State, h.Revision
		h.State, h.Revision, h.CheckpointHash = next, revision+1, strings.Repeat("b", 64)
		h.UpdatedAt = h.UpdatedAt.Add(time.Second)
		if changed, err := s.AdvanceMachineHandoff(ctx, h, previous, revision); err != nil || !changed {
			_ = s.Close()
			t.Fatal(changed, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	read, ok, err := s.GetMachineHandoff(ctx, h.ID, h.Role)
	if err != nil || !ok || read.State != domain.HandoffRelinquished || read.ActivationSecret != h.ActivationSecret {
		t.Fatalf("lost tombstone/grant: %+v %v %v", read.State, ok, err)
	}
	if fenced, err := s.MachineHandoffFencesSession(ctx, h.SessionID); err != nil || !fenced {
		t.Fatal("restart reopened source", err)
	}
	other := sourceHandoff()
	other.ID = "second-transfer"
	if _, _, err := s.CreateMachineHandoff(ctx, other); !errors.Is(err, domain.ErrMachineHandoffConflict) {
		t.Fatal("restart allowed overlapping source", err)
	}
}
