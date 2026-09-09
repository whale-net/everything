package session

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TurnUsage is a `turn_usage` row (LB6): one turn's provider-reported (or
// estimated) token usage and cost. CostUSD is never null and never a
// zero-as-unknown sentinel (FR7) -- when the provider omits cost it is
// estimated from tokens and CostEstimated is set true instead.
// GenerationID is the provider's returned generation id; M1 has no reader
// for it, it is still recorded.
type TurnUsage struct {
	SessionID        uuid.UUID
	Turn             int
	Model            string
	PromptTokens     int64
	CompletionTokens int64
	CostUSD          float64
	CostEstimated    bool
	GenerationID     *string
	CreatedAt        time.Time
}

// UsageSummary is FR4's read-path aggregate over a session's committed
// `turn_usage` rows (NFR5: summed fresh from the rows every call, never a
// separately-mutated counter -- the same rule SumCost already follows).
type UsageSummary struct {
	// TurnsUsed is COUNT(*) of turn_usage rows for the session.
	TurnsUsed int
	// CostUSD is COALESCE(SUM(cost_usd), 0) across those rows, to full
	// NUMERIC(12,6) precision.
	CostUSD float64
	// CostEstimated is true when any summed row's CostEstimated is true.
	CostEstimated bool
}

// UsageStore is the `turn_usage` table's repository interface. SumCost is
// the only cap input FR7 allows -- a running total derived from turn_usage
// rows, never a separately-mutated counter. Summary is FR4's read path:
// the same summed-rows source, additionally reporting turn count and
// whether any summed cost was estimated.
type UsageStore interface {
	// RecordTurn inserts one turn's usage row.
	RecordTurn(ctx context.Context, usage TurnUsage) error
	// SumCost returns the total cost_usd across every turn_usage row for
	// sessionID, including rows with CostEstimated true.
	SumCost(ctx context.Context, sessionID uuid.UUID) (float64, error)
	// Summary returns sessionID's UsageSummary: turn count, summed cost,
	// and whether any row's cost was estimated -- one query, always
	// derived fresh from turn_usage (NFR5).
	Summary(ctx context.Context, sessionID uuid.UUID) (UsageSummary, error)
}

// usageStore is the Postgres-backed UsageStore implementation.
type usageStore struct{ pool *pgxpool.Pool }

var _ UsageStore = usageStore{}

// RecordTurn inserts one turn_usage row. Retrying an already-recorded
// (session_id, turn) is not a supported use of this method -- the PK
// constraint surfaces as a plain error, matching the interface's "insert"
// wording; callers that need retry-safety compose it themselves (e.g. via
// IdempotencyLedger).
func (s usageStore) RecordTurn(ctx context.Context, usage TurnUsage) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO turn_usage (session_id, turn, model, prompt_tokens, completion_tokens, cost_usd, cost_estimated, generation_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, usage.SessionID, usage.Turn, usage.Model, usage.PromptTokens, usage.CompletionTokens, usage.CostUSD, usage.CostEstimated, usage.GenerationID)
	if err != nil {
		return fmt.Errorf("record turn usage: %w", err)
	}
	return nil
}

// SumCost is FR7's only cap input: a running total derived fresh from
// turn_usage on every call, never a separately-mutated counter that could
// drift from the rows it is supposed to summarize.
func (s usageStore) SumCost(ctx context.Context, sessionID uuid.UUID) (float64, error) {
	var total float64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(cost_usd), 0) FROM turn_usage WHERE session_id = $1
	`, sessionID).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("sum turn usage cost: %w", err)
	}
	return total, nil
}

// Summary is FR4's read path (NFR5): one query over turn_usage returning
// the committed turn count, summed cost (to NUMERIC(12,6) precision, same
// scan approach as SumCost above), and whether any summed row's cost was
// estimated. Returns the zero UsageSummary (0/0/false) for a session with
// no turn_usage rows -- COUNT/COALESCE(SUM)/bool_or all degrade cleanly
// over zero rows, no special-casing needed here.
func (s usageStore) Summary(ctx context.Context, sessionID uuid.UUID) (UsageSummary, error) {
	var summary UsageSummary
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*), COALESCE(SUM(cost_usd), 0), COALESCE(bool_or(cost_estimated), false)
		FROM turn_usage WHERE session_id = $1
	`, sessionID).Scan(&summary.TurnsUsed, &summary.CostUSD, &summary.CostEstimated)
	if err != nil {
		return UsageSummary{}, fmt.Errorf("summarize turn usage: %w", err)
	}
	return summary, nil
}
