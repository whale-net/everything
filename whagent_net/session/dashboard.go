package session

import (
	"context"
	"fmt"
)

// AgentTraffic is one agent's activity for a UTC day.
type AgentTraffic struct {
	AgentID       string
	SessionsToday int64
	TurnsToday    int64
}

// DashboardSummary is the landing-page rollup for one UTC day.
type DashboardSummary struct {
	UTCDate              string
	Agents               []AgentTraffic
	TotalCostUSDToday    float64
	CostIncludesEstimate bool
}

// GetDashboardSummary returns per-agent traffic and total cost for utcDate
// (YYYY-MM-DD, the server's current UTC date). A day is the UTC calendar
// date of created_at regardless of the DB session's TimeZone. Every current
// agent appears, with zeros when idle.
func (s *Store) GetDashboardSummary(ctx context.Context, utcDate string) (*DashboardSummary, error) {
	out := &DashboardSummary{UTCDate: utcDate}

	rows, err := s.pool.Query(ctx, `
		WITH turns AS (
			SELECT d.agent_id, COUNT(*) AS n
			FROM turn_usage tu
			JOIN agent_definition d ON d.id = tu.agent_definition_id
			WHERE (tu.created_at AT TIME ZONE 'UTC')::date = $1::text::date
			GROUP BY d.agent_id
		), first_assignment AS (
			SELECT DISTINCT ON (session_id) session_id, agent_definition_id
			FROM session_agent
			ORDER BY session_id, valid_from ASC
		), sess AS (
			SELECT d.agent_id, COUNT(*) AS n
			FROM sessions s
			JOIN first_assignment fa ON fa.session_id = s.session_id
			JOIN agent_definition d ON d.id = fa.agent_definition_id
			WHERE (s.created_at AT TIME ZONE 'UTC')::date = $1::text::date
			GROUP BY d.agent_id
		)
		SELECT cur.agent_id, COALESCE(sess.n, 0), COALESCE(turns.n, 0)
		FROM agent_definition cur
		LEFT JOIN sess ON sess.agent_id = cur.agent_id
		LEFT JOIN turns ON turns.agent_id = cur.agent_id
		WHERE cur.valid_to IS NULL
		ORDER BY cur.agent_id
	`, utcDate)
	if err != nil {
		return nil, fmt.Errorf("dashboard agent traffic: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var a AgentTraffic
		if err := rows.Scan(&a.AgentID, &a.SessionsToday, &a.TurnsToday); err != nil {
			return nil, fmt.Errorf("scan dashboard agent traffic: %w", err)
		}
		out.Agents = append(out.Agents, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("dashboard agent traffic: %w", err)
	}

	if err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(cost_usd), 0)::float8, COALESCE(bool_or(cost_estimated), false)
		FROM turn_usage
		WHERE (created_at AT TIME ZONE 'UTC')::date = $1::text::date
	`, utcDate).Scan(&out.TotalCostUSDToday, &out.CostIncludesEstimate); err != nil {
		return nil, fmt.Errorf("dashboard total cost: %w", err)
	}
	return out, nil
}
