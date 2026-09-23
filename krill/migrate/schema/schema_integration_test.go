//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. It exists to prove migration 001 (issue #2487's `scope` table, LB1)
// and migration 002 (issue #2488's spec entity model, LB2/LB3) actually
// apply against real Postgres, reverse cleanly, and are re-runnable through
// //libs/go/migrate, plus the specific column-shape/constraint contracts
// each issue's Testing section calls out. See
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
	"sort"
	"testing"

	"github.com/google/uuid"
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

// nullableColumn returns column's data_type and is_nullable ("YES"/"NO") from
// information_schema -- shared by every *_SchemaContract test below.
func nullableColumn(t *testing.T, ctx context.Context, db *dbtest.Postgres, table, column string) (dataType, nullable string) {
	t.Helper()
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT data_type, is_nullable FROM information_schema.columns
		WHERE table_name = $1 AND column_name = $2
	`, table, column).Scan(&dataType, &nullable))
	return dataType, nullable
}

// hasPartialUniqueIndexOnCurrentID reports whether table carries a UNIQUE
// index on (id) partial-filtered to `valid_to IS NULL` -- migration 002's
// LB3 boundary contract ("CREATE UNIQUE INDEX ... ON <table>(id) WHERE
// valid_to IS NULL") every spec table must carry.
func hasPartialUniqueIndexOnCurrentID(t *testing.T, ctx context.Context, db *dbtest.Postgres, table string) bool {
	t.Helper()
	var exists bool
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_indexes
			WHERE schemaname = 'public' AND tablename = $1
			  AND indexdef ILIKE '%UNIQUE%'
			  AND indexdef ILIKE '%(id)%'
			  AND indexdef ILIKE '%WHERE%valid_to IS NULL%'
		)
	`, table).Scan(&exists))
	return exists
}

