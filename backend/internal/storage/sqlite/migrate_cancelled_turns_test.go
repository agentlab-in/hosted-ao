package sqlite

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

const seed0118Turn = `
INSERT INTO projects (id, path, display_name, registered_at) VALUES ('p-0118', '/tmp/p-0118', 'Migration 0118', CURRENT_TIMESTAMP);
INSERT INTO sessions (id, project_id, num, harness, session_mode, activity_last_at, created_at, updated_at)
VALUES ('s-0118', 'p-0118', 1, 'codex', 'chat', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
INSERT INTO conversations (id, scope, project_id, session_id, current_session_id, active_branch_id, created_at, updated_at)
VALUES ('c-0118', 'session', 'p-0118', 's-0118', 's-0118', 'c-0118:root', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
INSERT INTO conversation_branches (id, conversation_id, session_id, provider_conversation_id, created_at)
VALUES ('c-0118:root', 'c-0118', 's-0118', 'native-0118', CURRENT_TIMESTAMP);
INSERT INTO conversation_turns (id, conversation_id, handled_by_session_id, provider_turn_id, state, requested_at, completed_at, branch_id)
VALUES ('turn-0118', 'c-0118', 's-0118', 'provider-0118', 'completed', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 'c-0118:root');`

func open0118DB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	return db
}

func assert0118Schema(t *testing.T, db *sql.DB, cancelled bool) {
	t.Helper()
	var schema string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='conversation_turns'`).Scan(&schema); err != nil {
		t.Fatalf("read conversation_turns schema: %v", err)
	}
	if got := strings.Contains(schema, "'cancelled'"); got != cancelled {
		t.Fatalf("cancelled constraint present = %v, want %v; schema=%s", got, cancelled, schema)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM conversation_turns WHERE id='turn-0118'`).Scan(&state); err != nil {
		t.Fatalf("read preserved turn: %v", err)
	}
	if state != "completed" {
		t.Fatalf("preserved turn state = %q, want completed", state)
	}
	for _, object := range []struct{ kind, name string }{
		{"index", "idx_conversation_turns_conversation"},
		{"index", "idx_conversation_turns_provider"},
		{"index", "idx_conversation_turns_branch"},
		{"index", "idx_conversation_turns_retry_source"},
		{"trigger", "conversation_turns_branch_insert"},
		{"trigger", "conversation_turns_cdc_update"},
	} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type=? AND name=?`, object.kind, object.name).Scan(&count); err != nil {
			t.Fatalf("inspect %s %s: %v", object.kind, object.name, err)
		}
		if count != 1 {
			t.Errorf("%s %s count = %d, want 1", object.kind, object.name, count)
		}
	}
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("foreign key check: %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("migration left a foreign key violation")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read foreign key check: %v", err)
	}
}

func TestMigration0118FreshDatabase(t *testing.T) {
	db := open0118DB(t, filepath.Join(t.TempDir(), "fresh.db"))
	t.Cleanup(func() { _ = db.Close() })
	upTo(t, db, 118)
	mustExec(t, db, seed0118Turn)
	assert0118Schema(t, db, true)
}

func TestMigration0118UpgradesLatestHostedSchema(t *testing.T) {
	db := open0118DB(t, filepath.Join(t.TempDir(), "upgrade.db"))
	t.Cleanup(func() { _ = db.Close() })
	// The frozen downstream base ends at 0116.
	upTo(t, db, 116)
	mustExec(t, db, seed0118Turn)
	upTo(t, db, 118)
	assert0118Schema(t, db, true)
	var applied int
	if err := db.QueryRow(`SELECT COUNT(*) FROM goose_db_version WHERE version_id=118 AND is_applied=1`).Scan(&applied); err != nil {
		t.Fatalf("read migration ledger: %v", err)
	}
	if applied != 1 {
		t.Fatalf("applied migration 0118 rows = %d, want 1", applied)
	}
}

func TestMigration0118InterruptionRollsBackAndRestartCompletes(t *testing.T) {
	stages := []struct {
		name string
		sql  string
	}{
		{"after trigger removal", `DROP TRIGGER conversation_turns_cdc_update; DROP TRIGGER conversation_turns_branch_insert;`},
		{"after replacement copy", `DROP TRIGGER conversation_turns_cdc_update; DROP TRIGGER conversation_turns_branch_insert; CREATE TABLE conversation_turns_next AS SELECT * FROM conversation_turns;`},
		{"after live table drop", `DROP TRIGGER conversation_turns_cdc_update; DROP TRIGGER conversation_turns_branch_insert; CREATE TABLE conversation_turns_next AS SELECT * FROM conversation_turns; DROP TABLE conversation_turns;`},
		{"after replacement rename", `DROP TRIGGER conversation_turns_cdc_update; DROP TRIGGER conversation_turns_branch_insert; CREATE TABLE conversation_turns_next AS SELECT * FROM conversation_turns; DROP TABLE conversation_turns; ALTER TABLE conversation_turns_next RENAME TO conversation_turns;`},
		{"during index rebuild", `DROP TRIGGER conversation_turns_cdc_update; DROP TRIGGER conversation_turns_branch_insert; CREATE TABLE conversation_turns_next AS SELECT * FROM conversation_turns; DROP TABLE conversation_turns; ALTER TABLE conversation_turns_next RENAME TO conversation_turns; CREATE INDEX idx_conversation_turns_conversation ON conversation_turns(conversation_id, requested_at);`},
	}

	for _, stage := range stages {
		t.Run(stage.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "ao.db")
			db := open0118DB(t, dbPath)
			upTo(t, db, 117)
			mustExec(t, db, seed0118Turn)
			if _, err := db.Exec("PRAGMA foreign_keys=OFF; BEGIN IMMEDIATE; " + stage.sql); err != nil {
				t.Fatalf("execute interruption stage: %v", err)
			}
			if err := db.Close(); err != nil {
				t.Fatalf("close interrupted connection: %v", err)
			}

			db = open0118DB(t, dbPath)
			t.Cleanup(func() { _ = db.Close() })
			assert0118Schema(t, db, false)
			var replacementCount int
			if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='conversation_turns_next'`).Scan(&replacementCount); err != nil {
				t.Fatalf("inspect replacement table: %v", err)
			}
			if replacementCount != 0 {
				t.Fatalf("interrupted replacement table count = %d, want 0", replacementCount)
			}

			upTo(t, db, 118)
			assert0118Schema(t, db, true)
			if _, err := db.Exec(`INSERT INTO conversation_turns (id, conversation_id, handled_by_session_id, state, requested_at, branch_id)
				VALUES ('cancelled-after-restart', 'c-0118', 's-0118', 'cancelled', CURRENT_TIMESTAMP, 'c-0118:root')`); err != nil {
				t.Fatalf("completed schema rejects cancelled turn: %v", err)
			}
		})
	}
}
