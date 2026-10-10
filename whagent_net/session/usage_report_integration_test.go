//go:build integration

package session_test

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/whagent_net/session"
)

type reportTurn struct {
	agent     string
	model     string
	at        time.Time
	prompt    int64
	completed int64
	cost      float64
	estimated bool
}

func utc(y int, m time.Month, d, h, mi int) time.Time {
	return time.Date(y, m, d, h, mi, 0, 0, time.UTC)
}

// Fixture: one session whose agent changes mid-session (alpha -> beta),
// straddling an ISO week boundary and skipping February entirely.
var reportFixture = []reportTurn{
	{"alpha", "m1", utc(2026, 1, 4, 23, 30), 100, 10, 1.0, false},
	{"alpha", "m1", utc(2026, 1, 5, 0, 30), 200, 20, 2.0, true},
	{"beta", "m2", utc(2026, 1, 5, 12, 0), 300, 30, 4.0, false},
	{"beta", "m2", utc(2026, 3, 15, 10, 0), 400, 40, 8.0, true},
	{"beta", "m1", utc(2026, 3, 16, 10, 0), 500, 50, 16.0, false},
}

func loadReportFixture(t *testing.T, ctx context.Context, s *session.Store, exec func(string, ...any) error) {
	t.Helper()
	sess := createTestSession(t, ctx, s)
	cur := ""
	for i, tr := range reportFixture {
		if tr.agent != cur {
			def := upsertAgent(t, ctx, s, tr.agent)
			require.NoError(t, s.AgentDefinitions().AssignToSession(ctx, sess.SessionID, def.ID))
			cur = tr.agent
		}
		require.NoError(t, s.Usage().RecordTurn(ctx, session.TurnUsage{
			SessionID: sess.SessionID, Turn: i + 1, Model: tr.model,
			PromptTokens: tr.prompt, CompletionTokens: tr.completed,
			CostUSD: tr.cost, CostEstimated: tr.estimated,
		}))
		require.NoError(t, exec(`UPDATE turn_usage SET created_at = $1 WHERE session_id = $2 AND turn = $3`, tr.at, sess.SessionID, i+1))
	}
}

func periodKey(p session.UsagePeriod, at time.Time) string {
	switch p {
	case session.UsagePeriodDay:
		return at.Format("2006-01-02")
	case session.UsagePeriodWeek:
		wd := (int(at.Weekday()) + 6) % 7
		return at.AddDate(0, 0, -wd).Format("2006-01-02")
	case session.UsagePeriodMonth:
		return at.Format("2006-01") + "-01"
	}
	return ""
}

// expectedRows is an independent Go oracle for the SQL aggregation.
func expectedRows(q session.UsageReportQuery) []session.UsageReportRow {
	m := map[[3]string]*session.UsageReportRow{}
	for _, tr := range reportFixture {
		k := [3]string{periodKey(q.Period, tr.at)}
		if q.ByAgent {
			k[1] = tr.agent
		}
		if q.ByModel {
			k[2] = tr.model
		}
		r := m[k]
		if r == nil {
			r = &session.UsageReportRow{PeriodStart: k[0], AgentID: k[1], Model: k[2]}
			m[k] = r
		}
		r.PromptTokens += tr.prompt
		r.CompletionTokens += tr.completed
		r.CostUSD += tr.cost
		r.Turns++
		r.CostIncludesEstimate = r.CostIncludesEstimate || tr.estimated
	}
	var out []session.UsageReportRow
	for _, r := range m {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.PeriodStart != b.PeriodStart {
			return a.PeriodStart > b.PeriodStart
		}
		if a.AgentID != b.AgentID {
			return a.AgentID < b.AgentID
		}
		return a.Model < b.Model
	})
	return out
}

func TestUsageReport_AllCombinations(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	loadReportFixture(t, ctx, s, func(q string, a ...any) error { _, err := db.Pool.Exec(ctx, q, a...); return err })

	periods := map[string]session.UsagePeriod{
		"none": session.UsagePeriodNone, "day": session.UsagePeriodDay, "week": session.UsagePeriodWeek,
		"month": session.UsagePeriodMonth, "all_time": session.UsagePeriodAllTime,
	}
	for name, p := range periods {
		for _, byAgent := range []bool{false, true} {
			for _, byModel := range []bool{false, true} {
				q := session.UsageReportQuery{Period: p, ByAgent: byAgent, ByModel: byModel}
				t.Run(fmt.Sprintf("%s/agent=%v/model=%v", name, byAgent, byModel), func(t *testing.T) {
					rep, err := s.Usage().UsageReport(ctx, q)
					require.NoError(t, err)
					want := expectedRows(q)
					require.Len(t, rep.Rows, len(want))
					for i, w := range want {
						g := rep.Rows[i]
						assert.Equal(t, w.PeriodStart, g.PeriodStart)
						assert.Equal(t, w.AgentID, g.AgentID)
						assert.Equal(t, w.Model, g.Model)
						assert.Equal(t, w.PromptTokens, g.PromptTokens)
						assert.Equal(t, w.CompletionTokens, g.CompletionTokens)
						assert.InDelta(t, w.CostUSD, g.CostUSD, 1e-6)
						assert.Equal(t, w.Turns, g.Turns)
						assert.Equal(t, w.CostIncludesEstimate, g.CostIncludesEstimate)
					}
					assert.Equal(t, int64(5), rep.Total.Turns)
					assert.InDelta(t, 31.0, rep.Total.CostUSD, 1e-6)
					assert.Equal(t, int64(1500), rep.Total.PromptTokens)
					assert.True(t, rep.Total.CostIncludesEstimate)
				})
			}
		}
	}
}

