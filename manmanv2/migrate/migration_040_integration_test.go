//go:build integration

// Real-migration round-trip coverage for migration 040 (#2181, plan #2175):
// proves migration 040 itself (not a paraphrase) applies on top of the full
// prior history, creates workshop_cache_entries and
// workshop_cache_host_presence with exactly the columns and constraints
// NFR1/NFR2/FR9 require, and that down/up round-trips cleanly. This is the
// executable form of:
//
//   - NFR1: identity is workshop_id + content_version ONLY -- no sgc_id,
//     deployment_id, server_id, or library_id column on
//     workshop_cache_entries, and the only unique constraint on that table
//     is on cache_key.
//   - not-SCD2 (FR9): no valid_from/valid_to/is_current apparatus.
//
// Same precedent as migration_038_integration_test.go: build-tagged
// `integration`, drives the real embedded migrations FS through
// //libs/go/migrate's Runner, run explicitly with Docker.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //manmanv2/migrate:migration_040_integration_test --test_output=all
package main

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

// openMigrateTestDB040 opens a *sql.DB against db's isolated dbtest
// database/role using the pgx stdlib driver the Runner requires (same shape
// as migration_038_integration_test.go's helper; this target compiles only
// its own srcs, so it carries its own copy).
func openMigrateTestDB040(t *testing.T, db *dbtest.Postgres) *sql.DB {
	t.Helper()

	sqlDB, err := sql.Open("pgx", db.ConnString)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	if err := sqlDB.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}
	return sqlDB
}

// forbiddenCacheEntryColumns are the identity/ambient-scope columns NFR1
// forbids on workshop_cache_entries -- the entry's identity is workshop_id
// + content_version ONLY, so presence of any of these would mean the
// migration leaked host/SGC/deployment/library scope into the entry itself.
var forbiddenCacheEntryColumns = []string{
	"sgc_id",
	"deployment_id",
	"server_id",
	"library_id",
	"game_id",
	// not-SCD2 (FR9): this is an append-only version history, not a
	// dimension -- none of the SCD2 apparatus belongs here.
	"valid_from",
	"valid_to",
	"is_current",
}

// requiredCacheEntryColumns are the columns the issue's scaffold shape
// requires on workshop_cache_entries.
var requiredCacheEntryColumns = []string{
	"cache_entry_id",
	"workshop_id",
	"content_version",
	"cache_key",
	"s3_key",
	"size_bytes",
	"last_verified_at",
	"created_at",
}

