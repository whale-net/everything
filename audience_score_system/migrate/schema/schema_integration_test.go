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
	"time"

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

// ── migration 016 helpers (research_thread, research_note.thread_id +
// backfill, research_note_relation, v_current_research_note; root plan
// #1934, this task #1936) ───────────────────────────────────────────────────

func insertPersonRow(t *testing.T, ctx context.Context, db *dbtest.Postgres, sub, email, name string) string {
	t.Helper()
	var id string
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO person (google_subject, email, display_name) VALUES ($1, $2, $3) RETURNING id
	`, sub, email, name).Scan(&id))
	return id
}

func insertChannelRow(t *testing.T, ctx context.Context, db *dbtest.Postgres, ytID, title string) string {
	t.Helper()
	var id string
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO channel (youtube_channel_id, title, connection_state) VALUES ($1, $2, 'connected') RETURNING id
	`, ytID, title).Scan(&id))
	return id
}

func insertIdeaRow(t *testing.T, ctx context.Context, db *dbtest.Postgres, channelID, title, personID string) string {
	t.Helper()
	var id string
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO idea (channel_id, title, created_by_person_id) VALUES ($1, $2, $3) RETURNING id
	`, channelID, title, personID).Scan(&id))
	return id
}

// insertPre016ResearchNote inserts a research_note row using only the
// columns that existed before migration 016 (no thread_id) -- used to seed
// pre-existing data the backfill must pick up. ideaID == "" means a note
// that predates an Idea (idea_id NULL).
func insertPre016ResearchNote(t *testing.T, ctx context.Context, db *dbtest.Postgres, channelID, ideaID, text, authorPersonID string, createdAt time.Time) string {
	t.Helper()
	var id string
	var ideaArg interface{}
	if ideaID != "" {
		ideaArg = ideaID
	}
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO research_note (channel_id, idea_id, text, author_person_id, created_at)
		VALUES ($1, $2, $3, $4, $5) RETURNING id
	`, channelID, ideaArg, text, authorPersonID, createdAt).Scan(&id))
	return id
}

// insertResearchThread inserts a research_thread row directly -- this task
// adds no store method for it (structs only), so tests that need a thread
// post-migration-016 construct one via raw SQL, same as the rest of this
// file. ideaID == "" means a NULL idea_id.
func insertResearchThread(t *testing.T, ctx context.Context, db *dbtest.Postgres, channelID, ideaID, title, personID string) string {
	t.Helper()
	var id string
	var ideaArg interface{}
	if ideaID != "" {
		ideaArg = ideaID
	}
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO research_thread (channel_id, idea_id, title, created_by_person_id) VALUES ($1, $2, $3, $4) RETURNING id
	`, channelID, ideaArg, title, personID).Scan(&id))
	return id
}

// insertPost016ResearchNote inserts a research_note row with an explicit
// thread_id -- the column migration 016 adds. ideaID == "" means a NULL
// idea_id.
func insertPost016ResearchNote(t *testing.T, ctx context.Context, db *dbtest.Postgres, channelID, ideaID, threadID, text, authorPersonID string) string {
	t.Helper()
	var id string
	var ideaArg interface{}
	if ideaID != "" {
		ideaArg = ideaID
	}
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO research_note (channel_id, idea_id, thread_id, text, author_person_id) VALUES ($1, $2, $3, $4, $5) RETURNING id
	`, channelID, ideaArg, threadID, text, authorPersonID).Scan(&id))
	return id
}

