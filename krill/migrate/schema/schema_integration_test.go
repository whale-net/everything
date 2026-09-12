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
// embedded (007_ui_sessions, mcpauth auth-flow gap): Up() creates every
// table including `krill_session`, `milestone_ref`, `entity_milestone`,
// `pointer_artifact`, `mcp_credential`/`mcp_oauth_client`/`mcp_auth_code`,
// and `ui_sessions`, Down() drops all of them (a clean database), and
// Up() again succeeds a second time from that clean state -- the
// migration set is re-runnable through //libs/go/migrate, not a one-shot
// script. The hardcoded latest-version assertion below must be bumped
// whenever a new migration lands (it was 1 for 001_scope alone, issue
// #2487; it is 7 now that 002_spec_entities, 003_session,
// 004_milestone_assoc, 005_pointer_artifact, 006_mcpauth_credential, and
// 007_ui_sessions have all landed).
func TestMigrations_UpDownUp_LeavesCleanDatabaseAndIsRerunnable(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)

	latest, err := runner.LatestVersion()
	require.NoError(t, err)
	require.Equal(t, uint(7), latest, "expected the latest migration source version to be 7 (001_scope, 002_spec_entities, 003_session, 004_milestone_assoc, 005_pointer_artifact, 006_mcpauth_credential, 007_ui_sessions) -- update this test if a later migration has since landed")

	// -- Up: scope, krill_session, the milestone tables, pointer_artifact,
	// the mcpauth tables, and ui_sessions must exist, version must land
	// clean at the latest --
	require.NoError(t, runner.Up(), "apply migrations 001-007")

	version, dirty, err := runner.Version()
	require.NoError(t, err)
	assert.False(t, dirty)
	assert.Equal(t, uint(7), version)

	assert.True(t, tableExists(t, ctx, db, "scope"), "expected table \"scope\" to exist after Up()")
	assert.True(t, tableExists(t, ctx, db, "krill_session"), "expected table \"krill_session\" to exist after Up() (003_session, issue #2489)")
	assert.True(t, tableExists(t, ctx, db, "milestone_ref"), "expected table \"milestone_ref\" to exist after Up() (004_milestone_assoc, issue #2492)")
	assert.True(t, tableExists(t, ctx, db, "entity_milestone"), "expected table \"entity_milestone\" to exist after Up() (004_milestone_assoc, issue #2492)")
	assert.True(t, tableExists(t, ctx, db, "pointer_artifact"), "expected table \"pointer_artifact\" to exist after Up() (005_pointer_artifact, issue #2496)")
	assert.True(t, tableExists(t, ctx, db, "mcp_credential"), "expected table \"mcp_credential\" to exist after Up() (006_mcpauth_credential)")
	assert.True(t, tableExists(t, ctx, db, "mcp_oauth_client"), "expected table \"mcp_oauth_client\" to exist after Up() (006_mcpauth_credential)")
	assert.True(t, tableExists(t, ctx, db, "mcp_auth_code"), "expected table \"mcp_auth_code\" to exist after Up() (006_mcpauth_credential)")
	assert.True(t, tableExists(t, ctx, db, "ui_sessions"), "expected table \"ui_sessions\" to exist after Up() (007_ui_sessions)")

	// -- Down: every table must be gone -------------------------------------
	require.NoError(t, runner.Down(), "roll back every migration")

	assert.False(t, tableExists(t, ctx, db, "scope"), "expected table \"scope\" to be dropped after Down() -- a clean database")
	assert.False(t, tableExists(t, ctx, db, "krill_session"), "expected table \"krill_session\" to be dropped after Down() -- a clean database")
	assert.False(t, tableExists(t, ctx, db, "milestone_ref"), "expected table \"milestone_ref\" to be dropped after Down() -- a clean database")
	assert.False(t, tableExists(t, ctx, db, "entity_milestone"), "expected table \"entity_milestone\" to be dropped after Down() -- a clean database")
	assert.False(t, tableExists(t, ctx, db, "pointer_artifact"), "expected table \"pointer_artifact\" to be dropped after Down() -- a clean database")
	assert.False(t, tableExists(t, ctx, db, "mcp_credential"), "expected table \"mcp_credential\" to be dropped after Down() -- a clean database")
	assert.False(t, tableExists(t, ctx, db, "mcp_oauth_client"), "expected table \"mcp_oauth_client\" to be dropped after Down() -- a clean database")
	assert.False(t, tableExists(t, ctx, db, "mcp_auth_code"), "expected table \"mcp_auth_code\" to be dropped after Down() -- a clean database")
	assert.False(t, tableExists(t, ctx, db, "ui_sessions"), "expected table \"ui_sessions\" to be dropped after Down() -- a clean database")

	// -- Up again: re-runnable from the clean state --------------------------
	require.NoError(t, runner.Up(), "re-apply every migration after Down() -- must be re-runnable")

	version, dirty, err = runner.Version()
	require.NoError(t, err)
	assert.False(t, dirty)
	assert.Equal(t, uint(7), version)

	assert.True(t, tableExists(t, ctx, db, "scope"), "expected table \"scope\" to exist again after the second Up()")
	assert.True(t, tableExists(t, ctx, db, "krill_session"), "expected table \"krill_session\" to exist again after the second Up()")
	assert.True(t, tableExists(t, ctx, db, "milestone_ref"), "expected table \"milestone_ref\" to exist again after the second Up()")
	assert.True(t, tableExists(t, ctx, db, "entity_milestone"), "expected table \"entity_milestone\" to exist again after the second Up()")
	assert.True(t, tableExists(t, ctx, db, "pointer_artifact"), "expected table \"pointer_artifact\" to exist again after the second Up()")
	assert.True(t, tableExists(t, ctx, db, "mcp_credential"), "expected table \"mcp_credential\" to exist again after the second Up()")
	assert.True(t, tableExists(t, ctx, db, "mcp_oauth_client"), "expected table \"mcp_oauth_client\" to exist again after the second Up()")
	assert.True(t, tableExists(t, ctx, db, "mcp_auth_code"), "expected table \"mcp_auth_code\" to exist again after the second Up()")
	assert.True(t, tableExists(t, ctx, db, "ui_sessions"), "expected table \"ui_sessions\" to exist again after the second Up()")
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
		"feature_set":            "product_id",
		"feature":                "feature_set_id",
		"requirement":            "feature_id",
		"load_bearing_decision":  "feature_set_id",
		"persona":                "product_id",
		"non_goal":               "product_id",
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
		INSERT INTO feature (scope_id, feature_set_id, name) VALUES ($1, $2, 'F') RETURNING id
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
		"schema_migrations", "scope", "krill_session", "milestone_ref", "entity_milestone", "pointer_artifact",
		"mcp_credential", "mcp_oauth_client", "mcp_auth_code", "ui_sessions",
	}, specTables...)
	sort.Strings(expected)
	assert.Equal(t, expected, tables, "the public schema must contain exactly scope + the seven spec tables + krill_session (003_session, issue #2489) + milestone_ref + entity_milestone (004_milestone_assoc, issue #2492) + pointer_artifact (005_pointer_artifact, issue #2496) + mcp_credential/mcp_oauth_client/mcp_auth_code (006_mcpauth_credential) + ui_sessions (007_ui_sessions) + golang-migrate's schema_migrations -- no fourth parallel table (e.g. \"capability\") and no join/bridge table for parentage (LB2)")

	// No display-number-shaped column on any spec table -- LB2's own
	// vocabulary for the trap this guards against.
	forbidden := []string{"display_number", "fr_number", "nfr_number", "lb_number", "c_number", "ordinal"}
	for _, table := range specTables {
		cols := columnNames(t, ctx, db, table)
		for _, col := range cols {
			for _, bad := range forbidden {
				assert.NotEqual(t, bad, col, "%s must not have a stored display-number column %q (LB2's trap -- display numbers are rendered, never persisted)", table, bad)
			}
		}
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
// column and no milepebble breakdown or other authoring field -- those are
// M3's (C13, C28) -- and no spec entity table (`feature`, `requirement`,
// `load_bearing_decision`) may have acquired a `milestone_id` column. It
// also asserts the shape the importer and MilestoneStore depend on:
// scope_id NOT NULL on both new tables (LB1), the milestone_ref
// (scope_id, product_id, name) uniqueness that makes GetOrCreateRef
// idempotent, the entity_milestone (entity_id, milestone_id) uniqueness
// that makes AddAssociation idempotent, and milestone_id's real FK to
// milestone_ref(id).
func TestMigration004_SchemaContract(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up())

	// -- LB6's trap: no status/milepebble/authoring surface on milestone_ref --
	milestoneRefCols := columnNames(t, ctx, db, "milestone_ref")
	for _, forbidden := range []string{"status", "milepebble", "milepebble_id", "state", "author", "authored_by", "authored_at"} {
		assert.NotContains(t, milestoneRefCols, forbidden,
			"milestone_ref must not carry a %q column -- status/milepebbles/authoring fields are M3's (C13, C28), not M1's (LB6's own note)", forbidden)
	}
	assert.ElementsMatch(t, []string{"id", "scope_id", "product_id", "name", "created_at"}, milestoneRefCols,
		"milestone_ref must be exactly the bare reference shape LB6 specifies -- no more, no less")

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
		INSERT INTO feature (scope_id, feature_set_id, name) VALUES ($1, $2, 'f') RETURNING id
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
