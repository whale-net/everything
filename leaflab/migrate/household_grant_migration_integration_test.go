//go:build integration

// Real-Postgres integration coverage for migration 018_household_grant
// (FR7, NFR6.3) -- the migration-level half of this task's Testing
// section that can only be proven against the real migration file, not a
// hermetic test schema: household_grant carries no valid_to column (the
// real assertion leaflab/migrate/ownership_migration_integration_test.go's
// NFR6.3 comment anticipated for this table), and up/down/up-down-up are
// clean. Requires a real TimescaleDB image, same as
// //leaflab/migrate:ownership_migration_integration_test, since migration
// 001 creates a hypertable. See //libs/go/dbtest's README for how to run
// tests like this one.
package main_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

// newPreHouseholdGrantDB migrates up through 016 (the last migration
// before 018_household_grant in this branch's ancestry -- 017 is not
// present here, claimed by plant_region_history on a sibling branch not
// merged into this branch, per 018_household_grant.up.sql's header) and
// returns the runner (still positioned at 016) plus a pool for fixture
// setup / assertions.
func newPreHouseholdGrantDB(t *testing.T) (*migrate.Runner, *dbtest.Postgres) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Image: timescaleImage})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := sqlDB.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}

	runner := migrate.NewRunner(sqlDB, migrations, "migrations")
	if err := runner.Migrate(16); err != nil {
		t.Fatalf("migrate to version 16 (pre-household-grant): %v", err)
	}

	return runner, db
}

// TestHouseholdGrantMigration_NoValidToColumn is NFR6.3's negative
// assertion for household_grant: it is a short-lived, expiring record,
// not SCD2 -- no valid_to column, ever. Mirrors
// TestAuditLogMigration_NoValidToColumn's shape.
func TestHouseholdGrantMigration_NoValidToColumn(t *testing.T) {
	runner, db := newPreHouseholdGrantDB(t)
	ctx := context.Background()

	if err := runner.Steps(1); err != nil {
		t.Fatalf("apply migration 018: %v", err)
	}

	var exists bool
	if err := db.Pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name = 'household_grant' AND column_name = 'valid_to')
	`).Scan(&exists); err != nil {
		t.Fatalf("query information_schema.columns: %v", err)
	}
	if exists {
		t.Error("household_grant has a valid_to column, want none (NFR6.3: a short-lived expiring record, not SCD2 shape)")
	}
}

// TestHouseholdGrantMigration_ActiveIndexesExist asserts against
// pg_indexes that both of migration 018's partial indexes exist --
// grantee-scoped (ScopeForPrincipal's UNION query) and household-scoped
// (ListHouseholdGrants), both filtered to revoked_at IS NULL.
func TestHouseholdGrantMigration_ActiveIndexesExist(t *testing.T) {
	runner, db := newPreHouseholdGrantDB(t)
	ctx := context.Background()

	if err := runner.Steps(1); err != nil {
		t.Fatalf("apply migration 018: %v", err)
	}

	for _, indexName := range []string{
		"idx_household_grant_grantee_subject_active",
		"idx_household_grant_household_id_active",
	} {
		t.Run(indexName, func(t *testing.T) {
			var exists bool
			if err := db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_indexes WHERE indexname = $1)`, indexName).Scan(&exists); err != nil {
				t.Fatalf("query pg_indexes for %s: %v", indexName, err)
			}
			if !exists {
				t.Errorf("index %s does not exist", indexName)
			}
		})
	}
}

// TestHouseholdGrantMigration_DownReversesCleanly and
// TestHouseholdGrantMigration_UpDownUpIsIdempotentSafe mirror the
// Validation discipline the other migration test files in this package
// already apply.
func TestHouseholdGrantMigration_DownReversesCleanly(t *testing.T) {
	runner, db := newPreHouseholdGrantDB(t)
	ctx := context.Background()

	if err := runner.Steps(1); err != nil {
		t.Fatalf("apply migration 018: %v", err)
	}
	if err := runner.Steps(-1); err != nil {
		t.Fatalf("reverse migration 018: %v", err)
	}

	var exists bool
	if err := db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name = 'household_grant')`).Scan(&exists); err != nil {
		t.Fatalf("check household_grant exists after down: %v", err)
	}
	if exists {
		t.Error("household_grant still exists after reversing migration 018")
	}
}

func TestHouseholdGrantMigration_UpDownUpIsIdempotentSafe(t *testing.T) {
	runner, db := newPreHouseholdGrantDB(t)
	ctx := context.Background()

	if err := runner.Steps(1); err != nil {
		t.Fatalf("first apply of migration 018: %v", err)
	}
	if err := runner.Steps(-1); err != nil {
		t.Fatalf("reverse migration 018: %v", err)
	}
	if err := runner.Steps(1); err != nil {
		t.Fatalf("second apply of migration 018: %v", err)
	}

	var exists bool
	if err := db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name = 'household_grant')`).Scan(&exists); err != nil {
		t.Fatalf("check household_grant exists after up-down-up: %v", err)
	}
	if !exists {
		t.Error("household_grant does not exist after up-down-up, want it re-created by the second apply")
	}
}
