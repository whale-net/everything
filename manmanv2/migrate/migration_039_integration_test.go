//go:build integration

// This file only builds under the "integration" build tag, so `bazel test
// //...` (Docker-less machines included) never even compiles it. See
// //libs/go/dbtest's README for how to run it and why this tag set exists.
//
// Drives the domain's *real* embedded migrations FS (declared in main.go,
// same package) through //libs/go/migrate's Runner -- proving migration 039
// itself (not a paraphrase of it) applies cleanly on top of every prior
// migration, rolls back cleanly, and that neither new table carries an
// sgc_id column (NFR1/NFR2, load-bearing per the migration's own header
// comment). Same pattern as migration_038_integration_test.go.
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

// openMigrateTestDB39 opens a *sql.DB against db's isolated dbtest
// database/role, using the pgx stdlib driver Runner requires, and registers
// it for cleanup.
func openMigrateTestDB39(t *testing.T, db *dbtest.Postgres) *sql.DB {
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

// TestMigration039_AppliesOnTopOfFullHistoryAndCreatesExpectedShape proves
// migration 039 applies cleanly against a fresh database, lands at the
// expected version, creates both new tables with their documented columns,
// and enforces the job_type/status CHECK constraints and the FK/cascade
// relationship between workshop_batch_jobs and workshop_batch_job_items.
func TestMigration039_AppliesOnTopOfFullHistoryAndCreatesExpectedShape(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB := openMigrateTestDB39(t, db)

	runner := migrate.NewRunner(sqlDB, migrations, "migrations")

	latest, err := runner.LatestVersion()
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	if latest != 39 {
		t.Fatalf("expected the latest migration source version to be 39, got %d -- update this test if a newer migration has since landed", latest)
	}

	if err := runner.Up(); err != nil {
		t.Fatalf("Up (applying every migration through 039): %v", err)
	}

	version, dirty, err := runner.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if dirty {
		t.Fatalf("expected clean state after Up, got dirty")
	}
	if version != 39 {
		t.Fatalf("expected version 39 after Up, got %d", version)
	}

	// All documented columns of both tables must exist.
	jobColumns := []string{
		"batch_job_id", "job_type", "game_id", "library_id", "source_input",
		"status", "total_items", "succeeded_items", "failed_items",
		"created_at", "updated_at",
	}
	for _, col := range jobColumns {
		var exists bool
		if err := db.Pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name = 'workshop_batch_jobs' AND column_name = $1
			)`, col,
		).Scan(&exists); err != nil {
			t.Fatalf("query information_schema.columns (workshop_batch_jobs): %v", err)
		}
		if !exists {
			t.Fatalf("expected column %s on workshop_batch_jobs after migration 039", col)
		}
	}

	itemColumns := []string{
		"batch_job_item_id", "batch_job_id", "raw_input", "workshop_id",
		"addon_id", "status", "error_message", "display_order",
		"created_at", "updated_at",
	}
	for _, col := range itemColumns {
		var exists bool
		if err := db.Pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name = 'workshop_batch_job_items' AND column_name = $1
			)`, col,
		).Scan(&exists); err != nil {
			t.Fatalf("query information_schema.columns (workshop_batch_job_items): %v", err)
		}
		if !exists {
			t.Fatalf("expected column %s on workshop_batch_job_items after migration 039", col)
		}
	}

	// NFR1/NFR2 (load-bearing): neither table may carry an sgc_id column.
	for _, table := range []string{"workshop_batch_jobs", "workshop_batch_job_items"} {
		var hasSGCID bool
		if err := db.Pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name = $1 AND column_name = 'sgc_id'
			)`, table,
		).Scan(&hasSGCID); err != nil {
			t.Fatalf("query information_schema.columns (sgc_id check) for %s: %v", table, err)
		}
		if hasSGCID {
			t.Fatalf("table %s must not carry an sgc_id column (NFR1/NFR2)", table)
		}
	}

	// End-to-end: a job and its items insert; the CHECK constraints and the
	// FK/cascade live.
	var gameID int64
	if err := db.Pool.QueryRow(ctx, `INSERT INTO games (name) VALUES ('m039-game') RETURNING game_id`).Scan(&gameID); err != nil {
		t.Fatalf("seed game: %v", err)
	}

	var batchJobID int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO workshop_batch_jobs (job_type, game_id, source_input, total_items)
		VALUES ('batch_create', $1, 'raw pasted block', 2)
		RETURNING batch_job_id`, gameID,
	).Scan(&batchJobID); err != nil {
		t.Fatalf("seed batch job: %v", err)
	}

	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO workshop_batch_job_items (batch_job_id, raw_input, display_order)
		VALUES ($1, '450814997', 0), ($1, 'not-a-thing', 1)`, batchJobID,
	); err != nil {
		t.Fatalf("seed batch job items: %v", err)
	}

	var itemCount int
	if err := db.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM workshop_batch_job_items WHERE batch_job_id = $1`, batchJobID,
	).Scan(&itemCount); err != nil {
		t.Fatalf("count items: %v", err)
	}
	if itemCount != 2 {
		t.Fatalf("expected 2 items for batch job %d, got %d", batchJobID, itemCount)
	}

	// Unknown job_type is rejected.
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO workshop_batch_jobs (job_type, game_id) VALUES ('not_a_type', $1)`, gameID,
	); err == nil {
		t.Fatalf("expected unknown job_type insert to fail, it succeeded")
	}

	// Unknown status is rejected.
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO workshop_batch_jobs (job_type, game_id, status) VALUES ('batch_create', $1, 'not_a_status')`, gameID,
	); err == nil {
		t.Fatalf("expected unknown status insert to fail, it succeeded")
	}

	// Deleting the batch job cascades to its items.
	if _, err := db.Pool.Exec(ctx, `DELETE FROM workshop_batch_jobs WHERE batch_job_id = $1`, batchJobID); err != nil {
		t.Fatalf("delete batch job: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM workshop_batch_job_items WHERE batch_job_id = $1`, batchJobID).Scan(&itemCount); err != nil {
		t.Fatalf("count items after cascade: %v", err)
	}
	if itemCount != 0 {
		t.Fatalf("expected items to cascade-delete with the batch job, got %d", itemCount)
	}
}

