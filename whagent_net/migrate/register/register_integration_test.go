//go:build integration

package register_test

import (
	"context"
	"database/sql"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
	"github.com/whale-net/everything/whagent_net/config"
	"github.com/whale-net/everything/whagent_net/migrate/register"
	"github.com/whale-net/everything/whagent_net/migrate/schema"
)

// Migrate up then register, twice: the second pass changes nothing.
func TestMigrateThenRegister_IdempotentAcrossRuns(t *testing.T) {
	ctx := context.Background()
	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB, err := sql.Open("pgx", pg.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	_, agents, err := config.Load()
	require.NoError(t, err)

	snapshot := func() map[string]string {
		rows, err := pg.Pool.Query(ctx, `SELECT agent_id, id::text || valid_from::text FROM agent_definition WHERE valid_to IS NULL`)
		require.NoError(t, err)
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var k, v string
			require.NoError(t, rows.Scan(&k, &v))
			out[k] = v
		}
		return out
	}

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up())
	require.NoError(t, register.Seeder(ctx, sqlDB))
	first := snapshot()
	assert.Len(t, first, len(agents))

	require.NoError(t, runner.Up())
	require.NoError(t, register.Seeder(ctx, sqlDB))
	assert.Equal(t, first, snapshot())

	var current int
	require.NoError(t, pg.Pool.QueryRow(ctx, `SELECT count(*) FROM agent_definition`).Scan(&current))
	assert.Equal(t, len(agents), current)
}

// A yaml idle_timeout seeds idle_timeout_seconds as integer seconds, and an
// agent without one stores NULL (platform default applies).
func TestRun_IdleTimeoutSeededAsSeconds(t *testing.T) {
	ctx := context.Background()
	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB, err := sql.Open("pgx", pg.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up())

	scope := "test-scope"
	agents := []config.AgentDefinitionConfig{
		{AgentID: "timed-agent", Scope: &scope, Model: "test-model", MaxTurns: 1, MaxCostUSD: 1, IdleTimeout: "90s",
			ToolSet: []config.ToolServerRefConfig{{ServerURL: "https://mcp.example.com/x"}}},
		{AgentID: "plain-agent", Scope: &scope, Model: "test-model", MaxTurns: 1, MaxCostUSD: 1,
			ToolSet: []config.ToolServerRefConfig{{ServerURL: "https://mcp.example.com/y"}}},
	}
	created, err := register.Run(ctx, pg.Pool, nil, agents, slog.Default())
	require.NoError(t, err)
	assert.Equal(t, 2, created)

	var timed, plain *int32
	require.NoError(t, pg.Pool.QueryRow(ctx,
		`SELECT idle_timeout_seconds FROM agent_definition WHERE agent_id = 'timed-agent' AND valid_to IS NULL`).Scan(&timed))
	require.NoError(t, pg.Pool.QueryRow(ctx,
		`SELECT idle_timeout_seconds FROM agent_definition WHERE agent_id = 'plain-agent' AND valid_to IS NULL`).Scan(&plain))
	require.NotNil(t, timed)
	assert.Equal(t, int32(90), *timed)
	assert.Nil(t, plain)

	// An invalid yaml value fails the seed rather than being stored.
	bad := []config.AgentDefinitionConfig{
		{AgentID: "bad-agent", Scope: &scope, Model: "test-model", MaxTurns: 1, MaxCostUSD: 1, IdleTimeout: "-5m",
			ToolSet: []config.ToolServerRefConfig{{ServerURL: "https://mcp.example.com/z"}}},
	}
	_, err = register.Run(ctx, pg.Pool, nil, bad, slog.Default())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "idle_timeout")
}
