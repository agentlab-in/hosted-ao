package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// PREnrichmentRow is the SCM observer's cache of the rich PR facts that do not
// live in the canonical lifecycle (which keeps only pr_state/reason/number/url).
// It is 1:1 with a session and written OFF the canonical CDC path: upserting it
// never bumps revision and never emits a change_log/outbox event. pending_comments
// and ci_log_tail are opaque blobs the SCM observer serializes.
type PREnrichmentRow struct {
	SessionID       string
	CISummary       string
	ReviewDecision  string
	Mergeability    string
	PendingComments string
	CILogTail       string
	LastFetchedAt   time.Time
}

// UpsertPREnrichment inserts or replaces the cached PR facts for one session.
func (s *Store) UpsertPREnrichment(ctx context.Context, r PREnrichmentRow) error {
	return s.q.UpsertPREnrichment(ctx, gen.UpsertPREnrichmentParams{
		SessionID:       r.SessionID,
		CiSummary:       r.CISummary,
		ReviewDecision:  r.ReviewDecision,
		Mergeability:    r.Mergeability,
		PendingComments: r.PendingComments,
		CiLogTail:       r.CILogTail,
		LastFetchedAt:   r.LastFetchedAt,
	})
}

// GetPREnrichment returns the cached PR facts for one session. ok is false when
// no row exists (the SCM observer has not yet fetched, or the session has no PR).
func (s *Store) GetPREnrichment(ctx context.Context, sessionID string) (PREnrichmentRow, bool, error) {
	e, err := s.q.GetPREnrichment(ctx, sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return PREnrichmentRow{}, false, nil
	}
	if err != nil {
		return PREnrichmentRow{}, false, fmt.Errorf("get pr enrichment: %w", err)
	}
	return PREnrichmentRow{
		SessionID:       e.SessionID,
		CISummary:       e.CiSummary,
		ReviewDecision:  e.ReviewDecision,
		Mergeability:    e.Mergeability,
		PendingComments: e.PendingComments,
		CILogTail:       e.CiLogTail,
		LastFetchedAt:   e.LastFetchedAt,
	}, true, nil
}

// DeletePREnrichment drops the cached PR facts for one session. Normally
// unnecessary (the FK cascades on session delete), exposed for explicit eviction.
func (s *Store) DeletePREnrichment(ctx context.Context, sessionID string) error {
	return s.q.DeletePREnrichment(ctx, sessionID)
}
