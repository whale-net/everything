//go:build integration

package schema_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/whagent_net/migrate/schema"
)

const dropVersionFile = "/018_agent_definition_drop_version.up.sql"

// seedNullIDs migrates to 017 and leaves definition ids NULL, as old binaries would.
func (f *scd2Fixture) seedNullIDs(t *testing.T) {
	t.Helper()
	require.NoError(t, f.runner.Migrate(preSCD2))
	f.seed(t)
	require.NoError(t, f.runner.Migrate(scd2))
	_, err := f.sqlDB.Exec(`UPDATE turn_usage SET agent_definition_id = NULL`)
	require.NoError(t, err)
	_, err = f.sqlDB.Exec(`UPDATE session_agent SET agent_definition_id = NULL`)
	require.NoError(t, err)
}

func (f *scd2Fixture) colExists(t *testing.T, table, col string) bool {
	t.Helper()
	var n int
	require.NoError(t, f.sqlDB.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_name=$1 AND column_name=$2`, table, col).Scan(&n))
	return n > 0
}

func (f *scd2Fixture) nullable(t *testing.T, table, col string) string {
	t.Helper()
	var s string
	require.NoError(t, f.sqlDB.QueryRow(`SELECT is_nullable FROM information_schema.columns WHERE table_name=$1 AND column_name=$2`, table, col).Scan(&s))
	return s
}

func TestMigration018_HeadSchemaAndBackfill(t *testing.T) {
	f := newSCD2Fixture(t)
	f.seedNullIDs(t)
	require.NoError(t, f.runner.Migrate(scd2+1))

	latest, err := f.runner.LatestVersion()
	require.NoError(t, err)
	require.NoError(t, f.runner.Migrate(latest))

	assert.False(t, f.colExists(t, "agent_definition", "version"))
	assert.False(t, f.colExists(t, "session_agent", "agent_version"))
	assert.Equal(t, "NO", f.nullable(t, "session_agent", "agent_definition_id"))
	assert.Equal(t, "NO", f.nullable(t, "turn_usage", "agent_definition_id"))

	var nulls int
	require.NoError(t, f.sqlDB.QueryRow(`SELECT (SELECT count(*) FROM session_agent) + (SELECT count(*) FROM turn_usage)`).Scan(&nulls))
	assert.Equal(t, 6, nulls, "rows survive the backfill")
}

func TestMigration018_UpSQLIsIdempotent(t *testing.T) {
	f := newSCD2Fixture(t)
	f.seedNullIDs(t)
	require.NoError(t, f.runner.Migrate(scd2+1))
	before := f.snapshot(t)

	upSQL, err := schema.Migrations.ReadFile(schema.Dir + dropVersionFile)
	require.NoError(t, err)
	_, err = f.sqlDB.Exec(string(upSQL))
	require.NoError(t, err, "re-applying the up SQL must be a no-op")
	assert.Equal(t, before, f.snapshot(t))
}

func TestMigration018_DownRestoresVersionsAndRoundTrips(t *testing.T) {
	f := newSCD2Fixture(t)
	f.seedNullIDs(t)
	require.NoError(t, f.runner.Migrate(scd2+1))
	before := f.snapshot(t)

	require.NoError(t, f.runner.Steps(-1))
	assert.True(t, f.colExists(t, "agent_definition", "version"))
	rows, err := f.sqlDB.Query(`SELECT agent_id, version FROM agent_definition ORDER BY agent_id, valid_from`)
	require.NoError(t, err)
	var got []string
	for rows.Next() {
		var a string
		var v int
		require.NoError(t, rows.Scan(&a, &v))
		got = append(got, a+string(rune('0'+v)))
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"a1", "a2", "a3", "b1"}, got)

	var mismatched, nulls int
	require.NoError(t, f.sqlDB.QueryRow(`SELECT count(*) FROM session_agent sa JOIN agent_definition d ON d.id = sa.agent_definition_id WHERE d.version <> sa.agent_version`).Scan(&mismatched))
	assert.Zero(t, mismatched)
	require.NoError(t, f.sqlDB.QueryRow(`SELECT count(*) FROM session_agent WHERE agent_version IS NULL`).Scan(&nulls))
	assert.Zero(t, nulls)

	require.NoError(t, f.runner.Steps(1))
	assert.Equal(t, before, f.snapshot(t))
}