// hasForeignKeyTo reports whether table carries a DB-level FOREIGN KEY
// constraint (any column) referencing referencedTable -- used to confirm
// `scope_id REFERENCES scope(id)` is a real, DB-enforced FK (LB1), unlike a
// parent-entity column (LB2's deliberately-not-REFERENCES design, see
// migration 002.up.sql's comment).
func hasForeignKeyTo(t *testing.T, ctx context.Context, db *dbtest.Postgres, table, referencedTable string) bool {
	t.Helper()
	var exists bool
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_constraint c
			JOIN pg_class t ON t.oid = c.conrelid
			JOIN pg_class rt ON rt.oid = c.confrelid
			WHERE t.relname = $1 AND rt.relname = $2 AND c.contype = 'f'
		)
	`, table, referencedTable).Scan(&exists))
	return exists
}

// columnNames returns every column name for table from information_schema.
func columnNames(t *testing.T, ctx context.Context, db *dbtest.Postgres, table string) []string {
	t.Helper()
	rows, err := db.Pool.Query(ctx, `
		SELECT column_name FROM information_schema.columns WHERE table_name = $1
	`, table)
	require.NoError(t, err)
	defer rows.Close()

	var cols []string
	for rows.Next() {
		var col string
		require.NoError(t, rows.Scan(&col))
		cols = append(cols, col)
	}
	require.NoError(t, rows.Err())
	return cols
}

// specTables is every table migration 002 (issue #2488) creates -- the spec
// axis's entity model. Shared by the 002 tests below.
var specTables = []string{"product", "feature_set", "feature", "requirement", "load_bearing_decision", "persona", "non_goal"}

// TestMigrations_UpDownUp_LeavesCleanDatabaseAndIsRerunnable proves the
// whole migration set's lifecycle through the latest migration currently
// embedded (015_work_axis, krill M4 issue #2719): Up() creates every
// table including `krill_session`, `milestone_ref`, `entity_milestone`,
// `milestone_deferral`, `pointer_artifact`,
// `mcp_credential`/`mcp_oauth_client`/`mcp_auth_code`, `ui_sessions`,
// `design_session`/`revision_event`, `import_completion`,
// `milestone_status_event`, `delivery_shipment`, and the six work-axis
// tables (`task`, `task_dependency`, `task_claim`, `task_lease_event`,
// `task_attempt`, `task_note`), Down() drops all of them (a clean
// database), and Up() again succeeds a second time from that clean state
// -- the migration set is re-runnable through //libs/go/migrate, not a
// one-shot script. The hardcoded latest-version assertion below must be
// bumped whenever a new migration lands (it was 1 for 001_scope alone,
// issue #2487; it is 15 now that 002_spec_entities, 003_session,
// 004_milestone_assoc, 005_pointer_artifact, 006_mcpauth_credential,
// 007_ui_sessions, 008_design_session, 009_import_completion,
// 010_milestone_authoring, 011_milepebble, 012_milestone_status,
// 013_delivery_shipment, 014_backlog_bucket, and 015_work_axis have all
// landed -- 008/009 rather than 006/007 because 006/007 were already
// claimed by the mcpauth auth-flow gap work by the time this plan's
// migrations merged; see ARCHITECTURE.md's "Migration numbering (M2)"
// table). `milestone_ref` itself is not a new table (004 created it) so it
// is not listed again below -- only `milestone_deferral` is new since
// migration 010; migration 011 (issue #2684) only widens `milestone_ref`
// (a new `parent_milestone_id` column, no new table) -- covered by
// TestMigration011_SchemaContract, not here; migration 012 (issue #2685)
// is a brand-new table, `milestone_status_event` -- covered in detail by
// TestMigration012_SchemaContract, not here; migration 013 (issue #2686)
// is another brand-new table, `delivery_shipment` -- covered in detail by
// TestMigration013_SchemaContract, not here; migration 014 (issue #2687)
// only widens `milestone_ref` again (the backlog bucket is a
// `kind='backlog'` row on the same table, no new table) -- covered by
// TestMigration014_SchemaContract, not here; migration 015 (issue #2719)
// creates the whole work axis in one migration, six brand-new tables --
// covered in detail by TestMigration015_UpDownRoundTrip, not here.
func TestMigrations_UpDownUp_LeavesCleanDatabaseAndIsRerunnable(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)

	latest, err := runner.LatestVersion()
	require.NoError(t, err)
	require.Equal(t, uint(17), latest, "expected the latest migration source version to be 17 (001_scope, 002_spec_entities, 003_session, 004_milestone_assoc, 005_pointer_artifact, 006_mcpauth_credential, 007_ui_sessions, 008_design_session, 009_import_completion, 010_milestone_authoring, 011_milepebble, 012_milestone_status, 013_delivery_shipment, 014_backlog_bucket, 015_work_axis, 016_escalation_axis, 017_display_numbers) -- update this test if a later migration has since landed")

	// -- Up: scope, krill_session, the milestone tables, pointer_artifact,
	// the mcpauth tables, ui_sessions, design_session/revision_event,
	// milestone_status_event, delivery_shipment, and the work-axis tables
	// must exist, version must land clean at the latest --
	require.NoError(t, runner.Up(), "apply migrations 001-017")

	version, dirty, err := runner.Version()
	require.NoError(t, err)
	assert.False(t, dirty)
	assert.Equal(t, uint(17), version)

	assert.True(t, tableExists(t, ctx, db, "scope"), "expected table \"scope\" to exist after Up()")
	assert.True(t, tableExists(t, ctx, db, "krill_session"), "expected table \"krill_session\" to exist after Up() (003_session, issue #2489)")
	assert.True(t, tableExists(t, ctx, db, "milestone_ref"), "expected table \"milestone_ref\" to exist after Up() (004_milestone_assoc, issue #2492)")
	assert.True(t, tableExists(t, ctx, db, "entity_milestone"), "expected table \"entity_milestone\" to exist after Up() (004_milestone_assoc, issue #2492)")
	assert.True(t, tableExists(t, ctx, db, "milestone_deferral"), "expected table \"milestone_deferral\" to exist after Up() (010_milestone_authoring, issue #2683)")
	assert.True(t, tableExists(t, ctx, db, "pointer_artifact"), "expected table \"pointer_artifact\" to exist after Up() (005_pointer_artifact, issue #2496)")
	assert.True(t, tableExists(t, ctx, db, "mcp_credential"), "expected table \"mcp_credential\" to exist after Up() (006_mcpauth_credential)")
	assert.True(t, tableExists(t, ctx, db, "mcp_oauth_client"), "expected table \"mcp_oauth_client\" to exist after Up() (006_mcpauth_credential)")
	assert.True(t, tableExists(t, ctx, db, "mcp_auth_code"), "expected table \"mcp_auth_code\" to exist after Up() (006_mcpauth_credential)")
	assert.True(t, tableExists(t, ctx, db, "ui_sessions"), "expected table \"ui_sessions\" to exist after Up() (007_ui_sessions)")
	assert.True(t, tableExists(t, ctx, db, "design_session"), "expected table \"design_session\" to exist after Up() (008_design_session, issue #2542)")
	assert.True(t, tableExists(t, ctx, db, "revision_event"), "expected table \"revision_event\" to exist after Up() (008_design_session, issue #2542)")
	assert.True(t, tableExists(t, ctx, db, "import_completion"), "expected table \"import_completion\" to exist after Up() (009_import_completion, issue #2548)")
	assert.True(t, tableExists(t, ctx, db, "milestone_status_event"), "expected table \"milestone_status_event\" to exist after Up() (012_milestone_status, issue #2685)")
	assert.True(t, tableExists(t, ctx, db, "delivery_shipment"), "expected table \"delivery_shipment\" to exist after Up() (013_delivery_shipment, issue #2686)")
	assert.True(t, tableExists(t, ctx, db, "task"), "expected table \"task\" to exist after Up() (015_work_axis, issue #2719)")
	assert.True(t, tableExists(t, ctx, db, "task_dependency"), "expected table \"task_dependency\" to exist after Up() (015_work_axis, issue #2719)")
	assert.True(t, tableExists(t, ctx, db, "task_claim"), "expected table \"task_claim\" to exist after Up() (015_work_axis, issue #2719)")
	assert.True(t, tableExists(t, ctx, db, "task_lease_event"), "expected table \"task_lease_event\" to exist after Up() (015_work_axis, issue #2719)")
	assert.True(t, tableExists(t, ctx, db, "task_attempt"), "expected table \"task_attempt\" to exist after Up() (015_work_axis, issue #2719)")
	assert.True(t, tableExists(t, ctx, db, "task_note"), "expected table \"task_note\" to exist after Up() (015_work_axis, issue #2719)")

	// -- Down: every table must be gone -------------------------------------
	require.NoError(t, runner.Down(), "roll back every migration")

	assert.False(t, tableExists(t, ctx, db, "scope"), "expected table \"scope\" to be dropped after Down() -- a clean database")
	assert.False(t, tableExists(t, ctx, db, "krill_session"), "expected table \"krill_session\" to be dropped after Down() -- a clean database")
	assert.False(t, tableExists(t, ctx, db, "milestone_ref"), "expected table \"milestone_ref\" to be dropped after Down() -- a clean database")
	assert.False(t, tableExists(t, ctx, db, "entity_milestone"), "expected table \"entity_milestone\" to be dropped after Down() -- a clean database")
	assert.False(t, tableExists(t, ctx, db, "milestone_deferral"), "expected table \"milestone_deferral\" to be dropped after Down() -- a clean database, and 010's own Down must leave 004's tables intact (checked separately by TestMigration010_DownLeavesMilestoneAssocTablesIntact)")
	assert.False(t, tableExists(t, ctx, db, "pointer_artifact"), "expected table \"pointer_artifact\" to be dropped after Down() -- a clean database")
	assert.False(t, tableExists(t, ctx, db, "mcp_credential"), "expected table \"mcp_credential\" to be dropped after Down() -- a clean database")
	assert.False(t, tableExists(t, ctx, db, "mcp_oauth_client"), "expected table \"mcp_oauth_client\" to be dropped after Down() -- a clean database")
	assert.False(t, tableExists(t, ctx, db, "mcp_auth_code"), "expected table \"mcp_auth_code\" to be dropped after Down() -- a clean database")
	assert.False(t, tableExists(t, ctx, db, "ui_sessions"), "expected table \"ui_sessions\" to be dropped after Down() -- a clean database")
	assert.False(t, tableExists(t, ctx, db, "design_session"), "expected table \"design_session\" to be dropped after Down() -- a clean database")
	assert.False(t, tableExists(t, ctx, db, "revision_event"), "expected table \"revision_event\" to be dropped after Down() -- a clean database")
	assert.False(t, tableExists(t, ctx, db, "import_completion"), "expected table \"import_completion\" to be dropped after Down() -- a clean database")
	assert.False(t, tableExists(t, ctx, db, "milestone_status_event"), "expected table \"milestone_status_event\" to be dropped after Down() -- a clean database (012_milestone_status, issue #2685, Testing item 9)")
	assert.False(t, tableExists(t, ctx, db, "delivery_shipment"), "expected table \"delivery_shipment\" to be dropped after Down() -- a clean database (013_delivery_shipment, issue #2686, Testing item 8)")
	assert.False(t, tableExists(t, ctx, db, "task"), "expected table \"task\" to be dropped after Down() -- a clean database (015_work_axis, issue #2719)")
	assert.False(t, tableExists(t, ctx, db, "task_dependency"), "expected table \"task_dependency\" to be dropped after Down() -- a clean database (015_work_axis, issue #2719)")
	assert.False(t, tableExists(t, ctx, db, "task_claim"), "expected table \"task_claim\" to be dropped after Down() -- a clean database (015_work_axis, issue #2719)")
	assert.False(t, tableExists(t, ctx, db, "task_lease_event"), "expected table \"task_lease_event\" to be dropped after Down() -- a clean database (015_work_axis, issue #2719)")
	assert.False(t, tableExists(t, ctx, db, "task_attempt"), "expected table \"task_attempt\" to be dropped after Down() -- a clean database (015_work_axis, issue #2719)")
	assert.False(t, tableExists(t, ctx, db, "task_note"), "expected table \"task_note\" to be dropped after Down() -- a clean database (015_work_axis, issue #2719)")

	// -- Up again: re-runnable from the clean state --------------------------
	require.NoError(t, runner.Up(), "re-apply every migration after Down() -- must be re-runnable")

	version, dirty, err = runner.Version()
	require.NoError(t, err)
	assert.False(t, dirty)
	assert.Equal(t, uint(17), version)

	assert.True(t, tableExists(t, ctx, db, "scope"), "expected table \"scope\" to exist again after the second Up()")
	assert.True(t, tableExists(t, ctx, db, "krill_session"), "expected table \"krill_session\" to exist again after the second Up()")
	assert.True(t, tableExists(t, ctx, db, "milestone_ref"), "expected table \"milestone_ref\" to exist again after the second Up()")
	assert.True(t, tableExists(t, ctx, db, "entity_milestone"), "expected table \"entity_milestone\" to exist again after the second Up()")
	assert.True(t, tableExists(t, ctx, db, "milestone_deferral"), "expected table \"milestone_deferral\" to exist again after the second Up()")
	assert.True(t, tableExists(t, ctx, db, "pointer_artifact"), "expected table \"pointer_artifact\" to exist again after the second Up()")
	assert.True(t, tableExists(t, ctx, db, "mcp_credential"), "expected table \"mcp_credential\" to exist again after the second Up()")
	assert.True(t, tableExists(t, ctx, db, "mcp_oauth_client"), "expected table \"mcp_oauth_client\" to exist again after the second Up()")
	assert.True(t, tableExists(t, ctx, db, "mcp_auth_code"), "expected table \"mcp_auth_code\" to exist again after the second Up()")
	assert.True(t, tableExists(t, ctx, db, "ui_sessions"), "expected table \"ui_sessions\" to exist again after the second Up()")
	assert.True(t, tableExists(t, ctx, db, "design_session"), "expected table \"design_session\" to exist again after the second Up()")
	assert.True(t, tableExists(t, ctx, db, "revision_event"), "expected table \"revision_event\" to exist again after the second Up()")
	assert.True(t, tableExists(t, ctx, db, "import_completion"), "expected table \"import_completion\" to exist again after the second Up()")
	assert.True(t, tableExists(t, ctx, db, "milestone_status_event"), "expected table \"milestone_status_event\" to exist again after the second Up()")
	assert.True(t, tableExists(t, ctx, db, "delivery_shipment"), "expected table \"delivery_shipment\" to exist again after the second Up()")
	assert.True(t, tableExists(t, ctx, db, "task"), "expected table \"task\" to exist again after the second Up()")
	assert.True(t, tableExists(t, ctx, db, "task_dependency"), "expected table \"task_dependency\" to exist again after the second Up()")
	assert.True(t, tableExists(t, ctx, db, "task_claim"), "expected table \"task_claim\" to exist again after the second Up()")
	assert.True(t, tableExists(t, ctx, db, "task_lease_event"), "expected table \"task_lease_event\" to exist again after the second Up()")
	assert.True(t, tableExists(t, ctx, db, "task_attempt"), "expected table \"task_attempt\" to exist again after the second Up()")
	assert.True(t, tableExists(t, ctx, db, "task_note"), "expected table \"task_note\" to exist again after the second Up()")
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

// TestMigration002_UpDownUp_LeavesCleanDatabaseAndIsRerunnable proves
// migration 002's (issue #2488) spec tables survive a full up/down/up
// roundtrip cleanly, on top of whatever else has landed since -- runner.Up()
// always migrates to the latest embedded version (not pinned to 2), so this
// checks LatestVersion() dynamically rather than hardcoding 2 the way
// TestMigrations_UpDownUp_LeavesCleanDatabaseAndIsRerunnable above must
// (that one is the "combined, always-latest" contract and is expected to be
// edited each time a migration lands; this one only cares that 002's own
// tables round-trip correctly regardless of what else is now latest).
func TestMigration002_UpDownUp_LeavesCleanDatabaseAndIsRerunnable(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)

	latest, err := runner.LatestVersion()
	require.NoError(t, err)

	// -- Up: every spec table plus scope must exist --------------------------
	require.NoError(t, runner.Up(), "apply every migration through the latest")

	version, dirty, err := runner.Version()
	require.NoError(t, err)
	assert.False(t, dirty)
	assert.Equal(t, latest, version)

	assert.True(t, tableExists(t, ctx, db, "scope"), "expected table \"scope\" to exist after Up()")
	for _, table := range specTables {
		assert.True(t, tableExists(t, ctx, db, table), "expected table %q to exist after Up() (migration 002, issue #2488)", table)
	}

	// -- Down: everything must be gone, a clean database ---------------------
	require.NoError(t, runner.Down(), "roll back every migration")

	assert.False(t, tableExists(t, ctx, db, "scope"), "expected table \"scope\" to be dropped after Down()")
	for _, table := range specTables {
		assert.False(t, tableExists(t, ctx, db, table), "expected table %q to be dropped after Down() -- a clean database", table)
	}

	// -- Up again: re-runnable from the clean state --------------------------
	require.NoError(t, runner.Up(), "re-apply every migration after Down() -- must be re-runnable")

	version, dirty, err = runner.Version()
	require.NoError(t, err)
	assert.False(t, dirty)
	assert.Equal(t, latest, version)

	for _, table := range specTables {
		assert.True(t, tableExists(t, ctx, db, table), "expected table %q to exist again after the second Up()", table)
	}
}

// TestMigration002_SchemaContract asserts the specific column/constraint
// shapes issue #2488 commits to for every spec table (LB1/LB2/LB3):
// valid_from NOT NULL, valid_to nullable, scope_id NOT NULL with a real
// DB-enforced FK to `scope`, a partial UNIQUE index on (id) WHERE valid_to
// IS NULL, and a plain (non-array) uuid parent column that is deliberately
// NOT a DB-enforced FK (see migration 002.up.sql's LB2 parentage note).
func TestMigration002_SchemaContract(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up())

	// parentColumn is the plain-uuid parent-id column each child table
	// carries (LB2 parentage) -- product has none, it is the chain's root.
	parentColumn := map[string]string{
		"feature_set":           "product_id",
		"feature":               "feature_set_id",
		"requirement":           "feature_id",
		"load_bearing_decision": "feature_set_id",
		"persona":               "product_id",
		"non_goal":              "product_id",
	}

	for _, table := range specTables {
		t.Run(table, func(t *testing.T) {
			_, nullable := nullableColumn(t, ctx, db, table, "valid_from")
			assert.Equal(t, "NO", nullable, "%s.valid_from must be NOT NULL", table)

			_, nullable = nullableColumn(t, ctx, db, table, "valid_to")
			assert.Equal(t, "YES", nullable, "%s.valid_to must be nullable (NULL = current)", table)

			_, nullable = nullableColumn(t, ctx, db, table, "id")
			assert.Equal(t, "NO", nullable, "%s.id (the LB2 immutable surrogate id) must be NOT NULL", table)

			dataType, nullable := nullableColumn(t, ctx, db, table, "scope_id")
			assert.Equal(t, "NO", nullable, "%s.scope_id must be NOT NULL (LB1)", table)
			assert.Equal(t, "uuid", dataType, "%s.scope_id must be a plain uuid column", table)
			assert.True(t, hasForeignKeyTo(t, ctx, db, table, "scope"), "%s.scope_id must carry a real DB-enforced FK to scope(id) (LB1)", table)

			assert.True(t, hasPartialUniqueIndexOnCurrentID(t, ctx, db, table),
				"%s must carry a UNIQUE index on (id) WHERE valid_to IS NULL (LB3's current-row contract)", table)

			if parent, ok := parentColumn[table]; ok {
				dataType, nullable := nullableColumn(t, ctx, db, table, parent)
				assert.Equal(t, "NO", nullable, "%s.%s (parent id) must be NOT NULL", table, parent)
				assert.Equal(t, "uuid", dataType, "%s.%s must be a plain uuid column, never an array (LB2 parentage)", table, parent)
			}
		})
	}

	// LB2 parentage: none of the child tables' parent columns is a
	// DB-enforced FK (migration 002.up.sql explains why a REFERENCES clause
	// cannot express the invariant -- `id` is not unique table-wide). This
	// is checked once per (table, parent-table) pair rather than inside the
	// loop above (whose placeholder assertion is a no-op against a
	// nonexistent "" table) so a future migration accidentally adding a
	// REFERENCES clause on a parent column is caught here explicitly.
	noParentFK := []struct{ table, parentTable string }{
		{"feature_set", "product"},
		{"feature", "feature_set"},
		{"requirement", "feature"},
		{"load_bearing_decision", "feature_set"},
		{"persona", "product"},
		{"non_goal", "product"},
	}
	for _, c := range noParentFK {
		assert.False(t, hasForeignKeyTo(t, ctx, db, c.table, c.parentTable),
			"%s must NOT carry a DB-enforced FK to %s -- LB2 parentage is store-layer-enforced only (migration 002.up.sql's comment)", c.table, c.parentTable)
	}

	// requirement.kind and non_goal.kind carry a CHECK constraint, not a
	// stored display number (LB2) -- proven directly by exercising the
	// constraint: an invalid kind must be rejected.
	var scopeID string
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ('kind-check/repo', 'main') RETURNING id
	`).Scan(&scopeID))
	var productID string
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO product (scope_id, name, vision) VALUES ($1, 'P', 'V') RETURNING id
	`, scopeID).Scan(&productID))
	var featureSetID string
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO feature_set (scope_id, product_id, name) VALUES ($1, $2, 'FS') RETURNING id
	`, scopeID, productID).Scan(&featureSetID))
	var featureID string
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO feature (scope_id, feature_set_id, name, display_number) VALUES ($1, $2, 'F', 1) RETURNING id
	`, scopeID, featureSetID).Scan(&featureID))

	_, err = db.Pool.Exec(ctx, `
		INSERT INTO requirement (scope_id, feature_id, kind, name) VALUES ($1, $2, 'BOGUS', 'R')
	`, scopeID, featureID)
	assert.Error(t, err, "requirement.kind must reject a value other than FR/NFR (CHECK constraint)")

	_, err = db.Pool.Exec(ctx, `
		INSERT INTO non_goal (scope_id, product_id, kind, name) VALUES ($1, $2, 'BOGUS', 'N')
	`, scopeID, productID)
	assert.Error(t, err, "non_goal.kind must reject a value other than permanent/deferred (CHECK constraint)")
}

// TestMigration002_NoDisplayNumberColumnsOrJoinTables is the schema guard
// test issue #2488's Testing section calls for: no spec table stores a
// display-number column (LB2's trap -- "C4", "FR7", "LB3" are rendered, never
// persisted), and no fourth parallel table or join table was introduced for
// parentage (LB2 -- "never an array, never a join table"). Asserted
// structurally: the public schema's table set is exactly what every landed
// migration creates (runner.Up() always migrates to latest -- krill_session
// from 003_session, issue #2489, is included below for that reason) plus
// golang-migrate's own bookkeeping table, and no spec table has a column
// matching a display-number-shaped name.
func TestMigration002_NoDisplayNumberColumnsOrJoinTables(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up())

	// No fourth parallel table (e.g. a dedicated "capability" table) and no
	// join/bridge table (e.g. "product_feature_set") -- the public schema's
	// table set is exactly scope + the seven spec tables + golang-migrate's
	// own version-tracking table.
	rows, err := db.Pool.Query(ctx, `
		SELECT table_name FROM information_schema.tables
		WHERE table_schema = 'public' ORDER BY table_name
	`)
	require.NoError(t, err)
	var tables []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		tables = append(tables, name)
	}
	require.NoError(t, rows.Err())
	rows.Close()

	expected := append([]string{
		"schema_migrations", "scope", "krill_session", "milestone_ref", "entity_milestone", "milestone_deferral", "pointer_artifact",
		"mcp_credential", "mcp_oauth_client", "mcp_auth_code", "ui_sessions", "design_session", "revision_event", "import_completion",
		"milestone_status_event", "delivery_shipment",
		"task", "task_dependency", "task_claim", "task_lease_event", "task_attempt", "task_note",
		"task_escalation_event", "task_intervention_event", "task_note_lifecycle_event",
	}, specTables...)
	sort.Strings(expected)
	assert.Equal(t, expected, tables, "the public schema must contain exactly scope + the seven spec tables + krill_session (003_session, issue #2489) + milestone_ref + entity_milestone (004_milestone_assoc, issue #2492) + pointer_artifact (005_pointer_artifact, issue #2496) + mcp_credential/mcp_oauth_client/mcp_auth_code (006_mcpauth_credential) + ui_sessions (007_ui_sessions) + design_session/revision_event (008_design_session, issue #2542) + import_completion (009_import_completion, issue #2548) + milestone_deferral (010_milestone_authoring, issue #2683) + milestone_status_event (012_milestone_status, issue #2685) + delivery_shipment (013_delivery_shipment, issue #2686) + task/task_dependency/task_claim/task_lease_event/task_attempt/task_note (015_work_axis, issue #2719) + task_escalation_event/task_intervention_event/task_note_lifecycle_event (016_escalation_axis, issue #2868) + golang-migrate's schema_migrations -- no fourth parallel table (e.g. \"capability\") and no join/bridge table for parentage (LB2)")

	// No display-number-shaped column on any spec table EXCEPT feature and
	// load_bearing_decision -- migration 017 (issue #2969) reversed LB2's
	// original "no such column exists" stance for exactly those two
	// tables (Cn/LBn citations were not stable across renders once entities
	// were appended out of order, reordered, or superseded). Every other
	// spec table -- crucially requirement, whose FRn/NFRn has no render
	// path yet -- still carries no display-number column at all.
	forbidden := []string{"display_number", "fr_number", "nfr_number", "lb_number", "c_number", "ordinal"}
	displayNumberReversedTables := map[string]bool{"feature": true, "load_bearing_decision": true}
	for _, table := range specTables {
		cols := columnNames(t, ctx, db, table)
		for _, col := range cols {
			for _, bad := range forbidden {
				if displayNumberReversedTables[table] && bad == "display_number" {
					continue
				}
				assert.NotEqual(t, bad, col, "%s must not have a stored display-number column %q (LB2's trap -- display numbers are rendered, never persisted)", table, bad)
			}
		}
	}
}

// TestMigration017_SchemaContract asserts 017_display_numbers' shape
// (issue #2969): feature and load_bearing_decision each gain a NOT NULL
// display_number column, and every current row was backfilled to the exact
// number krill/render's old numberByOrder would already have rendered it
// as -- this migration changes no existing citation, it only stops a
// future render from being able to change one.
func TestMigration017_SchemaContract(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up())

	for _, table := range []string{"feature", "load_bearing_decision"} {
		cols := columnNames(t, ctx, db, table)
		assert.Contains(t, cols, "display_number", "%s must carry a display_number column", table)

		var isNullable string
		require.NoError(t, db.Pool.QueryRow(ctx, `
			SELECT is_nullable FROM information_schema.columns
			WHERE table_name = $1 AND column_name = 'display_number'
		`, table).Scan(&isNullable))
		assert.Equal(t, "NO", isNullable, "%s.display_number must be NOT NULL", table)
	}
}

// TestMigration003_SchemaContract asserts 003_session's krill_session
// column shapes (issue #2489's Testing section): all six subject columns
// (acting_iss/acting_sub/acting_kind, on_behalf_of_iss/on_behalf_of_sub/
// on_behalf_of_kind, LB4) are NOT NULL, whagent_session_id is nullable
// (correlation only -- a human/OAuth2 caller has none), and scope_id is
// NOT NULL and FK-backed (LB1).
func TestMigration003_SchemaContract(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up())

	for _, col := range []string{
		"scope_id",
		"acting_iss", "acting_sub", "acting_kind",
		"on_behalf_of_iss", "on_behalf_of_sub", "on_behalf_of_kind",
	} {
		_, nullable := nullableColumn(t, ctx, db, "krill_session", col)
		assert.Equal(t, "NO", nullable, "krill_session.%s must be NOT NULL (LB1/LB4)", col)
	}

	_, nullable := nullableColumn(t, ctx, db, "krill_session", "whagent_session_id")
	assert.Equal(t, "YES", nullable, "krill_session.whagent_session_id must be nullable -- a human/OAuth2 caller has no whagent claim")

	_, nullable = nullableColumn(t, ctx, db, "krill_session", "created_at")
	assert.Equal(t, "NO", nullable, "krill_session.created_at must be NOT NULL")

	// scope_id must actually be FK-enforced against scope(id), not merely
	// NOT NULL -- an insert against a nonexistent scope must fail.
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO krill_session (
			scope_id, acting_iss, acting_sub, acting_kind,
			on_behalf_of_iss, on_behalf_of_sub, on_behalf_of_kind
		) VALUES (
			gen_random_uuid(), 'https://issuer.example.com', 'someone', 'human',
			'https://issuer.example.com', 'someone', 'human'
		)
	`)
	assert.Error(t, err, "krill_session.scope_id must be FK-enforced against scope(id)")
}

