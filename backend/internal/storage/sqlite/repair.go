package sqlite

import (
	"database/sql"
	"fmt"
	"sort"
)

// A goose version number identifies a migration only within one build's
// migration history. `goose_db_version` stores bare integers, so a data
// directory that has also been opened by a DIFFERENT build (a fork, or a
// release whose migrations were numbered differently) carries rows for
// versions this build never applied: the other build's 0027 records version
// 27, and this build's 0027_shell_terminals.sql is then skipped forever,
// because goose.Up only applies versions absent from the table.
//
// The skip is silent until a later migration touches the table that was never
// created, at which point startup dies on a raw SQLite error. That is the
// reported failure: 0036 fails with "no such table: shell_terminals" on a
// database whose goose history stops at 27 and whose schema carries another
// build's tables instead of ours.
//
// tableChains lists, per table, every migration version in THIS build's
// history that creates or alters it. When the table is absent while those
// versions are recorded as applied, the rows came from another history, so
// repairPhantomVersions un-records them and lets goose replay our own
// migrations. Un-recording rather than re-issuing the DDL here keeps the
// repaired schema identical to a fresh database's by construction.
//
// Extend this when a new migration creates or alters one of these tables.
var tableChains = map[string][]int64{
	"shell_terminals":    {27, 36},
	"worker_idle_events": {25},
}

// repairRequires are the tables the repaired migrations hold foreign keys
// into. If they are gone, the file is not an AO database (or is corrupt) and
// replaying the migrations would build a table nothing could ever be written
// to, so the repair refuses instead of booting on a broken schema.
var repairRequires = []string{"projects", "sessions"}

// repairPhantomVersions runs before goose.Up. It is a no-op on a fresh
// database and on any database whose goose history is genuinely ours.
func repairPhantomVersions(db *sql.DB) error {
	has, err := tableExists(db, "goose_db_version")
	if err != nil || !has {
		return err
	}
	applied, err := appliedVersions(db)
	if err != nil {
		return err
	}

	tables := make([]string, 0, len(tableChains))
	for table := range tableChains {
		tables = append(tables, table)
	}
	sort.Strings(tables)

	for _, table := range tables {
		has, err := tableExists(db, table)
		if err != nil {
			return err
		}
		if has {
			continue
		}

		var phantom []int64
		for _, version := range tableChains[table] {
			if applied[version] {
				phantom = append(phantom, version)
			}
		}
		if len(phantom) == 0 {
			// Never applied here either, so goose applies the chain normally.
			continue
		}
		if err := checkRepairable(db, table, phantom[0]); err != nil {
			return err
		}
		for _, version := range phantom {
			if _, err := db.Exec(
				`DELETE FROM goose_db_version WHERE version_id = ?`, version,
			); err != nil {
				return fmt.Errorf("clear phantom migration version %d: %w", version, err)
			}
		}
	}
	return nil
}

// checkRepairable separates "another build's history numbered over ours",
// which is a valid upgrade path, from a database that is malformed or was
// never ours, which must still fail with something a user can act on rather
// than a raw SQLite message from whichever migration happens to break first.
func checkRepairable(db *sql.DB, table string, version int64) error {
	for _, parent := range repairRequires {
		has, err := tableExists(db, parent)
		if err != nil {
			return err
		}
		if !has {
			return fmt.Errorf(
				"cannot upgrade ao.db: migration %d is recorded as applied but the table it builds (%s) "+
					"is missing, and so is the core table %s, so this file is not an AO database this "+
					"build can repair. Move ao.db aside, or point AO_DATA_DIR at an empty directory, "+
					"then start again",
				version, table, parent,
			)
		}
	}
	return nil
}

func tableExists(db *sql.DB, name string) (bool, error) {
	var count int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name,
	).Scan(&count); err != nil {
		return false, fmt.Errorf("look up table %s: %w", name, err)
	}
	return count > 0, nil
}

func appliedVersions(db *sql.DB) (map[int64]bool, error) {
	rows, err := db.Query(
		`SELECT DISTINCT version_id FROM goose_db_version WHERE is_applied = 1`,
	)
	if err != nil {
		return nil, fmt.Errorf("read applied migration versions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	applied := map[int64]bool{}
	for rows.Next() {
		var version int64
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("scan applied migration version: %w", err)
		}
		applied[version] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read applied migration versions: %w", err)
	}
	return applied, nil
}
