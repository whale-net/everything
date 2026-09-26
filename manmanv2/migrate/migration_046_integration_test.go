//go:build integration

// Real-migration round-trip coverage for migration 046 (M7 NFR2, plan
// #2777, task #2819): proves migration 046 itself (not a paraphrase) drops
// every River-owned object when present, and is a true no-op -- neither an
// error nor a partial failure -- on a database that never ran River. River
// itself is removed from this repo's module graph by this same task, so
// this test recreates a representative slice of the schema the removed
// River scheduler's migration runner (versions 001-006 of its "main" line)
// used to create, rather than depending on the now-removed River Go
// modules to create it for real.
//
// Same precedent as migration_044_integration_test.go: build-tagged
// `integration`, drives the real embedded migrations FS through
// //libs/go/migrate's Runner, run explicitly with Docker. This target
// compiles only its own srcs (main.go + this file), so it carries its own
// copy of the seed/open helpers rather than sharing another migration
// test's.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //manmanv2/migrate:migration_046_integration_test --test_output=all
package main

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
	"github.com/whale-net/everything/manmanv2/migrate/schema"
)

// openMigrateTestDB046 mirrors migration_044_integration_test.go's helper --
// this target compiles only its own srcs, so it carries its own copy.
func openMigrateTestDB046(t *testing.T, db *dbtest.Postgres) *sql.DB {
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

// createRiverSchemaFixture046 recreates the object set rivermigrate's
// "main" line left behind at the old startBackupScheduler startup path
// (manmanv2/processor/backup_scheduler.go, removed by this task): the
// river_job_state enum and river_job_state_in_bitmask() function it's
// typed against, plus river_job/river_leader/river_queue/river_client/
// river_client_queue/river_migration, seeded with one row each so the
// drop must contend with real data, not just empty tables.
func createRiverSchemaFixture046(ctx context.Context, t *testing.T, db *dbtest.Postgres) {
	t.Helper()

	stmts := []string{
		`CREATE TYPE river_job_state AS ENUM ('available', 'cancelled', 'completed', 'discarded', 'pending', 'retryable', 'running', 'scheduled')`,
		`CREATE OR REPLACE FUNCTION river_job_state_in_bitmask(bitmask BIT(8), state river_job_state)
			RETURNS boolean LANGUAGE SQL IMMUTABLE AS $$ SELECT true $$`,
		`CREATE TABLE river_migration (
			line TEXT NOT NULL,
			version bigint NOT NULL,
			created_at timestamptz NOT NULL DEFAULT NOW(),
			PRIMARY KEY (line, version)
		)`,
		`CREATE TABLE river_job (
			id bigserial PRIMARY KEY,
			state river_job_state NOT NULL DEFAULT 'available',
			kind text NOT NULL,
			args jsonb NOT NULL DEFAULT '{}',
			queue text NOT NULL DEFAULT 'default',
			created_at timestamptz NOT NULL DEFAULT NOW()
		)`,
		`CREATE UNLOGGED TABLE river_leader (
			name text PRIMARY KEY DEFAULT 'default',
			leader_id text NOT NULL,
			elected_at timestamptz NOT NULL,
			expires_at timestamptz NOT NULL
		)`,
		`CREATE TABLE river_queue (
			name text PRIMARY KEY NOT NULL,
			created_at timestamptz NOT NULL DEFAULT now(),
			updated_at timestamptz NOT NULL DEFAULT now()
		)`,
		`CREATE UNLOGGED TABLE river_client (
			id text PRIMARY KEY NOT NULL,
			created_at timestamptz NOT NULL DEFAULT now(),
			updated_at timestamptz NOT NULL DEFAULT now()
		)`,
		`CREATE UNLOGGED TABLE river_client_queue (
			river_client_id text NOT NULL REFERENCES river_client (id) ON DELETE CASCADE,
			name text NOT NULL,
			created_at timestamptz NOT NULL DEFAULT now(),
			updated_at timestamptz NOT NULL DEFAULT now(),
			PRIMARY KEY (river_client_id, name)
		)`,
		`INSERT INTO river_migration (line, version) VALUES ('main', 6)`,
		`INSERT INTO river_job (kind) VALUES ('backup_scan')`,
		`INSERT INTO river_leader (leader_id, elected_at, expires_at) VALUES ('test-leader', now(), now() + interval '1 minute')`,
		`INSERT INTO river_queue (name) VALUES ('default')`,
		`INSERT INTO river_client (id) VALUES ('test-client')`,
		`INSERT INTO river_client_queue (river_client_id, name) VALUES ('test-client', 'default')`,
	}
	for _, stmt := range stmts {
		if _, err := db.Pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("river schema fixture %q: %v", stmt, err)
		}
	}
}