// TestMigration004_SchemaContract guards LB6's own stated trap (migration
// 004_milestone_assoc.up.sql's "LB6 -- an association, never a second
// parent" comment, issue #2492): `milestone_ref` must carry no status
// column -- that remains out of scope even after M3's authoring surface
// (010_milestone_authoring, issue #2683) added Kind/Outcome/FRBudget/
// Position and 011_milepebble (issue #2684) added ParentMilestoneID -- and
// no spec entity table (`feature`, `requirement`, `load_bearing_decision`)
// may have acquired a `milestone_id` or `milepebble_id` column (LB6's
// other direction, and FR3/FR4's own boundary call -- a milepebble is
// never a new parentage column on a spec entity). It also asserts the
// shape the importer and MilestoneStore depend on: scope_id NOT NULL on
// both new tables (LB1), the milestone_ref (scope_id, product_id, name)
// uniqueness that makes GetOrCreateRef idempotent, the entity_milestone
// (entity_id, milestone_id, relation) uniqueness (widened by migration 010
// to include relation) that makes AddAssociation idempotent, and
// milestone_id's real FK to milestone_ref(id).
func TestMigration004_SchemaContract(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up())

	// -- LB6's trap: no status column on milestone_ref, even after M3's
	// authoring columns and the milepebble parent column landed --
	milestoneRefCols := columnNames(t, ctx, db, "milestone_ref")
	for _, forbidden := range []string{"status", "milepebble_id", "state", "author", "authored_by", "authored_at"} {
		assert.NotContains(t, milestoneRefCols, forbidden,
			"milestone_ref must not carry a %q column -- status is a later issue's, not M1's/M3's, and a milepebble is never referenced by a milepebble_id column even on its own table (LB6's own note)", forbidden)
	}
	assert.ElementsMatch(t, []string{
		"id", "scope_id", "product_id", "name", "created_at",
		"kind", "outcome", "fr_budget", "position", "parent_milestone_id",
		"created_by_acting_iss", "created_by_acting_sub", "created_by_acting_kind",
		"created_by_on_behalf_of_iss", "created_by_on_behalf_of_sub", "created_by_on_behalf_of_kind",
	}, milestoneRefCols,
		"milestone_ref must be exactly the bare reference shape LB6 specifies (004_milestone_assoc) plus the authoring columns migration 010 (issue #2683) added plus migration 011's (issue #2684) parent_milestone_id -- no more, no less")

	// -- LB6's trap, other direction: no spec entity table may have grown a milestone_id column --
	for _, table := range []string{"feature", "requirement", "load_bearing_decision"} {
		assert.NotContains(t, columnNames(t, ctx, db, table), "milestone_id",
			"%s must never carry a milestone_id column -- the delivery axis is the entity_milestone association table, never a second parent column on a spec entity (LB6)", table)
	}

	nullableColumn := func(t *testing.T, ctx context.Context, db *dbtest.Postgres, table, column string) (dataType, nullable string) {
		t.Helper()
		require.NoError(t, db.Pool.QueryRow(ctx, `
			SELECT data_type, is_nullable FROM information_schema.columns
			WHERE table_name = $1 AND column_name = $2
		`, table, column).Scan(&dataType, &nullable))
		return dataType, nullable
	}

	for _, table := range []string{"milestone_ref", "entity_milestone"} {
		_, nullable := nullableColumn(t, ctx, db, table, "scope_id")
		assert.Equal(t, "NO", nullable, "%s.scope_id must be NOT NULL (LB1)", table)
	}

	// -- milestone_ref(scope_id, product_id, name) uniqueness: GetOrCreateRef's idempotency key --
	var productID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ('dup/dup-004', 'main') RETURNING id
	`).Scan(&productID)) // reuse productID var for scopeID first, reassigned below
	scopeID := productID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO product (scope_id, name, vision) VALUES ($1, 'p', 'v') RETURNING id
	`, scopeID).Scan(&productID))

	_, err = db.Pool.Exec(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name) VALUES ($1, $2, 'M1')
	`, scopeID, productID)
	require.NoError(t, err)
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name) VALUES ($1, $2, 'M1')
	`, scopeID, productID)
	assert.Error(t, err, "a second milestone_ref row for the same (scope_id, product_id, name) must violate the UNIQUE constraint GetOrCreateRef relies on")

	var milestoneID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT id FROM milestone_ref WHERE scope_id = $1 AND product_id = $2 AND name = 'M1'
	`, scopeID, productID).Scan(&milestoneID))

	var featureSetID, featureID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO feature_set (scope_id, product_id, name) VALUES ($1, $2, 'fs') RETURNING id
	`, scopeID, productID).Scan(&featureSetID))
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO feature (scope_id, feature_set_id, name, display_number) VALUES ($1, $2, 'f', 1) RETURNING id
	`, scopeID, featureSetID).Scan(&featureID))

	_, err = db.Pool.Exec(ctx, `
		INSERT INTO entity_milestone (scope_id, entity_id, milestone_id) VALUES ($1, $2, $3)
	`, scopeID, featureID, milestoneID)
	require.NoError(t, err)
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO entity_milestone (scope_id, entity_id, milestone_id) VALUES ($1, $2, $3)
	`, scopeID, featureID, milestoneID)
	assert.Error(t, err, "a second entity_milestone row for the same (entity_id, milestone_id) must violate the UNIQUE constraint AddAssociation relies on")

	// -- milestone_id is a real DB-enforced FK, unlike every parent link in migration 002 --
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO entity_milestone (scope_id, entity_id, milestone_id) VALUES ($1, $2, gen_random_uuid())
	`, scopeID, featureID)
	assert.Error(t, err, "entity_milestone.milestone_id must be FK-enforced against milestone_ref(id)")
}

// TestMigration005_SchemaContract asserts 005_pointer_artifact's own
// boundary calls (issue #2496, FR20, C9): scope_id NOT NULL with a real
// DB-enforced FK (LB1), a `kind` CHECK constraint rejecting anything but
// "github_issue", every LB4 subject column NOT NULL, a UNIQUE index on
// product_id (at most one pointer issue per Product), and -- C20's one
// recorded condition -- no branch-name, PR-number, commit-SHA, or
// conversation-URL-shaped column anywhere on the table.
func TestMigration005_SchemaContract(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up())

	// -- Not SCD2 (LB3): no valid_from/valid_to columns -----------------------
	cols := columnNames(t, ctx, db, "pointer_artifact")
	assert.NotContains(t, cols, "valid_from", "pointer_artifact must not be SCD2 (LB3) -- it is a plain fact table")
	assert.NotContains(t, cols, "valid_to", "pointer_artifact must not be SCD2 (LB3) -- it is a plain fact table")

	// -- C20: no branch-name-derived or PR/commit/conversation-shaped column --
	for _, forbidden := range []string{
		"branch", "branch_name", "pr_number", "pull_request_number",
		"commit_sha", "conversation_url", "conversation_id",
	} {
		assert.NotContains(t, cols, forbidden, "pointer_artifact must not carry a %q column -- C20: krill stores references only, never branch/PR/commit/conversation-derived state", forbidden)
	}

	// -- scope_id: NOT NULL, real DB-enforced FK (LB1) ------------------------
	dataType, nullable := nullableColumn(t, ctx, db, "pointer_artifact", "scope_id")
	assert.Equal(t, "NO", nullable, "pointer_artifact.scope_id must be NOT NULL (LB1)")
	assert.Equal(t, "uuid", dataType, "pointer_artifact.scope_id must be a plain uuid column")
	assert.True(t, hasForeignKeyTo(t, ctx, db, "pointer_artifact", "scope"), "pointer_artifact.scope_id must carry a real DB-enforced FK to scope(id) (LB1)")

	// -- product_id: NOT NULL, plain uuid, NOT a DB-enforced FK (LB2 parentage) --
	dataType, nullable = nullableColumn(t, ctx, db, "pointer_artifact", "product_id")
	assert.Equal(t, "NO", nullable, "pointer_artifact.product_id must be NOT NULL")
	assert.Equal(t, "uuid", dataType, "pointer_artifact.product_id must be a plain uuid column, never an array (LB2 parentage)")
	assert.False(t, hasForeignKeyTo(t, ctx, db, "pointer_artifact", "product"), "pointer_artifact.product_id must NOT carry a DB-enforced FK to product -- LB2 parentage is store-layer-enforced only, same as every migration 002 child table")

	// -- LB4: every subject column NOT NULL -----------------------------------
	for _, col := range []string{
		"created_by_acting_iss", "created_by_acting_sub", "created_by_acting_kind",
		"created_by_on_behalf_of_iss", "created_by_on_behalf_of_sub", "created_by_on_behalf_of_kind",
	} {
		_, nullable := nullableColumn(t, ctx, db, "pointer_artifact", col)
		assert.Equal(t, "NO", nullable, "pointer_artifact.%s must be NOT NULL (LB4 -- both subjects always recorded)", col)
	}

	_, nullable = nullableColumn(t, ctx, db, "pointer_artifact", "issue_number")
	assert.Equal(t, "NO", nullable, "pointer_artifact.issue_number must be NOT NULL")
	_, nullable = nullableColumn(t, ctx, db, "pointer_artifact", "issue_url")
	assert.Equal(t, "NO", nullable, "pointer_artifact.issue_url must be NOT NULL")

	// -- kind CHECK constraint: only "github_issue" is accepted ---------------
	var scopeID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ('pointer-check/repo', 'main') RETURNING id
	`).Scan(&scopeID))
	var productID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO product (scope_id, name, vision) VALUES ($1, 'P', 'V') RETURNING id
	`, scopeID).Scan(&productID))

	_, err = db.Pool.Exec(ctx, `
		INSERT INTO pointer_artifact (
			scope_id, product_id, kind, issue_number, issue_url,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES (
			$1, $2, 'not_github_issue', 1, 'https://example.com/issues/1',
			'iss', 'sub', 'human', 'iss', 'sub', 'human'
		)
	`, scopeID, productID)
	assert.Error(t, err, "pointer_artifact.kind must reject a value other than \"github_issue\" (CHECK constraint)")

	// -- pointer_artifact_product_idx: at most one pointer artifact per Product --
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO pointer_artifact (
			scope_id, product_id, issue_number, issue_url,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES (
			$1, $2, 1, 'https://example.com/issues/1',
			'iss', 'sub', 'human', 'iss', 'sub', 'human'
		)
	`, scopeID, productID)
	require.NoError(t, err)

	_, err = db.Pool.Exec(ctx, `
		INSERT INTO pointer_artifact (
			scope_id, product_id, issue_number, issue_url,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES (
			$1, $2, 2, 'https://example.com/issues/2',
			'iss', 'sub', 'human', 'iss', 'sub', 'human'
		)
	`, scopeID, productID)
	assert.Error(t, err, "a second pointer_artifact row for the same product_id must violate pointer_artifact_product_idx's UNIQUE constraint")
}

