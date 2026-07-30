package sqlite

import "testing"

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