func TestUsageReport_ISOWeekBoundary(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	loadReportFixture(t, ctx, s, func(q string, a ...any) error { _, err := db.Pool.Exec(ctx, q, a...); return err })

	rep, err := s.Usage().UsageReport(ctx, session.UsageReportQuery{Period: session.UsagePeriodWeek})
	require.NoError(t, err)
	got := map[string]int64{}
	for _, r := range rep.Rows {
		got[r.PeriodStart] = r.Turns
	}
	// Sun 2026-01-04 belongs to the week of Mon 2025-12-29; Mon 01-05 starts a new week.
	assert.Equal(t, int64(1), got["2025-12-29"])
	assert.Equal(t, int64(2), got["2026-01-05"])
	assert.Equal(t, int64(1), got["2026-03-09"])
	assert.Equal(t, int64(1), got["2026-03-16"])
}

func TestUsageReport_DayBoundaryUnderNonUTCSessionTimeZone(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	_, err := db.Pool.Exec(ctx, `DO $$ BEGIN EXECUTE format('ALTER DATABASE %I SET TimeZone = ''Pacific/Auckland''', current_database()); END $$`)
	require.NoError(t, err)
	db.Pool.Reset()
	var tz string
	require.NoError(t, db.Pool.QueryRow(ctx, `SHOW TimeZone`).Scan(&tz))
	require.Equal(t, "Pacific/Auckland", tz)

	sess := createTestSession(t, ctx, s)
	assignNewAgent(t, ctx, s, sess, "tz-agent")
	for i, at := range []time.Time{utc(2026, 5, 10, 23, 59), utc(2026, 5, 11, 0, 1)} {
		require.NoError(t, s.Usage().RecordTurn(ctx, session.TurnUsage{SessionID: sess.SessionID, Turn: i + 1, Model: "m", CostUSD: 1}))
		_, err := db.Pool.Exec(ctx, `UPDATE turn_usage SET created_at = $1 WHERE session_id = $2 AND turn = $3`, at, sess.SessionID, i+1)
		require.NoError(t, err)
	}

	rep, err := s.Usage().UsageReport(ctx, session.UsageReportQuery{Period: session.UsagePeriodDay})
	require.NoError(t, err)
	require.Len(t, rep.Rows, 2)
	assert.Equal(t, "2026-05-11", rep.Rows[0].PeriodStart)
	assert.Equal(t, "2026-05-10", rep.Rows[1].PeriodStart)
	assert.Equal(t, int64(1), rep.Rows[0].Turns)
	assert.Equal(t, int64(1), rep.Rows[1].Turns)
}

func TestUsageReport_MidSessionAgentChangeReconciles(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	loadReportFixture(t, ctx, s, func(q string, a ...any) error { _, err := db.Pool.Exec(ctx, q, a...); return err })

	for _, p := range []session.UsagePeriod{session.UsagePeriodDay, session.UsagePeriodWeek, session.UsagePeriodMonth, session.UsagePeriodAllTime} {
		all, err := s.Usage().UsageReport(ctx, session.UsageReportQuery{Period: p})
		require.NoError(t, err)
		by, err := s.Usage().UsageReport(ctx, session.UsageReportQuery{Period: p, ByAgent: true})
		require.NoError(t, err)

		sumCost := map[string]float64{}
		sumTurns := map[string]int64{}
		for _, r := range by.Rows {
			sumCost[r.PeriodStart] += r.CostUSD
			sumTurns[r.PeriodStart] += r.Turns
		}
		for _, r := range all.Rows {
			assert.InDelta(t, r.CostUSD, sumCost[r.PeriodStart], 1e-6, "period %d bucket %q", p, r.PeriodStart)
			assert.Equal(t, r.Turns, sumTurns[r.PeriodStart])
		}
	}

	// Each turn is attributed to the agent that was current when it was recorded.
	by, err := s.Usage().UsageReport(ctx, session.UsageReportQuery{Period: session.UsagePeriodAllTime, ByAgent: true})
	require.NoError(t, err)
	got := map[string]int64{}
	for _, r := range by.Rows {
		got[r.AgentID] = r.Turns
	}
	assert.Equal(t, map[string]int64{"alpha": 2, "beta": 3}, got)
}

func TestUsageReport_EmptyMonthOmittedAndEstimateFlag(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	loadReportFixture(t, ctx, s, func(q string, a ...any) error { _, err := db.Pool.Exec(ctx, q, a...); return err })

	rep, err := s.Usage().UsageReport(ctx, session.UsageReportQuery{Period: session.UsagePeriodMonth, ByModel: true})
	require.NoError(t, err)
	for _, r := range rep.Rows {
		assert.NotEqual(t, "2026-02-01", r.PeriodStart, "a month with no usage must not appear")
	}
	flags := map[string]bool{}
	for _, r := range rep.Rows {
		flags[r.PeriodStart+"/"+r.Model] = r.CostIncludesEstimate
	}
	assert.Equal(t, map[string]bool{
		"2026-03-01/m2": true, "2026-03-01/m1": false,
		"2026-01-01/m1": true, "2026-01-01/m2": false,
	}, flags)
}

func TestUsageReport_TimeRangeFilter(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	loadReportFixture(t, ctx, s, func(q string, a ...any) error { _, err := db.Pool.Exec(ctx, q, a...); return err })

	from, to := utc(2026, 3, 1, 0, 0), utc(2026, 3, 16, 0, 0)
	rep, err := s.Usage().UsageReport(ctx, session.UsageReportQuery{Period: session.UsagePeriodAllTime, From: &from, To: &to})
	require.NoError(t, err)
	assert.Equal(t, int64(1), rep.Total.Turns)
	require.Len(t, rep.Rows, 1)
	assert.InDelta(t, 8.0, rep.Rows[0].CostUSD, 1e-6)
}