// TestMigration010_SchemaContract asserts 010_milestone_authoring's own
// boundary calls (issue #2683's Testing section, item 7): milestone_ref's
// `kind` CHECK rejects anything but "milestone", `outcome`/`fr_budget` are
// nullable (FR2 does not require a budget at creation), `position` is
// NOT NULL, and the LB4 subject-pair columns are nullable (unlike
// pointer_artifact's, and unlike milestone_deferral's own subject pair
// below) so the importer's session-less GetOrCreateRef path keeps working;
// milestone_deferral's `destination` is NOT NULL (FR1: every deferred
// entry cites where it went) and its subject-pair columns ARE mandatory
// (every write path onto that table is AddDeferral, which always has a
// real caller session); milestone_deferral.milestone_id is a real
// DB-enforced FK to milestone_ref(id); and entity_milestone.relation
// rejects a value other than "delivers"/"must_not_foreclose", defaults to
// "delivers" (so a pre-migration-010 importer-written row keeps its
// existing meaning), and its widened unique index
// (entity_id, milestone_id, relation) allows the same entity to appear
// against the same milestone once per relation without a spurious
// duplicate rejection -- the specific shape AddDelivers/AddMustNotForeclose
// (krill/store/milestone_authoring.go) both rely on.
func TestMigration010_SchemaContract(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up())

	// -- milestone_ref: kind CHECK, nullable outcome/fr_budget, NOT NULL position --
	_, nullable := nullableColumn(t, ctx, db, "milestone_ref", "outcome")
	assert.Equal(t, "YES", nullable, "milestone_ref.outcome must be nullable -- a milestone need not have an outcome sentence yet")
	_, nullable = nullableColumn(t, ctx, db, "milestone_ref", "fr_budget")
	assert.Equal(t, "YES", nullable, "milestone_ref.fr_budget must be nullable -- FR2 does not require a budget at creation time")
	_, nullable = nullableColumn(t, ctx, db, "milestone_ref", "position")
	assert.Equal(t, "NO", nullable, "milestone_ref.position must be NOT NULL (FR7)")
	_, nullable = nullableColumn(t, ctx, db, "milestone_ref", "kind")
	assert.Equal(t, "NO", nullable, "milestone_ref.kind must be NOT NULL")

	for _, col := range []string{
		"created_by_acting_iss", "created_by_acting_sub", "created_by_acting_kind",
		"created_by_on_behalf_of_iss", "created_by_on_behalf_of_sub", "created_by_on_behalf_of_kind",
	} {
		_, nullable := nullableColumn(t, ctx, db, "milestone_ref", col)
		assert.Equal(t, "YES", nullable, "milestone_ref.%s must be nullable -- krill/importer's GetOrCreateRef path writes no session-attributable subject", col)
	}

	var scopeID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ('milestone-010-check/repo', 'main') RETURNING id
	`).Scan(&scopeID))
	var productID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO product (scope_id, name, vision) VALUES ($1, 'P', 'V') RETURNING id
	`, scopeID).Scan(&productID))

	// -- milestone_ref.kind CHECK: only "milestone" is accepted today --
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name, kind) VALUES ($1, $2, 'M1', 'milepebble')
	`, scopeID, productID)
	assert.Error(t, err, "milestone_ref.kind must reject a value other than \"milestone\" (CHECK constraint) -- later issues on this board widen this, not migration 010")

	var milestoneID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name) VALUES ($1, $2, 'M1') RETURNING id
	`, scopeID, productID).Scan(&milestoneID))

	// -- milestone_deferral: destination NOT NULL, subject pair mandatory, real FK --
	deferralCols := columnNames(t, ctx, db, "milestone_deferral")
	assert.NotContains(t, deferralCols, "valid_from", "milestone_deferral must not be SCD2 (LB3) -- it is a plain append-only fact table")
	assert.NotContains(t, deferralCols, "valid_to", "milestone_deferral must not be SCD2 (LB3) -- it is a plain append-only fact table")

	_, nullable = nullableColumn(t, ctx, db, "milestone_deferral", "destination")
	assert.Equal(t, "NO", nullable, "milestone_deferral.destination must be NOT NULL (FR1: every deferred entry cites where it went)")
	_, nullable = nullableColumn(t, ctx, db, "milestone_deferral", "body")
	assert.Equal(t, "NO", nullable, "milestone_deferral.body must be NOT NULL")

	for _, col := range []string{
		"created_by_acting_iss", "created_by_acting_sub", "created_by_acting_kind",
		"created_by_on_behalf_of_iss", "created_by_on_behalf_of_sub", "created_by_on_behalf_of_kind",
	} {
		_, nullable := nullableColumn(t, ctx, db, "milestone_deferral", col)
		assert.Equal(t, "NO", nullable, "milestone_deferral.%s must be NOT NULL -- every write path onto this table (AddDeferral) always has a real caller session", col)
	}

	dataType, nullable := nullableColumn(t, ctx, db, "milestone_deferral", "scope_id")
	assert.Equal(t, "NO", nullable, "milestone_deferral.scope_id must be NOT NULL (LB1)")
	assert.Equal(t, "uuid", dataType, "milestone_deferral.scope_id must be a plain uuid column")
	assert.True(t, hasForeignKeyTo(t, ctx, db, "milestone_deferral", "scope"), "milestone_deferral.scope_id must carry a real DB-enforced FK to scope(id) (LB1)")
	assert.True(t, hasForeignKeyTo(t, ctx, db, "milestone_deferral", "milestone_ref"), "milestone_deferral.milestone_id must carry a real DB-enforced FK to milestone_ref(id) -- milestone_ref is not SCD2, so its id is table-wide unique")

	// NULL destination is rejected by the NOT NULL constraint itself (an
	// empty string is NOT NULL's blind spot -- that half of FR1's
	// "every deferred entry cites where it went" is the store layer's job,
	// AddDeferral's own explicit check, verified by
	// TestMilestoneAuthoringStore_AddDeferral_EmptyDestination_Rejected).
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO milestone_deferral (
			scope_id, milestone_id, body, destination,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES ($1, $2, 'deferred body', NULL, 'iss', 'sub', 'human', 'iss', 'sub', 'human')
	`, scopeID, milestoneID)
	assert.Error(t, err, "milestone_deferral.destination must reject NULL (NOT NULL constraint)")

	_, err = db.Pool.Exec(ctx, `
		INSERT INTO milestone_deferral (
			scope_id, milestone_id, body, destination,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES ($1, gen_random_uuid(), 'deferred body', 'M4', 'iss', 'sub', 'human', 'iss', 'sub', 'human')
	`, scopeID)
	assert.Error(t, err, "milestone_deferral.milestone_id must be FK-enforced against milestone_ref(id)")

	// -- entity_milestone.relation: CHECK, default, and the widened unique index --
	var featureSetID, featureID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO feature_set (scope_id, product_id, name) VALUES ($1, $2, 'fs') RETURNING id
	`, scopeID, productID).Scan(&featureSetID))
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO feature (scope_id, feature_set_id, name, display_number) VALUES ($1, $2, 'f', 1) RETURNING id
	`, scopeID, featureSetID).Scan(&featureID))

	_, err = db.Pool.Exec(ctx, `
		INSERT INTO entity_milestone (scope_id, entity_id, milestone_id, relation) VALUES ($1, $2, $3, 'bogus')
	`, scopeID, featureID, milestoneID)
	assert.Error(t, err, "entity_milestone.relation must reject a value other than \"delivers\"/\"must_not_foreclose\" (CHECK constraint)")

	// No relation specified: must default to "delivers" (a pre-migration-010
	// importer-written row keeps its existing meaning).
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO entity_milestone (scope_id, entity_id, milestone_id) VALUES ($1, $2, $3)
	`, scopeID, featureID, milestoneID)
	require.NoError(t, err)
	var relation string
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT relation FROM entity_milestone WHERE entity_id = $1 AND milestone_id = $2
	`, featureID, milestoneID).Scan(&relation))
	assert.Equal(t, "delivers", relation, "entity_milestone.relation must default to \"delivers\"")

	// Same (entity_id, milestone_id) but the OTHER relation must be allowed
	// -- the widened unique index is (entity_id, milestone_id, relation),
	// not (entity_id, milestone_id) alone.
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO entity_milestone (scope_id, entity_id, milestone_id, relation) VALUES ($1, $2, $3, 'must_not_foreclose')
	`, scopeID, featureID, milestoneID)
	assert.NoError(t, err, "the same (entity_id, milestone_id) pair must be allowed once per relation -- migration 010 widens the unique index to (entity_id, milestone_id, relation)")

	// But the exact same (entity_id, milestone_id, relation) triple again
	// must still be rejected.
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO entity_milestone (scope_id, entity_id, milestone_id, relation) VALUES ($1, $2, $3, 'must_not_foreclose')
	`, scopeID, featureID, milestoneID)
	assert.Error(t, err, "a second entity_milestone row for the exact same (entity_id, milestone_id, relation) triple must still violate the widened unique index")
}

// TestMigration010_DownLeavesMilestoneAssocTablesIntact proves 010's own
// Down() rolls back exactly its own additions (milestone_ref's authoring
// columns, milestone_deferral, entity_milestone.relation) and leaves
// migration 004's milestone_ref/entity_milestone tables themselves intact
// and usable -- issue #2683's Testing section item 7's explicit "010 down
// leaves 004's tables intact" clause. Migrates up to exactly version 10
// (Migrate(10), not Up()/latest -- migration 011 and later would otherwise
// shift what "roll back one step" lands on as soon as a further migration
// is added) and then rolls back that one step, so this is the one test in
// this file that exercises a partial rollback instead of the whole-set
// Up()/Down() TestMigrations_UpDownUp_LeavesCleanDatabaseAndIsRerunnable
// above already covers.
func TestMigration010_DownLeavesMilestoneAssocTablesIntact(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Migrate(10), "apply every migration through exactly 010")

	require.NoError(t, runner.Steps(-1), "roll back exactly migration 010")

	version, dirty, err := runner.Version()
	require.NoError(t, err)
	assert.False(t, dirty)
	assert.Equal(t, uint(9), version, "rolling back exactly one step from 10 must land on 9 (009_import_completion)")

	assert.True(t, tableExists(t, ctx, db, "milestone_ref"), "010's Down() must leave 004's milestone_ref table intact")
	assert.True(t, tableExists(t, ctx, db, "entity_milestone"), "010's Down() must leave 004's entity_milestone table intact")
	assert.False(t, tableExists(t, ctx, db, "milestone_deferral"), "010's Down() must drop milestone_deferral -- it is entirely 010's own addition")

	milestoneRefCols := columnNames(t, ctx, db, "milestone_ref")
	assert.NotContains(t, milestoneRefCols, "kind", "010's Down() must drop milestone_ref.kind")
	assert.NotContains(t, milestoneRefCols, "outcome", "010's Down() must drop milestone_ref.outcome")
	assert.NotContains(t, milestoneRefCols, "fr_budget", "010's Down() must drop milestone_ref.fr_budget")
	assert.NotContains(t, milestoneRefCols, "position", "010's Down() must drop milestone_ref.position")

	entityMilestoneCols := columnNames(t, ctx, db, "entity_milestone")
	assert.NotContains(t, entityMilestoneCols, "relation", "010's Down() must drop entity_milestone.relation")

	// 004's own tables must still be functionally usable after 010's Down().
	var scopeID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ('milestone-010-down-check/repo', 'main') RETURNING id
	`).Scan(&scopeID))
	var productID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO product (scope_id, name, vision) VALUES ($1, 'P', 'V') RETURNING id
	`, scopeID).Scan(&productID))
	var milestoneID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name) VALUES ($1, $2, 'M1') RETURNING id
	`, scopeID, productID).Scan(&milestoneID))
	require.NotEqual(t, uuid.Nil, milestoneID, "milestone_ref must still accept inserts of its bare 004 shape after 010's Down()")
}

// indexExists reports whether a named index exists on table -- used below
// where hasPartialUniqueIndexOnCurrentID's fixed "(id) WHERE valid_to IS
// NULL" shape doesn't apply (011_milepebble's partial indexes are keyed
// differently and milestone_ref is not SCD2 in the first place).
func indexExists(t *testing.T, ctx context.Context, db *dbtest.Postgres, table, indexName string) bool {
	t.Helper()
	var exists bool
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname = 'public' AND tablename = $1 AND indexname = $2)
	`, table, indexName).Scan(&exists))
	return exists
}

// TestMigration011_SchemaContract asserts 011_milepebble's own boundary
// calls (issue #2684's Testing section, items 2 and 6): milestone_ref's
// widened `kind` CHECK accepts "milepebble" as well as "milestone",
// `parent_milestone_id` is a real DB-enforced FK back onto
// milestone_ref(id) (milestone_ref is not SCD2, so its id is table-wide
// unique -- the same reasoning entity_milestone.milestone_id and
// milestone_deferral.milestone_id already rely on), and the CHECK pairing
// `(kind = 'milepebble') = (parent_milestone_id IS NOT NULL)` is enforced
// by the database itself, not merely by convention -- a milepebble row
// with a NULL parent, or a milestone row with a non-NULL parent, must both
// be rejected. It also asserts the re-scoped uniqueness: two milepebbles
// under different parents may share a name, two milepebbles under the SAME
// parent may not, and the original one-name-per-(scope,product) milestone
// uniqueness is unchanged. Finally, LB6's own trap in this migration's
// direction: no spec entity table (`feature`, `requirement`) has acquired
// a `milepebble_id` column -- asserted directly against
// information_schema.columns so a future "convenience column" fails the
// build (issue #2684's Testing section item 6, verbatim).
func TestMigration011_SchemaContract(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up())

	// -- LB6's own trap, this migration's direction: no milepebble_id column
	// anywhere on a spec entity table (issue #2684's Testing item 6) --
	for _, table := range []string{"feature", "requirement"} {
		cols := columnNames(t, ctx, db, table)
		assert.NotContains(t, cols, "milepebble_id",
			"%s must never carry a milepebble_id column -- a milepebble's delivered scope is recorded via entity_milestone, the same association mechanism migration 004 established, never a second parent column on a spec entity (FR3/LB6)", table)
		assert.NotContains(t, cols, "milestone_id", "%s must never carry a milestone_id column either (LB6, re-asserted here now that milepebbles exist too)", table)
	}

	// -- parent_milestone_id: nullable, real DB-enforced FK to milestone_ref(id) --
	dataType, nullable := nullableColumn(t, ctx, db, "milestone_ref", "parent_milestone_id")
	assert.Equal(t, "YES", nullable, "milestone_ref.parent_milestone_id must be nullable -- NULL for every kind=\"milestone\" row")
	assert.Equal(t, "uuid", dataType, "milestone_ref.parent_milestone_id must be a plain uuid column")
	assert.True(t, hasForeignKeyTo(t, ctx, db, "milestone_ref", "milestone_ref"), "milestone_ref.parent_milestone_id must carry a real DB-enforced FK back onto milestone_ref(id)")

	var scopeID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ('milepebble-011-check/repo', 'main') RETURNING id
	`).Scan(&scopeID))
	var productID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO product (scope_id, name, vision) VALUES ($1, 'P', 'V') RETURNING id
	`, scopeID).Scan(&productID))

	// -- milestone_ref.kind CHECK: "milepebble" is now accepted, given a parent --
	var milestoneID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name) VALUES ($1, $2, 'M1') RETURNING id
	`, scopeID, productID).Scan(&milestoneID))

	var milepebbleID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name, kind, parent_milestone_id) VALUES ($1, $2, 'cut 1', 'milepebble', $3) RETURNING id
	`, scopeID, productID, milestoneID).Scan(&milepebbleID))
	require.NotEqual(t, uuid.Nil, milepebbleID)

	// A kind other than "milestone"/"milepebble" is still rejected.
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name, kind, parent_milestone_id) VALUES ($1, $2, 'bogus-kind', 'backlog', $3)
	`, scopeID, productID, milestoneID)
	assert.Error(t, err, "milestone_ref.kind must still reject a value other than \"milestone\"/\"milepebble\" (CHECK constraint)")

	// -- milestone_ref_milepebble_has_parent_check: the pairing is DB-enforced --
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name, kind, parent_milestone_id) VALUES ($1, $2, 'orphan-milepebble', 'milepebble', NULL)
	`, scopeID, productID)
	assert.Error(t, err, "a kind=\"milepebble\" row with a NULL parent_milestone_id must be rejected -- FR3's \"exactly one milestone\"")

	_, err = db.Pool.Exec(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name, kind, parent_milestone_id) VALUES ($1, $2, 'double-parented-milestone', 'milestone', $3)
	`, scopeID, productID, milestoneID)
	assert.Error(t, err, "a kind=\"milestone\" row with a non-NULL parent_milestone_id must be rejected -- a milestone has no parent milestone")

	// -- parent_milestone_id FK is real, not just a plain uuid column --
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name, kind, parent_milestone_id) VALUES ($1, $2, 'dangling-parent', 'milepebble', gen_random_uuid())
	`, scopeID, productID)
	assert.Error(t, err, "milestone_ref.parent_milestone_id must be FK-enforced against milestone_ref(id)")

	// -- re-scoped uniqueness: two milepebbles under the SAME parent sharing a name is rejected --
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name, kind, parent_milestone_id) VALUES ($1, $2, 'cut 1', 'milepebble', $3)
	`, scopeID, productID, milestoneID)
	assert.Error(t, err, "two milepebbles under the same parent milestone must not share a name (milestone_ref_milepebble_parent_name_idx)")

	// -- but two milepebbles under DIFFERENT parents may share a name --
	var milestone2ID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name) VALUES ($1, $2, 'M2') RETURNING id
	`, scopeID, productID).Scan(&milestone2ID))
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name, kind, parent_milestone_id) VALUES ($1, $2, 'cut 1', 'milepebble', $3)
	`, scopeID, productID, milestone2ID)
	assert.NoError(t, err, "two milepebbles cut from DIFFERENT parent milestones may legitimately share a name")

	// -- the original milestone-level uniqueness (one name per scope/product, among milestones) is unchanged --
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name) VALUES ($1, $2, 'M1')
	`, scopeID, productID)
	assert.Error(t, err, "two milestones (kind=\"milestone\", parent_milestone_id IS NULL) under the same product must still not share a name")

	// -- both partial indexes exist by name, proving the re-scoping was a
	// widen, not a silent drop --
	assert.True(t, indexExists(t, ctx, db, "milestone_ref", "milestone_ref_scope_product_name_idx"))
	assert.True(t, indexExists(t, ctx, db, "milestone_ref", "milestone_ref_milepebble_parent_name_idx"))
}

// TestMigration011_DownLeavesMilestoneRefIntact proves 011's own Down()
// rolls back exactly its own additions (parent_milestone_id, the widened
// kind CHECK, the milepebble-parent CHECK, and the milepebble-scoped
// unique index) and leaves migration 004/010's milestone_ref shape intact
// and usable -- the same "down leaves the earlier migration's tables
// intact" clause TestMigration010_DownLeavesMilestoneAssocTablesIntact
// proves for 010, applied to 011. Migrates up to exactly version 11
// (Migrate(11)) and rolls back that one step, landing on 10. It also
// covers issue #2684's Testing section item 8's explicit
// "down-migrating with milepebble rows present" clause: a real
// kind="milepebble" row exists before the rollback, and 011.down.sql's
// own documented behavior (its "DELETE FROM milestone_ref WHERE
// kind = 'milepebble'" comment) is exercised for real -- the milepebble
// row is gone afterward, while its parent milestone row survives.
func TestMigration011_DownLeavesMilestoneRefIntact(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Migrate(11), "apply every migration through exactly 011")

	// -- seed a real milepebble row (item 8: "down-migrating with milepebble
	// rows present behaves as documented in the .down.sql comment") --
	var seedScopeID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ('milepebble-011-down-seed/repo', 'main') RETURNING id
	`).Scan(&seedScopeID))
	var seedProductID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO product (scope_id, name, vision) VALUES ($1, 'P', 'V') RETURNING id
	`, seedScopeID).Scan(&seedProductID))
	var seedMilestoneID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name) VALUES ($1, $2, 'M1') RETURNING id
	`, seedScopeID, seedProductID).Scan(&seedMilestoneID))
	var seedMilepebbleID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name, kind, parent_milestone_id) VALUES ($1, $2, 'cut 1', 'milepebble', $3) RETURNING id
	`, seedScopeID, seedProductID, seedMilestoneID).Scan(&seedMilepebbleID))

	require.NoError(t, runner.Steps(-1), "roll back exactly migration 011")

	// The milepebble row itself must be gone -- 011.down.sql documents (and
	// this proves) that a kind="milepebble" row has no meaning once
	// parent_milestone_id is dropped, so it is deleted rather than left as
	// an orphaned bare milestone_ref row. Its parent milestone survives.
	var milepebbleStillExists bool
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM milestone_ref WHERE id = $1)
	`, seedMilepebbleID).Scan(&milepebbleStillExists))
	assert.False(t, milepebbleStillExists, "011's Down() must delete every kind=\"milepebble\" row, per 011.down.sql's own documented behavior")

	var milestoneStillExists bool
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM milestone_ref WHERE id = $1)
	`, seedMilestoneID).Scan(&milestoneStillExists))
	assert.True(t, milestoneStillExists, "011's Down() must leave the parent milestone row itself untouched")

	version, dirty, err := runner.Version()
	require.NoError(t, err)
	assert.False(t, dirty)
	assert.Equal(t, uint(10), version, "rolling back exactly one step from 11 must land on 10 (010_milestone_authoring)")

	assert.True(t, tableExists(t, ctx, db, "milestone_ref"), "011's Down() must leave milestone_ref itself intact")

	milestoneRefCols := columnNames(t, ctx, db, "milestone_ref")
	assert.NotContains(t, milestoneRefCols, "parent_milestone_id", "011's Down() must drop milestone_ref.parent_milestone_id")
	assert.Contains(t, milestoneRefCols, "kind", "011's Down() must leave migration 010's kind column intact")
	assert.Contains(t, milestoneRefCols, "outcome", "011's Down() must leave migration 010's outcome column intact")

	var scopeID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ('milepebble-011-down-check/repo', 'main') RETURNING id
	`).Scan(&scopeID))
	var productID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO product (scope_id, name, vision) VALUES ($1, 'P', 'V') RETURNING id
	`, scopeID).Scan(&productID))

	// milestone_ref's kind CHECK reverts to accepting only "milestone" --
	// 011's widening is fully undone, not left partially in place.
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name, kind) VALUES ($1, $2, 'M1', 'milepebble')
	`, scopeID, productID)
	assert.Error(t, err, "after 011's Down(), milestone_ref.kind must reject \"milepebble\" again -- the CHECK widening must be fully reverted")

	// The bare (post-010) shape must still be usable.
	var milestoneID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name) VALUES ($1, $2, 'M1') RETURNING id
	`, scopeID, productID).Scan(&milestoneID))
	require.NotEqual(t, uuid.Nil, milestoneID, "milestone_ref must still accept inserts of its post-010 shape after 011's Down()")
}

// TestMigration012_SchemaContract asserts 012_milestone_status's own
// boundary calls (issue #2685's Testing section, items 2, 3, and 7):
// `status` is CHECK-constrained to exactly FR8's seven values (an eighth,
// made-up value is rejected by the database itself), `milestone_id` is a
// real DB-enforced FK back onto milestone_ref(id) (accepting both a
// kind='milestone' and a kind='milepebble' row, FR9), `scope_id` is
// NOT NULL with a real DB-enforced FK to scope(id) (LB1), every LB4
// subject-pair column is NOT NULL (NFR4 -- unlike milestone_ref's own
// nullable subject-pair columns, migration 010's LB4 note), `note` is
// nullable, and the table carries no `valid_from`/`valid_to` pair at all
// -- LB3/NFR2's append-only boundary, not SCD2.
func TestMigration012_SchemaContract(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up())

	// -- not SCD2 (LB3/NFR2): no valid_from/valid_to columns -----------------
	cols := columnNames(t, ctx, db, "milestone_status_event")
	assert.NotContains(t, cols, "valid_from", "milestone_status_event must not be SCD2 (LB3/NFR2) -- it is an append-only history table")
	assert.NotContains(t, cols, "valid_to", "milestone_status_event must not be SCD2 (LB3/NFR2) -- it is an append-only history table")

	// -- note: nullable ---------------------------------------------------
	_, nullable := nullableColumn(t, ctx, db, "milestone_status_event", "note")
	assert.Equal(t, "YES", nullable, "milestone_status_event.note must be nullable")

	// -- scope_id: NOT NULL, real DB-enforced FK (LB1) ------------------------
	dataType, nullable := nullableColumn(t, ctx, db, "milestone_status_event", "scope_id")
	assert.Equal(t, "NO", nullable, "milestone_status_event.scope_id must be NOT NULL (LB1)")
	assert.Equal(t, "uuid", dataType, "milestone_status_event.scope_id must be a plain uuid column")
	assert.True(t, hasForeignKeyTo(t, ctx, db, "milestone_status_event", "scope"), "milestone_status_event.scope_id must carry a real DB-enforced FK to scope(id) (LB1)")

	// -- milestone_id: NOT NULL, real DB-enforced FK back onto milestone_ref(id) --
	dataType, nullable = nullableColumn(t, ctx, db, "milestone_status_event", "milestone_id")
	assert.Equal(t, "NO", nullable, "milestone_status_event.milestone_id must be NOT NULL")
	assert.Equal(t, "uuid", dataType, "milestone_status_event.milestone_id must be a plain uuid column")
	assert.True(t, hasForeignKeyTo(t, ctx, db, "milestone_status_event", "milestone_ref"), "milestone_status_event.milestone_id must carry a real DB-enforced FK to milestone_ref(id) -- milestone_ref is not SCD2, so its id is table-wide unique")

	// -- LB4/NFR4: every subject column NOT NULL ------------------------------
	for _, col := range []string{
		"created_by_acting_iss", "created_by_acting_sub", "created_by_acting_kind",
		"created_by_on_behalf_of_iss", "created_by_on_behalf_of_sub", "created_by_on_behalf_of_kind",
	} {
		_, nullable := nullableColumn(t, ctx, db, "milestone_status_event", col)
		assert.Equal(t, "NO", nullable, "milestone_status_event.%s must be NOT NULL (NFR4 -- unlike milestone_ref's own nullable subject-pair columns)", col)
	}

	_, nullable = nullableColumn(t, ctx, db, "milestone_status_event", "created_at")
	assert.Equal(t, "NO", nullable, "milestone_status_event.created_at must be NOT NULL")

	// -- seed a scope/product/milestone/milepebble to exercise the CHECK and FK --
	var scopeID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ('milestone-status-012-check/repo', 'main') RETURNING id
	`).Scan(&scopeID))
	var productID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO product (scope_id, name, vision) VALUES ($1, 'P', 'V') RETURNING id
	`, scopeID).Scan(&productID))
	var milestoneID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name) VALUES ($1, $2, 'M1') RETURNING id
	`, scopeID, productID).Scan(&milestoneID))
	var milepebbleID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name, kind, parent_milestone_id) VALUES ($1, $2, 'cut 1', 'milepebble', $3) RETURNING id
	`, scopeID, productID, milestoneID).Scan(&milepebbleID))

	insertStatusEvent := func(milestoneRefID uuid.UUID, status string) error {
		_, err := db.Pool.Exec(ctx, `
			INSERT INTO milestone_status_event (
				scope_id, milestone_id, status,
				created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
				created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
			) VALUES ($1, $2, $3, 'iss', 'sub', 'human', 'iss', 'sub', 'human')
		`, scopeID, milestoneRefID, status)
		return err
	}

	// -- status CHECK: all seven fixed values accepted ------------------------
	for _, status := range []string{
		"not started", "in design", "planned", "in progress", "shipped", "partially complete", "abandoned",
	} {
		assert.NoError(t, insertStatusEvent(milestoneID, status), "status %q must be one of FR8's fixed seven values", status)
	}

	// -- status CHECK: an eighth, made-up value is rejected -------------------
	assert.Error(t, insertStatusEvent(milestoneID, "bogus-status"), "milestone_status_event.status must reject a value outside FR8's fixed seven (CHECK constraint)")

	// -- FR9: the exact same operation works against a kind='milepebble' row --
	assert.NoError(t, insertStatusEvent(milepebbleID, "planned"), "milestone_status_event must accept a kind='milepebble' target the same as a kind='milestone' one (FR9)")

	// -- milestone_id FK is real, not just a plain uuid column ----------------
	assert.Error(t, insertStatusEvent(uuid.New(), "planned"), "milestone_status_event.milestone_id must be FK-enforced against milestone_ref(id)")

	// -- subject-pair columns are mandatory: NULL is rejected -----------------
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO milestone_status_event (
			scope_id, milestone_id, status,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES ($1, $2, 'planned', NULL, 'sub', 'human', 'iss', 'sub', 'human')
	`, scopeID, milestoneID)
	assert.Error(t, err, "milestone_status_event's LB4 subject-pair columns must reject NULL (NFR4)")
}

