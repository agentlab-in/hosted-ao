package sqlite

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// affectedDB reproduces the reported upgrade state: a data directory that a
// build with a DIFFERENT migration history has also opened, so goose_db_version
// carries versions 25 to 27 that this build never applied. The observed
// database (`~/.ao/db-backup-20260719/ao.db`) looked exactly like this: history
// 0 to 27, tables from another lineage, and neither shell_terminals (our 0027)
// nor worker_idle_events (our 0025).
func affectedDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	upTo(t, db, 24)
	for _, version := range []int64{25, 26, 27} {
		if _, err := db.Exec(
			`INSERT INTO goose_db_version (version_id, is_applied) VALUES (?, 1)`, version,
		); err != nil {
			t.Fatalf("record foreign migration version %d: %v", version, err)
		}
	}
	return db
}

// TestMigrateRepairsTablesSkippedByForeignVersions is the regression test for
// the startup failure: 0036's ALTER TABLE died with "no such table:
// shell_terminals" because goose had skipped our 0027 at a version another
// build had already claimed. The affected database must now migrate cleanly.
func TestMigrateRepairsTablesSkippedByForeignVersions(t *testing.T) {
	db := affectedDB(t)

	if err := migrate(db); err != nil {
		t.Fatalf("migrate database whose 25-27 came from another history: %v", err)
	}

	for _, table := range []string{"shell_terminals", "worker_idle_events"} {
		has, err := tableExists(db, table)
		if err != nil {
			t.Fatalf("look up %s: %v", table, err)
		}
		if !has {
			t.Errorf("table %s is still missing after migrate", table)
		}
	}

	// The repair replays the real migrations rather than re-issuing DDL, so
	// the repaired schema must be byte-identical to a fresh database's,
	// session_id from 0036 included.
	fresh, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "fresh.db")+pragmas)
	if err != nil {
		t.Fatalf("open fresh sqlite: %v", err)
	}
	t.Cleanup(func() { _ = fresh.Close() })
	if err := migrate(fresh); err != nil {
		t.Fatalf("migrate fresh database: %v", err)
	}

	for _, table := range []string{"shell_terminals", "worker_idle_events"} {
		repaired, expected := tableSchema(t, db, table), tableSchema(t, fresh, table)
		if repaired != expected {
			t.Errorf("repaired %s schema:\n%s\nwant fresh schema:\n%s", table, repaired, expected)
		}
	}
	if !strings.Contains(tableSchema(t, db, "shell_terminals"), "session_id") {
		t.Error("repaired shell_terminals is missing the session_id column added by 0036")
	}

	if err := migrate(db); err != nil {
		t.Fatalf("repeat migrate: %v", err)
	}
}

// TestMigrateRejectsDatabaseItCannotRepair covers the other side of the same
// check: a file that claims our migration history but has none of our schema
// must still fail, and must say what to do about it instead of surfacing
// whichever raw SQLite error a migration trips over first.
func TestMigrateRejectsDatabaseItCannotRepair(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`
CREATE TABLE goose_db_version (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    version_id INTEGER NOT NULL,
    is_applied INTEGER NOT NULL,
    tstamp     TIMESTAMP DEFAULT (datetime('now'))
);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (0, 1), (27, 1), (36, 1);
CREATE TABLE something_else (id TEXT PRIMARY KEY);
`); err != nil {
		t.Fatalf("seed foreign database: %v", err)
	}

	err = migrate(db)
	if err == nil {
		t.Fatal("migrate succeeded on a database that is not ours")
	}
	for _, want := range []string{"cannot upgrade ao.db", "shell_terminals", "projects", "AO_DATA_DIR"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "no such table") {
		t.Errorf("error %q is a raw SQLite message, not an actionable one", err)
	}
}

func tableSchema(t *testing.T, db *sql.DB, name string) string {
	t.Helper()
	var schema string
	if err := db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, name,
	).Scan(&schema); err != nil {
		t.Fatalf("read %s schema: %v", name, err)
	}
	return schema
}
