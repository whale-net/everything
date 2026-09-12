//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. It exists to prove migration 001 (issue #2109's whole schema
// commitment) actually applies against real Postgres and reverses cleanly
// -- the issue's Testing item "001 up then down leaves a clean database;
// up is re-runnable through //libs/go/migrate". See
// audience_score_system/migrate/schema/schema_integration_test.go for the
// precedent this mirrors.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //whagent_net/migrate/schema:schema_integration_test --test_output=all
package schema_test

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
	"github.com/whale-net/everything/whagent_net/migrate/schema"
)

// everyTable is every table migration 001 creates (issue #2109's schema
// contract) -- used both to assert Up() created all of them and Down()
// left none behind.
var everyTable = []string{
	"agent_definition",
	"sessions",
	"transcript_event",
	"turn_context",
	"turn_usage",
	"session_agent",
	"tool_call_idempotency",
	"ui_sessions",
	"model_definition",
	"grpcauth_delegated_grant",
	"grpcauth_grant_index",
}

func tableExists(t *testing.T, ctx context.Context, db *dbtest.Postgres, table string) bool {
	t.Helper()
	var exists bool
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1)
	`, table).Scan(&exists))
	return exists
}

// TestMigration001_UpDownUp_LeavesCleanDatabaseAndIsRerunnable proves the
// whole up/down/up lifecycle for the M1 schema: Up() creates every table
// the issue's contract lists, Down() drops every one of them (a clean
// database, not merely "some tables gone"), and Up() again succeeds a
// second time from that clean state -- migration 001 is re-runnable
// through //libs/go/migrate, not a one-shot script.
func TestMigration001_UpDownUp_LeavesCleanDatabaseAndIsRerunnable(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)

	latest, err := runner.LatestVersion()
	require.NoError(t, err)
	require.Equal(t, uint(9), latest, "expected the latest migration source version to be 9 (001_initial_schema + 002_transcript_archive, issue #2240 + 003_sessions_list_index, issue #2241 + 004_mcpauth_credential, issue #2245 + 005_ui_sessions, issue #2288 + 006_model_definition + 007_agent_definition_domain, issue #2424 + 008_delegated_grant, issue #2426 + 009_mcpauth_cutover, issue #2434) -- update this test if a later migration has since landed")

	// -- Up: every table must exist, version must land clean at the latest --
	require.NoError(t, runner.Up(), "apply every migration")

	version, dirty, err := runner.Version()
	require.NoError(t, err)
	assert.False(t, dirty)
	assert.Equal(t, uint(9), version)

	for _, table := range everyTable {
		assert.True(t, tableExists(t, ctx, db, table), "expected table %q to exist after Up()", table)
	}

	// 005_ui_sessions (issue #2288): confirm the expires_at index landed
	// alongside the table, not just the table itself.
	var hasUISessionsExpiresAtIndex bool
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE tablename = 'ui_sessions' AND indexname = 'idx_ui_sessions_expires_at')
	`).Scan(&hasUISessionsExpiresAtIndex))
	assert.True(t, hasUISessionsExpiresAtIndex, "expected idx_ui_sessions_expires_at to exist after Up()")

	// -- Down: every table must be gone, not just some of them -------------
	require.NoError(t, runner.Down(), "roll back every migration")

	for _, table := range everyTable {
		assert.False(t, tableExists(t, ctx, db, table), "expected table %q to be dropped after Down() -- a clean database", table)
	}

	// -- Up again: re-runnable from the clean state -------------------------
	require.NoError(t, runner.Up(), "re-apply every migration after Down() -- must be re-runnable")

	version, dirty, err = runner.Version()
	require.NoError(t, err)
	assert.False(t, dirty)
	assert.Equal(t, uint(9), version)

	for _, table := range everyTable {
		assert.True(t, tableExists(t, ctx, db, table), "expected table %q to exist again after the second Up()", table)
	}
}

