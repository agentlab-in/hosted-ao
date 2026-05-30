package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// TestSpikeOutboxTxn de-risks the whole adapter: it proves the sqlc-generated
// Querier composes inside one *sql.Tx and that the change_log seq returned
// mid-transaction threads into the outbox row — the transactional-outbox shape
// the publisher later drains. Step 0 of the implementation plan.
func TestSpikeOutboxTxn(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	now := time.Now().UTC()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	q := gen.New(db).WithTx(tx)

	// 1. CAS insert of a brand-new session (revision 0 -> persisted 1).
	rows, err := q.InsertSession(ctx, gen.InsertSessionParams{
		ID:             "s1",
		ProjectID:      "p1",
		Kind:           "worker",
		CreatedAt:      now,
		UpdatedAt:      now,
		SessionState:   "working",
		SessionReason:  "spawn_requested",
		PrState:        "none",
		PrReason:       "not_created",
		RuntimeState:   "unknown",
		RuntimeReason:  "spawn_incomplete",
		ActivityState:  "active",
		ActivityLastAt: now,
		ActivitySource: "none",
	})
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}
	if rows != 1 {
		t.Fatalf("insert session affected %d rows, want 1", rows)
	}

	// 2. Append the change_log entry and capture its seq mid-transaction.
	seq, err := q.InsertChangeLog(ctx, gen.InsertChangeLogParams{
		SessionID: "s1",
		EventType: "session_created",
		Revision:  1,
		Payload:   `{"id":"s1"}`,
		CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("insert change_log: %v", err)
	}
	if seq != 1 {
		t.Fatalf("change_log seq = %d, want 1", seq)
	}

	// 3. Thread the seq into the outbox row — the key thing the spike validates.
	if err := q.InsertOutbox(ctx, gen.InsertOutboxParams{ChangeLogSeq: seq, CreatedAt: now}); err != nil {
		t.Fatalf("insert outbox: %v", err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Verify the outbox row is visible, unsent, and linked to change_log seq 1.
	unsent, err := gen.New(db).ListUnsentOutbox(ctx, 10)
	if err != nil {
		t.Fatalf("list unsent: %v", err)
	}
	if len(unsent) != 1 {
		t.Fatalf("unsent outbox = %d rows, want 1", len(unsent))
	}
	if unsent[0].ChangeLogSeq != 1 || unsent[0].SessionID != "s1" || unsent[0].EventType != "session_created" {
		t.Fatalf("unexpected outbox row: %+v", unsent[0])
	}
}
