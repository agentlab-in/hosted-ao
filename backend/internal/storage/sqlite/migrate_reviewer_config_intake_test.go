package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestIntakeReviewerConfigPreservesCancelledMigration(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	upTo(t, db, 117)
	if _, err := db.Exec("ALTER TABLE sessions ADD COLUMN reviewer_agent_config TEXT NOT NULL DEFAULT ''"); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	assertAppliedMigrations(t, db, 118, 121)
	assertTableSQLContains(t, db, "conversation_turns", "'cancelled'")
	if err := migrate(db); err != nil {
		t.Fatalf("second startup: %v", err)
	}
}

func TestIntakeRepairsReviewerHistoryRecordedAt118(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	upTo(t, db, 117)
	if _, err := db.Exec("ALTER TABLE sessions ADD COLUMN reviewer_agent_config TEXT NOT NULL DEFAULT ''; INSERT INTO goose_db_version (version_id,is_applied) VALUES (118,1)"); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	assertAppliedMigrations(t, db, 118, 121)
	assertTableSQLContains(t, db, "conversation_turns", "'cancelled'")
	if err := migrate(db); err != nil {
		t.Fatalf("second startup: %v", err)
	}
}

func TestIntakeReviewerRepairKeepsCanonical118History(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	upTo(t, db, 118)
	if _, err := db.Exec("ALTER TABLE sessions ADD COLUMN reviewer_agent_config TEXT NOT NULL DEFAULT ''"); err != nil {
		t.Fatal(err)
	}
	var before, after int64
	if err := db.QueryRow("SELECT max(id) FROM goose_db_version WHERE version_id = 118").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT max(id) FROM goose_db_version WHERE version_id = 118").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("canonical 118 was rewritten: before=%d after=%d", before, after)
	}
	assertAppliedMigrations(t, db, 118, 121)
	assertTableSQLContains(t, db, "conversation_turns", "'cancelled'")
}
