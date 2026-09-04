package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestIntakeForeignHistoryRefusedBeforeRepairWrites(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE goose_db_version (id INTEGER PRIMARY KEY, version_id INTEGER, is_applied INTEGER);
 INSERT INTO goose_db_version VALUES (1,27,1);
 CREATE TABLE agent_install_jobs (target TEXT,status TEXT,method TEXT,command TEXT,expected_destination TEXT,output TEXT,error TEXT,started_at TEXT,finished_at TEXT,updated_at TEXT);`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err == nil {
		t.Fatal("foreign history accepted")
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM goose_db_version").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("migration history mutated before refusal: %d rows", count)
	}
}
