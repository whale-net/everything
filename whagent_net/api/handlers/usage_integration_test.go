//go:build integration

// This file proves GetSessionUsage (issue #2238's read path) end to end,
// following session_integration_test.go's pattern: a real gRPC server
// (bufconn) in front of a *handlers.SessionServer backed by a real
// Postgres via //libs/go/dbtest, with the store's own embedded migrations
// applied exactly as whagent_net/session's own integration tests do.
package handlers_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/whale-net/everything/whagent_net/protos"
	"github.com/whale-net/everything/whagent_net/session"
)

// testAgentDefinition builds an AgentDefinition fixture with the given
// caps -- version 1 always exists so tests can also seed a version 2 to
// prove the pinned (not latest) version is read.
func testAgentDefinition(agentID string, version, maxTurns int, maxCostUSD float64) *session.AgentDefinition {
	return &session.AgentDefinition{
		AgentID:    agentID,
		Version:    version,
		Model:      "test-model",
		ToolSet:    []session.ToolServerRef{{ServerURL: "https://mcp.example.com/research"}},
		MaxTurns:   maxTurns,
		MaxCostUSD: maxCostUSD,
	}
}

// TestGetSessionUsage_NotFound proves an unknown session id is NOT_FOUND.
func TestGetSessionUsage_NotFound(t *testing.T) {
	client, _ := newTestServer(t)

	_, err := client.GetSessionUsage(context.Background(), &pb.GetSessionUsageRequest{SessionId: uuid.NewString()})

	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}

// TestGetSessionUsage_NoAssignment_AppliesDefaultCaps proves that a
// session with no agent assignment at all reports caps.go's documented
// defaults (100 turns / $1.00), and turns_used/cost_usd of 0 for a
// session with no committed turn_usage rows.
func TestGetSessionUsage_NoAssignment_AppliesDefaultCaps(t *testing.T) {
	client, store := newTestServer(t)
	ctx := context.Background()
	sess := createSession(t, ctx, store, devSubject, session.StatusRunning)

	resp, err := client.GetSessionUsage(ctx, &pb.GetSessionUsageRequest{SessionId: sess.SessionID.String()})
	require.NoError(t, err)

	assert.Equal(t, int32(0), resp.Usage.TurnsUsed)
	assert.Equal(t, int32(100), resp.Usage.TurnCap, "turn_cap must fall back to caps.go's default (100) when there is no pinned definition")
	assert.Equal(t, 0.0, resp.Usage.CostUsd)
	assert.InDelta(t, 1.0, resp.Usage.CostCapUsd, 0.0000001, "cost_cap_usd must fall back to caps.go's default ($1.00) when there is no pinned definition")
	assert.False(t, resp.Usage.CostEstimated)
}

// TestGetSessionUsage_ZeroValuedDefinitionFields_ApplyDefaults proves that
// a pinned agent_definition with zero-valued MaxTurns/MaxCostUSD still
// falls back to caps.go's defaults, exactly as an absent assignment does
// -- zero is "unset", not "cap of zero".
func TestGetSessionUsage_ZeroValuedDefinitionFields_ApplyDefaults(t *testing.T) {
	client, store := newTestServer(t)
	ctx := context.Background()
	sess := createSession(t, ctx, store, devSubject, session.StatusRunning)

	require.NoError(t, store.AgentDefinitions().Upsert(ctx, testAgentDefinition("zero-caps-agent", 1, 0, 0)))
	require.NoError(t, store.AgentDefinitions().AssignToSession(ctx, sess.SessionID, "zero-caps-agent", 1))

	resp, err := client.GetSessionUsage(ctx, &pb.GetSessionUsageRequest{SessionId: sess.SessionID.String()})
	require.NoError(t, err)

	assert.Equal(t, int32(100), resp.Usage.TurnCap, "a zero-valued MaxTurns on the pinned definition must still fall back to the default")
	assert.InDelta(t, 1.0, resp.Usage.CostCapUsd, 0.0000001, "a zero-valued MaxCostUSD on the pinned definition must still fall back to the default")
}

// TestGetSessionUsage_ReadsCapsFromPinnedVersionNotLatest proves
// GetSessionUsage reads caps from the session's pinned SCD2 assignment
// (session_agent.agent_version), not whatever the latest agent_definition
// version happens to be -- seeding a version 2 with different caps after
// the session was assigned to version 1 must not change what this session
// reports.
func TestGetSessionUsage_ReadsCapsFromPinnedVersionNotLatest(t *testing.T) {
	client, store := newTestServer(t)
	ctx := context.Background()
	sess := createSession(t, ctx, store, devSubject, session.StatusRunning)

	require.NoError(t, store.AgentDefinitions().Upsert(ctx, testAgentDefinition("versioned-agent", 1, 10, 0.50)))
	require.NoError(t, store.AgentDefinitions().AssignToSession(ctx, sess.SessionID, "versioned-agent", 1))

	// A newer version is seeded after the session was pinned to v1, with
	// very different caps -- must not be what this session reports.
	require.NoError(t, store.AgentDefinitions().Upsert(ctx, testAgentDefinition("versioned-agent", 2, 999, 99.0)))

	resp, err := client.GetSessionUsage(ctx, &pb.GetSessionUsageRequest{SessionId: sess.SessionID.String()})
	require.NoError(t, err)

	assert.Equal(t, int32(10), resp.Usage.TurnCap, "turn_cap must come from the session's pinned version (1), not the newer version (2) seeded afterward")
	assert.InDelta(t, 0.50, resp.Usage.CostCapUsd, 0.0000001, "cost_cap_usd must come from the session's pinned version (1), not the newer version (2) seeded afterward")
}

// TestGetSessionUsage_ReportsSummedUsageAgainstPinnedCaps proves the
// turns_used/cost_usd/cost_estimated figures reflect the summed
// turn_usage rows (NFR5), reported alongside the pinned definition's caps.
func TestGetSessionUsage_ReportsSummedUsageAgainstPinnedCaps(t *testing.T) {
	client, store := newTestServer(t)
	ctx := context.Background()
	sess := createSession(t, ctx, store, devSubject, session.StatusRunning)

	require.NoError(t, store.AgentDefinitions().Upsert(ctx, testAgentDefinition("usage-agent", 1, 25, 5.0)))
	require.NoError(t, store.AgentDefinitions().AssignToSession(ctx, sess.SessionID, "usage-agent", 1))

	require.NoError(t, store.Usage().RecordTurn(ctx, session.TurnUsage{
		SessionID: sess.SessionID, Turn: 1, Model: "test-model",
		PromptTokens: 100, CompletionTokens: 50, CostUSD: 0.25, CostEstimated: false,
	}))
	require.NoError(t, store.Usage().RecordTurn(ctx, session.TurnUsage{
		SessionID: sess.SessionID, Turn: 2, Model: "test-model",
		PromptTokens: 200, CompletionTokens: 100, CostUSD: 0.10, CostEstimated: true,
	}))

	resp, err := client.GetSessionUsage(ctx, &pb.GetSessionUsageRequest{SessionId: sess.SessionID.String()})
	require.NoError(t, err)

	assert.Equal(t, int32(2), resp.Usage.TurnsUsed)
	assert.Equal(t, int32(25), resp.Usage.TurnCap)
	assert.InDelta(t, 0.35, resp.Usage.CostUsd, 0.0000001)
	assert.InDelta(t, 5.0, resp.Usage.CostCapUsd, 0.0000001)
	assert.True(t, resp.Usage.CostEstimated, "cost_estimated must be true when any summed row was estimated")
}
