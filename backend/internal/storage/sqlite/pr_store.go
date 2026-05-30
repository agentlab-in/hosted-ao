package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// PRRow is the SCM observer's cache of the scalar PR facts that do not live in
// the canonical lifecycle (which keeps only pr_state/reason/number/url). It is
// 1:1 with a session and written OFF the canonical CDC path: upserting it never
// bumps revision and never emits a change_log/outbox event. The list facts
// (checks, comments) are separate rows — see PRCheck / PRComment.
type PRRow struct {
	SessionID      string
	ReviewDecision string // none | approved | changes_requested | review_required
	Mergeability   string // unknown | mergeable | conflicting | blocked | unstable
	CIState        string // unknown | pending | passing | failing
	CIPassed       int64
	CIFailed       int64
	CIPending      int64
	CILogTail      string
	LastFetchedAt  time.Time
}

// PRCheck is one CI check belonging to a session's PR.
type PRCheck struct {
	Name   string
	Status string // unknown | queued | in_progress | passed | failed | skipped | cancelled
	URL    string
}

// PRComment is one review comment belonging to a session's PR.
type PRComment struct {
	CommentID string
	Author    string
	File      string
	Line      int64
	Body      string
	Resolved  bool
	CreatedAt time.Time
}

// UpsertPR inserts or replaces the scalar PR facts for one session.
func (s *Store) UpsertPR(ctx context.Context, r PRRow) error {
	return s.q.UpsertPR(ctx, gen.UpsertPRParams{
		SessionID:      r.SessionID,
		ReviewDecision: r.ReviewDecision,
		Mergeability:   r.Mergeability,
		CiState:        r.CIState,
		CiPassed:       r.CIPassed,
		CiFailed:       r.CIFailed,
		CiPending:      r.CIPending,
		CiLogTail:      r.CILogTail,
		LastFetchedAt:  r.LastFetchedAt,
	})
}

// GetPR returns the scalar PR facts for one session. ok is false when no row
// exists (the SCM observer has not fetched yet, or the session has no PR).
func (s *Store) GetPR(ctx context.Context, sessionID string) (PRRow, bool, error) {
	p, err := s.q.GetPR(ctx, sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return PRRow{}, false, nil
	}
	if err != nil {
		return PRRow{}, false, fmt.Errorf("get pr: %w", err)
	}
	return PRRow{
		SessionID:      p.SessionID,
		ReviewDecision: p.ReviewDecision,
		Mergeability:   p.Mergeability,
		CIState:        p.CiState,
		CIPassed:       p.CiPassed,
		CIFailed:       p.CiFailed,
		CIPending:      p.CiPending,
		CILogTail:      p.CiLogTail,
		LastFetchedAt:  p.LastFetchedAt,
	}, true, nil
}

// DeletePR drops the scalar PR facts for one session, cascading its checks and
// comments. Normally unnecessary (the chain cascades on session delete); exposed
// for explicit eviction.
func (s *Store) DeletePR(ctx context.Context, sessionID string) error {
	return s.q.DeletePR(ctx, sessionID)
}

// ReplacePRChecks atomically replaces the full set of CI checks for a session's
// PR — each SCM fetch reports the current set, so a replace (not a merge) keeps
// the table in sync (a check that disappeared upstream is removed). The PR row
// must already exist (pr_check FKs pr).
func (s *Store) ReplacePRChecks(ctx context.Context, sessionID string, checks []PRCheck) error {
	return s.inTx(ctx, "replace pr checks", func(qtx *gen.Queries) error {
		if err := qtx.DeletePRChecks(ctx, sessionID); err != nil {
			return err
		}
		for _, c := range checks {
			if err := qtx.InsertPRCheck(ctx, gen.InsertPRCheckParams{
				SessionID: sessionID,
				Name:      c.Name,
				Status:    c.Status,
				Url:       c.URL,
			}); err != nil {
				return fmt.Errorf("check %q: %w", c.Name, err)
			}
		}
		return nil
	})
}

// ListPRChecks returns a session's CI checks, ordered by name.
func (s *Store) ListPRChecks(ctx context.Context, sessionID string) ([]PRCheck, error) {
	rows, err := s.q.ListPRChecks(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list pr checks: %w", err)
	}
	out := make([]PRCheck, 0, len(rows))
	for _, r := range rows {
		out = append(out, PRCheck{Name: r.Name, Status: r.Status, URL: r.Url})
	}
	return out, nil
}

// ReplacePRComments atomically replaces the full set of review comments for a
// session's PR (same replace-not-merge rationale as ReplacePRChecks).
func (s *Store) ReplacePRComments(ctx context.Context, sessionID string, comments []PRComment) error {
	return s.inTx(ctx, "replace pr comments", func(qtx *gen.Queries) error {
		if err := qtx.DeletePRComments(ctx, sessionID); err != nil {
			return err
		}
		for _, c := range comments {
			if err := qtx.InsertPRComment(ctx, gen.InsertPRCommentParams{
				SessionID: sessionID,
				CommentID: c.CommentID,
				Author:    c.Author,
				File:      c.File,
				Line:      c.Line,
				Body:      c.Body,
				Resolved:  boolToInt(c.Resolved),
				CreatedAt: c.CreatedAt,
			}); err != nil {
				return fmt.Errorf("comment %q: %w", c.CommentID, err)
			}
		}
		return nil
	})
}

// ListPRComments returns a session's review comments, ordered by creation time.
func (s *Store) ListPRComments(ctx context.Context, sessionID string) ([]PRComment, error) {
	rows, err := s.q.ListPRComments(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list pr comments: %w", err)
	}
	out := make([]PRComment, 0, len(rows))
	for _, r := range rows {
		out = append(out, PRComment{
			CommentID: r.CommentID,
			Author:    r.Author,
			File:      r.File,
			Line:      r.Line,
			Body:      r.Body,
			Resolved:  r.Resolved != 0,
			CreatedAt: r.CreatedAt,
		})
	}
	return out, nil
}

// inTx runs fn inside a single transaction over the store's queries, rolling
// back on error.
func (s *Store) inTx(ctx context.Context, what string, fn func(*gen.Queries) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin %s: %w", what, err)
	}
	defer tx.Rollback()
	if err := fn(s.q.WithTx(tx)); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return tx.Commit()
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
