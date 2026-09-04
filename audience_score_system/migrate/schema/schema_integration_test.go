//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. It exists specifically to catch what schema_test.go's pure-Go
// ReadDir check cannot: whether a migration's SQL actually applies against
// real Postgres. Migration 012 (v_prediction_vs_outcome's re-anchor from
// schedule_entry to video_script, FR44/#1830) originally used
// CREATE OR REPLACE VIEW to rename/reorder an existing view's output
// columns, which Postgres rejects outright at apply time (SQLSTATE 42P16)
// -- a failure schema_test.go's ReadDir-only check could never have
// caught, and which broke every Postgres-backed integration test in the
// domain until fixed (see #1849). This file proves migration 012's up and
// down both apply cleanly against a real database, and that
// v_prediction_vs_outcome has the right shape on both sides.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //audience_score_system/migrate/schema:schema_integration_test --test_output=all
package schema_test

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/migrate/schema"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

// viewColumns returns the ordered output column names of view (as recorded
// in information_schema.columns, which reports them in ordinal_position
// order) -- the simplest way to assert both that a view exists/is
// queryable and exactly which columns it exposes, without depending on any
// row actually being present in the underlying tables.
func viewColumns(t *testing.T, ctx context.Context, db *dbtest.Postgres, view string) []string {
	t.Helper()

	rows, err := db.Pool.Query(ctx, `
		SELECT column_name
		FROM information_schema.columns
		WHERE table_name = $1
		ORDER BY ordinal_position
	`, view)
	require.NoError(t, err)
	defer rows.Close()

	var cols []string
	for rows.Next() {
		var c string
		require.NoError(t, rows.Scan(&c))
		cols = append(cols, c)
	}
	require.NoError(t, rows.Err())
	return cols
}

// TestMigration012_UpDown_AppliesCleanly proves the specific failure #1849
// documented (CREATE OR REPLACE VIEW rejecting a renamed/reordered output
// column, SQLSTATE 42P16) is fixed: migration 012's up applies cleanly
// re-anchoring v_prediction_vs_outcome onto video_script, and its down
// applies cleanly restoring migration 002's schedule_entry-anchored shape
// -- both via DROP VIEW; CREATE VIEW, not CREATE OR REPLACE VIEW.
func TestMigration012_UpDown_AppliesCleanly(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)

	// Up through 011 first (migration 002's original, schedule_entry-
	// anchored view), then step 012's up on its own -- isolating exactly
	// the transition #1849 broke, rather than only ever exercising the
	// full-stack Up() every other integration test in this domain already
	// relies on (store_integration_test.go's
	// TestMigrations_UpDownUp_LeavesNoOrphanObjects, etc.).
	require.NoError(t, runner.Migrate(11), "apply migrations 1-11")
	preCols := viewColumns(t, ctx, db, "v_prediction_vs_outcome")
	require.Contains(t, preCols, "schedule_entry_id", "before migration 012, the view must still be migration 002's schedule_entry-anchored shape")
	require.NotContains(t, preCols, "video_script_id")

	require.NoError(t, runner.Migrate(12), "apply migration 012's up -- must not hit SQLSTATE 42P16")
	upCols := viewColumns(t, ctx, db, "v_prediction_vs_outcome")
	assert.Contains(t, upCols, "video_script_id", "migration 012's up must re-anchor the view onto video_script")
	assert.Contains(t, upCols, "script_title")
	assert.Contains(t, upCols, "script_status")
	assert.Contains(t, upCols, "target_publish_date")
	assert.Contains(t, upCols, "decided_at")
	assert.NotContains(t, upCols, "schedule_entry_id", "the up migration must not leave the old column behind")
	assert.NotContains(t, upCols, "proposed_publish_at")
	assert.NotContains(t, upCols, "approved_at")

	// A query against the view must actually succeed post-up (not just
	// "the view exists") -- proves the join chain (video_script,
	// video_schedule_match.video_script_id) is valid SQL, not merely that
	// information_schema reports columns for a broken view definition.
	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM v_prediction_vs_outcome`).Scan(&count))
	assert.Equal(t, 0, count, "no rows expected against an empty database, but the query itself must succeed")

	require.NoError(t, runner.Migrate(11), "apply migration 012's down -- must not hit SQLSTATE 42P16 either")
	downCols := viewColumns(t, ctx, db, "v_prediction_vs_outcome")
	assert.Equal(t, preCols, downCols, "migration 012's down must restore migration 002's exact original column list/order")
}
