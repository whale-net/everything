package session

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ToolCallReservation is a `tool_call_idempotency` row (LB4): whagent-net's
// own ledger of idempotency key -> outcome for tool calls the worker
// dispatches, keyed on (SessionID, Turn, CallIndex). The domain MCP server
// keeps its own idempotency ledger separately -- this one only lets a
// retried Temporal activity return the recorded outcome instead of
// re-dispatching the call.
type ToolCallReservation struct {
	IdempotencyKey string
	SessionID      uuid.UUID
	Turn           int
	CallIndex      int
	Tool           string
	ServerURL      string
	Outcome        json.RawMessage
	CreatedAt      time.Time
}

// IdempotencyLedger is the `tool_call_idempotency` table's repository
// interface.
type IdempotencyLedger interface {
	// Reserve records meta under key if no reservation exists yet for
	// (SessionID, Turn, CallIndex); if one already exists it is returned
	// unchanged rather than inserting a duplicate.
	Reserve(ctx context.Context, key string, meta ToolCallReservation) (ToolCallReservation, error)
	// RecordOutcome fills in the outcome for an existing reservation.
	RecordOutcome(ctx context.Context, key string, outcome json.RawMessage) error
	// Lookup returns the reservation for key, or nil if none exists.
	Lookup(ctx context.Context, key string) (*ToolCallReservation, error)
}

// idempotencyLedgerStore is the Postgres-backed IdempotencyLedger
// implementation.
type idempotencyLedgerStore struct{ pool *pgxpool.Pool }

var _ IdempotencyLedger = idempotencyLedgerStore{}

func (s idempotencyLedgerStore) Reserve(ctx context.Context, key string, meta ToolCallReservation) (ToolCallReservation, error) {
	return ToolCallReservation{}, errNotImplemented
}

func (s idempotencyLedgerStore) RecordOutcome(ctx context.Context, key string, outcome json.RawMessage) error {
	return errNotImplemented
}

func (s idempotencyLedgerStore) Lookup(ctx context.Context, key string) (*ToolCallReservation, error) {
	return nil, errNotImplemented
}
