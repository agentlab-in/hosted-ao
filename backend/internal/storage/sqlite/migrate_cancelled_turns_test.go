package sqlite

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigration0118InterruptionRollsBackAndRestartCompletes(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ao.db")
	open := func() *sql.DB {
		db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(5000)")
		if err != nil {
			t.Fatalf("open sqlite: %v", err)
		}
		return db
	}

	db := open()
	upTo(t, db, 117)

	// Reproduce an interruption after the destructive rebuild has started. A
	// connection close is the process-loss boundary: SQLite must roll the whole
	// explicit transaction back, leaving a restart with the intact old schema.
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF; BEGIN IMMEDIATE;
DROP TRIGGER IF EXISTS conversation_turns_cdc_update;
DROP TRIGGER IF EXISTS conversation_turns_branch_insert;
CREATE TABLE conversation_turns_next AS SELECT * FROM conversation_turns;
DROP TABLE conversation_turns;`); err != nil {
		t.Fatalf("begin interrupted rebuild: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close interrupted connection: %v", err)
	}

	db = open()
	t.Cleanup(func() { _ = db.Close() })
	var oldSchema string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='conversation_turns'`).Scan(&oldSchema); err != nil {
		t.Fatalf("old conversation_turns did not survive interruption: %v", err)
	}
	if strings.Contains(oldSchema, "'cancelled'") {
		t.Fatalf("interrupted schema unexpectedly contains cancelled state: %s", oldSchema)
	}
	var replacementCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='conversation_turns_next'`).Scan(&replacementCount); err != nil {
		t.Fatalf("inspect replacement table: %v", err)
	}
	if replacementCount != 0 {
		t.Fatalf("interrupted replacement table count = %d, want 0", replacementCount)
	}

	upTo(t, db, 118)
	var newSchema string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='conversation_turns'`).Scan(&newSchema); err != nil {
		t.Fatalf("read completed schema: %v", err)
	}
	if !strings.Contains(newSchema, "'cancelled'") {
		t.Fatalf("restart did not complete migration 0118: %s", newSchema)
	}
}