func columnExists(ctx context.Context, t *testing.T, db *dbtest.Postgres, table, column string) bool {
	t.Helper()
	var exists bool
	if err := db.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name = $1 AND column_name = $2
		)`, table, column,
	).Scan(&exists); err != nil {
		t.Fatalf("query information_schema.columns for %s.%s: %v", table, column, err)
	}
	return exists
}

// TestMigration040_AppliesOnTopOfFullHistoryAndCreatesExpectedShape proves
// migration 040 applies cleanly against a fresh database, lands at the
// expected version, and creates workshop_cache_entries /
// workshop_cache_host_presence with exactly the columns the scaffold
// requires -- and none of the identity/SCD2 columns NFR1/FR9 forbid.
func TestMigration040_AppliesOnTopOfFullHistoryAndCreatesExpectedShape(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB := openMigrateTestDB040(t, db)

	runner := migrate.NewRunner(sqlDB, migrations, "migrations")

	latest, err := runner.LatestVersion()
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	if latest != 42 {
		t.Fatalf("expected the latest migration source version to be 42, got %d -- update this test if a newer migration has since landed", latest)
	}

	if err := runner.Up(); err != nil {
		t.Fatalf("Up (applying every migration through 042): %v", err)
	}

	version, dirty, err := runner.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if dirty {
		t.Fatalf("expected clean state after Up, got dirty")
	}
	if version != 42 {
		t.Fatalf("expected version 42 after Up, got %d", version)
	}

	for _, col := range requiredCacheEntryColumns {
		if !columnExists(ctx, t, db, "workshop_cache_entries", col) {
			t.Fatalf("expected column %s on workshop_cache_entries after migration 040", col)
		}
	}
	for _, col := range []string{"cache_entry_id", "server_id", "first_seen_at", "last_seen_at"} {
		if !columnExists(ctx, t, db, "workshop_cache_host_presence", col) {
			t.Fatalf("expected column %s on workshop_cache_host_presence after migration 040", col)
		}
	}

	// NFR1 / not-SCD2 (FR9), as executable assertions: none of these
	// columns exist on workshop_cache_entries.
	for _, col := range forbiddenCacheEntryColumns {
		if columnExists(ctx, t, db, "workshop_cache_entries", col) {
			t.Fatalf("workshop_cache_entries must not have a %q column (NFR1/FR9 forbid identity/SCD2 scope creep)", col)
		}
	}

	// The only unique constraint on workshop_cache_entries is on cache_key
	// -- no SGC-scoped (or any other) uniqueness anywhere in this layer
	// (NFR2).
	rows, err := db.Pool.Query(ctx, `
		SELECT tc.constraint_name, string_agg(kcu.column_name, ',' ORDER BY kcu.ordinal_position)
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu
			ON kcu.constraint_name = tc.constraint_name
			AND kcu.table_schema = tc.table_schema
		WHERE tc.table_name = 'workshop_cache_entries'
			AND tc.constraint_type = 'UNIQUE'
		GROUP BY tc.constraint_name`,
	)
	if err != nil {
		t.Fatalf("query unique constraints on workshop_cache_entries: %v", err)
	}
	defer rows.Close()

	type uniqueConstraint struct {
		name    string
		columns string
	}
	var uniques []uniqueConstraint
	for rows.Next() {
		var uc uniqueConstraint
		if err := rows.Scan(&uc.name, &uc.columns); err != nil {
			t.Fatalf("scan unique constraint row: %v", err)
		}
		uniques = append(uniques, uc)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate unique constraints: %v", err)
	}
	if len(uniques) != 1 {
		t.Fatalf("expected exactly one UNIQUE constraint on workshop_cache_entries, got %d: %+v", len(uniques), uniques)
	}
	if uniques[0].columns != "cache_key" {
		t.Fatalf("expected the sole UNIQUE constraint on workshop_cache_entries to cover exactly cache_key, got columns %q (constraint %q)", uniques[0].columns, uniques[0].name)
	}

	// End-to-end: an entry inserts, presence attaches, and the presence FK
	// to servers is live.
	var serverID int64
	if err := db.Pool.QueryRow(ctx, `INSERT INTO servers (name) VALUES ('m040-server') RETURNING server_id`).Scan(&serverID); err != nil {
		t.Fatalf("seed server: %v", err)
	}
	var cacheEntryID int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO workshop_cache_entries (workshop_id, content_version, cache_key, s3_key)
		VALUES ('123', '20240102', 'ws/123/20240102', 'workshop-cache/123/20240102.tar')
		RETURNING cache_entry_id`,
	).Scan(&cacheEntryID); err != nil {
		t.Fatalf("seed cache entry: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO workshop_cache_host_presence (cache_entry_id, server_id)
		VALUES ($1, $2)`, cacheEntryID, serverID,
	); err != nil {
		t.Fatalf("seed host presence: %v", err)
	}

	// Duplicate cache_key is rejected -- the UNIQUE(cache_key) constraint
	// is actually enforced, not just declared.
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO workshop_cache_entries (workshop_id, content_version, cache_key, s3_key)
		VALUES ('999', '999', 'ws/123/20240102', 'workshop-cache/999/999.tar')`,
	); err == nil {
		t.Fatalf("expected duplicate cache_key insert to fail, it succeeded")
	}

	// Deleting the cache entry cascades to its presence rows.
	if _, err := db.Pool.Exec(ctx, `DELETE FROM workshop_cache_entries WHERE cache_entry_id = $1`, cacheEntryID); err != nil {
		t.Fatalf("delete cache entry: %v", err)
	}
	var presenceCount int
	if err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM workshop_cache_host_presence WHERE cache_entry_id = $1`, cacheEntryID).Scan(&presenceCount); err != nil {
		t.Fatalf("count presence rows after cascade: %v", err)
	}
	if presenceCount != 0 {
		t.Fatalf("expected host presence rows to cascade-delete with the cache entry, got %d", presenceCount)
	}
}

// TestMigration040_DownThenUpRoundTrips proves the down migration cleanly
// removes both tables and re-applying up restores empty, correctly-shaped
// ones (no data survives a down by design -- these are new tables, so
// round-trip = shape restored + other tables untouched).
func TestMigration040_DownThenUpRoundTrips(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB := openMigrateTestDB040(t, db)

	runner := migrate.NewRunner(sqlDB, migrations, "migrations")
	if err := runner.Up(); err != nil {
		t.Fatalf("Up (full history): %v", err)
	}

	var serverID int64
	if err := db.Pool.QueryRow(ctx, `INSERT INTO servers (name) VALUES ('m040-roundtrip') RETURNING server_id`).Scan(&serverID); err != nil {
		t.Fatalf("seed server: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO workshop_cache_entries (workshop_id, content_version, cache_key, s3_key)
		VALUES ('123', '20240102', 'ws/123/20240102', 'workshop-cache/123/20240102.tar')`,
	); err != nil {
		t.Fatalf("seed cache entry: %v", err)
	}

	// Roll back to exactly version 39 (one before this migration), by
	// target version rather than a relative Steps(-1) off of whatever the
	// latest migration happens to be -- same rationale as
	// migration_038_integration_test.go/migration_039_integration_test.go/
	// migration_041_integration_test.go. A relative Steps(-1) would instead
	// undo whatever migration is current HEAD (e.g. 041 once it lands),
	// leaving 040's own tables in place and this test passing for the
	// wrong reason.
	if err := runner.Migrate(39); err != nil {
		t.Fatalf("Migrate(39) (rolling back 040): %v", err)
	}
	version, _, err := runner.Version()
	if err != nil {
		t.Fatalf("Version after rollback: %v", err)
	}
	if version != 39 {
		t.Fatalf("expected version 39 after Migrate(39), got %d", version)
	}
	for _, table := range []string{"workshop_cache_entries", "workshop_cache_host_presence"} {
		var exists bool
		if err := db.Pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)`, table,
		).Scan(&exists); err != nil {
			t.Fatalf("check table %s after down: %v", table, err)
		}
		if exists {
			t.Fatalf("expected %s to be dropped by the down migration", table)
		}
	}

	// Up again: empty tables restored, sibling data (the server row)
	// untouched.
	if err := runner.Up(); err != nil {
		t.Fatalf("Up (re-apply): %v", err)
	}
	var entryCount int
	if err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM workshop_cache_entries`).Scan(&entryCount); err != nil {
		t.Fatalf("count cache entries after re-apply: %v", err)
	}
	if entryCount != 0 {
		t.Fatalf("expected empty workshop_cache_entries after down/up round-trip, got %d rows", entryCount)
	}
	var serverName string
	if err := db.Pool.QueryRow(ctx, `SELECT name FROM servers WHERE server_id = $1`, serverID).Scan(&serverName); err != nil {
		t.Fatalf("server row must survive the round-trip: %v", err)
	}
	if serverName != "m040-roundtrip" {
		t.Fatalf("server row = %q, want m040-roundtrip", serverName)
	}
}