// TestMigration039_DownThenUpRoundTrips proves the down migration cleanly
// removes both tables and re-applying up restores an empty, correctly-shaped
// pair (no data survives a down by design -- these are new tables, so
// round-trip = shape restored + other tables untouched).
func TestMigration039_DownThenUpRoundTrips(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB := openMigrateTestDB39(t, db)

	runner := migrate.NewRunner(sqlDB, migrations, "migrations")
	if err := runner.Up(); err != nil {
		t.Fatalf("Up (full history): %v", err)
	}

	var gameID int64
	if err := db.Pool.QueryRow(ctx, `INSERT INTO games (name) VALUES ('m039-roundtrip') RETURNING game_id`).Scan(&gameID); err != nil {
		t.Fatalf("seed game: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO workshop_batch_jobs (job_type, game_id) VALUES ('collection_add', $1)`, gameID,
	); err != nil {
		t.Fatalf("seed batch job: %v", err)
	}

	// Down exactly one step (both tables gone). Runner.Down() rolls back ALL
	// migrations, which trips over pre-existing down-chain issues in
	// unrelated early migrations -- one step is all this round-trip needs.
	if err := runner.Steps(-1); err != nil {
		t.Fatalf("Steps(-1): %v", err)
	}
	version, _, err := runner.Version()
	if err != nil {
		t.Fatalf("Version after down-step: %v", err)
	}
	if version == 39 {
		t.Fatalf("expected version below 39 after down-step, got %d", version)
	}
	for _, table := range []string{"workshop_batch_jobs", "workshop_batch_job_items"} {
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

	// Up again: empty tables restored, sibling data untouched.
	if err := runner.Up(); err != nil {
		t.Fatalf("Up (re-apply): %v", err)
	}
	var jobCount int
	if err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM workshop_batch_jobs`).Scan(&jobCount); err != nil {
		t.Fatalf("count jobs after re-apply: %v", err)
	}
	if jobCount != 0 {
		t.Fatalf("expected empty table after down/up round-trip, got %d rows", jobCount)
	}
	var gameName string
	if err := db.Pool.QueryRow(ctx, `SELECT name FROM games WHERE game_id = $1`, gameID).Scan(&gameName); err != nil {
		t.Fatalf("game row must survive the round-trip: %v", err)
	}
	if gameName != "m039-roundtrip" {
		t.Fatalf("game row = %q, want m039-roundtrip", gameName)
	}
}
