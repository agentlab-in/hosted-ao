package lifecycle

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// reactionStoreAdapter bridges the concrete *sqlite.Store to the lifecycle
// package's ReactionStore interface (string/row types <-> domain types). This is
// the same glue the composition root installs.
type reactionStoreAdapter struct{ s *sqlite.Store }

func (a reactionStoreAdapter) LoadReactionTrackers(ctx context.Context) ([]PersistedTracker, error) {
	rows, err := a.s.ListReactionTrackers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]PersistedTracker, len(rows))
	for i, r := range rows {
		out[i] = PersistedTracker{
			SessionID:      domain.SessionID(r.SessionID),
			Key:            r.ReactionKey,
			Attempts:       r.Attempts,
			Escalated:      r.Escalated,
			FirstAttemptAt: r.FirstAttemptAt,
			ProjectID:      domain.ProjectID(r.ProjectID),
		}
	}
	return out, nil
}

func (a reactionStoreAdapter) SaveReactionTracker(ctx context.Context, t PersistedTracker) error {
	return a.s.SaveReactionTracker(ctx, sqlite.ReactionTrackerRow{
		SessionID:      string(t.SessionID),
		ReactionKey:    t.Key,
		Attempts:       t.Attempts,
		Escalated:      t.Escalated,
		FirstAttemptAt: t.FirstAttemptAt,
		ProjectID:      string(t.ProjectID),
	})
}

func (a reactionStoreAdapter) DeleteReactionTracker(ctx context.Context, id domain.SessionID, key string) error {
	return a.s.DeleteReactionTracker(ctx, string(id), key)
}

func (a reactionStoreAdapter) DeleteSessionReactionTrackers(ctx context.Context, id domain.SessionID) error {
	return a.s.DeleteSessionReactionTrackers(ctx, string(id))
}

// TestReaction_DurabilitySurvivesRestart is the plan's reaction_trackers
// durability check: once a reaction has escalated, a daemon restart (a fresh
// Manager hydrated from the same store) must NOT re-fire the human page — the
// exact failure the in-memory-only version had.
func TestReaction_DurabilitySurvivesRestart(t *testing.T) {
	db, err := sqlite.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := sqlite.NewStore(db)
	adapter := reactionStoreAdapter{store}

	// --- first process lifetime: drive ci-failed to escalation ---
	notf1 := &recordingNotifier{}
	m1 := New(store, notf1, &recordingMessenger{})
	m1.clock = func() time.Time { return t0 }
	if err := m1.WithReactionStore(context.Background(), adapter); err != nil {
		t.Fatalf("hydrate m1: %v", err)
	}
	seedViaUpsert(t, store, lcOpenPR(domain.PRReasonReviewPending))

	// ci-failed: retries 2, persistent → escalate on the third failure.
	for i := 0; i < 4; i++ {
		failCI(t, m1)
		pendingCI(t, m1)
	}
	if c := notifyCount(notf1, "reaction.escalated"); c != 1 {
		t.Fatalf("precondition: want one escalation in first lifetime, got %d", c)
	}

	// --- simulated restart: a fresh Manager hydrated from the same store ---
	notf2 := &recordingNotifier{}
	msgr2 := &recordingMessenger{}
	m2 := New(store, notf2, msgr2)
	m2.clock = func() time.Time { return t0 }
	if err := m2.WithReactionStore(context.Background(), adapter); err != nil {
		t.Fatalf("hydrate m2: %v", err)
	}

	// The ci-failed tracker rehydrates with escalated=true, so further failures
	// are silenced: no new send-to-agent, no re-escalation.
	failCI(t, m2)
	if c := notifyCount(notf2, "reaction.escalated"); c != 0 {
		t.Errorf("restart re-fired an already-escalated page: got %d escalations", c)
	}
	if len(msgr2.sent) != 0 {
		t.Errorf("restart re-sent to agent despite escalated budget: got %d sends", len(msgr2.sent))
	}
}

// TestReaction_DurabilityClearsOnIncidentOver proves the durable rows are
// removed when an incident resolves, so a later unrelated incident starts from a
// fresh budget rather than a stale escalated=true.
func TestReaction_DurabilityClearsOnIncidentOver(t *testing.T) {
	db, err := sqlite.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := sqlite.NewStore(db)
	adapter := reactionStoreAdapter{store}

	m := New(store, &recordingNotifier{}, &recordingMessenger{})
	m.clock = func() time.Time { return t0 }
	if err := m.WithReactionStore(context.Background(), adapter); err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	seedViaUpsert(t, store, lcOpenPR(domain.PRReasonReviewPending))

	failCI(t, m)
	if rows, _ := store.ListReactionTrackers(context.Background()); len(rows) == 0 {
		t.Fatalf("precondition: expected a persisted ci-failed tracker")
	}

	// Approved+green ends the incident → recovered() clears every tracker.
	if err := m.ApplySCMObservation(ctx(), sid, ports.SCMFacts{
		Fetched: true, PRState: domain.PROpen, ReviewDecision: ports.ReviewApproved, CISummary: ports.CIPassing, PRNumber: 7,
	}); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if rows, _ := store.ListReactionTrackers(context.Background()); len(rows) != 0 {
		t.Errorf("incident-over must clear durable trackers, got %d rows", len(rows))
	}
}
