package session

import (
	"context"
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

// UsageStore is the `turn_usage` table's repository interface. SumCost is
// the only cap input FR7 allows -- a running total derived from turn_usage
// rows, never a separately-mutated counter.
type UsageStore interface {
	// RecordTurn inserts one turn's usage row.
	RecordTurn(ctx context.Context, usage TurnUsage) error
	// SumCost returns the total cost_usd across every turn_usage row for
	// sessionID, including rows with CostEstimated true.
	SumCost(ctx context.Context, sessionID uuid.UUID) (float64, error)
}

// usageStore is the Postgres-backed UsageStore implementation.
type usageStore struct{ pool *pgxpool.Pool }

var _ UsageStore = usageStore{}

func (s usageStore) RecordTurn(ctx context.Context, usage TurnUsage) error {
	return errNotImplemented
}

func (s usageStore) SumCost(ctx context.Context, sessionID uuid.UUID) (float64, error) {
	return 0, errNotImplemented
}
