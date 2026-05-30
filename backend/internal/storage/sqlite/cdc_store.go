package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// OutboxEvent is a single undelivered change, joined from outbox + change_log.
// It is the unit the CDC publisher drains to JSONL.
type OutboxEvent struct {
	OutboxID  int64
	Seq       int64
	SessionID string
	EventType string
	Revision  int64
	Payload   string
	CreatedAt time.Time
}

// ListUnsent returns up to limit undelivered events in seq order.
func (s *Store) ListUnsent(ctx context.Context, limit int) ([]OutboxEvent, error) {
	rows, err := s.q.ListUnsentOutbox(ctx, int64(limit))
	if err != nil {
		return nil, fmt.Errorf("list unsent outbox: %w", err)
	}
	out := make([]OutboxEvent, 0, len(rows))
	for _, r := range rows {
		out = append(out, OutboxEvent{
			OutboxID:  r.ID,
			Seq:       r.ChangeLogSeq,
			SessionID: r.SessionID,
			EventType: r.EventType,
			Revision:  r.Revision,
			Payload:   r.Payload,
			CreatedAt: r.CreatedAt,
		})
	}
	return out, nil
}

// MarkSent flags an outbox row delivered.
func (s *Store) MarkSent(ctx context.Context, outboxID int64, at time.Time) error {
	return s.q.MarkOutboxSent(ctx, gen.MarkOutboxSentParams{
		SentAt: sql.NullTime{Time: at, Valid: true},
		ID:     outboxID,
	})
}

// MarkFailed bumps the attempt count and records the last error for an outbox row.
func (s *Store) MarkFailed(ctx context.Context, outboxID int64, errMsg string) error {
	return s.q.MarkOutboxFailed(ctx, gen.MarkOutboxFailedParams{LastError: errMsg, ID: outboxID})
}

// GetOffset returns a consumer's last acknowledged seq (0 if it has none).
func (s *Store) GetOffset(ctx context.Context, consumer string) (int64, error) {
	seq, err := s.q.GetConsumerOffset(ctx, consumer)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("get consumer offset %s: %w", consumer, err)
	}
	return seq, nil
}

// SetOffset durably records a consumer's acknowledged seq.
func (s *Store) SetOffset(ctx context.Context, consumer string, seq int64, at time.Time) error {
	return s.q.UpsertConsumerOffset(ctx, gen.UpsertConsumerOffsetParams{
		Consumer:  consumer,
		LastSeq:   seq,
		UpdatedAt: at,
	})
}

// MaxChangeLogSeq returns the highest change_log seq (0 if empty). Used by the
// consumer to resume after a snapshot resync.
func (s *Store) MaxChangeLogSeq(ctx context.Context) (int64, error) {
	v, err := s.q.MaxChangeLogSeq(ctx)
	if err != nil {
		return 0, fmt.Errorf("max change_log seq: %w", err)
	}
	return v, nil
}

// MinConsumerOffset returns the lowest acknowledged seq across all consumers
// (0 if none). The janitor uses it as the safe outbox-deletion watermark.
func (s *Store) MinConsumerOffset(ctx context.Context) (int64, error) {
	v, err := s.q.MinConsumerOffset(ctx)
	if err != nil {
		return 0, fmt.Errorf("min consumer offset: %w", err)
	}
	return v, nil
}

// DeleteSentOutboxBelow removes delivered outbox rows whose seq is below the
// watermark, returning the number removed.
func (s *Store) DeleteSentOutboxBelow(ctx context.Context, seq int64) (int64, error) {
	return s.q.DeleteSentOutboxBelow(ctx, seq)
}
