package sqlite

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntakeReviewerRepairRollsBackAndRetriesWithData(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	upTo(t, db, 117)
	mustExec(t, db, seed0118Turn)
	mustExec(t, db, `ALTER TABLE sessions ADD COLUMN reviewer_agent_config TEXT NOT NULL DEFAULT '';
UPDATE sessions SET reviewer_agent_config = '{"harness":"codex"}' WHERE id = 's-0118';
INSERT INTO goose_db_version (version_id, is_applied) VALUES (118, 1);`)

	// Observe the deleted row inside the failing INSERT, then abort that
	// statement. SQLite ABORT alone does not roll back prior transaction writes.
	mustExec(t, db, `CREATE TRIGGER intake_reject_reviewer_ledger
BEFORE INSERT ON goose_db_version WHEN NEW.version_id = 121
BEGIN
 SELECT CASE WHEN EXISTS (SELECT 1 FROM goose_db_version WHERE version_id = 118)
   THEN RAISE(ABORT, 'false 118 was not deleted before insert') END;
 SELECT RAISE(ABORT, 'injected reviewer ledger failure');
END;`)

	var original118 int64
	if err := db.QueryRow(`SELECT id FROM goose_db_version WHERE version_id = 118`).Scan(&original118); err != nil {
		t.Fatal(err)
	}
	readData := func() string {
		t.Helper()
		var snapshot string
		if err := db.QueryRow(`SELECT json_object(
 'project', p.id, 'path', p.path, 'name', p.display_name,
 'session', s.id, 'reviewer', s.reviewer_agent_config,
 'conversation', c.id, 'branch', c.active_branch_id,
 'turn', t.id, 'provider', t.provider_turn_id, 'state', t.state,
 'requested', t.requested_at, 'completed', t.completed_at)
FROM projects p JOIN sessions s ON s.project_id = p.id
JOIN conversations c ON c.session_id = s.id
JOIN conversation_turns t ON t.conversation_id = c.id
WHERE t.id = 'turn-0118'`).Scan(&snapshot); err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	before := readData()
	if err := migrate(db); err == nil || !strings.Contains(err.Error(), "injected reviewer ledger failure") {
		t.Fatalf("migration error = %v, want injected failure after false 118 deletion", err)
	}
	var restored118 int64
	if err := db.QueryRow(`SELECT id FROM goose_db_version WHERE version_id = 118 AND is_applied = 1`).Scan(&restored118); err != nil {
		t.Fatalf("false 118 ledger entry was not restored: %v", err)
	}
	if restored118 != original118 {
		t.Fatalf("rollback changed ledger identity: got %d, want %d", restored118, original118)
	}
	assert121Count := func(want int) {
		t.Helper()
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM goose_db_version WHERE version_id = 121`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("121 ledger row count = %d, want %d", count, want)
		}
	}
	assert121Count(0)
	assert0118Schema(t, db, false)
	if got := readData(); got != before {
		t.Fatalf("failed repair changed data: before=%s after=%s", before, got)
	}

	mustExec(t, db, `DROP TRIGGER intake_reject_reviewer_ledger`)
	for attempt := 0; attempt < 2; attempt++ {
		if err := migrate(db); err != nil {
			t.Fatalf("retry/startup %d: %v", attempt, err)
		}
		assert121Count(1)
		assertAppliedMigrations(t, db, 118, 121)
		assert0118Schema(t, db, true)
		if got := readData(); got != before {
			t.Fatalf("retry/startup %d changed data: before=%s after=%s", attempt, before, got)
		}
	}
}
