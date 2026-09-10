//go:build integration

// This file only builds under the "integration" build tag, so `bazel test
// //...` (Docker-less machines included) never even compiles it. See
// //libs/go/dbtest's README for how to run it and why this tag set exists.
//
// Drives the domain's *real* embedded migrations FS (declared in main.go,
// same package) through //libs/go/migrate's Runner -- proving migration 038
// itself (not a paraphrase of it) applies cleanly on top of every prior
// migration, preserves rows through down/up round-trips, and enforces its
// CHECK constraints. Task #2095 scaffold: same pattern as
// migration_036_integration_test.go.
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

// openMigrateTestDB opens a *sql.DB against db's isolated dbtest
// database/role, using the pgx stdlib driver Runner requires, and registers
// it for cleanup.
func openMigrateTestDB38(t *testing.T, db *dbtest.Postgres) *sql.DB {
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

// TestMigration038_AppliesOnTopOfFullHistoryAndCreatesExpectedShape proves
// migration 038 applies cleanly against a fresh database, lands at the
// expected version, and creates server_allowed_port_ranges with the
// PortRange-shaped columns and its CHECK constraints live.
func TestMigration038_AppliesOnTopOfFullHistoryAndCreatesExpectedShape(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB := openMigrateTestDB38(t, db)

	runner := migrate.NewRunner(sqlDB, migrations, "migrations")

	latest, err := runner.LatestVersion()
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	if latest != 42 {
		t.Fatalf("expected the latest migration source version to be 42, got %d -- update this test if a newer migration has since landed", latest)
	}

	if err := runner.Up(); err != nil {
		t.Fatalf("Up (applying every migration through 038): %v", err)
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

	// All columns of the PortRange shape (start/end/protocol) must exist.
	for _, col := range []string{"server_id", "start_port", "end_port", "protocol"} {
		var exists bool
		if err := db.Pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name = 'server_allowed_port_ranges' AND column_name = $1
			)`, col,
		).Scan(&exists); err != nil {
			t.Fatalf("query information_schema.columns: %v", err)
		}
		if !exists {
			t.Fatalf("expected column %s on server_allowed_port_ranges after migration 038", col)
		}
	}

	// End-to-end: ranges insert; the CHECK constraints and the FK live.
	var serverID int64
	if err := db.Pool.QueryRow(ctx, `INSERT INTO servers (name) VALUES ('m038-server') RETURNING server_id`).Scan(&serverID); err != nil {
		t.Fatalf("seed server: %v", err)
	}

	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO server_allowed_port_ranges (server_id, start_port, end_port, protocol)
		VALUES ($1, 25565, 25570, 'TCP'), ($1, 8123, 8123, 'UDP')`, serverID,
	); err != nil {
		t.Fatalf("seed allowed ranges: %v", err)
	}

	var rangeCount int
	if err := db.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM server_allowed_port_ranges WHERE server_id = $1`, serverID,
	).Scan(&rangeCount); err != nil {
		t.Fatalf("count ranges: %v", err)
	}
	if rangeCount != 2 {
		t.Fatalf("expected 2 ranges for server %d, got %d", serverID, rangeCount)
	}

	// end_port < start_port is rejected.
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO server_allowed_port_ranges (server_id, start_port, end_port, protocol)
		VALUES ($1, 100, 50, 'TCP')`, serverID,
	); err == nil {
		t.Fatalf("expected end_port < start_port insert to fail, it succeeded")
	}

	// Unknown protocol is rejected.
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO server_allowed_port_ranges (server_id, start_port, end_port, protocol)
		VALUES ($1, 100, 110, 'SCTP')`, serverID,
	); err == nil {
		t.Fatalf("expected non-TCP/UDP protocol insert to fail, it succeeded")
	}

	// Deleting the server cascades to its ranges.
	if _, err := db.Pool.Exec(ctx, `DELETE FROM servers WHERE server_id = $1`, serverID); err != nil {
		t.Fatalf("delete server: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM server_allowed_port_ranges`).Scan(&rangeCount); err != nil {
		t.Fatalf("count ranges after cascade: %v", err)
	}
	if rangeCount != 0 {
		t.Fatalf("expected ranges to cascade-delete with the server, got %d", rangeCount)
	}
}

// TestMigration038_DownThenUpPreservesExistingRowsAndRoundTrips proves the
// down migration cleanly removes the table and re-applying up restores an
// empty, correctly-shaped one (no data survives a down by design -- this is
// a new table, so round-trip = shape restored + other tables untouched).
func TestMigration038_DownThenUpPreservesExistingRowsAndRoundTrips(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB := openMigrateTestDB38(t, db)

	runner := migrate.NewRunner(sqlDB, migrations, "migrations")
	if err := runner.Up(); err != nil {
		t.Fatalf("Up (full history): %v", err)
	}

	var serverID int64
	if err := db.Pool.QueryRow(ctx, `INSERT INTO servers (name) VALUES ('m038-roundtrip') RETURNING server_id`).Scan(&serverID); err != nil {
		t.Fatalf("seed server: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO server_allowed_port_ranges (server_id, start_port, end_port, protocol)
		VALUES ($1, 20000, 20100, 'UDP')`, serverID,
	); err != nil {
		t.Fatalf("seed range: %v", err)
	}

	// Roll back to exactly one below 038 (table gone). Runner.Down() rolls
	// back ALL migrations, which trips over pre-existing down-chain issues
	// in unrelated early migrations -- targeting 037 explicitly (rather
	// than a relative Steps(-1) off of whatever the latest migration
	// happens to be) is what keeps this round-trip testing 038's own down
	// migration specifically, regardless of how many later migrations have
	// since landed on top of it.
	if err := runner.Migrate(37); err != nil {
		t.Fatalf("Migrate(37) (rolling back 038): %v", err)
	}
	version, _, err := runner.Version()
	if err != nil {
		t.Fatalf("Version after down-step: %v", err)
	}
	if version != 37 {
		t.Fatalf("expected version 37 after Migrate(37), got %d", version)
	}
	var exists bool
	if err := db.Pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'server_allowed_port_ranges')`,
	).Scan(&exists); err != nil {
		t.Fatalf("check table after down: %v", err)
	}
	if exists {
		t.Fatalf("expected server_allowed_port_ranges to be dropped by the down migration")
	}

	// Up again: empty table restored, sibling data untouched.
	if err := runner.Up(); err != nil {
		t.Fatalf("Up (re-apply): %v", err)
	}
	var rangeCount int
	if err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM server_allowed_port_ranges`).Scan(&rangeCount); err != nil {
		t.Fatalf("count ranges after re-apply: %v", err)
	}
	if rangeCount != 0 {
		t.Fatalf("expected empty table after down/up round-trip, got %d rows", rangeCount)
	}
	var serverName string
	if err := db.Pool.QueryRow(ctx, `SELECT name FROM servers WHERE server_id = $1`, serverID).Scan(&serverName); err != nil {
		t.Fatalf("server row must survive the round-trip: %v", err)
	}
	if serverName != "m038-roundtrip" {
		t.Fatalf("server row = %q, want m038-roundtrip", serverName)
	}
}