// insertPost018ResearchNote inserts a research_note row with NO idea_id
// column at all (migration 018/#1947 dropped it) and a required thread_id
// -- the shape every research_note row has post-Stage-3. Used by tests
// that seed data against the head schema (or migration 018 and later),
// where insertPost016ResearchNote's idea_id column would no longer exist.
func insertPost018ResearchNote(t *testing.T, ctx context.Context, db *dbtest.Postgres, channelID, threadID, text, authorPersonID string) string {
	t.Helper()
	var id string
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO research_note (channel_id, thread_id, text, author_person_id) VALUES ($1, $2, $3, $4) RETURNING id
	`, channelID, threadID, text, authorPersonID).Scan(&id))
	return id
}

// TestMigration016_Backfill_CreatesPerBucketThreadsAndPreservesNoteData
// proves migration 016's backfill (FR2 Stage 1) creates exactly one
// synthetic research_thread per distinct (channel_id, idea_id) bucket of
// pre-existing research_note rows -- including the (channel_id, NULL)
// bucket for notes that predate an Idea -- points every pre-existing note at
// its bucket's thread, and leaves every note's own text/source_url/
// author_person_id/created_at/idea_id byte-identical (NFR3: an old binary
// reading research_note.idea_id must keep working unchanged). The bucket's
// created_by_person_id is asserted to be the author of the EARLIEST note in
// the bucket (migration 016's documented deterministic tie-break), not
// merely some arbitrary author of the bucket.
func TestMigration016_Backfill_CreatesPerBucketThreadsAndPreservesNoteData(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Migrate(15), "apply migrations 1-15 (the full chain up to, but not including, this task's migration)")

	p1 := insertPersonRow(t, ctx, db, "sub-016-p1", "p1@example.com", "Person One")
	p2 := insertPersonRow(t, ctx, db, "sub-016-p2", "p2@example.com", "Person Two")
	c1 := insertChannelRow(t, ctx, db, "yt-016-c1", "Channel One")
	c2 := insertChannelRow(t, ctx, db, "yt-016-c2", "Channel Two")
	i1 := insertIdeaRow(t, ctx, db, c1, "Idea One", p1)
	i2 := insertIdeaRow(t, ctx, db, c2, "Idea Two", p1)

	// Truncated to microsecond precision -- Postgres timestamptz only
	// stores microseconds, so an untruncated Go time.Time (nanosecond
	// precision) would never compare byte-identical after a round trip.
	base := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Microsecond)
	// Bucket (c1, i1): two notes, earliest authored by p1 -- the bucket's
	// thread must be created_by p1, not p2, even though p2 wrote later.
	n1 := insertPre016ResearchNote(t, ctx, db, c1, i1, "n1 text", p1, base)
	n2 := insertPre016ResearchNote(t, ctx, db, c1, i1, "n2 text", p2, base.Add(time.Hour))
	// Bucket (c1, NULL): predates an Idea.
	n3 := insertPre016ResearchNote(t, ctx, db, c1, "", "n3 text, no idea yet", p2, base.Add(2*time.Hour))
	// Bucket (c2, i2): isolated from c1's buckets even though the idea
	// titles/authors overlap.
	n4 := insertPre016ResearchNote(t, ctx, db, c2, i2, "n4 text", p1, base.Add(3*time.Hour))

	require.NoError(t, runner.Migrate(16), "apply migration 016's up -- must not fail")

	type noteRow struct {
		text, authorPersonID string
		ideaID               *string
		createdAt            time.Time
		sourceURL            *string
		threadID             *string
	}
	fetch := func(id string) noteRow {
		var r noteRow
		require.NoError(t, db.Pool.QueryRow(ctx, `
			SELECT text, source_url, author_person_id, created_at, idea_id, thread_id
			FROM research_note WHERE id = $1
		`, id).Scan(&r.text, &r.sourceURL, &r.authorPersonID, &r.createdAt, &r.ideaID, &r.threadID))
		return r
	}

	r1, r2, r3, r4 := fetch(n1), fetch(n2), fetch(n3), fetch(n4)

	// -- data preservation (no DROP/UPDATE of the untouched columns) --------
	assert.Equal(t, "n1 text", r1.text)
	assert.Equal(t, p1, r1.authorPersonID)
	assert.True(t, base.Equal(r1.createdAt), "created_at must be byte-identical (unchanged) after the backfill")
	assert.Nil(t, r1.sourceURL, "source_url must remain untouched (NULL) after the backfill")
	require.NotNil(t, r1.ideaID, "NFR3: research_note.idea_id must still be populated and readable exactly as before")
	assert.Equal(t, i1, *r1.ideaID)
	assert.Equal(t, "n3 text, no idea yet", r3.text)
	assert.Nil(t, r3.ideaID, "a note that predated an Idea must keep idea_id NULL -- migration 016 never assigns one retroactively")

	// -- every note ends with a non-null thread_id ---------------------------
	for _, r := range []noteRow{r1, r2, r3, r4} {
		require.NotNil(t, r.threadID, "every pre-existing research_note must have a non-null thread_id after migration 016's backfill")
	}

	requireThreadMatchesBucket := func(threadID, channelID, ideaID string) {
		t.Helper()
		var gotChannelID, gotTitle string
		var gotIdeaID *string
		require.NoError(t, db.Pool.QueryRow(ctx, `
			SELECT channel_id, idea_id, title FROM research_thread WHERE id = $1
		`, threadID).Scan(&gotChannelID, &gotIdeaID, &gotTitle))
		assert.Equal(t, channelID, gotChannelID, "thread's channel_id must equal the note's channel_id")
		if ideaID == "" {
			assert.Nil(t, gotIdeaID, "thread's idea_id must be NULL for the (channel_id, NULL) bucket")
		} else {
			require.NotNil(t, gotIdeaID)
			assert.Equal(t, ideaID, *gotIdeaID, "thread's idea_id must be IS NOT DISTINCT FROM the note's own idea_id")
		}
		assert.Equal(t, "Research", gotTitle, "every backfilled thread must be titled \"Research\"")
	}

	requireThreadMatchesBucket(*r1.threadID, c1, i1)
	requireThreadMatchesBucket(*r2.threadID, c1, i1)
	requireThreadMatchesBucket(*r3.threadID, c1, "")
	requireThreadMatchesBucket(*r4.threadID, c2, i2)

	assert.Equal(t, *r1.threadID, *r2.threadID, "two notes in the same (channel_id, idea_id) bucket must share exactly one backfilled thread")
	assert.NotEqual(t, *r1.threadID, *r3.threadID, "the (channel_id, NULL) bucket must be a distinct thread from the (channel_id, idea_id) bucket on the same channel")
	assert.NotEqual(t, *r1.threadID, *r4.threadID, "buckets on different channels must never share a thread")

	// -- deterministic created_by: earliest note in the bucket, not p2 -------
	var threadCreatedBy string
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT created_by_person_id FROM research_thread WHERE id = $1`, *r1.threadID).Scan(&threadCreatedBy))
	assert.Equal(t, p1, threadCreatedBy, "the bucket's thread must be created_by the author of the EARLIEST note in the bucket (n1, by p1), not a later author (n2, by p2)")

	// -- exactly one thread per distinct bucket, and no more -----------------
	var threadCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM research_thread`).Scan(&threadCount))
	assert.Equal(t, 3, threadCount, "exactly one synthetic thread per distinct (channel_id, idea_id) bucket -- (c1,i1), (c1,NULL), (c2,i2)")

	// -- zero research_note_relation rows after the backfill -----------------
	var relationCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM research_note_relation`).Scan(&relationCount))
	assert.Equal(t, 0, relationCount, "the backfill must not create any research_note_relation rows (root plan Out of scope)")
}

