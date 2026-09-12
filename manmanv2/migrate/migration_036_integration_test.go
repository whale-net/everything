//go:build integration

// This file only builds under the "integration" build tag, so `bazel test
// //...` (Docker-less machines included) never even compiles it. See
// //libs/go/dbtest's README for how to run it and why this tag set exists.
//
// Unlike the pending_restart repository tests in
// //manmanv2/api/repository/postgres, which use a hand-written
// self-contained schema, this file drives the domain's *real* embedded
// migrations FS (declared in main.go, same package) through
// //libs/go/migrate's Runner -- proving migration 036 itself (not a
// paraphrase of it) applies cleanly on top of every prior migration and
// rolls back cleanly, per the issue's Testing item 8.
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
func openMigrateTestDB(t *testing.T, db *dbtest.Postgres) *sql.DB {
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

// TestMigration036_AppliesOnTopOfFullHistoryAndCreatesExpectedShape proves
// migration 036 applies cleanly against a fresh database (i.e. every prior
// migration plus 036 itself runs without error), lands at the expected
// version, creates pending_restarts with no valid_from/valid_to columns,
// and that the unique partial index actually enforces at-most-one-pending
// per deployment.
func TestMigration036_AppliesOnTopOfFullHistoryAndCreatesExpectedShape(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB := openMigrateTestDB(t, db)

	runner := migrate.NewRunner(sqlDB, migrations, "migrations")

	latest, err := runner.LatestVersion()
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	if latest != 43 {
		t.Fatalf("expected the latest migration source version to be 43, got %d -- update this test if a newer migration has since landed", latest)
	}

	// Target version 36 explicitly rather than Up() (which now also
	// applies every later migration through 043) -- this test is about
	// migration 036 specifically, not "whatever the latest migration
	// happens to be".
	if err := runner.Migrate(36); err != nil {
		t.Fatalf("Migrate(36) (applying every migration through 036): %v", err)
	}

	version, dirty, err := runner.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if dirty {
		t.Fatalf("expected clean state after Migrate(36), got dirty")
	}
	if version != 36 {
		t.Fatalf("expected version 36 after Migrate(36), got %d", version)
	}

	// pending_restarts must have no valid_from/valid_to columns -- this is
	// deliberately not an SCD2 table (AGENTS.md "SCD2").
	var scd2ColumnCount int
	if err := db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM information_schema.columns
		WHERE table_name = 'pending_restarts' AND column_name IN ('valid_from', 'valid_to')
	`).Scan(&scd2ColumnCount); err != nil {
		t.Fatalf("query information_schema.columns: %v", err)
	}
	if scd2ColumnCount != 0 {
		t.Fatalf("expected zero valid_from/valid_to columns on pending_restarts, got %d", scd2ColumnCount)
	}

	// All three indexes from the migration must exist.
	for _, indexName := range []string{
		"pending_restarts_one_pending_per_sgc",
		"pending_restarts_gating_session_pending",
		"pending_restarts_stall_deadline",
	} {
		var exists bool
		if err := db.Pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = $1)`, indexName,
		).Scan(&exists); err != nil {
			t.Fatalf("check index %s exists: %v", indexName, err)
		}
		if !exists {
			t.Fatalf("expected index %s to exist after migration 036, it does not", indexName)
		}
	}

	// End-to-end: seed the FK chain the real schema requires and prove the
	// unique partial index is live (not just present, but enforcing).
	var serverID, gameID, configID, sgcID, sessionID int64
	if err := db.Pool.QueryRow(ctx, `INSERT INTO servers (name) VALUES ('m036-server') RETURNING server_id`).Scan(&serverID); err != nil {
		t.Fatalf("seed server: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `INSERT INTO games (name) VALUES ('m036-game') RETURNING game_id`).Scan(&gameID); err != nil {
		t.Fatalf("seed game: %v", err)
	}
	if err := db.Pool.QueryRow(ctx,
		`INSERT INTO game_configs (game_id, name, image) VALUES ($1, 'm036-config', 'image') RETURNING config_id`, gameID,
	).Scan(&configID); err != nil {
		t.Fatalf("seed game_config: %v", err)
	}
	if err := db.Pool.QueryRow(ctx,
		`INSERT INTO server_game_configs (server_id, game_config_id) VALUES ($1, $2) RETURNING sgc_id`, serverID, configID,
	).Scan(&sgcID); err != nil {
		t.Fatalf("seed server_game_config: %v", err)
	}
	if err := db.Pool.QueryRow(ctx,
		`INSERT INTO sessions (sgc_id) VALUES ($1) RETURNING session_id`, sgcID,
	).Scan(&sessionID); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	insertPending := `INSERT INTO pending_restarts (server_game_config_id, gating_session_id, status, stall_deadline)
		VALUES ($1, $2, 'pending', NOW() + interval '1 hour')`
	if _, err := db.Pool.Exec(ctx, insertPending, sgcID, sessionID); err != nil {
		t.Fatalf("first pending insert should succeed: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, insertPending, sgcID, sessionID); err == nil {
		t.Fatal("second concurrently-pending insert for the same server_game_config_id succeeded; pending_restarts_one_pending_per_sgc did not fire")
	}
}

// TestMigration036_RollsBackCleanlyToPriorVersion proves migration 036 rolls
// back cleanly: after Up(), Migrate(35) removes pending_restarts (and its
// indexes with it) and leaves the database at version 35, undirty.
func TestMigration036_RollsBackCleanlyToPriorVersion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB := openMigrateTestDB(t, db)

	runner := migrate.NewRunner(sqlDB, migrations, "migrations")

	if err := runner.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}

	if err := runner.Migrate(35); err != nil {
		t.Fatalf("Migrate(35) (rolling back 036): %v", err)
	}

	version, dirty, err := runner.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if dirty {
		t.Fatalf("expected clean state after rolling back to 35, got dirty")
	}
	if version != 35 {
		t.Fatalf("expected version 35 after Migrate(35), got %d", version)
	}

	var tableExists bool
	if err := db.Pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'pending_restarts')`,
	).Scan(&tableExists); err != nil {
		t.Fatalf("check pending_restarts table existence: %v", err)
	}
	if tableExists {
		t.Fatal("expected pending_restarts to be dropped after rolling back migration 036, it still exists")
	}
}
