//go:build integration

// This file only builds under the "integration" build tag, so `bazel test
// //...` (Docker-less machines included) never even compiles it. See
// //libs/go/dbtest's README for how to run it and why this tag set exists.
//
// Drives the domain's *real* embedded migrations FS (declared in main.go,
// same package) through //libs/go/migrate's Runner -- proving migration 041
// itself (not a paraphrase of it) applies cleanly on top of every prior
// migration, defaults pre-existing rows to 'schedulable', enforces the
// drain_state CHECK, and rolls back cleanly. Task #2360 scaffold: same
// pattern as migration_038_integration_test.go.
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

// openMigrateTestDB41 opens a *sql.DB against db's isolated dbtest
// database/role, using the pgx stdlib driver Runner requires, and registers
// it for cleanup.
func openMigrateTestDB41(t *testing.T, db *dbtest.Postgres) *sql.DB {
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

// TestMigration041_AppliesOnTopOfFullHistoryAndDefaultsExistingRows proves
// migration 041 applies cleanly against a fresh database, lands at the
// expected version, and that a servers row inserted *before* the migration
// runs (i.e. the DEFAULT clause, not just new-row behavior) reads back
// drain_state = 'schedulable' with a NULL drain_requested_at -- the issue's
// acceptance criterion 1.
func TestMigration041_AppliesOnTopOfFullHistoryAndDefaultsExistingRows(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB := openMigrateTestDB41(t, db)

	runner := migrate.NewRunner(sqlDB, migrations, "migrations")

	latest, err := runner.LatestVersion()
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	if latest != 43 {
		t.Fatalf("expected the latest migration source version to be 43, got %d -- update this test if a newer migration has since landed", latest)
	}

	// Apply every migration up through 040, seed a servers row *before* 041
	// lands, then apply 041 -- this is what actually proves the DEFAULT
	// clause backfills existing rows rather than just new inserts picking it
	// up implicitly.
	if err := runner.Migrate(40); err != nil {
		t.Fatalf("Migrate(40) (applying every migration up to but excluding 041): %v", err)
	}

	var preExistingServerID int64
	if err := db.Pool.QueryRow(ctx, `INSERT INTO servers (name) VALUES ('m041-pre-existing') RETURNING server_id`).Scan(&preExistingServerID); err != nil {
		t.Fatalf("seed pre-existing server: %v", err)
	}

	// Target version 41 explicitly rather than Up() (which now also
	// applies every later migration through 043) -- this test is about
	// migration 041 specifically, not "whatever the latest migration
	// happens to be".
	if err := runner.Migrate(41); err != nil {
		t.Fatalf("Migrate(41) (applying migration 041): %v", err)
	}

	version, dirty, err := runner.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if dirty {
		t.Fatalf("expected clean state after Migrate(41), got dirty")
	}
	if version != 41 {
		t.Fatalf("expected version 41 after Migrate(41), got %d", version)
	}

	var drainState string
	var drainRequestedAt *time.Time
	if err := db.Pool.QueryRow(ctx,
		`SELECT drain_state, drain_requested_at FROM servers WHERE server_id = $1`, preExistingServerID,
	).Scan(&drainState, &drainRequestedAt); err != nil {
		t.Fatalf("query pre-existing server's drain state: %v", err)
	}
	if drainState != "schedulable" {
		t.Fatalf("expected pre-existing server's drain_state = %q, got %q", "schedulable", drainState)
	}
	if drainRequestedAt != nil {
		t.Fatalf("expected pre-existing server's drain_requested_at to be nil, got %v", *drainRequestedAt)
	}

	// Not SCD2 (AGENTS.md "SCD2"): servers is a mutable dimension, and this
	// migration must not have added valid_from/valid_to columns.
	var scd2ColumnCount int
	if err := db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM information_schema.columns
		WHERE table_name = 'servers' AND column_name IN ('valid_from', 'valid_to')
	`).Scan(&scd2ColumnCount); err != nil {
		t.Fatalf("query information_schema.columns: %v", err)
	}
	if scd2ColumnCount != 0 {
		t.Fatalf("expected zero valid_from/valid_to columns on servers, got %d", scd2ColumnCount)
	}

	// The drain_state CHECK constraint rejects an invalid value.
	if _, err := db.Pool.Exec(ctx,
		`UPDATE servers SET drain_state = 'bogus' WHERE server_id = $1`, preExistingServerID,
	); err == nil {
		t.Fatal("expected an invalid drain_state value to be rejected by the CHECK constraint, it succeeded")
	}

	// Every valid drain_state value is accepted.
	for _, state := range []string{"schedulable", "draining", "drained"} {
		if _, err := db.Pool.Exec(ctx,
			`UPDATE servers SET drain_state = $2 WHERE server_id = $1`, preExistingServerID, state,
		); err != nil {
			t.Fatalf("expected drain_state = %q to be accepted, got: %v", state, err)
		}
	}
}

// TestMigration041_DownThenUpRoundTrips proves the down migration cleanly
// drops the drain_state/drain_requested_at columns and their CHECK
// constraint, and re-applying up restores them without disturbing sibling
// server rows.
func TestMigration041_DownThenUpRoundTrips(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB := openMigrateTestDB41(t, db)

	runner := migrate.NewRunner(sqlDB, migrations, "migrations")
	if err := runner.Up(); err != nil {
		t.Fatalf("Up (full history): %v", err)
	}

	var serverID int64
	if err := db.Pool.QueryRow(ctx, `INSERT INTO servers (name) VALUES ('m041-roundtrip') RETURNING server_id`).Scan(&serverID); err != nil {
		t.Fatalf("seed server: %v", err)
	}

	// Roll back to exactly one below 041. Runner.Down() rolls back ALL
	// migrations, which trips over pre-existing down-chain issues in
	// unrelated early migrations -- targeting 040 explicitly (rather than a
	// relative Steps(-1) off of whatever the latest migration happens to be)
	// is what keeps this round-trip testing 041's own down migration
	// specifically, regardless of how many later migrations have since
	// landed on top of it.
	if err := runner.Migrate(40); err != nil {
		t.Fatalf("Migrate(40) (rolling back 041): %v", err)
	}
	version, dirty, err := runner.Version()
	if err != nil {
		t.Fatalf("Version after down-step: %v", err)
	}
	if dirty {
		t.Fatalf("expected clean state after rolling back to 40, got dirty")
	}
	if version != 40 {
		t.Fatalf("expected version 40 after Migrate(40), got %d", version)
	}

	for _, col := range []string{"drain_state", "drain_requested_at"} {
		var exists bool
		if err := db.Pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name = 'servers' AND column_name = $1
			)`, col,
		).Scan(&exists); err != nil {
			t.Fatalf("check column %s after down: %v", col, err)
		}
		if exists {
			t.Fatalf("expected column %s to be dropped by the down migration", col)
		}
	}

	// Up again: columns restored with the default, sibling data untouched.
	if err := runner.Up(); err != nil {
		t.Fatalf("Up (re-apply): %v", err)
	}
	var drainState string
	var serverName string
	if err := db.Pool.QueryRow(ctx,
		`SELECT name, drain_state FROM servers WHERE server_id = $1`, serverID,
	).Scan(&serverName, &drainState); err != nil {
		t.Fatalf("server row must survive the round-trip: %v", err)
	}
	if serverName != "m041-roundtrip" {
		t.Fatalf("server row name = %q, want m041-roundtrip", serverName)
	}
	if drainState != "schedulable" {
		t.Fatalf("expected drain_state = %q after re-apply, got %q", "schedulable", drainState)
	}
}