// assertRiverObjectsAbsent046 asserts none of the River-owned tables, the
// river_job_state enum, or the river_job_state_in_bitmask() function exist.
func assertRiverObjectsAbsent046(ctx context.Context, t *testing.T, db *dbtest.Postgres) {
	t.Helper()

	for _, table := range []string{"river_job", "river_leader", "river_queue", "river_client", "river_client_queue", "river_migration"} {
		var exists bool
		if err := db.Pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)`, table,
		).Scan(&exists); err != nil {
			t.Fatalf("check table %s: %v", table, err)
		}
		if exists {
			t.Fatalf("expected table %s to be dropped by migration 046, it still exists", table)
		}
	}

	var typeExists bool
	if err := db.Pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'river_job_state')`,
	).Scan(&typeExists); err != nil {
		t.Fatalf("check type river_job_state: %v", err)
	}
	if typeExists {
		t.Fatal("expected the river_job_state enum to be dropped by migration 046, it still exists")
	}

	var funcExists bool
	if err := db.Pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_proc WHERE proname = 'river_job_state_in_bitmask')`,
	).Scan(&funcExists); err != nil {
		t.Fatalf("check function river_job_state_in_bitmask: %v", err)
	}
	if funcExists {
		t.Fatal("expected river_job_state_in_bitmask() to be dropped by migration 046, it still exists")
	}
}

// TestMigration046_DropsRiverTablesWhenPresent proves migration 046 applies
// cleanly on top of the full prior history, lands at version 46, and drops
// every River-owned table/type/function -- including data-bearing rows,
// not just empty tables -- when the River schema is present (acceptance
// criterion 3, Testing item 2's "applies on a database with River tables
// present" case).
func TestMigration046_DropsRiverTablesWhenPresent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB := openMigrateTestDB046(t, db)

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)

	latest, err := runner.LatestVersion()
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	if latest != 46 {
		t.Fatalf("expected the latest migration source version to be 46, got %d -- update this test if a newer migration has since landed", latest)
	}

	if err := runner.Migrate(45); err != nil {
		t.Fatalf("Migrate(45) (applying every migration up to but excluding 046): %v", err)
	}

	createRiverSchemaFixture046(ctx, t, db)

	if err := runner.Migrate(46); err != nil {
		t.Fatalf("Migrate(46) (applying migration 046): %v", err)
	}

	version, dirty, err := runner.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if dirty {
		t.Fatalf("expected clean state after Migrate(46), got dirty")
	}
	if version != 46 {
		t.Fatalf("expected version 46 after Migrate(46), got %d", version)
	}

	assertRiverObjectsAbsent046(ctx, t, db)
}

// TestMigration046_NoopOnDatabaseThatNeverRanRiver proves migration 046
// applies without error on a database that never had a River schema at all
// -- the IF EXISTS guards making this a true no-op (acceptance criterion 3,
// Testing item 2's "succeeds as a no-op" case) -- and that a subsequent
// down/up round-trip is likewise harmless.
func TestMigration046_NoopOnDatabaseThatNeverRanRiver(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB := openMigrateTestDB046(t, db)

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)

	if err := runner.Up(); err != nil {
		t.Fatalf("Up (full history, no River schema ever present): %v", err)
	}

	version, dirty, err := runner.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if dirty {
		t.Fatalf("expected clean state after Up, got dirty")
	}
	if version != 46 {
		t.Fatalf("expected version 46 after Up, got %d", version)
	}

	assertRiverObjectsAbsent046(ctx, t, db)

	// The down migration is an explicit no-op (it cannot faithfully restore
	// River's schema); rolling back to 45 and re-applying must both succeed
	// without disturbing anything.
	if err := runner.Migrate(45); err != nil {
		t.Fatalf("Migrate(45) (rolling back the no-op down): %v", err)
	}
	if err := runner.Migrate(46); err != nil {
		t.Fatalf("Migrate(46) (re-applying after rollback): %v", err)
	}
}
