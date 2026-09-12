//go:build integration

// This file only builds under the "integration" build tag (see
// session_integration_test.go's own doc comment for the pattern). It
// proves ListAgentDefinitionScopes (issue #2432's grants-page extension):
// distinct, sorted scopes across every agent_definition row, deduplicated
// across agent_id/version, and never including a null scope.
package handlers_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/whale-net/everything/whagent_net/protos"
	"github.com/whale-net/everything/whagent_net/session"
)

func upsertScopedAgentDefinition(t *testing.T, ctx context.Context, store *session.Store, agentID string, scope *string) {
	t.Helper()
	def := &session.AgentDefinition{
		AgentID:  agentID,
		Scope:    scope,
		Version:  1,
		Model:    strPtr2("test-model"),
		MaxTurns: 100,
	}
	require.NoError(t, store.AgentDefinitions().Upsert(ctx, def))
}

// TestListAgentDefinitionScopes_DistinctSortedExcludingNull proves the
// RPC returns every distinct non-null scope, sorted, deduplicated across
// agent_id/version, with no null-scope row surfaced as an empty string.
func TestListAgentDefinitionScopes_DistinctSortedExcludingNull(t *testing.T) {
	ctx := context.Background()
	client, store := newTestServer(t)

	upsertScopedAgentDefinition(t, ctx, store, "agent-manman", strPtr2("manmanv2"))
	upsertScopedAgentDefinition(t, ctx, store, "agent-ass", strPtr2("audience_score_system"))
	upsertScopedAgentDefinition(t, ctx, store, "agent-ass-2", strPtr2("audience_score_system"))
	upsertScopedAgentDefinition(t, ctx, store, "agent-scopeless", nil)

	resp, err := client.ListAgentDefinitionScopes(ctx, &pb.ListAgentDefinitionScopesRequest{})
	require.NoError(t, err)
	assert.Equal(t, []string{"audience_score_system", "manmanv2"}, resp.GetScopes())
}

// TestListAgentDefinitionScopes_NoScopedDefinitions_ReturnsEmpty proves an
// empty deployment returns an empty (not error) list.
func TestListAgentDefinitionScopes_NoScopedDefinitions_ReturnsEmpty(t *testing.T) {
	ctx := context.Background()
	client, _ := newTestServer(t)

	resp, err := client.ListAgentDefinitionScopes(ctx, &pb.ListAgentDefinitionScopesRequest{})
	require.NoError(t, err)
	assert.Empty(t, resp.GetScopes())
}