// TestMigration016_SameThreadTrigger_EnforcesRelationsWithinOneThreadOnly
// proves FR6/NFR4's same-thread enforcement is a real DB trigger, not
// merely documentation: a relation between two notes in the SAME thread is
// accepted, and one between two notes in DIFFERENT threads is rejected.
func TestMigration016_SameThreadTrigger_EnforcesRelationsWithinOneThreadOnly(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Migrate(16), "apply the full chain through migration 016")

	p := insertPersonRow(t, ctx, db, "sub-016-trigger", "trigger@example.com", "Trigger Person")
	c := insertChannelRow(t, ctx, db, "yt-016-trigger", "Trigger Channel")

	threadA := insertResearchThread(t, ctx, db, c, "", "Thread A", p)
	threadB := insertResearchThread(t, ctx, db, c, "", "Thread B", p)

	noteA1 := insertPost016ResearchNote(t, ctx, db, c, "", threadA, "a1", p)
	noteA2 := insertPost016ResearchNote(t, ctx, db, c, "", threadA, "a2", p)
	noteB1 := insertPost016ResearchNote(t, ctx, db, c, "", threadB, "b1", p)

	_, err = db.Pool.Exec(ctx, `
		INSERT INTO research_note_relation (note_id, related_note_id, relation_type) VALUES ($1, $2, 'supersedes')
	`, noteA2, noteA1)
	assert.NoError(t, err, "a relation between two notes in the SAME thread must be accepted")

	_, err = db.Pool.Exec(ctx, `
		INSERT INTO research_note_relation (note_id, related_note_id, relation_type) VALUES ($1, $2, 'supersedes')
	`, noteB1, noteA1)
	assert.Error(t, err, "a relation between two notes in DIFFERENT threads must be rejected by the same-thread trigger (FR6/NFR4)")
}

