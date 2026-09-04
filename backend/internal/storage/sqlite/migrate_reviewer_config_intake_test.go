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
