package sqlite

import (
	"database/sql"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// foreignHistoryDB reproduces the reported upgrade state: a data directory that
// a build with a DIFFERENT migration history has also opened, so
// goose_db_version carries versions 25 to 27 that this build never applied.
// The observed database (`~/.ao/db-backup-20260719/ao.db`) looked exactly like
// this: history 0 to 27, tables from another lineage, and neither
// shell_terminals (our 0027) nor worker_idle_events (our 0025).
func foreignHistoryDB(t *testing.T) *sql.DB {
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

// TestMigrateRefusesForeignHistory covers the startup failure from #57: 0036's
// ALTER TABLE died with "no such table: shell_terminals" because goose had
// skipped our 0027 at a version another build had already claimed. hosted-ao
// now keeps its own state root so this cannot happen by accident, but pointing
// AO_DATA_DIR at another build's directory still reaches here, and must fail
// with something a user can act on rather than a raw SQLite message.
func TestMigrateRefusesForeignHistory(t *testing.T) {
	db := foreignHistoryDB(t)
	before := recordedVersions(t, db)

	err := migrate(db)
	if err == nil {
		t.Fatal("migrate succeeded on a database written by another migration history")
	}
	for _, want := range []string{
		"refusing to migrate ao.db", "shell_terminals", "AO_DATA_DIR", "move ao.db aside",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "no such table") {
		t.Errorf("error %q is a raw SQLite message, not an actionable one", err)
	}

	// The refusal is the whole behaviour: the other build's history is its own
	// and this daemon must not edit it, least of all by deleting rows that
	// would make that build re-apply its migrations over existing tables.
	if after := recordedVersions(t, db); !equalVersions(before, after) {
		t.Errorf("migrate changed goose_db_version from %v to %v; it must never write to a history it does not own", before, after)
	}
}

// TestMigrateRefusesDatabaseThatIsNotOurs is the same guard against a file that
// claims our migration history while carrying none of our schema.
func TestMigrateRefusesDatabaseThatIsNotOurs(t *testing.T) {
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

	if err := migrate(db); err == nil {
		t.Fatal("migrate succeeded on a database that is not ours")
	} else if !strings.Contains(err.Error(), "refusing to migrate ao.db") {
		t.Errorf("error %q is not the actionable refusal", err)
	}
}

// TestMigrateAcceptsVersionsAheadOfThisBuild pins the boundary of the refusal.
// A version this build simply does not have is normal across nightlies of the
// same lineage, and is what WithAllowMissing exists to tolerate. Only a version
// whose table is demonstrably absent proves a foreign history.
func TestMigrateAcceptsVersionsAheadOfThisBuild(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	upTo(t, db, 30)
	if _, err := db.Exec(
		`INSERT INTO goose_db_version (version_id, is_applied) VALUES (46, 1)`,
	); err != nil {
		t.Fatalf("record later migration version: %v", err)
	}

	if err := migrate(db); err != nil {
		t.Fatalf("migrate database carrying a version ahead of this build: %v", err)
	}
}

func recordedVersions(t *testing.T, db *sql.DB) []int64 {
	t.Helper()
	applied, err := appliedVersions(db)
	if err != nil {
		t.Fatalf("read applied versions: %v", err)
	}
	out := make([]int64, 0, len(applied))
	for version := range applied {
		out = append(out, version)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func equalVersions(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
