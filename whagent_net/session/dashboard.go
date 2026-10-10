package session

import (
	"context"
	"errors"
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
// (YYYY-MM-DD, the server's current UTC date).
func (s *Store) GetDashboardSummary(ctx context.Context, utcDate string) (*DashboardSummary, error) {
	return nil, errors.New("not implemented")
}