// TestMigration016_RelationConstraints_RejectsSelfReferenceAndUnknownType
// proves research_note_relation's own CHECK constraints -- self-reference
// and the closed relation_type vocabulary -- reject bad inserts and leave
// nothing behind.
func TestMigration016_RelationConstraints_RejectsSelfReferenceAndUnknownType(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Migrate(16), "apply the full chain through migration 016")

	p := insertPersonRow(t, ctx, db, "sub-016-constraints", "constraints@example.com", "Constraints Person")
	c := insertChannelRow(t, ctx, db, "yt-016-constraints", "Constraints Channel")
	thread := insertResearchThread(t, ctx, db, c, "", "Thread", p)
	n1 := insertPost016ResearchNote(t, ctx, db, c, "", thread, "n1", p)
	n2 := insertPost016ResearchNote(t, ctx, db, c, "", thread, "n2", p)

	_, err = db.Pool.Exec(ctx, `
		INSERT INTO research_note_relation (note_id, related_note_id, relation_type) VALUES ($1, $1, 'supersedes')
	`, n1)
	assert.Error(t, err, "a self-referencing relation (note_id = related_note_id) must be rejected by the table CHECK constraint")

	_, err = db.Pool.Exec(ctx, `
		INSERT INTO research_note_relation (note_id, related_note_id, relation_type) VALUES ($1, $2, 'bogus')
	`, n1, n2)
	assert.Error(t, err, "an unknown relation_type must be rejected by the closed CHECK constraint set")

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM research_note_relation`).Scan(&count))
	assert.Equal(t, 0, count, "neither rejected insert may leave a row behind")
}

// TestMigration016_CurrentResearchNoteView_RelationTypesDetermineInclusion
// proves v_current_research_note's exact FR7 inclusion rule: a note is
// excluded IFF it is the related_note_id target of a 'supersedes' or
// 'excludes' relation; being the target of 'caveats', 'follows_up', or
// 'summarizes' does not exclude it.
func TestMigration016_CurrentResearchNoteView_RelationTypesDetermineInclusion(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Migrate(16), "apply the full chain through migration 016")

	p := insertPersonRow(t, ctx, db, "sub-016-view", "view@example.com", "View Person")
	c := insertChannelRow(t, ctx, db, "yt-016-view", "View Channel")
	thread := insertResearchThread(t, ctx, db, c, "", "Thread", p)

	relator := insertPost016ResearchNote(t, ctx, db, c, "", thread, "relator", p)
	superseded := insertPost016ResearchNote(t, ctx, db, c, "", thread, "superseded", p)
	excluded := insertPost016ResearchNote(t, ctx, db, c, "", thread, "excluded", p)
	caveated := insertPost016ResearchNote(t, ctx, db, c, "", thread, "caveated", p)
	followedUp := insertPost016ResearchNote(t, ctx, db, c, "", thread, "followed up", p)
	summarized := insertPost016ResearchNote(t, ctx, db, c, "", thread, "summarized", p)

	for _, rel := range []struct{ target, relType string }{
		{superseded, "supersedes"},
		{excluded, "excludes"},
		{caveated, "caveats"},
		{followedUp, "follows_up"},
		{summarized, "summarizes"},
	} {
		_, err := db.Pool.Exec(ctx, `
			INSERT INTO research_note_relation (note_id, related_note_id, relation_type) VALUES ($1, $2, $3)
		`, relator, rel.target, rel.relType)
		require.NoError(t, err)
	}

	isCurrent := func(id string) bool {
		var count int
		require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM v_current_research_note WHERE id = $1`, id).Scan(&count))
		return count == 1
	}

	assert.True(t, isCurrent(relator), "a note that is not the target of any relation must remain current")
	assert.False(t, isCurrent(superseded), "a note targeted by a 'supersedes' relation must be excluded (FR7)")
	assert.False(t, isCurrent(excluded), "a note targeted by an 'excludes' relation must be excluded (FR7)")
	assert.True(t, isCurrent(caveated), "a note targeted by a 'caveats' relation must remain current -- only supersedes/excludes exclude (FR7)")
	assert.True(t, isCurrent(followedUp), "a note targeted by a 'follows_up' relation must remain current (FR7)")
	assert.True(t, isCurrent(summarized), "a note targeted by a 'summarizes' relation must remain current (FR7)")
}

