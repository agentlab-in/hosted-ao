// Package sqlite owns the control plane's SQLite connection setup and
// goose-managed schema migrations.
package sqlite

import (
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pressly/goose/v3"

	// modernc.org/sqlite is the pure-Go (CGO-free) SQLite driver, chosen so the
	// control plane binary cross-compiles and ships without a libsqlite/CGO
	// toolchain dependency, at the cost of some raw throughput vs a C-backed
	// driver.
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// pragmas are applied on every connection open. WAL + NORMAL lets readers run
// concurrently with the writer; busy_timeout absorbs brief writer contention;
// foreign_keys enforces the accounts/machines/refresh_tokens references.
const pragmas = "?_pragma=journal_mode(WAL)" +
	"&_pragma=busy_timeout(5000)" +
	"&_pragma=foreign_keys(ON)" +
	"&_pragma=synchronous(NORMAL)"

// Open opens (creating if absent) the SQLite database under dataDir, runs
// pending goose migrations, and returns the *sql.DB. Unlike the desktop
// daemon's storage layer, this returns a single pool rather than a
// writer/reader split: there is no store layer yet to justify the split, and
// this is a single-process service, so there is no concurrent Open() to
// guard against with a migration mutex either.
func Open(dataDir string) (*sql.DB, error) {
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	dsn := "file:" + filepath.Join(dataDir, "controlplane.db") + pragmas

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
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
