package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

const toolCallReservationColumns = `idempotency_key, session_id, turn, call_index, tool, server_url, outcome, created_at`

func scanToolCallReservation(row pgx.Row) (ToolCallReservation, error) {
	var r ToolCallReservation
	if err := row.Scan(&r.IdempotencyKey, &r.SessionID, &r.Turn, &r.CallIndex, &r.Tool, &r.ServerURL, &r.Outcome, &r.CreatedAt); err != nil {
		return ToolCallReservation{}, err
	}
	return r, nil
}

// Reserve races an INSERT ... ON CONFLICT (session_id, turn, call_index)
// DO NOTHING against every other caller reserving the same tool call --
// same shape as audience_score_system/store's mcp_idempotency guard
// (idempotency.go), minus fingerprint-conflict detection, which this
// ledger's interface has no room for (see ToolCallReservation's doc
// comment: retried-activity replay only). The INSERT's own RETURNING wins
// the race case; a losing caller's RETURNING returns no row (ON CONFLICT
// DO NOTHING never fires RETURNING), so it falls through to a plain SELECT
// for the reservation the winner just created.
func (s idempotencyLedgerStore) Reserve(ctx context.Context, key string, meta ToolCallReservation) (ToolCallReservation, error) {
	reservation, err := scanToolCallReservation(s.pool.QueryRow(ctx, `
		INSERT INTO tool_call_idempotency (idempotency_key, session_id, turn, call_index, tool, server_url)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (session_id, turn, call_index) DO NOTHING
		RETURNING `+toolCallReservationColumns,
		key, meta.SessionID, meta.Turn, meta.CallIndex, meta.Tool, meta.ServerURL,
	))
	if err == nil {
		return reservation, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ToolCallReservation{}, fmt.Errorf("reserve tool call idempotency: %w", err)
	}

	existing, err := scanToolCallReservation(s.pool.QueryRow(ctx, `
		SELECT `+toolCallReservationColumns+`
		FROM tool_call_idempotency
		WHERE session_id = $1 AND turn = $2 AND call_index = $3
	`, meta.SessionID, meta.Turn, meta.CallIndex))
	if err != nil {
		return ToolCallReservation{}, fmt.Errorf("look up existing tool call reservation: %w", err)
	}
	return existing, nil
}

// RecordOutcome returns pgx.ErrNoRows if key has no reservation.
func (s idempotencyLedgerStore) RecordOutcome(ctx context.Context, key string, outcome json.RawMessage) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE tool_call_idempotency SET outcome = $2 WHERE idempotency_key = $1
	`, key, outcome)
	if err != nil {
		return fmt.Errorf("record tool call outcome: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// Lookup returns nil (not an error) when key has no reservation.
func (s idempotencyLedgerStore) Lookup(ctx context.Context, key string) (*ToolCallReservation, error) {
	r, err := scanToolCallReservation(s.pool.QueryRow(ctx, `
		SELECT `+toolCallReservationColumns+`
		FROM tool_call_idempotency
		WHERE idempotency_key = $1
	`, key))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("look up tool call reservation: %w", err)
	}
	return &r, nil
}
