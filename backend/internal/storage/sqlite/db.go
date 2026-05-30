// Package sqlite is the durable persistence adapter behind ports.LifecycleStore.
// It owns the SQLite schema (goose migrations), the revision-CAS upsert, and the
// transactional outbox (one txn writes the session row, a change_log entry, and
// the outbox row that the CDC publisher later drains to JSONL).
package sqlite

import (
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// pragmas are applied on every connection open. WAL + NORMAL lets readers run
// concurrently with the writer; busy_timeout absorbs brief writer contention;
// foreign_keys enforces the cascades.
const pragmas = "?_pragma=journal_mode(WAL)" +
	"&_pragma=busy_timeout(5000)" +
	"&_pragma=foreign_keys(ON)" +
	"&_pragma=synchronous(NORMAL)"

// maxConnections caps the pool. WAL allows many concurrent readers, so reads
// (List/Get/GetPR/...) scale across the pool instead of queuing behind a single
// connection. Writes do NOT rely on the pool for serialization — the Store funnels
// every write through its writeMu (see store.go), which keeps WAL's single-writer
// rule and the revision-CAS read-then-write atomic regardless of pool size.
const maxConnections = 8

// Open opens (creating if absent) the SQLite database under dataDir, applies the
// connection pragmas, and runs all goose migrations up. The returned *sql.DB is
// sized for the many-reader / serialized-single-writer workload the LCM and
// readers impose.
func Open(dataDir string) (*sql.DB, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	dsn := "file:" + filepath.Join(dataDir, "ao.db") + pragmas
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(maxConnections)
	db.SetMaxIdleConns(maxConnections) // keep reader conns warm; avoid open/close churn

	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(db *sql.DB) error {
	goose.SetBaseFS(migrationsFS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}
	if err := goose.Up(db, "migrations"); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	return nil
}