// TestMigration012_UpDownRoundTrip is issue #2685's Testing item 9:
// migration 012 applies cleanly (creating milestone_status_event with a
// real row present), rolls back cleanly (dropping the table and its
// index), and re-applies cleanly a second time -- the append-only history
// table is as re-runnable as every other migration in this package.
// Migrates up to exactly version 12 (Migrate(12), not Up()/latest) so a
// later migration landing on top of this one does not shift what "roll
// back one step" means here, mirroring
// TestMigration010_DownLeavesMilestoneAssocTablesIntact's and
// TestMigration011_DownLeavesMilestoneRefIntact's own choice.
func TestMigration012_UpDownRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Migrate(12), "apply every migration through exactly 012")

	assert.True(t, tableExists(t, ctx, db, "milestone_status_event"), "012's Up() must create milestone_status_event")
	assert.True(t, indexExists(t, ctx, db, "milestone_status_event", "milestone_status_event_milestone_created_idx"), "012's Up() must create the (milestone_id, created_at DESC) index")

	// Seed a real row before rolling back, so Down()'s "drops the whole
	// table, history and all" behavior is exercised for real, not just
	// against an empty table.
	var scopeID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ('milestone-status-012-roundtrip/repo', 'main') RETURNING id
	`).Scan(&scopeID))
	var productID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO product (scope_id, name, vision) VALUES ($1, 'P', 'V') RETURNING id
	`, scopeID).Scan(&productID))
	var milestoneID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name) VALUES ($1, $2, 'M1') RETURNING id
	`, scopeID, productID).Scan(&milestoneID))
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO milestone_status_event (
			scope_id, milestone_id, status,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES ($1, $2, 'planned', 'iss', 'sub', 'human', 'iss', 'sub', 'human')
	`, scopeID, milestoneID)
	require.NoError(t, err)

	require.NoError(t, runner.Steps(-1), "roll back exactly migration 012")

	version, dirty, err := runner.Version()
	require.NoError(t, err)
	assert.False(t, dirty)
	assert.Equal(t, uint(11), version, "rolling back exactly one step from 12 must land on 11 (011_milepebble)")

	assert.False(t, tableExists(t, ctx, db, "milestone_status_event"), "012's Down() must drop milestone_status_event entirely -- the whole history along with the table")
	assert.True(t, tableExists(t, ctx, db, "milestone_ref"), "012's Down() must leave milestone_ref itself untouched -- this migration never alters that table")

	// Re-applying must succeed a second time from the rolled-back state.
	require.NoError(t, runner.Steps(1), "re-apply migration 012 after Down() -- must be re-runnable")

	version, dirty, err = runner.Version()
	require.NoError(t, err)
	assert.False(t, dirty)
	assert.Equal(t, uint(12), version)
	assert.True(t, tableExists(t, ctx, db, "milestone_status_event"), "milestone_status_event must exist again after re-applying 012")

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM milestone_status_event`).Scan(&count))
	assert.Equal(t, 0, count, "re-applying 012 creates a fresh, empty table -- the row seeded before the rollback is gone for good")
}

// TestMigration013_SchemaContract asserts 013_delivery_shipment's own
// boundary calls (issue #2686's Testing section, items 3 and 8): the
// table carries no `valid_from`/`valid_to` pair at all (LB3/NFR2's
// append-only boundary, mirroring migration 012's own posture, never
// SCD2), `note` is nullable, `scope_id` is NOT NULL with a real
// DB-enforced FK to `scope` (LB1), `entity_id` is a plain, non-FK uuid
// column (it names a spec entity, which migration 002's LB2 parentage
// note already established is never a DB-enforced FK), `milestone_id` is
// NOT NULL with a real DB-enforced FK back onto `milestone_ref(id)`
// (accepting both a kind='milestone' and a kind='milepebble' row, FR9,
// same as migration 012's own milestone_id), and every LB4 subject-pair
// column is NOT NULL (NFR4).
func TestMigration013_SchemaContract(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up())

	// -- not SCD2 (LB3/NFR2): no valid_from/valid_to columns -----------------
	cols := columnNames(t, ctx, db, "delivery_shipment")
	assert.NotContains(t, cols, "valid_from", "delivery_shipment must not be SCD2 (LB3/NFR2) -- it is an append-only history table")
	assert.NotContains(t, cols, "valid_to", "delivery_shipment must not be SCD2 (LB3/NFR2) -- it is an append-only history table")

	// -- note: nullable --------------------------------------------------
	_, nullable := nullableColumn(t, ctx, db, "delivery_shipment", "note")
	assert.Equal(t, "YES", nullable, "delivery_shipment.note must be nullable")

	// -- scope_id: NOT NULL, real DB-enforced FK (LB1) ------------------------
	dataType, nullable := nullableColumn(t, ctx, db, "delivery_shipment", "scope_id")
	assert.Equal(t, "NO", nullable, "delivery_shipment.scope_id must be NOT NULL (LB1)")
	assert.Equal(t, "uuid", dataType, "delivery_shipment.scope_id must be a plain uuid column")
	assert.True(t, hasForeignKeyTo(t, ctx, db, "delivery_shipment", "scope"), "delivery_shipment.scope_id must carry a real DB-enforced FK to scope(id) (LB1)")

	// -- entity_id: NOT NULL, plain uuid, NOT a DB-enforced FK (LB2 parentage) --
	dataType, nullable = nullableColumn(t, ctx, db, "delivery_shipment", "entity_id")
	assert.Equal(t, "NO", nullable, "delivery_shipment.entity_id must be NOT NULL")
	assert.Equal(t, "uuid", dataType, "delivery_shipment.entity_id must be a plain uuid column, never an array")
	assert.False(t, hasForeignKeyTo(t, ctx, db, "delivery_shipment", "feature"), "delivery_shipment.entity_id must NOT carry a DB-enforced FK to feature -- LB2 parentage is store-layer-enforced only, same as every migration 002 child table")
	assert.False(t, hasForeignKeyTo(t, ctx, db, "delivery_shipment", "requirement"), "delivery_shipment.entity_id must NOT carry a DB-enforced FK to requirement -- LB2 parentage is store-layer-enforced only")

	// -- milestone_id: NOT NULL, real DB-enforced FK back onto milestone_ref(id) --
	dataType, nullable = nullableColumn(t, ctx, db, "delivery_shipment", "milestone_id")
	assert.Equal(t, "NO", nullable, "delivery_shipment.milestone_id must be NOT NULL")
	assert.Equal(t, "uuid", dataType, "delivery_shipment.milestone_id must be a plain uuid column")
	assert.True(t, hasForeignKeyTo(t, ctx, db, "delivery_shipment", "milestone_ref"), "delivery_shipment.milestone_id must carry a real DB-enforced FK to milestone_ref(id)")

	// -- LB4/NFR4: every subject column NOT NULL ------------------------------
	for _, col := range []string{
		"created_by_acting_iss", "created_by_acting_sub", "created_by_acting_kind",
		"created_by_on_behalf_of_iss", "created_by_on_behalf_of_sub", "created_by_on_behalf_of_kind",
	} {
		_, nullable := nullableColumn(t, ctx, db, "delivery_shipment", col)
		assert.Equal(t, "NO", nullable, "delivery_shipment.%s must be NOT NULL (NFR4)", col)
	}

	_, nullable = nullableColumn(t, ctx, db, "delivery_shipment", "created_at")
	assert.Equal(t, "NO", nullable, "delivery_shipment.created_at must be NOT NULL")

	// -- seed a scope/product/milestone/milepebble to exercise the FKs --
	var scopeID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ('delivery-shipment-013-check/repo', 'main') RETURNING id
	`).Scan(&scopeID))
	var productID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO product (scope_id, name, vision) VALUES ($1, 'P', 'V') RETURNING id
	`, scopeID).Scan(&productID))
	var milestoneID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name) VALUES ($1, $2, 'M1') RETURNING id
	`, scopeID, productID).Scan(&milestoneID))
	var milepebbleID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name, kind, parent_milestone_id) VALUES ($1, $2, 'cut 1', 'milepebble', $3) RETURNING id
	`, scopeID, productID, milestoneID).Scan(&milepebbleID))

	insertShipment := func(milestoneRefID uuid.UUID) error {
		_, err := db.Pool.Exec(ctx, `
			INSERT INTO delivery_shipment (
				scope_id, entity_id, milestone_id,
				created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
				created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
			) VALUES ($1, $2, $3, 'iss', 'sub', 'human', 'iss', 'sub', 'human')
		`, scopeID, uuid.New(), milestoneRefID)
		return err
	}

	// -- FR9: the exact same operation works against a kind='milepebble' row --
	assert.NoError(t, insertShipment(milestoneID), "delivery_shipment must accept a kind='milestone' target")
	assert.NoError(t, insertShipment(milepebbleID), "delivery_shipment must accept a kind='milepebble' target the same as a kind='milestone' one (FR9)")

	// -- milestone_id FK is real, not just a plain uuid column ----------------
	assert.Error(t, insertShipment(uuid.New()), "delivery_shipment.milestone_id must be FK-enforced against milestone_ref(id)")

	// -- a second row for the exact same (entity, milestone) pair is accepted,
	// never rejected as a duplicate (NFR2/NFR3: appending, not upserting) --
	entityID := uuid.New()
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO delivery_shipment (
			scope_id, entity_id, milestone_id,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES ($1, $2, $3, 'iss', 'sub', 'human', 'iss', 'sub', 'human')
	`, scopeID, entityID, milestoneID)
	require.NoError(t, err)
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO delivery_shipment (
			scope_id, entity_id, milestone_id,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES ($1, $2, $3, 'iss', 'sub', 'human', 'iss', 'sub', 'human')
	`, scopeID, entityID, milestoneID)
	assert.NoError(t, err, "delivery_shipment must have no uniqueness constraint on (entity_id, milestone_id) -- a second shipment of the same pair must be a second row, never rejected as a duplicate")

	// -- subject-pair columns are mandatory: NULL is rejected -----------------
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO delivery_shipment (
			scope_id, entity_id, milestone_id,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES ($1, $2, $3, NULL, 'sub', 'human', 'iss', 'sub', 'human')
	`, scopeID, uuid.New(), milestoneID)
	assert.Error(t, err, "delivery_shipment's LB4 subject-pair columns must reject NULL (NFR4)")
}

