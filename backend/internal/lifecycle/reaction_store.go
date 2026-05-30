package lifecycle

// reaction_store.go is the optional durability seam for the escalation engine.
// By default the Manager keeps escalation budgets in memory only (a restart
// resets them, which costs at most a few extra agent retries — never a missed
// human page). When a ReactionStore is wired via WithReactionStore the in-memory
// map becomes a write-through cache over durable rows, so a restart does NOT
// re-fire an already-escalated human notification.
//
// The interface uses lifecycle-local types so the package stays free of any
// storage dependency; the composition root adapts the concrete store to it
// (mirroring the cdc.OutboxStore adapter).

import (
	"context"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// PersistedTracker is the durable form of one (session,reaction) escalation
// budget — the storage-facing mirror of the in-memory reactionTracker.
type PersistedTracker struct {
	SessionID      domain.SessionID
	Key            string
	Attempts       int
	Escalated      bool
	FirstAttemptAt time.Time
	ProjectID      domain.ProjectID
}

// ReactionStore persists escalation budgets so they survive a daemon restart.
type ReactionStore interface {
	LoadReactionTrackers(ctx context.Context) ([]PersistedTracker, error)
	SaveReactionTracker(ctx context.Context, t PersistedTracker) error
	DeleteReactionTracker(ctx context.Context, id domain.SessionID, key string) error
	DeleteSessionReactionTrackers(ctx context.Context, id domain.SessionID) error
}

// WithReactionStore makes escalation budgets durable: it hydrates the in-memory
// trackers from rs and turns on write-through for subsequent mutations. Like
// WithSessionLister it must be called BEFORE any reaper or Apply* dispatch
// starts, since it populates the tracker map without holding trackerMu against
// concurrent reactors. A hydration error is returned so the caller can decide
// whether to proceed with an empty (in-memory) budget set.
func (m *Manager) WithReactionStore(ctx context.Context, rs ReactionStore) error {
	m.reactionStore = rs
	rows, err := rs.LoadReactionTrackers(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		m.trackers[trackerKey{id: r.SessionID, key: reactionKey(r.Key)}] = &reactionTracker{
			attempts:       r.Attempts,
			escalated:      r.Escalated,
			firstAttemptAt: r.FirstAttemptAt,
			projectID:      r.ProjectID,
		}
	}
	return nil
}

// persistTracker write-throughs one tracker's current state. Best-effort: a
// failed write degrades durability to the in-memory default (a restart may
// re-fire one page), so it must not break the synchronous dispatch path. The
// snapshot is taken by the caller under trackerMu and passed by value here so no
// DB I/O happens while the lock is held.
func (m *Manager) persistTracker(ctx context.Context, id domain.SessionID, key reactionKey, snap reactionTracker) {
	if m.reactionStore == nil {
		return
	}
	_ = m.reactionStore.SaveReactionTracker(ctx, PersistedTracker{
		SessionID:      id,
		Key:            string(key),
		Attempts:       snap.attempts,
		Escalated:      snap.escalated,
		FirstAttemptAt: snap.firstAttemptAt,
		ProjectID:      snap.projectID,
	})
}

func (m *Manager) deletePersistedTracker(ctx context.Context, id domain.SessionID, key reactionKey) {
	if m.reactionStore == nil {
		return
	}
	_ = m.reactionStore.DeleteReactionTracker(ctx, id, string(key))
}

func (m *Manager) deletePersistedSessionTrackers(ctx context.Context, id domain.SessionID) {
	if m.reactionStore == nil {
		return
	}
	_ = m.reactionStore.DeleteSessionReactionTrackers(ctx, id)
}
