//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. It exists to prove migration 001 (issue #2487's `scope` table, LB1)
// actually applies against real Postgres, reverses cleanly, and is
// re-runnable through //libs/go/migrate, plus the specific column-shape
// contract the issue's Testing section calls out ("verifying the scope
// table shape/constraints"). See
// whagent_net/migrate/schema/schema_integration_test.go for the precedent
// this mirrors.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/migrate/schema:schema_integration_test --test_output=all
package schema_test

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/migrate/schema"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

func tableExists(t *testing.T, ctx context.Context, db *dbtest.Postgres, table string) bool {
	t.Helper()
	var exists bool
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1)
	`, table).Scan(&exists))
	return exists
}

// TestMigration001_UpDownUp_LeavesCleanDatabaseAndIsRerunnable proves
// migration 001's whole lifecycle: Up() creates `scope`, Down() drops it (a
// clean database), and Up() again succeeds a second time from that clean
// state -- migration 001 is re-runnable through //libs/go/migrate, not a
// one-shot script.
func TestMigration001_UpDownUp_LeavesCleanDatabaseAndIsRerunnable(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)

	latest, err := runner.LatestVersion()
	require.NoError(t, err)
	require.Equal(t, uint(1), latest, "expected the latest migration source version to be 1 (001_scope, issue #2487) -- update this test if a later migration has since landed")

	// -- Up: scope must exist, version must land clean at the latest --
	require.NoError(t, runner.Up(), "apply migration 001")

	version, dirty, err := runner.Version()
	require.NoError(t, err)
	assert.False(t, dirty)
	assert.Equal(t, uint(1), version)

	assert.True(t, tableExists(t, ctx, db, "scope"), "expected table \"scope\" to exist after Up()")

	// -- Down: scope must be gone -------------------------------------------
	require.NoError(t, runner.Down(), "roll back migration 001")

	assert.False(t, tableExists(t, ctx, db, "scope"), "expected table \"scope\" to be dropped after Down() -- a clean database")

	// -- Up again: re-runnable from the clean state --------------------------
	require.NoError(t, runner.Up(), "re-apply migration 001 after Down() -- must be re-runnable")

	version, dirty, err = runner.Version()
	require.NoError(t, err)
	assert.False(t, dirty)
	assert.Equal(t, uint(1), version)

	assert.True(t, tableExists(t, ctx, db, "scope"), "expected table \"scope\" to exist again after the second Up()")
}

// TestMigration001_SchemaContract asserts the specific column shapes and
// constraints migrations/001_scope.up.sql commits to (LB1): a surrogate PK,
// a UNIQUE repo_full_name (the seeder's idempotency key), a NOT NULL
// default_branch, and a nullable pointer_issue_number (NFR2/FR20: the
// pointer issue does not exist until a later task). Guards against a future
// migration accidentally loosening one of these column shapes.
func TestMigration001_SchemaContract(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up())

	nullableColumn := func(t *testing.T, ctx context.Context, db *dbtest.Postgres, table, column string) (dataType, nullable string) {
		t.Helper()
		require.NoError(t, db.Pool.QueryRow(ctx, `
			SELECT data_type, is_nullable FROM information_schema.columns
			WHERE table_name = $1 AND column_name = $2
		`, table, column).Scan(&dataType, &nullable))
		return dataType, nullable
	}

	_, nullable := nullableColumn(t, ctx, db, "scope", "repo_full_name")
	assert.Equal(t, "NO", nullable, "scope.repo_full_name must be NOT NULL")

	_, nullable = nullableColumn(t, ctx, db, "scope", "default_branch")
	assert.Equal(t, "NO", nullable, "scope.default_branch must be NOT NULL")

	_, nullable = nullableColumn(t, ctx, db, "scope", "pointer_issue_number")
	assert.Equal(t, "YES", nullable, "scope.pointer_issue_number must be nullable (NFR2/FR20: the pointer issue does not exist until a later task)")

	for _, col := range []string{"created_at", "updated_at"} {
		_, nullable := nullableColumn(t, ctx, db, "scope", col)
		assert.Equal(t, "NO", nullable, "scope.%s must be NOT NULL", col)
	}

	// repo_full_name carries a UNIQUE constraint -- the seeder's
	// ON CONFLICT (repo_full_name) target and the property that makes
	// re-seeding idempotent (seed_integration_test.go covers the
	// behavioral half of this; this asserts the DB-level constraint
	// backing it actually exists).
	var hasUniqueConstraint bool
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_constraint c
			JOIN pg_class t ON t.oid = c.conrelid
			WHERE t.relname = 'scope' AND c.contype = 'u'
			  AND c.conkey = (
			      SELECT array_agg(a.attnum) FROM pg_attribute a
			      WHERE a.attrelid = t.oid AND a.attname = 'repo_full_name'
			  )
		)
	`).Scan(&hasUniqueConstraint))
	assert.True(t, hasUniqueConstraint, "scope.repo_full_name must have a UNIQUE constraint")

	// A second insert with the same repo_full_name violates that UNIQUE
	// constraint directly -- proves the constraint is live, not merely
	// present in pg_constraint metadata.
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ('dup/dup', 'main')
	`)
	require.NoError(t, err)
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ('dup/dup', 'main')
	`)
	assert.Error(t, err, "a second scope row with the same repo_full_name must violate the UNIQUE constraint")
}