// TestMigration013_UpDownRoundTrip is issue #2686's Testing item 8:
// migration 013 applies cleanly (creating delivery_shipment with a real
// row present), rolls back cleanly (dropping the table and both indexes),
// and re-applies cleanly a second time -- the append-only shipment
// register is as re-runnable as every other migration in this package.
// Migrates up to exactly version 13 (Migrate(13), not Up()/latest) so a
// later migration landing on top of this one does not shift what "roll
// back one step" means here, mirroring TestMigration012_UpDownRoundTrip's
// own choice.
func TestMigration013_UpDownRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Migrate(13), "apply every migration through exactly 013")

	assert.True(t, tableExists(t, ctx, db, "delivery_shipment"), "013's Up() must create delivery_shipment")
	assert.True(t, indexExists(t, ctx, db, "delivery_shipment", "delivery_shipment_milestone_idx"), "013's Up() must create the (milestone_id) index")
	assert.True(t, indexExists(t, ctx, db, "delivery_shipment", "delivery_shipment_entity_milestone_idx"), "013's Up() must create the (entity_id, milestone_id) index")

	// Seed a real row before rolling back, so Down()'s "drops the whole
	// table, history and all" behavior is exercised for real, not just
	// against an empty table.
	var scopeID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ('delivery-shipment-013-roundtrip/repo', 'main') RETURNING id
	`).Scan(&scopeID))
	var productID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO product (scope_id, name, vision) VALUES ($1, 'P', 'V') RETURNING id
	`, scopeID).Scan(&productID))
	var milestoneID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name) VALUES ($1, $2, 'M1') RETURNING id
	`, scopeID, productID).Scan(&milestoneID))
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO delivery_shipment (
			scope_id, entity_id, milestone_id,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES ($1, $2, $3, 'iss', 'sub', 'human', 'iss', 'sub', 'human')
	`, scopeID, uuid.New(), milestoneID)
	require.NoError(t, err)

	require.NoError(t, runner.Steps(-1), "roll back exactly migration 013")

	version, dirty, err := runner.Version()
	require.NoError(t, err)
	assert.False(t, dirty)
	assert.Equal(t, uint(12), version, "rolling back exactly one step from 13 must land on 12 (012_milestone_status)")

	assert.False(t, tableExists(t, ctx, db, "delivery_shipment"), "013's Down() must drop delivery_shipment entirely -- the whole history along with the table")
	assert.True(t, tableExists(t, ctx, db, "milestone_ref"), "013's Down() must leave milestone_ref itself untouched -- this migration never alters that table")

	// Re-applying must succeed a second time from the rolled-back state.
	require.NoError(t, runner.Steps(1), "re-apply migration 013 after Down() -- must be re-runnable")

	version, dirty, err = runner.Version()
	require.NoError(t, err)
	assert.False(t, dirty)
	assert.Equal(t, uint(13), version)
	assert.True(t, tableExists(t, ctx, db, "delivery_shipment"), "delivery_shipment must exist again after re-applying 013")

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM delivery_shipment`).Scan(&count))
	assert.Equal(t, 0, count, "re-applying 013 creates a fresh, empty table -- the row seeded before the rollback is gone for good")
}

// TestMigration014_SchemaContract asserts 014_backlog_bucket's own
// boundary calls (issue #2687's Testing section): milestone_ref's `kind`
// CHECK now accepts "backlog" alongside "milestone"/"milepebble", a
// `kind='backlog'` row is DB-enforced to have a NULL parent_milestone_id
// (relaxing migration 011's two-way pairing check to a three-way one,
// never dropping either of 011's own original directions), and
// milestone_ref_backlog_product_idx guarantees at most one backlog row
// per (scope_id, product_id) while still allowing two different products
// to each have their own.
func TestMigration014_SchemaContract(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up())

	var scopeID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ('backlog-014-check/repo', 'main') RETURNING id
	`).Scan(&scopeID))
	var productAID, productBID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO product (scope_id, name, vision) VALUES ($1, 'PA', 'V') RETURNING id
	`, scopeID).Scan(&productAID))
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO product (scope_id, name, vision) VALUES ($1, 'PB', 'V') RETURNING id
	`, scopeID).Scan(&productBID))

	// -- kind CHECK: "backlog" is now accepted, with a NULL parent --
	var backlogAID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name, kind, parent_milestone_id) VALUES ($1, $2, '__backlog__', 'backlog', NULL) RETURNING id
	`, scopeID, productAID).Scan(&backlogAID))
	require.NotEqual(t, uuid.Nil, backlogAID)

	// A kind other than "milestone"/"milepebble"/"backlog" is still rejected.
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name, kind) VALUES ($1, $2, 'bogus-kind', 'bogus')
	`, scopeID, productAID)
	assert.Error(t, err, "milestone_ref.kind must still reject a value other than \"milestone\"/\"milepebble\"/\"backlog\" (CHECK constraint)")

	// -- milestone_ref_kind_parent_check: a backlog row must have a NULL parent --
	var milestoneID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name) VALUES ($1, $2, 'M1') RETURNING id
	`, scopeID, productAID).Scan(&milestoneID))
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name, kind, parent_milestone_id) VALUES ($1, $2, 'orphan-backlog', 'backlog', $3)
	`, scopeID, productAID, milestoneID)
	assert.Error(t, err, "a kind=\"backlog\" row with a non-NULL parent_milestone_id must be rejected")

	// -- the pairing's original two directions (011) still hold unchanged --
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name, kind, parent_milestone_id) VALUES ($1, $2, 'orphan-milepebble', 'milepebble', NULL)
	`, scopeID, productAID)
	assert.Error(t, err, "a kind=\"milepebble\" row with a NULL parent_milestone_id must still be rejected (011's own direction, unrelaxed by 014)")
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name, kind, parent_milestone_id) VALUES ($1, $2, 'double-parented-milestone', 'milestone', $3)
	`, scopeID, productAID, milestoneID)
	assert.Error(t, err, "a kind=\"milestone\" row with a non-NULL parent_milestone_id must still be rejected (011's own direction, unrelaxed by 014)")

	// A kind="milepebble" row with a real parent is still accepted.
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name, kind, parent_milestone_id) VALUES ($1, $2, 'cut 1', 'milepebble', $3)
	`, scopeID, productAID, milestoneID)
	assert.NoError(t, err, "a kind=\"milepebble\" row with a real parent must still be accepted")

	// -- milestone_ref_backlog_product_idx: at most one backlog row per (scope, product) --
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name, kind) VALUES ($1, $2, 'second-backlog-name', 'backlog')
	`, scopeID, productAID)
	assert.Error(t, err, "a second kind=\"backlog\" row for the same (scope_id, product_id) must violate milestone_ref_backlog_product_idx")

	// -- but a different product may have its own backlog bucket --
	var backlogBID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name, kind) VALUES ($1, $2, '__backlog__', 'backlog') RETURNING id
	`, scopeID, productBID).Scan(&backlogBID))
	assert.NotEqual(t, backlogAID, backlogBID)

	assert.True(t, indexExists(t, ctx, db, "milestone_ref", "milestone_ref_backlog_product_idx"))
}

// TestMigration014_UpDownRoundTrip is issue #2687's Testing item 8:
// migration 014 applies cleanly (widening milestone_ref to accept a real
// "backlog" row), rolls back cleanly -- deleting every kind="backlog" row
// per 014.down.sql's own documented behavior (mirroring 011.down.sql's
// "a kind row has no meaning once its schema support is gone" posture) and
// reverting the kind CHECK/pairing CHECK/partial index to their post-013
// shape -- and re-applies cleanly a second time. Migrates up to exactly
// version 14 (Migrate(14), not Up()/latest) and rolls back exactly one
// step, mirroring TestMigration011_DownLeavesMilestoneRefIntact and
// TestMigration013_UpDownRoundTrip's own choice.
func TestMigration014_UpDownRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Migrate(14), "apply every migration through exactly 014")

	assert.True(t, indexExists(t, ctx, db, "milestone_ref", "milestone_ref_backlog_product_idx"), "014's Up() must create the backlog partial unique index")

	// Seed a real scope/product/milestone and a real backlog row before
	// rolling back, so Down()'s documented "DELETE FROM milestone_ref WHERE
	// kind = 'backlog'" behavior is exercised for real, not just against an
	// empty table.
	var scopeID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ('backlog-014-roundtrip/repo', 'main') RETURNING id
	`).Scan(&scopeID))
	var productID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO product (scope_id, name, vision) VALUES ($1, 'P', 'V') RETURNING id
	`, scopeID).Scan(&productID))
	var milestoneID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name) VALUES ($1, $2, 'M1') RETURNING id
	`, scopeID, productID).Scan(&milestoneID))
	var backlogID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name, kind) VALUES ($1, $2, '__backlog__', 'backlog') RETURNING id
	`, scopeID, productID).Scan(&backlogID))

	require.NoError(t, runner.Steps(-1), "roll back exactly migration 014")

	version, dirty, err := runner.Version()
	require.NoError(t, err)
	assert.False(t, dirty)
	assert.Equal(t, uint(13), version, "rolling back exactly one step from 14 must land on 13 (013_delivery_shipment)")

	// The backlog row itself must be gone -- 014.down.sql's own documented
	// behavior -- while its unrelated milestone sibling survives.
	var backlogStillExists bool
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM milestone_ref WHERE id = $1)`, backlogID).Scan(&backlogStillExists))
	assert.False(t, backlogStillExists, "014's Down() must delete every kind=\"backlog\" row, per 014.down.sql's own documented behavior")
	var milestoneStillExists bool
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM milestone_ref WHERE id = $1)`, milestoneID).Scan(&milestoneStillExists))
	assert.True(t, milestoneStillExists, "014's Down() must leave an unrelated milestone row untouched")

	assert.True(t, tableExists(t, ctx, db, "milestone_ref"), "014's Down() must leave milestone_ref itself intact")
	assert.False(t, indexExists(t, ctx, db, "milestone_ref", "milestone_ref_backlog_product_idx"), "014's Down() must drop the backlog partial unique index")

	// The kind CHECK must revert to rejecting "backlog" -- 014's widening
	// is fully undone, not left partially in place.
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name, kind) VALUES ($1, $2, 'M2', 'backlog')
	`, scopeID, productID)
	assert.Error(t, err, "after 014's Down(), milestone_ref.kind must reject \"backlog\" again -- the CHECK widening must be fully reverted")

	// The post-013 shape (milestone/milepebble) must still be usable.
	var milepebbleID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name, kind, parent_milestone_id) VALUES ($1, $2, 'cut 1', 'milepebble', $3) RETURNING id
	`, scopeID, productID, milestoneID).Scan(&milepebbleID))
	require.NotEqual(t, uuid.Nil, milepebbleID, "milestone_ref must still accept its post-013 shape after 014's Down()")

	// -- re-apply: must be re-runnable from the rolled-back state --
	require.NoError(t, runner.Steps(1), "re-apply migration 014 after Down()")

	version, dirty, err = runner.Version()
	require.NoError(t, err)
	assert.False(t, dirty)
	assert.Equal(t, uint(14), version)
	assert.True(t, indexExists(t, ctx, db, "milestone_ref", "milestone_ref_backlog_product_idx"), "the backlog partial unique index must exist again after re-applying 014")

	var backlogID2 uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name, kind) VALUES ($1, $2, '__backlog__', 'backlog') RETURNING id
	`, scopeID, productID).Scan(&backlogID2))
	assert.NotEqual(t, uuid.Nil, backlogID2, "milestone_ref.kind must accept \"backlog\" again after re-applying 014")
}

