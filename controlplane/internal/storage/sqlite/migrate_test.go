package sqlite

import (
	"os"
	"path/filepath"
	"testing"
)

// TestOpen_CreatesAllTables is the runnable check for the schema: it fails if
// a future migration change breaks one of the four tables the control plane
// depends on.
func TestOpen_CreatesAllTables(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open() unexpected error: %v", err)
	}
	defer db.Close()

	for _, table := range []string{"accounts", "machines", "device_codes", "refresh_tokens"} {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
			t.Errorf("querying table %q: %v", table, err)
		}
	}
}

// The data dir holds controlplane.db (Google subjects, emails, refresh-token
// hashes) and the EdDSA signing keys, so it must not be group readable.
func TestOpen_CreatesDataDirMode0700(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "controlplane-data")

	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open() unexpected error: %v", err)
	}
	defer db.Close()

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat %s: %v", dir, err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("data dir mode = %o, want 0700", perm)
	}
}
