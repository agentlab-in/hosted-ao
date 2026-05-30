package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// Upsert performs the one atomic canonical write: it CAS-checks and persists the
// session row (bumping revision), appends a change_log entry, and enqueues an
// outbox row linked to that entry's seq — all in a single transaction. Only the
// LCM calls this.
//
// Revision CAS (mirrors the in-memory store contract exactly):
//   - existing row: rec.Lifecycle.Revision must equal the stored revision, else
//     a revision-mismatch error and nothing is written; on match it persists at
//     stored+1.
//   - insert: rec.Lifecycle.Revision must be 0, persisted as 1.
func (s *Store) Upsert(ctx context.Context, rec domain.SessionRecord, eventType ports.EventType) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin upsert: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	newRevision, err := casPersist(ctx, qtx, rec)
	if err != nil {
		return err
	}

	if err := appendOutbox(ctx, qtx, rec, newRevision, eventType); err != nil {
		return err
	}

	return tx.Commit()
}

// casPersist applies the revision-CAS insert-or-update and returns the new
// stored revision.
func casPersist(ctx context.Context, q *gen.Queries, rec domain.SessionRecord) (int, error) {
	stored, err := q.GetSessionRevision(ctx, string(rec.ID))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Insert path: incoming revision must be 0; row persists at revision 1.
		if rec.Lifecycle.Revision != 0 {
			return 0, fmt.Errorf("revision mismatch for insert %s: have %d, want 0", rec.ID, rec.Lifecycle.Revision)
		}
		rows, err := q.InsertSession(ctx, recordToInsert(rec))
		if err != nil {
			return 0, fmt.Errorf("insert session %s: %w", rec.ID, err)
		}
		if rows != 1 {
			// Another writer raced us between the revision check and the insert.
			// With single-writer this should not happen; treat as a CAS failure.
			return 0, fmt.Errorf("revision mismatch for insert %s: row already exists", rec.ID)
		}
		return 1, nil
	case err != nil:
		return 0, fmt.Errorf("read revision %s: %w", rec.ID, err)
	default:
		// Update path: incoming revision must equal the stored revision.
		if int64(rec.Lifecycle.Revision) != stored {
			return 0, fmt.Errorf("revision mismatch for %s: have %d, want %d", rec.ID, rec.Lifecycle.Revision, stored)
		}
		rows, err := q.UpdateSessionCAS(ctx, recordToUpdate(rec, stored))
		if err != nil {
			return 0, fmt.Errorf("update session %s: %w", rec.ID, err)
		}
		if rows != 1 {
			return 0, fmt.Errorf("revision mismatch for %s: stale revision %d", rec.ID, rec.Lifecycle.Revision)
		}
		return int(stored) + 1, nil
	}
}

// appendOutbox writes the change_log entry and threads its seq into a fresh
// outbox row. The change_log payload is the persisted record at its new revision
// (metadata is excluded by SessionRecord's json:"-" tag — it is not on the
// canonical path).
func appendOutbox(ctx context.Context, q *gen.Queries, rec domain.SessionRecord, newRevision int, eventType ports.EventType) error {
	now := time.Now().UTC()
	payload := rec
	payload.Lifecycle.Revision = newRevision
	payload.Lifecycle.Version = domain.LifecycleVersion
	blob, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal change_log payload %s: %w", rec.ID, err)
	}

	seq, err := q.InsertChangeLog(ctx, gen.InsertChangeLogParams{
		SessionID: string(rec.ID),
		EventType: string(eventType),
		Revision:  int64(newRevision),
		Payload:   string(blob),
		CreatedAt: now,
	})
	if err != nil {
		return fmt.Errorf("insert change_log %s: %w", rec.ID, err)
	}

	if err := q.InsertOutbox(ctx, gen.InsertOutboxParams{ChangeLogSeq: seq, CreatedAt: now}); err != nil {
		return fmt.Errorf("insert outbox %s: %w", rec.ID, err)
	}
	return nil
}
