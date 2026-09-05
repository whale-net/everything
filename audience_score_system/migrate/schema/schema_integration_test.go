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

// tableExists reports whether table exists in the database's public
// schema.
func tableExists(t *testing.T, ctx context.Context, db *dbtest.Postgres, table string) bool {
	t.Helper()

	var exists bool
	require.NoError(t, db.Pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)`, table,
	).Scan(&exists))
	return exists
}

// TestMigration013_UpDown_AppliesCleanly proves the retirement migration
// (FR41/FR45/FR47's schema half, issue #1835 -- the milestone's final
// cutover) applies cleanly on a database that already has every earlier
// migration: up drops schedule_entry and pacing_policy outright and
// removes video_schedule_match.schedule_entry_id, with no CASCADE and no
// error (a hard failure here would mean a dependency on either table was
// missed upstream, per the migration's own header comment); down
// recreates both tables and the column with their original migration-002
// definitions (structural reversibility only, per FR45's best-effort
// policy -- the dropped data itself is not recovered). Mirrors
// TestMigration012_UpDown_AppliesCleanly's isolate-one-step shape rather
// than only the full-stack up/down/up cycle
// TestMigrations_UpDownUp_LeavesNoOrphanObjects (store_integration_test.go)
// already covers.
func TestMigration013_UpDown_AppliesCleanly(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)

	// The full 001->012 chain must apply cleanly from scratch on an empty
	// database before isolating migration 013's own step.
	require.NoError(t, runner.Migrate(12), "apply migrations 1-12 (the full chain up to, but not including, this task's migration)")
	assert.True(t, tableExists(t, ctx, db, "schedule_entry"), "before migration 013, schedule_entry must still exist")
	assert.True(t, tableExists(t, ctx, db, "pacing_policy"), "before migration 013, pacing_policy must still exist")
	preMatchCols := viewColumns(t, ctx, db, "video_schedule_match")
	require.Contains(t, preMatchCols, "schedule_entry_id", "before migration 013, video_schedule_match must still carry schedule_entry_id")

	require.NoError(t, runner.Migrate(13), "apply migration 013's up -- must not fail (no CASCADE past a missed dependency)")
	assert.False(t, tableExists(t, ctx, db, "schedule_entry"), "migration 013's up must drop schedule_entry")
	assert.False(t, tableExists(t, ctx, db, "pacing_policy"), "migration 013's up must drop pacing_policy")
	postUpMatchCols := viewColumns(t, ctx, db, "video_schedule_match")
	assert.NotContains(t, postUpMatchCols, "schedule_entry_id", "migration 013's up must drop video_schedule_match.schedule_entry_id")

	require.NoError(t, runner.Migrate(12), "apply migration 013's down -- must not fail")
	assert.True(t, tableExists(t, ctx, db, "schedule_entry"), "migration 013's down must recreate schedule_entry")
	assert.True(t, tableExists(t, ctx, db, "pacing_policy"), "migration 013's down must recreate pacing_policy")
	// Column *order* is not asserted here: migration 013's down recreates
	// schedule_entry_id via ALTER TABLE ADD COLUMN, which always appends
	// at the end -- it lands after video_script_id (added later, by
	// migration 010) rather than back in its original migration-002
	// position. That is expected, not a defect: the shape (column exists,
	// with the right FK target) is what "structural reversibility"
	// promises, not byte-for-byte column ordering.
	postDownMatchCols := viewColumns(t, ctx, db, "video_schedule_match")
	assert.ElementsMatch(t, preMatchCols, postDownMatchCols, "migration 013's down must restore every one of video_schedule_match's original columns")

	// The recreated column must actually be usable as the FK it claims to
	// be (REFERENCES schedule_entry(id)) -- proves it is wired to the
	// recreated table, not merely a same-named but unconstrained column.
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO video_schedule_match (synced_video_id, schedule_entry_id, confidence, state)
		VALUES (gen_random_uuid(), gen_random_uuid(), 0.5, 'auto')
	`)
	assert.Error(t, err, "video_schedule_match.schedule_entry_id must still be a FOREIGN KEY to schedule_entry(id) after migration 013's down, rejecting a nonexistent target")
}

