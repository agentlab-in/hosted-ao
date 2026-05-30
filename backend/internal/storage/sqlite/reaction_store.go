package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// ReactionTrackerRow is one persisted escalation budget, the durable mirror of
// the LCM's in-memory reactionTracker. It is the unit the lifecycle Manager
// hydrates on startup and writes through on each mutation.
type ReactionTrackerRow struct {
	SessionID      string
	ReactionKey    string
	Attempts       int
	Escalated      bool
	FirstAttemptAt time.Time
	ProjectID      string
}

// ListReactionTrackers returns every persisted escalation budget so the Manager
// can rehydrate its in-memory trackers after a restart.
func (s *Store) ListReactionTrackers(ctx context.Context) ([]ReactionTrackerRow, error) {
	rows, err := s.q.ListReactionTrackers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list reaction trackers: %w", err)
	}
	out := make([]ReactionTrackerRow, 0, len(rows))
	for _, r := range rows {
		var first time.Time
		if r.FirstAttemptAt.Valid {
			first = r.FirstAttemptAt.Time
		}
		out = append(out, ReactionTrackerRow{
			SessionID:      r.SessionID,
			ReactionKey:    r.ReactionKey,
			Attempts:       int(r.Attempts),
			Escalated:      r.Escalated != 0,
			FirstAttemptAt: first,
			ProjectID:      r.ProjectID,
		})
	}
	return out, nil
}

// SaveReactionTracker durably persists one escalation budget (insert or update).
func (s *Store) SaveReactionTracker(ctx context.Context, r ReactionTrackerRow) error {
	escalated := int64(0)
	if r.Escalated {
		escalated = 1
	}
	first := sql.NullTime{}
	if !r.FirstAttemptAt.IsZero() {
		first = sql.NullTime{Time: r.FirstAttemptAt, Valid: true}
	}
	return s.q.UpsertReactionTracker(ctx, gen.UpsertReactionTrackerParams{
		SessionID:      r.SessionID,
		ReactionKey:    r.ReactionKey,
		Attempts:       int64(r.Attempts),
		Escalated:      escalated,
		FirstAttemptAt: first,
		ProjectID:      r.ProjectID,
	})
}

// DeleteReactionTracker drops one escalation budget.
func (s *Store) DeleteReactionTracker(ctx context.Context, sessionID, reactionKey string) error {
	return s.q.DeleteReactionTracker(ctx, gen.DeleteReactionTrackerParams{
		SessionID:   sessionID,
		ReactionKey: reactionKey,
	})
}

// DeleteSessionReactionTrackers drops every escalation budget for a session.
func (s *Store) DeleteSessionReactionTrackers(ctx context.Context, sessionID string) error {
	return s.q.DeleteSessionReactionTrackers(ctx, sessionID)
}
