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

// pragmas are applied on every connection open. WAL + NORMAL gives concurrent
// reads alongside the single writer; busy_timeout absorbs brief writer
// contention; foreign_keys enforces the session_metadata cascade.
const pragmas = "?_pragma=journal_mode(WAL)" +
	"&_pragma=busy_timeout(5000)" +
	"&_pragma=foreign_keys(ON)" +
	"&_pragma=synchronous(NORMAL)"

// Open opens (creating if absent) the SQLite database under dataDir, applies the
// connection pragmas, and runs all goose migrations up. The returned *sql.DB is
// safe for the single-writer / many-reader workload the LCM and readers impose.
func Open(dataDir string) (*sql.DB, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	dsn := "file:" + filepath.Join(dataDir, "ao.db") + pragmas
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// Single writer: serialize all access through one connection so WAL's
	// single-writer rule is never violated by the pool handing out a second
	// writable conn mid-transaction.
	db.SetMaxOpenConns(1)

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
