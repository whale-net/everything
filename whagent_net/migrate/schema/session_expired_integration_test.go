//go:build integration

package schema_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
	"github.com/whale-net/everything/whagent_net/migrate/schema"
)

// TestMigrationExpired_AddsStatusAndIdleTimeoutColumns proves the head
// migration accepts status 'expired', rejects non-positive idle timeouts,
// and reverses cleanly. The head is derived, not hardcoded.
func TestMigrationExpired_AddsStatusAndIdleTimeoutColumns(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	head, err := runner.LatestVersion()
	require.NoError(t, err)
	require.NoError(t, runner.Migrate(head))

	var def string
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'sessions_status_check'
	`).Scan(&def))
	assert.Contains(t, def, "'expired'")

	for _, table := range []string{"sessions", "agent_definition"} {
		var n int
		require.NoError(t, sqlDB.QueryRowContext(ctx, `
			SELECT count(*) FROM information_schema.columns
			WHERE table_name = $1 AND column_name = 'idle_timeout_seconds'
		`, table).Scan(&n))
		assert.Equal(t, 1, n, "%s.idle_timeout_seconds must exist", table)
	}

	insertDef := `
		INSERT INTO agent_definition (agent_id, model, tool_set, max_turns, max_cost_usd, scope, idle_timeout_seconds)
		VALUES ($1, 'm', '[]', 10, 1.0, 'd', $2)
	`
	_, err = sqlDB.ExecContext(ctx, insertDef, "expired-probe-zero", 0)
	require.Error(t, err, "idle_timeout_seconds = 0 must violate the CHECK")
	assert.Contains(t, err.Error(), "23514")
	_, err = sqlDB.ExecContext(ctx, insertDef, "expired-probe-ok", 30)
	require.NoError(t, err, "a positive idle_timeout_seconds must be accepted")

	// Steps(-1) from head hops to the previous existing version, which is not
	// head-1 while migration 020 is absent from this tree.
	require.NoError(t, runner.Steps(-1), "head must step down cleanly")
	require.NoError(t, runner.Steps(1), "head must re-apply after a one-step down")
	version, dirty, err := runner.Version()
	require.NoError(t, err)
	assert.False(t, dirty)
	assert.Equal(t, head, version)
}