// TestMigration016_CurrentResearchNoteView_SupersedesChain_LeavesOnlyNewestCurrent
// proves a 3-long supersedes chain leaves only the newest note current with
// no extra transitive-closure logic in the view -- each edge retiring its
// own direct target already propagates (FR7).
func TestMigration016_CurrentResearchNoteView_SupersedesChain_LeavesOnlyNewestCurrent(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Migrate(16), "apply the full chain through migration 016")

	p := insertPersonRow(t, ctx, db, "sub-016-chain", "chain@example.com", "Chain Person")
	c := insertChannelRow(t, ctx, db, "yt-016-chain", "Chain Channel")
	thread := insertResearchThread(t, ctx, db, c, "", "Thread", p)

	n1 := insertPost016ResearchNote(t, ctx, db, c, "", thread, "v1", p)
	n2 := insertPost016ResearchNote(t, ctx, db, c, "", thread, "v2", p)
	n3 := insertPost016ResearchNote(t, ctx, db, c, "", thread, "v3", p)

	_, err = db.Pool.Exec(ctx, `INSERT INTO research_note_relation (note_id, related_note_id, relation_type) VALUES ($1, $2, 'supersedes')`, n2, n1)
	require.NoError(t, err)
	_, err = db.Pool.Exec(ctx, `INSERT INTO research_note_relation (note_id, related_note_id, relation_type) VALUES ($1, $2, 'supersedes')`, n3, n2)
	require.NoError(t, err)

	rows, err := db.Pool.Query(ctx, `SELECT id FROM v_current_research_note WHERE id IN ($1, $2, $3)`, n1, n2, n3)
	require.NoError(t, err)
	defer rows.Close()

	var current []string
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		current = append(current, id)
	}
	require.NoError(t, rows.Err())

	assert.Equal(t, []string{n3}, current, "a 3-long supersedes chain must leave only the newest note (n3) current")
}