// TestMigration001_SchemaContract asserts the specific column shapes the
// issue's Validation section calls out by name, so a future migration that
// accidentally renames/retypes one of these columns fails loudly here
// rather than only being caught by a store-level test exercising the same
// column indirectly.
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

	// subject_* and on_behalf_of_* are distinct NOT NULL column sets (NFR3).
	for _, col := range []string{"subject_iss", "subject_sub", "subject_kind", "on_behalf_of_iss", "on_behalf_of_sub", "on_behalf_of_kind"} {
		_, nullable := nullableColumn(t, ctx, db, "sessions", col)
		assert.Equal(t, "NO", nullable, "sessions.%s must be NOT NULL (NFR3)", col)
	}

	// session_agent uses valid_from/valid_to with the partial index (NFR6).
	var hasPartialIndex bool
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_indexes
			WHERE tablename = 'session_agent' AND indexdef LIKE '%valid_to IS NULL%'
		)
	`).Scan(&hasPartialIndex))
	assert.True(t, hasPartialIndex, "session_agent must have a partial unique index on valid_to IS NULL (NFR6)")

	// turn_usage.cost_usd is NOT NULL with a cost_estimated flag and a
	// generation_id column (LB6).
	_, nullable := nullableColumn(t, ctx, db, "turn_usage", "cost_usd")
	assert.Equal(t, "NO", nullable, "turn_usage.cost_usd must be NOT NULL (LB6)")
	_, nullable = nullableColumn(t, ctx, db, "turn_usage", "cost_estimated")
	assert.Equal(t, "NO", nullable)
	_, nullable = nullableColumn(t, ctx, db, "turn_usage", "generation_id")
	assert.Equal(t, "YES", nullable, "turn_usage.generation_id must be nullable")

	// transcript_event has event_id, seq, turn, type, payload (LB1).
	for _, col := range []string{"event_id", "seq", "turn", "type", "payload"} {
		_, nullable := nullableColumn(t, ctx, db, "transcript_event", col)
		assert.Equal(t, "NO", nullable, "transcript_event.%s must exist and be NOT NULL", col)
	}

	// migration 002 (issue #2241/FR3/C15): sessions has the composite
	// ordering/keyset index ListSessions relies on to avoid a sequential
	// scan.
	var hasListIndex bool
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE tablename = 'sessions' AND indexname = 'idx_sessions_created_at_id')
	`).Scan(&hasListIndex))
	assert.True(t, hasListIndex, "sessions must have idx_sessions_created_at_id (issue #2241's ListSessions ordering/keyset index)")

	// migration 010 (issue #2424 FR1, renamed from migration 007's
	// `domain`): agent_definition.scope is nullable -- a NULL scope means
	// the agent definition carries no delegated-grant scoping at all; when
	// set, it is the sole input whagent_net/grantkey.ForScope may derive a
	// delegated-grant key from.
	_, nullable = nullableColumn(t, ctx, db, "agent_definition", "scope")
	assert.Equal(t, "YES", nullable, "agent_definition.scope must be nullable (issue #2424 FR1, made optional)")

	// migration 008 (issue #2426 FR10/FR13): grpcauth_delegated_grant and
	// grpcauth_grant_index's column shapes are the actual schema contract
	// libs/go/grpcauth/pgstore and libs/go/grpcauth/grantindex check --
	// see delegatedgrant_integration_test.go's round-trip tests in this
	// same package for the deeper "these tables actually work with those
	// packages" proof; this is just the column-shape guard.
	for _, col := range []string{"subject", "grant_key", "token_material", "status", "created_at", "updated_at"} {
		_, nullable := nullableColumn(t, ctx, db, "grpcauth_delegated_grant", col)
		assert.Equal(t, "NO", nullable, "grpcauth_delegated_grant.%s must be NOT NULL (issue #2426, pgstore's schema contract)", col)
	}
	for _, col := range []string{"subject_iss", "subject_sub", "domain", "preferred_username", "granted_at"} {
		_, nullable := nullableColumn(t, ctx, db, "grpcauth_grant_index", col)
		assert.Equal(t, "NO", nullable, "grpcauth_grant_index.%s must be NOT NULL (issue #2426, grantindex's schema contract)", col)
	}
}
