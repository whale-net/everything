//go:build integration

package schema_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
	"github.com/whale-net/everything/whagent_net/migrate/schema"
)

// preSCD2 is the last migration before 017_agent_definition_scd2.
const (
	preSCD2 = 16
	scd2    = 17
)

type scd2Fixture struct {
	sqlDB  *sql.DB
	runner *migrate.Runner
	base   time.Time
}

func newSCD2Fixture(t *testing.T) *scd2Fixture {
	t.Helper()
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return &scd2Fixture{
		sqlDB:  sqlDB,
		runner: migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir),
		base:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func (f *scd2Fixture) at(h int) time.Time { return f.base.Add(time.Duration(h) * time.Hour) }

// seed inserts agent "a" with 3 versions, agent "b" with 1, and session s1
// that switches a v1 -> v2 at hour 10 with usage rows on both sides.
func (f *scd2Fixture) seed(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	for _, v := range []struct {
		agent string
		ver   int
		hour  int
	}{{"a", 1, 1}, {"a", 2, 5}, {"a", 3, 9}, {"b", 1, 2}} {
		_, err := f.sqlDB.ExecContext(ctx, `
			INSERT INTO agent_definition (agent_id, version, model, tool_set, max_turns, max_cost_usd, scope, created_at)
			VALUES ($1, $2, 'm', '[]', 10, 1.0, 'd', $3)`, v.agent, v.ver, f.at(v.hour))
		require.NoError(t, err)
	}
	sid := "00000000-0000-0000-0000-000000000001"
	for _, s := range []struct {
		ver      int
		from, to int
	}{{1, 6, 10}, {2, 10, -1}} {
		var to any
		if s.to >= 0 {
			to = f.at(s.to)
		}
		_, err := f.sqlDB.ExecContext(ctx, `
			INSERT INTO session_agent (session_id, agent_id, agent_version, valid_from, valid_to)
			VALUES ($1, 'a', $2, $3, $4)`, sid, s.ver, f.at(s.from), to)
		require.NoError(t, err)
	}
	for turn, hour := range []int{7, 8, 11, 12} {
		_, err := f.sqlDB.ExecContext(ctx, `
			INSERT INTO turn_usage (session_id, turn, model, prompt_tokens, completion_tokens, cost_usd, cost_estimated, created_at)
			VALUES ($1, $2, 'm', 1, 1, 0.1, false, $3)`, sid, turn+1, f.at(hour))
		require.NoError(t, err)
	}
}

func (f *scd2Fixture) defID(t *testing.T, agent string, ver int) string {
	t.Helper()
	var id string
	require.NoError(t, f.sqlDB.QueryRow(`SELECT id FROM agent_definition WHERE agent_id=$1 AND version=$2`, agent, ver).Scan(&id))
	return id
}

func (f *scd2Fixture) snapshot(t *testing.T) string {
	t.Helper()
	var s string
	require.NoError(t, f.sqlDB.QueryRow(`
		SELECT COALESCE((SELECT string_agg(concat_ws('|', id, valid_from, valid_to), ',' ORDER BY id) FROM agent_definition), '') || '#' ||
		       COALESCE((SELECT string_agg(concat_ws('|', session_id, valid_from, agent_definition_id), ',' ORDER BY valid_from) FROM session_agent), '') || '#' ||
		       COALESCE((SELECT string_agg(concat_ws('|', session_id, turn, agent_definition_id), ',' ORDER BY turn) FROM turn_usage), '')`).Scan(&s))
	return s
}

func TestMigration017_BackfillsSCD2AndDefinitionIDs(t *testing.T) {
	f := newSCD2Fixture(t)
	require.NoError(t, f.runner.Migrate(preSCD2))
	f.seed(t)
	require.NoError(t, f.runner.Migrate(scd2))

	rows, err := f.sqlDB.Query(`SELECT agent_id, version, created_at, valid_from, valid_to FROM agent_definition ORDER BY agent_id, version`)
	require.NoError(t, err)
	type row struct {
		agent   string
		ver     int
		created time.Time
		from    time.Time
		to      sql.NullTime
	}
	var defs []row
	for rows.Next() {
		var r row
		require.NoError(t, rows.Scan(&r.agent, &r.ver, &r.created, &r.from, &r.to))
		defs = append(defs, r)
	}
	require.NoError(t, rows.Err())
	require.Len(t, defs, 4)
	for i, r := range defs {
		assert.True(t, r.created.Equal(r.from), "valid_from = created_at")
		if i+1 < len(defs) && defs[i+1].agent == r.agent {
			require.True(t, r.to.Valid)
			assert.True(t, r.to.Time.Equal(defs[i+1].created), "valid_to = next version's created_at")
		} else {
			assert.False(t, r.to.Valid, "newest version stays open")
		}
	}

	var bad int
	require.NoError(t, f.sqlDB.QueryRow(`SELECT count(*) FROM (SELECT agent_id FROM agent_definition WHERE valid_to IS NULL GROUP BY agent_id HAVING count(*) <> 1) x`).Scan(&bad))
	assert.Zero(t, bad)

	var mismatched int
	require.NoError(t, f.sqlDB.QueryRow(`SELECT count(*) FROM session_agent sa JOIN agent_definition d ON d.id = sa.agent_definition_id WHERE d.agent_id <> sa.agent_id OR d.version <> sa.agent_version`).Scan(&mismatched))
	assert.Zero(t, mismatched)
	var nullIDs int
	require.NoError(t, f.sqlDB.QueryRow(`SELECT count(*) FROM session_agent WHERE agent_definition_id IS NULL`).Scan(&nullIDs))
	assert.Zero(t, nullIDs)

	v1, v2 := f.defID(t, "a", 1), f.defID(t, "a", 2)
	for turn, want := range map[int]string{1: v1, 2: v1, 3: v2, 4: v2} {
		var got string
		require.NoError(t, f.sqlDB.QueryRow(`SELECT agent_definition_id FROM turn_usage WHERE turn=$1`, turn).Scan(&got))
		assert.Equal(t, want, got, "turn %d", turn)
	}

	// New code omits agent_version.
	_, err = f.sqlDB.Exec(`INSERT INTO session_agent (session_id, agent_id, agent_definition_id) VALUES ('00000000-0000-0000-0000-000000000002', 'b', $1)`, f.defID(t, "b", 1))
	assert.NoError(t, err, "insert without agent_version must succeed")
}

func TestMigration017_SecondCurrentRowViolatesUnique(t *testing.T) {
	f := newSCD2Fixture(t)
	require.NoError(t, f.runner.Migrate(scd2))
	_, err := f.sqlDB.Exec(`INSERT INTO agent_definition (agent_id, version, model, tool_set, scope) VALUES ('a', 1, 'm', '[]', 'd')`)
	require.NoError(t, err)
	_, err = f.sqlDB.Exec(`INSERT INTO agent_definition (agent_id, version, model, tool_set, scope) VALUES ('a', 2, 'm', '[]', 'd')`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "23505")
}

func TestMigration017_UpSQLIsIdempotent(t *testing.T) {
	f := newSCD2Fixture(t)
	require.NoError(t, f.runner.Migrate(preSCD2))
	f.seed(t)
	require.NoError(t, f.runner.Migrate(scd2))
	before := f.snapshot(t)

	upSQL, err := schema.Migrations.ReadFile(schema.Dir + "/017_agent_definition_scd2.up.sql")
	require.NoError(t, err)
	_, err = f.sqlDB.Exec(string(upSQL))
	require.NoError(t, err, "re-applying the up SQL must be a no-op")
	assert.Equal(t, before, f.snapshot(t))
}

func TestMigration017_DownThenUpRoundTrips(t *testing.T) {
	f := newSCD2Fixture(t)
	require.NoError(t, f.runner.Migrate(preSCD2))
	f.seed(t)
	require.NoError(t, f.runner.Migrate(scd2))
	before := f.snapshot(t)

	require.NoError(t, f.runner.Steps(-1))
	var cols int
	require.NoError(t, f.sqlDB.QueryRow(`
		SELECT count(*) FROM information_schema.columns
		WHERE (table_name='agent_definition' AND column_name IN ('valid_from','valid_to'))
		   OR (table_name IN ('session_agent','turn_usage') AND column_name='agent_definition_id')`).Scan(&cols))
	assert.Zero(t, cols)
	var nulls int
	require.NoError(t, f.sqlDB.QueryRow(`SELECT count(*) FROM session_agent WHERE agent_version IS NULL`).Scan(&nulls))
	assert.Zero(t, nulls)
	var nullable string
	require.NoError(t, f.sqlDB.QueryRow(`SELECT is_nullable FROM information_schema.columns WHERE table_name='session_agent' AND column_name='agent_version'`).Scan(&nullable))
	assert.Equal(t, "NO", nullable)

	require.NoError(t, f.runner.Steps(1))
	assert.Equal(t, before, f.snapshot(t))
}