// TestMigration016_UpDown_AppliesCleanly proves the migration's structural
// shape on both sides: up creates research_thread and research_note_relation
// and adds research_note.thread_id, without dropping or NOT NULL-ing
// research_note.idea_id (FR2 Stage 1 is additive-plus-backfill only, NFR3);
// down reverses cleanly with no CASCADE, matching migration 013's precedent.
func TestMigration016_UpDown_AppliesCleanly(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)

	require.NoError(t, runner.Migrate(15), "apply migrations 1-15 (the full chain up to, but not including, this task's migration)")
	assert.False(t, tableExists(t, ctx, db, "research_thread"), "before migration 016, research_thread must not exist")
	assert.False(t, tableExists(t, ctx, db, "research_note_relation"), "before migration 016, research_note_relation must not exist")
	preCols := viewColumns(t, ctx, db, "research_note")
	require.NotContains(t, preCols, "thread_id", "before migration 016, research_note must not carry thread_id yet")
	require.Contains(t, preCols, "idea_id")

	require.NoError(t, runner.Migrate(16), "apply migration 016's up -- must not fail")
	assert.True(t, tableExists(t, ctx, db, "research_thread"), "migration 016's up must create research_thread")
	assert.True(t, tableExists(t, ctx, db, "research_note_relation"), "migration 016's up must create research_note_relation")
	upCols := viewColumns(t, ctx, db, "research_note")
	assert.Contains(t, upCols, "thread_id", "migration 016's up must add research_note.thread_id")
	assert.Contains(t, upCols, "idea_id", "research_note.idea_id must be untouched -- FR2 Stage 1 is additive-plus-backfill only, no DROP/SET NOT NULL (NFR3)")

	viewCols := viewColumns(t, ctx, db, "v_current_research_note")
	assert.Contains(t, viewCols, "thread_id")
	assert.Contains(t, viewCols, "idea_id", "v_current_research_note must still expose idea_id -- Stage 1 never retargets a reader off it (NFR3)")

	require.NoError(t, runner.Migrate(15), "apply migration 016's down -- must not fail")
	assert.False(t, tableExists(t, ctx, db, "research_thread"), "migration 016's down must drop research_thread")
	assert.False(t, tableExists(t, ctx, db, "research_note_relation"), "migration 016's down must drop research_note_relation")
	downCols := viewColumns(t, ctx, db, "research_note")
	assert.NotContains(t, downCols, "thread_id", "migration 016's down must drop research_note.thread_id cleanly")

	_, err = db.Pool.Exec(ctx, `SELECT count(*) FROM v_current_research_note`)
	assert.Error(t, err, "migration 016's down must drop v_current_research_note")
}

// ── migration 018 (research_note.thread_id NOT NULL + idea_id drop; FR2
// Stage 3, NFR4; root plan #1934, this task #1947) ─────────────────────────

// columnNullable reports whether table.column is nullable
// (information_schema.columns.is_nullable = 'YES') -- used to assert
// migration 018's SET NOT NULL / down's DROP NOT NULL took effect, which
// viewColumns' column-NAME-only check above cannot distinguish.
func columnNullable(t *testing.T, ctx context.Context, db *dbtest.Postgres, table, column string) bool {
	t.Helper()
	var nullable string
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT is_nullable FROM information_schema.columns WHERE table_name = $1 AND column_name = $2
	`, table, column).Scan(&nullable))
	return nullable == "YES"
}

// TestMigration018_UpDown_AppliesCleanly proves the migration's structural
// shape on both sides against data seeded through the full Stage 1 (016)
// path: up sets research_note.thread_id NOT NULL and drops
// research_note.idea_id entirely (FR2 Stage 3, NFR4), recreating
// v_current_research_note without the dropped column; down reverses
// cleanly with no CASCADE, matching migration 013's precedent, restoring
// idea_id (backfilled, not merely an empty nullable column) and
// thread_id's nullability.
func TestMigration018_UpDown_AppliesCleanly(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)

	require.NoError(t, runner.Migrate(17), "apply migrations 1-17 (the full chain up to, but not including, this task's migration)")
	preCols := viewColumns(t, ctx, db, "research_note")
	require.Contains(t, preCols, "idea_id", "before migration 018, research_note must still carry idea_id")
	require.Contains(t, preCols, "thread_id")
	require.True(t, columnNullable(t, ctx, db, "research_note", "thread_id"), "before migration 018, thread_id must still be nullable")

	// Seed a research_note row through the Stage 1 (016) shape -- a real
	// thread, idea_id and thread_id both populated and agreeing -- so
	// migration 018's SET NOT NULL runs against actual data, not an empty
	// table.
	p := insertPersonRow(t, ctx, db, "sub-018-updown", "updown@example.com", "UpDown Person")
	c := insertChannelRow(t, ctx, db, "yt-018-updown", "UpDown Channel")
	idea := insertIdeaRow(t, ctx, db, c, "UpDown Idea", p)
	thread := insertResearchThread(t, ctx, db, c, idea, "UpDown Thread", p)
	noteID := insertPost016ResearchNote(t, ctx, db, c, idea, thread, "seeded before migration 018", p)

	require.NoError(t, runner.Migrate(18), "apply migration 018's up -- must not fail against real Stage 1/2 data")
	upCols := viewColumns(t, ctx, db, "research_note")
	assert.NotContains(t, upCols, "idea_id", "migration 018's up must drop research_note.idea_id")
	assert.Contains(t, upCols, "thread_id")
	assert.False(t, columnNullable(t, ctx, db, "research_note", "thread_id"), "migration 018's up must make thread_id NOT NULL (NFR4)")

	viewCols := viewColumns(t, ctx, db, "v_current_research_note")
	assert.NotContains(t, viewCols, "idea_id", "v_current_research_note must be recreated without idea_id")
	assert.Contains(t, viewCols, "thread_id")

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM v_current_research_note WHERE id = $1`, noteID).Scan(&count))
	assert.Equal(t, 1, count, "the recreated view must still return the seeded note as current")

	require.NoError(t, runner.Migrate(17), "apply migration 018's down -- must not fail")
	downCols := viewColumns(t, ctx, db, "research_note")
	assert.Contains(t, downCols, "idea_id", "migration 018's down must restore research_note.idea_id")
	assert.True(t, columnNullable(t, ctx, db, "research_note", "thread_id"), "migration 018's down must restore thread_id's nullability")

	var restoredIdeaID string
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT idea_id FROM research_note WHERE id = $1`, noteID).Scan(&restoredIdeaID))
	assert.Equal(t, idea, restoredIdeaID, "migration 018's down must backfill idea_id from the note's thread, not merely add an empty column")

	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM v_current_research_note WHERE id = $1`, noteID).Scan(&count))
	assert.Equal(t, 1, count, "the down-restored view must still return the seeded note as current")
}

