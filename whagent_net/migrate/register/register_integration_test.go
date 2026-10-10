//go:build integration

package register_test

import (
	"context"
	"database/sql"
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