// TestMigration015_UpDownRoundTrip is issue #2719's Testing section's own
// migration test: migration 015 applies cleanly (creating all six
// work-axis tables with a real row in each, exercising the CHECK
// constraints each table's own LB3 note describes), rolls back cleanly --
// dropping all six tables per 015_work_axis.down.sql's documented
// FK-safe order, leaving every earlier table (`milestone_ref`,
// `krill_session`) untouched -- and re-applies cleanly a second time.
// Migrates up to exactly version 15 (Migrate(15), not Up()/latest) and
// rolls back exactly one step, mirroring TestMigration014_UpDownRoundTrip's
// own choice.
func TestMigration015_UpDownRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Migrate(15), "apply every migration through exactly 015")

	for _, table := range []string{"task", "task_dependency", "task_claim", "task_lease_event", "task_attempt", "task_note"} {
		assert.True(t, tableExists(t, ctx, db, table), "015's Up() must create table %q", table)
	}

	// Seed a real scope/product/milestone/session so every work-axis table
	// below can be populated with a real row, not just checked for
	// existence.
	var scopeID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ('work-axis-015-roundtrip/repo', 'main') RETURNING id
	`).Scan(&scopeID))
	var productID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO product (scope_id, name, vision) VALUES ($1, 'P', 'V') RETURNING id
	`, scopeID).Scan(&productID))
	var milestoneID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name, kind) VALUES ($1, $2, 'M1', 'milestone') RETURNING id
	`, scopeID, productID).Scan(&milestoneID))
	var sessionID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO krill_session (scope_id, acting_iss, acting_sub, acting_kind, on_behalf_of_iss, on_behalf_of_sub, on_behalf_of_kind)
		VALUES ($1, 'https://issuer.example.com', 'agent-1', 'service', 'https://issuer.example.com', 'agent-1', 'service')
		RETURNING id
	`, scopeID).Scan(&sessionID))

	const subjectCols = `created_by_acting_iss, created_by_acting_sub, created_by_acting_kind, created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind`
	const subjectVals = `'https://issuer.example.com', 'agent-1', 'service', 'https://issuer.example.com', 'agent-1', 'service'`

	var taskID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO task (scope_id, milestone_id, title, lane_sequence, current_lane, `+subjectCols+`)
		VALUES ($1, $2, 'T1', ARRAY['Scaffold', 'Implementation'], 'Scaffold', `+subjectVals+`)
		RETURNING id
	`, scopeID, milestoneID).Scan(&taskID))

	var dependsOnID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO task (scope_id, milestone_id, title, lane_sequence, current_lane, `+subjectCols+`)
		VALUES ($1, $2, 'T2', ARRAY['Scaffold'], 'Scaffold', `+subjectVals+`)
		RETURNING id
	`, scopeID, milestoneID).Scan(&dependsOnID))

	// task_dependency: a self-loop is rejected by the CHECK; a real edge
	// between two distinct tasks succeeds.
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO task_dependency (scope_id, task_id, depends_on_task_id, `+subjectCols+`) VALUES ($1, $2, $2, `+subjectVals+`)
	`, scopeID, taskID)
	assert.Error(t, err, "task_dependency must reject task_id = depends_on_task_id (self-loop CHECK)")
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO task_dependency (scope_id, task_id, depends_on_task_id, `+subjectCols+`) VALUES ($1, $2, $3, `+subjectVals+`)
	`, scopeID, taskID, dependsOnID)
	require.NoError(t, err)

	// task_claim: released_at and release_reason must be set together or
	// not at all (the pairing CHECK).
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO task_claim (scope_id, task_id, session_id, initial_lease_expires_at, released_at, `+subjectCols+`)
		VALUES ($1, $2, $3, NOW() + interval '1 hour', NOW(), `+subjectVals+`)
	`, scopeID, taskID, sessionID)
	assert.Error(t, err, "task_claim must reject released_at set without release_reason (pairing CHECK)")

	var claimID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO task_claim (scope_id, task_id, session_id, initial_lease_expires_at, `+subjectCols+`)
		VALUES ($1, $2, $3, NOW() + interval '1 hour', `+subjectVals+`)
		RETURNING id
	`, scopeID, taskID, sessionID).Scan(&claimID))

	// task_lease_event: a real heartbeat against the claim above.
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO task_lease_event (scope_id, task_id, claim_id, extended_to, `+subjectCols+`)
		VALUES ($1, $2, $3, NOW() + interval '2 hours', `+subjectVals+`)
	`, scopeID, taskID, claimID)
	require.NoError(t, err)

	// task_attempt: outcome must be one of the four fixed values.
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO task_attempt (scope_id, task_id, claim_id, outcome, `+subjectCols+`) VALUES ($1, $2, $3, 'not-a-real-outcome', `+subjectVals+`)
	`, scopeID, taskID, claimID)
	assert.Error(t, err, "task_attempt.outcome must reject a value outside {claimed, lapsed, abandoned, completed}")
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO task_attempt (scope_id, task_id, claim_id, outcome, `+subjectCols+`) VALUES ($1, $2, $3, 'claimed', `+subjectVals+`)
	`, scopeID, taskID, claimID)
	require.NoError(t, err)

	// task_note: exactly one of task_id / (entity_kind, entity_id) must be
	// populated -- neither, and both, are rejected.
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO task_note (scope_id, kind, body, `+subjectCols+`) VALUES ($1, 'comment', 'orphan note', `+subjectVals+`)
	`, scopeID)
	assert.Error(t, err, "task_note must reject neither task_id nor (entity_kind, entity_id) populated")
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO task_note (scope_id, task_id, entity_kind, entity_id, kind, body, `+subjectCols+`)
		VALUES ($1, $2, 'feature', $3, 'comment', 'both targets', `+subjectVals+`)
	`, scopeID, taskID, uuid.New())
	assert.Error(t, err, "task_note must reject both task_id and (entity_kind, entity_id) populated")
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO task_note (scope_id, task_id, kind, body, `+subjectCols+`) VALUES ($1, $2, 'scope-note', 'a real note', `+subjectVals+`)
	`, scopeID, taskID)
	require.NoError(t, err)

	require.NoError(t, runner.Steps(-1), "roll back exactly migration 015")

	version, dirty, err := runner.Version()
	require.NoError(t, err)
	assert.False(t, dirty)
	assert.Equal(t, uint(14), version, "rolling back exactly one step from 15 must land on 14 (014_backlog_bucket)")

	for _, table := range []string{"task", "task_dependency", "task_claim", "task_lease_event", "task_attempt", "task_note"} {
		assert.False(t, tableExists(t, ctx, db, table), "015's Down() must drop table %q entirely", table)
	}
	assert.True(t, tableExists(t, ctx, db, "milestone_ref"), "015's Down() must leave milestone_ref itself untouched")
	assert.True(t, tableExists(t, ctx, db, "krill_session"), "015's Down() must leave krill_session itself untouched")

	// The milestone_ref/krill_session rows seeded above must still resolve
	// -- 015's Down() must not have cascaded into unrelated earlier tables.
	var milestoneStillExists bool
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM milestone_ref WHERE id = $1)`, milestoneID).Scan(&milestoneStillExists))
	assert.True(t, milestoneStillExists)

	require.NoError(t, runner.Steps(1), "re-apply migration 015 after Down() -- must be re-runnable")

	version, dirty, err = runner.Version()
	require.NoError(t, err)
	assert.False(t, dirty)
	assert.Equal(t, uint(15), version)

	for _, table := range []string{"task", "task_dependency", "task_claim", "task_lease_event", "task_attempt", "task_note"} {
		assert.True(t, tableExists(t, ctx, db, table), "table %q must exist again after re-applying 015", table)
		var count int
		require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&count))
		assert.Equal(t, 0, count, "re-applying 015 creates a fresh, empty table %q -- the rows seeded before the rollback are gone for good", table)
	}
}

// TestMigration016_SchemaContract asserts 016_escalation_axis's own boundary
// calls (issue #2868's Testing section, FR2/NFR1-NFR6): scope_id and both
// LB4 subject pairs are NOT NULL on all three new tables (NFR1, NFR3);
// task_escalation_event's reason/counter/cap pairing CHECK (manual carries
// neither, every other reason carries both, NFR4) is DB-enforced, not just
// a store-layer convention; task's three additive columns
// (thrash_count/current_escalation_id/cancelled_at) and task_note's
// additive current_status column carry the shapes the migration commits
// to; task_claim.release_reason and task_attempt.outcome accept every
// value 016 widens their CHECKs to add; and task_claimable_idx's own
// indexdef carries both new predicates (FR2's index-enforced claimability
// rule).
func TestMigration016_SchemaContract(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up())

	// scope_id and both LB4 subject pairs are NOT NULL on every new table.
	subjectCols := []string{
		"scope_id",
		"created_by_acting_iss", "created_by_acting_sub", "created_by_acting_kind",
		"created_by_on_behalf_of_iss", "created_by_on_behalf_of_sub", "created_by_on_behalf_of_kind",
	}
	for _, table := range []string{"task_escalation_event", "task_intervention_event", "task_note_lifecycle_event"} {
		for _, col := range subjectCols {
			_, nullable := nullableColumn(t, ctx, db, table, col)
			assert.Equal(t, "NO", nullable, "%s.%s must be NOT NULL (NFR1/NFR3)", table, col)
		}
	}

	// task's three additive columns (FR1 thrash counter, FR2/FR3/FR6/FR9
	// current escalation, FR7 dead-letter).
	dataType, nullable := nullableColumn(t, ctx, db, "task", "thrash_count")
	assert.Equal(t, "integer", dataType)
	assert.Equal(t, "NO", nullable, "task.thrash_count must be NOT NULL (it defaults to 0)")
	_, nullable = nullableColumn(t, ctx, db, "task", "current_escalation_id")
	assert.Equal(t, "YES", nullable, "task.current_escalation_id must be nullable -- most tasks are never escalated")
	_, nullable = nullableColumn(t, ctx, db, "task", "cancelled_at")
	assert.Equal(t, "YES", nullable, "task.cancelled_at must be nullable -- most tasks are never dead-lettered")

	// task_note's additive current_status column (FR11).
	_, nullable = nullableColumn(t, ctx, db, "task_note", "current_status")
	assert.Equal(t, "NO", nullable, "task_note.current_status must be NOT NULL (it defaults to 'noted')")

	// task_escalation_event's reason/counter/cap pairing CHECK (NFR4): a
	// manual escalation carries neither counter_value nor cap_value; every
	// automatic reason carries both.
	var scopeID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ('escalation-016-schema/repo', 'main') RETURNING id
	`).Scan(&scopeID))
	var productID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO product (scope_id, name, vision) VALUES ($1, 'P', 'V') RETURNING id
	`, scopeID).Scan(&productID))
	var milestoneID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name, kind) VALUES ($1, $2, 'M1', 'milestone') RETURNING id
	`, scopeID, productID).Scan(&milestoneID))

	const subjectColList = `created_by_acting_iss, created_by_acting_sub, created_by_acting_kind, created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind`
	const subjectValList = `'https://issuer.example.com', 'agent-1', 'service', 'https://issuer.example.com', 'agent-1', 'service'`

	var taskID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO task (scope_id, milestone_id, title, lane_sequence, current_lane, `+subjectColList+`)
		VALUES ($1, $2, 'T1', ARRAY['Scaffold', 'Implementation'], 'Scaffold', `+subjectValList+`)
		RETURNING id
	`, scopeID, milestoneID).Scan(&taskID))

	_, err = db.Pool.Exec(ctx, `
		INSERT INTO task_escalation_event (scope_id, task_id, reason, lane_at_escalation, `+subjectColList+`)
		VALUES ($1, $2, 'manual', 'Scaffold', `+subjectValList+`)
	`, scopeID, taskID)
	assert.NoError(t, err, "reason='manual' with counter_value/cap_value both NULL must be accepted")

	_, err = db.Pool.Exec(ctx, `
		INSERT INTO task_escalation_event (scope_id, task_id, reason, lane_at_escalation, `+subjectColList+`)
		VALUES ($1, $2, 'thrash-cap', 'Scaffold', `+subjectValList+`)
	`, scopeID, taskID)
	assert.Error(t, err, "reason='thrash-cap' with counter_value/cap_value both NULL must be rejected -- the pairing CHECK")

	_, err = db.Pool.Exec(ctx, `
		INSERT INTO task_escalation_event (scope_id, task_id, reason, counter_value, cap_value, lane_at_escalation, `+subjectColList+`)
		VALUES ($1, $2, 'thrash-cap', 3, 3, 'Scaffold', `+subjectValList+`)
	`, scopeID, taskID)
	assert.NoError(t, err, "reason='thrash-cap' with counter_value/cap_value both set must be accepted")

	_, err = db.Pool.Exec(ctx, `
		INSERT INTO task_escalation_event (scope_id, task_id, reason, counter_value, lane_at_escalation, `+subjectColList+`)
		VALUES ($1, $2, 'manual', 3, 'Scaffold', `+subjectValList+`)
	`, scopeID, taskID)
	assert.Error(t, err, "reason='manual' with counter_value set must be rejected -- manual has no causing counter")

	// task_claim.release_reason's widened CHECK accepts the three new
	// operator-driven values and still rejects an unknown one.
	var sessionID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO krill_session (scope_id, acting_iss, acting_sub, acting_kind, on_behalf_of_iss, on_behalf_of_sub, on_behalf_of_kind)
		VALUES ($1, 'https://issuer.example.com', 'agent-1', 'service', 'https://issuer.example.com', 'agent-1', 'service')
		RETURNING id
	`, scopeID).Scan(&sessionID))

	for _, reason := range []string{"release", "cancel", "escalate"} {
		_, err = db.Pool.Exec(ctx, `
			INSERT INTO task_claim (scope_id, task_id, session_id, initial_lease_expires_at, released_at, release_reason, `+subjectColList+`)
			VALUES ($1, $2, $3, NOW() + interval '1 hour', NOW(), $4, `+subjectValList+`)
		`, scopeID, taskID, sessionID, reason)
		assert.NoError(t, err, "task_claim.release_reason must accept %q", reason)
	}
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO task_claim (scope_id, task_id, session_id, initial_lease_expires_at, released_at, release_reason, `+subjectColList+`)
		VALUES ($1, $2, $3, NOW() + interval '1 hour', NOW(), 'not-a-real-reason', `+subjectValList+`)
	`, scopeID, taskID, sessionID)
	assert.Error(t, err, "task_claim.release_reason must still reject an unknown value")

	// task_attempt.outcome's widened CHECK accepts the two new values and
	// still rejects an unknown one.
	var claimID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO task_claim (scope_id, task_id, session_id, initial_lease_expires_at, `+subjectColList+`)
		VALUES ($1, $2, $3, NOW() + interval '1 hour', `+subjectValList+`)
		RETURNING id
	`, scopeID, taskID, sessionID).Scan(&claimID))

	for _, outcome := range []string{"released", "force-closed"} {
		_, err = db.Pool.Exec(ctx, `
			INSERT INTO task_attempt (scope_id, task_id, claim_id, outcome, `+subjectColList+`) VALUES ($1, $2, $3, $4, `+subjectValList+`)
		`, scopeID, taskID, claimID, outcome)
		assert.NoError(t, err, "task_attempt.outcome must accept %q", outcome)
	}
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO task_attempt (scope_id, task_id, claim_id, outcome, `+subjectColList+`) VALUES ($1, $2, $3, 'not-a-real-outcome', `+subjectValList+`)
	`, scopeID, taskID, claimID)
	assert.Error(t, err, "task_attempt.outcome must still reject an unknown value")

	// task_intervention_event: escalation_event_id is optional, and a real
	// row referencing the escalation seeded above succeeds.
	var escalationID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT id FROM task_escalation_event WHERE task_id = $1 AND reason = 'manual' LIMIT 1
	`, taskID).Scan(&escalationID))
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO task_intervention_event (scope_id, task_id, action, escalation_event_id, `+subjectColList+`)
		VALUES ($1, $2, 'requeue', $3, `+subjectValList+`)
	`, scopeID, taskID, escalationID)
	assert.NoError(t, err, "task_intervention_event must accept a real escalation_event_id")

	_, err = db.Pool.Exec(ctx, `
		INSERT INTO task_intervention_event (scope_id, task_id, action, `+subjectColList+`)
		VALUES ($1, $2, 'not-a-real-action', `+subjectValList+`)
	`, scopeID, taskID)
	assert.Error(t, err, "task_intervention_event.action must reject a value outside {requeue, cancel, release, escalate}")

	// task_note_lifecycle_event: status must be one of the four fixed
	// values (FR11).
	var noteID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO task_note (scope_id, task_id, kind, body, `+subjectColList+`) VALUES ($1, $2, 'comment', 'a note', `+subjectValList+`)
		RETURNING id
	`, scopeID, taskID).Scan(&noteID))
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO task_note_lifecycle_event (scope_id, note_id, status, `+subjectColList+`) VALUES ($1, $2, 'not-a-real-status', `+subjectValList+`)
	`, scopeID, noteID)
	assert.Error(t, err, "task_note_lifecycle_event.status must reject a value outside {noted, carried-over, deferred, closed}")
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO task_note_lifecycle_event (scope_id, note_id, status, `+subjectColList+`) VALUES ($1, $2, 'carried-over', `+subjectValList+`)
	`, scopeID, noteID)
	assert.NoError(t, err, "task_note_lifecycle_event.status must accept 'carried-over'")

	// task_claimable_idx's own indexdef must carry both new predicates
	// (FR2's index-enforced claimability rule, not just an application
	// check).
	var indexDef string
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT indexdef FROM pg_indexes WHERE indexname = 'task_claimable_idx'
	`).Scan(&indexDef))
	assert.Contains(t, indexDef, "current_escalation_id", "task_claimable_idx must exclude escalated tasks in the index itself (FR2)")
	assert.Contains(t, indexDef, "cancelled_at", "task_claimable_idx must exclude cancelled tasks in the index itself (FR7)")
}

// TestMigration016_UpDownRoundTrip is issue #2868's Testing section's own
// migration test, mirroring TestMigration015_UpDownRoundTrip's shape:
// migration 016 applies cleanly (creating all three new tables with a real
// row in each, exercising the CHECK constraints each table's own LB3 note
// describes, plus the additive columns and widened CHECKs on
// task/task_note/task_claim/task_attempt), rolls back cleanly --
// 016_escalation_axis.down.sql's documented FK-safe order -- leaving every
// earlier table (`task`, `task_note`, `milestone_ref`) intact minus the
// columns 016 added, and re-applies cleanly a second time. Migrates up to
// exactly version 16 (Migrate(16), not Up()/latest) and rolls back exactly
// one step.
func TestMigration016_UpDownRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Migrate(16), "apply every migration through exactly 016")

	for _, table := range []string{"task_escalation_event", "task_intervention_event", "task_note_lifecycle_event"} {
		assert.True(t, tableExists(t, ctx, db, table), "016's Up() must create table %q", table)
	}

	// Seed a real scope/product/milestone/session/task/note so every new
	// table below can be populated with a real row, not just checked for
	// existence.
	var scopeID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ('escalation-016-roundtrip/repo', 'main') RETURNING id
	`).Scan(&scopeID))
	var productID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO product (scope_id, name, vision) VALUES ($1, 'P', 'V') RETURNING id
	`, scopeID).Scan(&productID))
	var milestoneID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name, kind) VALUES ($1, $2, 'M1', 'milestone') RETURNING id
	`, scopeID, productID).Scan(&milestoneID))
	var sessionID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO krill_session (scope_id, acting_iss, acting_sub, acting_kind, on_behalf_of_iss, on_behalf_of_sub, on_behalf_of_kind)
		VALUES ($1, 'https://issuer.example.com', 'agent-1', 'service', 'https://issuer.example.com', 'agent-1', 'service')
		RETURNING id
	`, scopeID).Scan(&sessionID))

	const subjectCols = `created_by_acting_iss, created_by_acting_sub, created_by_acting_kind, created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind`
	const subjectVals = `'https://issuer.example.com', 'agent-1', 'service', 'https://issuer.example.com', 'agent-1', 'service'`

	var taskID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO task (scope_id, milestone_id, title, lane_sequence, current_lane, `+subjectCols+`)
		VALUES ($1, $2, 'T1', ARRAY['Scaffold', 'Implementation'], 'Scaffold', `+subjectVals+`)
		RETURNING id
	`, scopeID, milestoneID).Scan(&taskID))

	// task's additive columns: thrash_count defaults to 0, current_escalation_id/cancelled_at are settable.
	var thrashCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT thrash_count FROM task WHERE id = $1`, taskID).Scan(&thrashCount))
	assert.Equal(t, 0, thrashCount, "task.thrash_count must default to 0")

	var escalationID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO task_escalation_event (scope_id, task_id, reason, lane_at_escalation, `+subjectCols+`)
		VALUES ($1, $2, 'manual', 'Scaffold', `+subjectVals+`)
		RETURNING id
	`, scopeID, taskID).Scan(&escalationID))
	_, err = db.Pool.Exec(ctx, `UPDATE task SET current_escalation_id = $1 WHERE id = $2`, escalationID, taskID)
	require.NoError(t, err)

	// release_reason/outcome below deliberately use values 015 already
	// accepted ("complete"/"completed"), not one of 016's widened-only
	// values ("escalate"/"force-closed") -- 016's down.sql narrows these
	// CHECKs back to their 015 shape without deleting any row, so a row
	// carrying a widened-only value would make the rollback below itself
	// fail with a check_violation; TestMigration016_SchemaContract is
	// where the widened values themselves are exercised.
	var claimID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO task_claim (scope_id, task_id, session_id, initial_lease_expires_at, released_at, release_reason, `+subjectCols+`)
		VALUES ($1, $2, $3, NOW() + interval '1 hour', NOW(), 'complete', `+subjectVals+`)
		RETURNING id
	`, scopeID, taskID, sessionID).Scan(&claimID))

	_, err = db.Pool.Exec(ctx, `
		INSERT INTO task_attempt (scope_id, task_id, claim_id, outcome, `+subjectCols+`) VALUES ($1, $2, $3, 'completed', `+subjectVals+`)
	`, scopeID, taskID, claimID)
	require.NoError(t, err)

	_, err = db.Pool.Exec(ctx, `
		INSERT INTO task_intervention_event (scope_id, task_id, action, escalation_event_id, `+subjectCols+`)
		VALUES ($1, $2, 'escalate', $3, `+subjectVals+`)
	`, scopeID, taskID, escalationID)
	require.NoError(t, err)

	var noteID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO task_note (scope_id, task_id, kind, body, `+subjectCols+`) VALUES ($1, $2, 'comment', 'a note', `+subjectVals+`)
		RETURNING id
	`, scopeID, taskID).Scan(&noteID))
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO task_note_lifecycle_event (scope_id, note_id, status, `+subjectCols+`) VALUES ($1, $2, 'deferred', `+subjectVals+`)
	`, scopeID, noteID)
	require.NoError(t, err)

	require.NoError(t, runner.Steps(-1), "roll back exactly migration 016")

	version, dirty, err := runner.Version()
	require.NoError(t, err)
	assert.False(t, dirty)
	assert.Equal(t, uint(15), version, "rolling back exactly one step from 16 must land on 15 (015_work_axis)")

	for _, table := range []string{"task_escalation_event", "task_intervention_event", "task_note_lifecycle_event"} {
		assert.False(t, tableExists(t, ctx, db, table), "016's Down() must drop table %q entirely", table)
	}
	assert.True(t, tableExists(t, ctx, db, "task"), "016's Down() must leave task itself untouched")
	assert.True(t, tableExists(t, ctx, db, "task_note"), "016's Down() must leave task_note itself untouched")

	// 016's additive columns must be gone.
	taskCols := columnNames(t, ctx, db, "task")
	assert.NotContains(t, taskCols, "thrash_count", "016's Down() must drop task.thrash_count")
	assert.NotContains(t, taskCols, "current_escalation_id", "016's Down() must drop task.current_escalation_id")
	assert.NotContains(t, taskCols, "cancelled_at", "016's Down() must drop task.cancelled_at")
	noteCols := columnNames(t, ctx, db, "task_note")
	assert.NotContains(t, noteCols, "current_status", "016's Down() must drop task_note.current_status")

	// The task/task_note rows seeded above must still resolve -- 016's
	// Down() must not have cascaded into unrelated earlier tables.
	var taskStillExists bool
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM task WHERE id = $1)`, taskID).Scan(&taskStillExists))
	assert.True(t, taskStillExists)
	var noteStillExists bool
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM task_note WHERE id = $1)`, noteID).Scan(&noteStillExists))
	assert.True(t, noteStillExists)

	// task_claim.release_reason and task_attempt.outcome must revert to
	// their pre-016 CHECKs -- the widening must be fully undone.
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO task_claim (scope_id, task_id, session_id, initial_lease_expires_at, released_at, release_reason, `+subjectCols+`)
		VALUES ($1, $2, $3, NOW() + interval '1 hour', NOW(), 'escalate', `+subjectVals+`)
	`, scopeID, taskID, sessionID)
	assert.Error(t, err, "after 016's Down(), task_claim.release_reason must reject 'escalate' again")
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO task_attempt (scope_id, task_id, claim_id, outcome, `+subjectCols+`) VALUES ($1, $2, $3, 'force-closed', `+subjectVals+`)
	`, scopeID, taskID, claimID)
	assert.Error(t, err, "after 016's Down(), task_attempt.outcome must reject 'force-closed' again")

	// task_claimable_idx must revert to 015's single-predicate shape.
	var indexDef string
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT indexdef FROM pg_indexes WHERE indexname = 'task_claimable_idx'
	`).Scan(&indexDef))
	assert.NotContains(t, indexDef, "current_escalation_id", "016's Down() must restore task_claimable_idx to 015's single-predicate shape")
	assert.NotContains(t, indexDef, "cancelled_at", "016's Down() must restore task_claimable_idx to 015's single-predicate shape")

	// -- re-apply: must be re-runnable from the rolled-back state --
	require.NoError(t, runner.Steps(1), "re-apply migration 016 after Down() -- must be re-runnable")

	version, dirty, err = runner.Version()
	require.NoError(t, err)
	assert.False(t, dirty)
	assert.Equal(t, uint(16), version)

	for _, table := range []string{"task_escalation_event", "task_intervention_event", "task_note_lifecycle_event"} {
		assert.True(t, tableExists(t, ctx, db, table), "table %q must exist again after re-applying 016", table)
		var count int
		require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&count))
		assert.Equal(t, 0, count, "re-applying 016 creates a fresh, empty table %q -- the rows seeded before the rollback are gone for good", table)
	}

	taskCols = columnNames(t, ctx, db, "task")
	assert.Contains(t, taskCols, "thrash_count", "016's additive task columns must exist again after re-applying 016")
	assert.Contains(t, taskCols, "current_escalation_id")
	assert.Contains(t, taskCols, "cancelled_at")
	noteCols = columnNames(t, ctx, db, "task_note")
	assert.Contains(t, noteCols, "current_status", "016's additive task_note column must exist again after re-applying 016")
}