// TestMigration018_ThreadIDNotNull_RejectsNullInsert is this task's named
// red/green regression test (NFR4): the exact same INSERT is attempted
// once against the pre-018 schema, where it must succeed (thread_id is
// still nullable), and again post-018, where it must fail -- that
// inversion is the evidence a DB-level guard now exists, not merely that
// application code happens to always supply a thread_id.
func TestMigration018_ThreadIDNotNull_RejectsNullInsert(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Migrate(17), "apply migrations 1-17")

	p := insertPersonRow(t, ctx, db, "sub-018-notnull", "notnull@example.com", "NotNull Person")
	c := insertChannelRow(t, ctx, db, "yt-018-notnull", "NotNull Channel")

	var preNoteID string
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO research_note (channel_id, thread_id, text, author_person_id) VALUES ($1, NULL, $2, $3) RETURNING id
	`, c, "pre-018: NULL thread_id", p).Scan(&preNoteID))

	// Clean up the GREEN row before migrating on -- migration 018's SET
	// NOT NULL must fail loudly against a genuinely orphaned row (that is
	// a real safety property, not this test's concern); this test is
	// about the column-level guard's presence, not about seeding an
	// invalid database and expecting the migration to succeed anyway.
	_, err = db.Pool.Exec(ctx, `DELETE FROM research_note WHERE id = $1`, preNoteID)
	require.NoError(t, err)

	require.NoError(t, runner.Migrate(18), "apply migration 018's up")

	_, err = db.Pool.Exec(ctx, `
		INSERT INTO research_note (channel_id, thread_id, text, author_person_id) VALUES ($1, NULL, $2, $3)
	`, c, "post-018: NULL thread_id", p)
	assert.Error(t, err, "RED (post-018): the identical insert must now be rejected -- thread_id is NOT NULL (NFR4)")
}

// TestMigration018_CurrentResearchNoteView_StillPartitionsCorrectly reruns
// migration 016's TestMigration016_CurrentResearchNoteView_
// RelationTypesDetermineInclusion scenario against the FULL migration
// chain (head, through 018) instead of stopping at 016, seeding rows with
// insertPost018ResearchNote (no idea_id column to insert into any more) --
// proving the DROP + CREATE rewrite (removing idea_id from the SELECT
// list) preserved the exact same current/retired partition logic (FR7),
// unchanged except for the dropped column.
func TestMigration018_CurrentResearchNoteView_StillPartitionsCorrectly(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "apply the full chain, including migration 018")

	p := insertPersonRow(t, ctx, db, "sub-018-view", "view018@example.com", "View Person")
	c := insertChannelRow(t, ctx, db, "yt-018-view", "View Channel")
	thread := insertResearchThread(t, ctx, db, c, "", "Thread", p)

	relator := insertPost018ResearchNote(t, ctx, db, c, thread, "relator", p)
	superseded := insertPost018ResearchNote(t, ctx, db, c, thread, "superseded", p)
	excluded := insertPost018ResearchNote(t, ctx, db, c, thread, "excluded", p)
	caveated := insertPost018ResearchNote(t, ctx, db, c, thread, "caveated", p)
	followedUp := insertPost018ResearchNote(t, ctx, db, c, thread, "followed up", p)
	summarized := insertPost018ResearchNote(t, ctx, db, c, thread, "summarized", p)

	for _, rel := range []struct{ target, relType string }{
		{superseded, "supersedes"},
		{excluded, "excludes"},
		{caveated, "caveats"},
		{followedUp, "follows_up"},
		{summarized, "summarizes"},
	} {
		_, err := db.Pool.Exec(ctx, `
			INSERT INTO research_note_relation (note_id, related_note_id, relation_type) VALUES ($1, $2, $3)
		`, relator, rel.target, rel.relType)
		require.NoError(t, err)
	}

	isCurrent := func(id string) bool {
		var count int
		require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM v_current_research_note WHERE id = $1`, id).Scan(&count))
		return count == 1
	}

	assert.True(t, isCurrent(relator), "a note that is not the target of any relation must remain current")
	assert.False(t, isCurrent(superseded), "a note targeted by a 'supersedes' relation must be excluded (FR7)")
	assert.False(t, isCurrent(excluded), "a note targeted by an 'excludes' relation must be excluded (FR7)")
	assert.True(t, isCurrent(caveated), "a note targeted by a 'caveats' relation must remain current -- only supersedes/excludes exclude (FR7)")
	assert.True(t, isCurrent(followedUp), "a note targeted by a 'follows_up' relation must remain current (FR7)")
	assert.True(t, isCurrent(summarized), "a note targeted by a 'summarizes' relation must remain current (FR7)")
}

