//go:build integration

package session_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/whagent_net/session"
)

// TestIdempotencyLedger_Reserve_DuplicateReturnsExistingReservation proves
// Reserve on a duplicate (session_id, turn, call_index) returns the
// existing reservation rather than inserting a second row -- even when
// called with a different idempotency key and different Tool/ServerURL
// metadata, proving the winner's original row (not the loser's arguments)
// is what comes back.
func TestIdempotencyLedger_Reserve_DuplicateReturnsExistingReservation(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	sess := createTestSession(t, ctx, s)

	first, err := s.Idempotency().Reserve(ctx, "key-1", session.ToolCallReservation{
		SessionID: sess.SessionID, Turn: 1, CallIndex: 0, Tool: "search", ServerURL: "https://mcp.example.com/a",
	})
	require.NoError(t, err)
	assert.Equal(t, "key-1", first.IdempotencyKey)
	assert.Equal(t, "search", first.Tool)

	second, err := s.Idempotency().Reserve(ctx, "key-2", session.ToolCallReservation{
		SessionID: sess.SessionID, Turn: 1, CallIndex: 0, Tool: "different-tool", ServerURL: "https://mcp.example.com/b",
	})
	require.NoError(t, err)
	assert.Equal(t, first.IdempotencyKey, second.IdempotencyKey, "a duplicate (session_id, turn, call_index) reservation must return the WINNER's key, not insert under the loser's key")
	assert.Equal(t, "search", second.Tool, "the returned reservation must be the original winner's row, not the losing caller's own metadata")

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM tool_call_idempotency WHERE session_id = $1 AND turn = 1 AND call_index = 0
	`, sess.SessionID).Scan(&count))
	assert.Equal(t, 1, count, "a duplicate reservation attempt must not insert a second row")
}

// TestIdempotencyLedger_RecordOutcome_Lookup_RoundTrip proves the plain
// write/read path: RecordOutcome fills in outcome for an existing
// reservation, and Lookup returns it.
func TestIdempotencyLedger_RecordOutcome_Lookup_RoundTrip(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	sess := createTestSession(t, ctx, s)

	_, err := s.Idempotency().Reserve(ctx, "key-outcome", session.ToolCallReservation{
		SessionID: sess.SessionID, Turn: 1, CallIndex: 0, Tool: "search", ServerURL: "https://mcp.example.com/a",
	})
	require.NoError(t, err)

	outcome := json.RawMessage(`{"result": "ok"}`)
	require.NoError(t, s.Idempotency().RecordOutcome(ctx, "key-outcome", outcome))

	got, err := s.Idempotency().Lookup(ctx, "key-outcome")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.JSONEq(t, string(outcome), string(got.Outcome))
}

// TestIdempotencyLedger_Lookup_UnknownKey_ReturnsNilNotError proves
// Lookup's documented "no rows -> nil, nil" contract.
func TestIdempotencyLedger_Lookup_UnknownKey_ReturnsNilNotError(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)

	got, err := s.Idempotency().Lookup(ctx, "does-not-exist")
	require.NoError(t, err)
	assert.Nil(t, got)
}