// TestMigration017_BackfillMatchesOldRenderOrder is issue #2969's own
// Testing ask: migration 017's backfill must assign every pre-existing
// Feature/LoadBearingDecision the exact number krill/render's old
// numberByOrder would already have rendered it as -- (feature_set.position,
// feature_set.name, <table>.position, <table>.name), scoped per product --
// so landing this migration changes no existing citation. Seeds two
// FeatureSets under one Product (deliberately out of alphabetical name
// order but in the position order that decides numbering) with Features
// and LoadBearingDecisions in each, migrates up to exactly 016 (before
// display_number exists) so the rows are seeded on a schema with no such
// column, then migrates to 017 and asserts the backfilled numbers.
func TestMigration017_BackfillMatchesOldRenderOrder(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Migrate(16), "apply every migration through exactly 016 -- before display_number exists")

	var scopeID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ('display-number-017/repo', 'main') RETURNING id
	`).Scan(&scopeID))
	var productID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO product (scope_id, name, vision) VALUES ($1, 'P', 'V') RETURNING id
	`, scopeID).Scan(&productID))

	// Two FeatureSets, position 0 and 1 -- "Zeta" sorts after "Alpha" by
	// name, so this also proves numbering follows position, not name, when
	// both are present (matching numberByOrder's own (position, name) sort
	// key).
	var fsZeta, fsAlpha uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO feature_set (scope_id, product_id, name, position) VALUES ($1, $2, 'Zeta', 0) RETURNING id
	`, scopeID, productID).Scan(&fsZeta))
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO feature_set (scope_id, product_id, name, position) VALUES ($1, $2, 'Alpha', 1) RETURNING id
	`, scopeID, productID).Scan(&fsAlpha))

	var featureZ1, featureZ2, featureA1 uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO feature (scope_id, feature_set_id, name, position) VALUES ($1, $2, 'Z1', 0) RETURNING id
	`, scopeID, fsZeta).Scan(&featureZ1))
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO feature (scope_id, feature_set_id, name, position) VALUES ($1, $2, 'Z2', 1) RETURNING id
	`, scopeID, fsZeta).Scan(&featureZ2))
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO feature (scope_id, feature_set_id, name, position) VALUES ($1, $2, 'A1', 0) RETURNING id
	`, scopeID, fsAlpha).Scan(&featureA1))

	var decisionZ1, decisionA1 uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO load_bearing_decision (scope_id, feature_set_id, name, position) VALUES ($1, $2, 'LB Zeta', 0) RETURNING id
	`, scopeID, fsZeta).Scan(&decisionZ1))
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO load_bearing_decision (scope_id, feature_set_id, name, position) VALUES ($1, $2, 'LB Alpha', 0) RETURNING id
	`, scopeID, fsAlpha).Scan(&decisionA1))

	require.NoError(t, runner.Migrate(17), "apply migration 017 -- backfills display_number on the rows seeded above")

	assertDisplayNumber := func(table string, id uuid.UUID, want int) {
		t.Helper()
		var got int
		require.NoError(t, db.Pool.QueryRow(ctx, `SELECT display_number FROM `+table+` WHERE id = $1`, id).Scan(&got))
		assert.Equal(t, want, got, "%s id %s must backfill to display_number %d (feature_set position order, not name)", table, id, want)
	}
	assertDisplayNumber("feature", featureZ1, 1)
	assertDisplayNumber("feature", featureZ2, 2)
	assertDisplayNumber("feature", featureA1, 3)
	assertDisplayNumber("load_bearing_decision", decisionZ1, 1)
	assertDisplayNumber("load_bearing_decision", decisionA1, 2)
}

// TestMigration017_UpDownRoundTrip proves 017_display_numbers rolls back
// cleanly (dropping both new columns, leaving every other column and every
// row untouched) and is re-runnable -- backfilling to the same numbers a
// second time, since the underlying position/name order it reads from
// never changed.
func TestMigration017_UpDownRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Migrate(17))

	var scopeID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ('display-number-017-roundtrip/repo', 'main') RETURNING id
	`).Scan(&scopeID))
	var productID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO product (scope_id, name, vision) VALUES ($1, 'P', 'V') RETURNING id
	`, scopeID).Scan(&productID))
	var fsID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO feature_set (scope_id, product_id, name, position) VALUES ($1, $2, 'Core', 0) RETURNING id
	`, scopeID, productID).Scan(&fsID))
	var featureID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO feature (scope_id, feature_set_id, name, position, display_number) VALUES ($1, $2, 'F1', 0, 1) RETURNING id
	`, scopeID, fsID).Scan(&featureID))

	require.NoError(t, runner.Steps(-1), "roll back exactly migration 017")

	version, dirty, err := runner.Version()
	require.NoError(t, err)
	assert.False(t, dirty)
	assert.Equal(t, uint(16), version)

	featureCols := columnNames(t, ctx, db, "feature")
	assert.NotContains(t, featureCols, "display_number", "017's Down() must drop feature.display_number")
	decisionCols := columnNames(t, ctx, db, "load_bearing_decision")
	assert.NotContains(t, decisionCols, "display_number", "017's Down() must drop load_bearing_decision.display_number")

	var featureName string
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT name FROM feature WHERE id = $1`, featureID).Scan(&featureName))
	assert.Equal(t, "F1", featureName, "rolling back 017 must not touch feature rows beyond dropping the column")

	require.NoError(t, runner.Steps(1), "re-apply migration 017 after Down() -- must be re-runnable")

	version, dirty, err = runner.Version()
	require.NoError(t, err)
	assert.False(t, dirty)
	assert.Equal(t, uint(17), version)

	var displayNumber int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT display_number FROM feature WHERE id = $1`, featureID).Scan(&displayNumber))
	assert.Equal(t, 1, displayNumber, "re-applying 017 must backfill the same row to the same display_number")
}