// TestMigration018_Down_RestoresIdeaIdForPreStage2Read proves the down
// migration's own contract (see 018.down.sql's header): after up then
// down, a pre-Stage-2 read of research_note.idea_id (a bare column SELECT,
// exactly what an old, un-migrated binary would issue) returns the
// correct Idea for every row -- not merely an empty/NULL column -- for
// both a note whose thread has an Idea and one whose thread does not
// (FR9's nil case must round-trip as NULL, never a stray zero UUID).
func TestMigration018_Down_RestoresIdeaIdForPreStage2Read(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "apply the full chain, including migration 018")

	p := insertPersonRow(t, ctx, db, "sub-018-down", "down018@example.com", "Down Person")
	c := insertChannelRow(t, ctx, db, "yt-018-down", "Down Channel")
	idea := insertIdeaRow(t, ctx, db, c, "Down Idea", p)
	threadWithIdea := insertResearchThread(t, ctx, db, c, idea, "Thread With Idea", p)
	threadWithoutIdea := insertResearchThread(t, ctx, db, c, "", "Thread Without Idea", p)

	withIdeaNote := insertPost018ResearchNote(t, ctx, db, c, threadWithIdea, "note on a thread with an idea", p)
	withoutIdeaNote := insertPost018ResearchNote(t, ctx, db, c, threadWithoutIdea, "note on a thread with no idea", p)

	require.NoError(t, runner.Migrate(17), "apply migration 018's down")

	var gotIdeaID string
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT idea_id FROM research_note WHERE id = $1`, withIdeaNote).Scan(&gotIdeaID))
	assert.Equal(t, idea, gotIdeaID, "a pre-Stage-2 read of idea_id must return the correct Idea after the down migration")

	var gotNullIdeaID sql.NullString
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT idea_id FROM research_note WHERE id = $1`, withoutIdeaNote).Scan(&gotNullIdeaID))
	assert.False(t, gotNullIdeaID.Valid, "a note on an idea-less thread must restore to idea_id IS NULL, not a stray zero value")
}
