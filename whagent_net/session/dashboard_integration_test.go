//go:build integration

package session_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/whagent_net/session"
)

const dashDay = "2026-03-15"

// dashTurn records a turn on the session's open assignment and backdates it.
func dashTurn(t *testing.T, ctx context.Context, s *session.Store, db *dbtest.Postgres, sessionID uuid.UUID, turn int, cost float64, estimated bool, at string) {
	t.Helper()
	require.NoError(t, s.Usage().RecordTurn(ctx, session.TurnUsage{
		SessionID: sessionID, Turn: turn, Model: "m", CostUSD: cost, CostEstimated: estimated,
	}))
	_, err := db.Pool.Exec(ctx, `UPDATE turn_usage SET created_at = $3::timestamptz WHERE session_id = $1 AND turn = $2`, sessionID, turn, at)
	require.NoError(t, err)
}

func dashSessionAt(t *testing.T, ctx context.Context, s *session.Store, db *dbtest.Postgres, at string) *session.Session {
	t.Helper()
	sess := createTestSession(t, ctx, s)
	_, err := db.Pool.Exec(ctx, `UPDATE sessions SET created_at = $2::timestamptz WHERE session_id = $1`, sess.SessionID, at)
	require.NoError(t, err)
	return sess
}

func trafficFor(sum *session.DashboardSummary, agentID string) (session.AgentTraffic, bool) {
	for _, a := range sum.Agents {
		if a.AgentID == agentID {
			return a, true
		}
	}
	return session.AgentTraffic{}, false
}

// Turns at 23:59 UTC the day before and 00:01 UTC today: only the latter
// counts, even when the DB session's TimeZone is not UTC.
func TestDashboard_UTCBucketing_NonUTCDBTimeZone(t *testing.T) {
	ctx := context.Background()
	_, db := newStore(t)

	cfg, err := pgxpool.ParseConfig(db.ConnString)
	require.NoError(t, err)
	cfg.ConnConfig.RuntimeParams["timezone"] = "America/New_York"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	s := session.New(pool, nil)

	var tz string
	require.NoError(t, pool.QueryRow(ctx, `SHOW TimeZone`).Scan(&tz))
	require.Equal(t, "America/New_York", tz)

	sess := dashSessionAt(t, ctx, s, db, dashDay+"T00:01:00Z")
	assignNewAgent(t, ctx, s, sess, "agent-a")
	dashTurn(t, ctx, s, db, sess.SessionID, 1, 1, false, "2026-03-14T23:59:00Z")
	dashTurn(t, ctx, s, db, sess.SessionID, 2, 2, false, dashDay+"T00:01:00Z")

	sum, err := s.GetDashboardSummary(ctx, dashDay)
	require.NoError(t, err)
	a, ok := trafficFor(sum, "agent-a")
	require.True(t, ok)
	assert.EqualValues(t, 1, a.TurnsToday)
	assert.EqualValues(t, 1, a.SessionsToday)
	assert.InDelta(t, 2.0, sum.TotalCostUSDToday, 1e-9)

	prev, err := s.GetDashboardSummary(ctx, "2026-03-14")
	require.NoError(t, err)
	a, _ = trafficFor(prev, "agent-a")
	assert.EqualValues(t, 1, a.TurnsToday)
	assert.EqualValues(t, 0, a.SessionsToday)
}

func TestDashboard_IdleAgentAppearsWithZeros(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	upsertAgent(t, ctx, s, "idle-agent")

	sum, err := s.GetDashboardSummary(ctx, dashDay)
	require.NoError(t, err)
	a, ok := trafficFor(sum, "idle-agent")
	require.True(t, ok, "idle agent must be listed")
	assert.EqualValues(t, 0, a.SessionsToday)
	assert.EqualValues(t, 0, a.TurnsToday)
	assert.Equal(t, 0.0, sum.TotalCostUSDToday)
	assert.False(t, sum.CostIncludesEstimate)
	assert.Equal(t, dashDay, sum.UTCDate)
}

// A session that switched agents counts the session for its first agent and
// each turn for the agent that ran it.
func TestDashboard_SwitchedAgentCountsTurnsPerAgent(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	sess := dashSessionAt(t, ctx, s, db, dashDay+"T10:00:00Z")
	assignNewAgent(t, ctx, s, sess, "agent-a")
	dashTurn(t, ctx, s, db, sess.SessionID, 1, 0.1, false, dashDay+"T10:01:00Z")
	dashTurn(t, ctx, s, db, sess.SessionID, 2, 0.1, false, dashDay+"T10:02:00Z")
	assignNewAgent(t, ctx, s, sess, "agent-b")
	dashTurn(t, ctx, s, db, sess.SessionID, 3, 0.1, false, dashDay+"T10:03:00Z")

	sum, err := s.GetDashboardSummary(ctx, dashDay)
	require.NoError(t, err)
	a, _ := trafficFor(sum, "agent-a")
	b, _ := trafficFor(sum, "agent-b")
	assert.EqualValues(t, 1, a.SessionsToday)
	assert.EqualValues(t, 2, a.TurnsToday)
	assert.EqualValues(t, 0, b.SessionsToday, "session belongs to its first agent only")
	assert.EqualValues(t, 1, b.TurnsToday)
}

func TestDashboard_CostSumAndEstimateFlag(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	sess := dashSessionAt(t, ctx, s, db, dashDay+"T10:00:00Z")
	assignNewAgent(t, ctx, s, sess, "agent-a")
	dashTurn(t, ctx, s, db, sess.SessionID, 1, 0.25, false, dashDay+"T10:01:00Z")

	sum, err := s.GetDashboardSummary(ctx, dashDay)
	require.NoError(t, err)
	assert.InDelta(t, 0.25, sum.TotalCostUSDToday, 1e-9)
	assert.False(t, sum.CostIncludesEstimate)

	dashTurn(t, ctx, s, db, sess.SessionID, 2, 0.5, true, dashDay+"T11:00:00Z")
	dashTurn(t, ctx, s, db, sess.SessionID, 3, 9, true, "2026-03-14T11:00:00Z") // other day: excluded

	sum, err = s.GetDashboardSummary(ctx, dashDay)
	require.NoError(t, err)
	assert.InDelta(t, 0.75, sum.TotalCostUSDToday, 1e-9)
	assert.True(t, sum.CostIncludesEstimate)
}
