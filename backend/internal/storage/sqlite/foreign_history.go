package sqlite

import (
	"database/sql"
	"fmt"
	"sort"
)

// A goose version number identifies a migration only within one build's
// migration history. `goose_db_version` stores bare integers, so a database
// written by a DIFFERENT build carries rows for versions this build never
// applied. Because migrate() runs goose with WithAllowMissing(), goose then
// applies exactly the embedded migrations whose version is absent from that
// table, which means the other build's 0027 permanently masks ours: our
// 0027_shell_terminals.sql never runs, and 0036 later dies on a raw
// "no such table: shell_terminals" from SQLite.
//
// hosted-ao keeps its own state root (see config.StateRootSubdir), so this can
// no longer happen by accident. It can still happen deliberately, by pointing
// AO_DATA_DIR at another AO build's data directory, and the daemon must refuse
// that rather than migrate a database whose history it does not own.
//
// tableVersions lists, per table, the migration versions in THIS build's
// history that build it. A table missing while those versions are recorded as
// applied is proof that the rows came from someone else's history.
//
// This deliberately does NOT flag a recorded version this build simply does
// not have. Versions ahead of ours are normal across nightlies of the same
// lineage and are exactly what WithAllowMissing exists to tolerate.
//
// Extend this when a new migration creates or alters one of these tables.
// Only track a table here for as long as it is expected to exist in every
// valid history: worker_idle_events (created in 0025) is deliberately absent,
// because upstream's own 0037_drop_worker_idle_outbox.sql drops it again, so
// "applied but missing" is the normal post-0037 state, not evidence of a
// foreign build.
var tableVersions = map[string][]int64{
	"shell_terminals": {27, 36},
}

// checkForeignHistory runs before goose.Up and reports a database whose
// migration history this build does not recognise.
//
// It never writes. Un-recording the offending versions so goose could replay
// our own migrations would work on a database that is only ours, but the same
// data directory can hold another AO build's live database, and deleting its
// goose rows would make that build re-apply its own migrations over tables
// that already exist. Refusing costs an affected user one env var; guessing
// wrong corrupts somebody else's database.
func checkForeignHistory(db *sql.DB) error {
	has, err := tableExists(db, "goose_db_version")
	if err != nil || !has {
		// No history at all: a fresh database, which goose builds from zero.
		return err
	}
	applied, err := appliedVersions(db)
	if err != nil {
		return err
	}

	tables := make([]string, 0, len(tableVersions))
	for table := range tableVersions {
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
		for _, version := range tableVersions[table] {
			if !applied[version] {
				// Not applied here either, so goose applies it normally.
				continue
			}
			return fmt.Errorf(
				"refusing to migrate ao.db in this data directory: migration %d is recorded as applied "+
					"but the table it builds (%s) is missing, so this database was written by a build with "+
					"a different migration history and migrating it further would corrupt it. If AO_DATA_DIR "+
					"points at another AO build's data directory, unset it or point it at an empty directory. "+
					"Otherwise move ao.db aside and start again",
				version, table,
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