// columnHasUniqueConstraint reports whether column on table is covered by a
// UNIQUE (or PRIMARY KEY) constraint -- used to prove migration 014's
// `channel_id UNIQUE` (the natural key OutcomeBarStore.Upsert's `ON
// CONFLICT (channel_id)` converges on, issue #1882) actually landed as a
// real constraint, not merely a same-named but unconstrained column.
func columnHasUniqueConstraint(t *testing.T, ctx context.Context, db *dbtest.Postgres, table, column string) bool {
	t.Helper()

	var exists bool
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM information_schema.table_constraints tc
			JOIN information_schema.constraint_column_usage ccu
				ON tc.constraint_name = ccu.constraint_name
				AND tc.table_schema = ccu.table_schema
			WHERE tc.table_name = $1
				AND ccu.column_name = $2
				AND tc.constraint_type IN ('UNIQUE', 'PRIMARY KEY')
		)
	`, table, column).Scan(&exists))
	return exists
}

// TestMigration014_UpDown_AppliesCleanly proves the per-Channel outcome
// bar's storage half (C14 / FR1 / FR2 / NFR1, issue #1882) applies cleanly
// on a database that already has every earlier migration: up creates
// outcome_bar with a real UNIQUE constraint on channel_id (the natural key
// OutcomeBarStore.Upsert's ON CONFLICT converges on, NFR1), down drops it
// outright. Mirrors TestMigration013_UpDown_AppliesCleanly's isolate-one-
// step shape.
func TestMigration014_UpDown_AppliesCleanly(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)

	require.NoError(t, runner.Migrate(13), "apply migrations 1-13 (the full chain up to, but not including, this task's migration)")
	assert.False(t, tableExists(t, ctx, db, "outcome_bar"), "before migration 014, outcome_bar must not exist")

	require.NoError(t, runner.Migrate(14), "apply migration 014's up -- must not fail")
	assert.True(t, tableExists(t, ctx, db, "outcome_bar"), "migration 014's up must create outcome_bar")
	assert.True(t, columnHasUniqueConstraint(t, ctx, db, "outcome_bar", "channel_id"), "outcome_bar.channel_id must carry a real UNIQUE constraint -- the natural key OutcomeBarStore.Upsert's ON CONFLICT converges on (NFR1)")

	require.NoError(t, runner.Migrate(13), "apply migration 014's down -- must not fail")
	assert.False(t, tableExists(t, ctx, db, "outcome_bar"), "migration 014's down must drop outcome_bar cleanly")
}

// TestMigration015_UpDown_AppliesCleanly proves the authorship-marker
// column (M4.1 FR5/NFR4, issue #1898) applies cleanly on a database that
// already has every earlier migration and at least one pre-existing
// viability_verdict row: up adds `source` with a deterministic backfill to
// 'agent' for that row (NFR4 -- no row is ever null/ambiguous), a real
// NOT NULL and a real CHECK rejecting anything but 'agent'/'human', and
// v_current_verdict (SELECT * derived from viability_verdict) picks the
// column up automatically; down drops the column cleanly. Mirrors
// TestMigration014_UpDown_AppliesCleanly's isolate-one-step shape.
func TestMigration015_UpDown_AppliesCleanly(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)

	require.NoError(t, runner.Migrate(14), "apply migrations 1-14 (the full chain up to, but not including, this task's migration)")
	preCols := viewColumns(t, ctx, db, "viability_verdict")
	require.NotContains(t, preCols, "source", "before migration 015, viability_verdict must not carry source yet")

	// Seed one pre-existing viability_verdict row -- migration 015's up
	// must backfill it to source = 'agent' (NFR4), not leave it null.
	var personID, channelID, ideaID, verdictID string
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO person (google_subject, email, display_name) VALUES ($1, $2, $3) RETURNING id
	`, "sub-015", "a@example.com", "Person").Scan(&personID))
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO channel (youtube_channel_id, title, connection_state) VALUES ($1, $2, 'connected') RETURNING id
	`, "yt-015", "Channel").Scan(&channelID))
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO idea (channel_id, title, created_by_person_id) VALUES ($1, $2, $3) RETURNING id
	`, channelID, "Idea", personID).Scan(&ideaID))
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO viability_verdict (idea_id, version, verdict, reasoning, author_person_id)
		VALUES ($1, 1, 'viable', 'pre-existing row before migration 015', $2) RETURNING id
	`, ideaID, personID).Scan(&verdictID))

	require.NoError(t, runner.Migrate(15), "apply migration 015's up -- must not fail")

	var source string
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT source FROM viability_verdict WHERE id = $1`, verdictID).Scan(&source))
	assert.Equal(t, "agent", source, "migration 015's up must backfill every pre-existing row to source = 'agent' (NFR4)")

	var nullCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM viability_verdict WHERE source IS NULL`).Scan(&nullCount))
	assert.Equal(t, 0, nullCount, "no viability_verdict row may be left null/ambiguous after migration 015 (NFR4)")

	_, err = db.Pool.Exec(ctx, `
		INSERT INTO viability_verdict (idea_id, version, verdict, reasoning, author_person_id, source)
		VALUES ($1, 2, 'viable', 'bogus source value', $2, 'bogus')
	`, ideaID, personID)
	assert.Error(t, err, "an INSERT with an invalid source value must be rejected by the CHECK constraint")

	_, err = db.Pool.Exec(ctx, `
		INSERT INTO viability_verdict (idea_id, version, verdict, reasoning, author_person_id, source)
		VALUES ($1, 3, 'viable', 'explicit null source', $2, NULL)
	`, ideaID, personID)
	assert.Error(t, err, "source must be NOT NULL -- an explicit NULL must be rejected")

	upCols := viewColumns(t, ctx, db, "v_current_verdict")
	assert.Contains(t, upCols, "source", "v_current_verdict must expose source after migration 015 -- it is SELECT * derived from viability_verdict")

	require.NoError(t, runner.Migrate(14), "apply migration 015's down -- must not fail")
	downCols := viewColumns(t, ctx, db, "viability_verdict")
	assert.NotContains(t, downCols, "source", "migration 015's down must drop the column cleanly")
}
