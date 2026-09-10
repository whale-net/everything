//go:build integration

package session_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/whagent_net/session"
)

func strPtr(s string) *string { return &s }

func newTestAgentDefinition(agentID string, version int) *session.AgentDefinition {
	return &session.AgentDefinition{
		AgentID:  agentID,
		Domain:   "test-domain",
		Version:  version,
		Model:    strPtr("test-model"),
		ToolSet:  []session.ToolServerRef{{ServerURL: "https://mcp.example.com/research"}},
		MaxTurns: 100,
	}
}

// TestAgentDefinitionStore_Upsert_GetLatest_GetVersion_RoundTrip proves the
// basic write/read paths, including that GetLatest returns the
// highest-version row and Upsert's ON CONFLICT replace path keeps
// created_at from the original insert.
func TestAgentDefinitionStore_Upsert_GetLatest_GetVersion_RoundTrip(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)

	def1 := newTestAgentDefinition("research-agent", 1)
	require.NoError(t, s.AgentDefinitions().Upsert(ctx, def1))
	assert.False(t, def1.CreatedAt.IsZero())

	def2 := newTestAgentDefinition("research-agent", 2)
	def2.Model = strPtr("test-model-v2")
	require.NoError(t, s.AgentDefinitions().Upsert(ctx, def2))

	latest, err := s.AgentDefinitions().GetLatest(ctx, "research-agent")
	require.NoError(t, err)
	require.NotNil(t, latest)
	assert.Equal(t, 2, latest.Version)
	require.NotNil(t, latest.Model)
	assert.Equal(t, "test-model-v2", *latest.Model)

	v1, err := s.AgentDefinitions().GetVersion(ctx, "research-agent", 1)
	require.NoError(t, err)
	require.NotNil(t, v1)
	require.NotNil(t, v1.Model)
	assert.Equal(t, "test-model", *v1.Model)

	// Re-upsert version 1 with a different model -- created_at must be
	// unchanged (the doc comment's "replace every column except
	// created_at" promise).
	replacement := newTestAgentDefinition("research-agent", 1)
	replacement.Model = strPtr("test-model-replaced")
	require.NoError(t, s.AgentDefinitions().Upsert(ctx, replacement))
	assert.Equal(t, def1.CreatedAt.UTC(), replacement.CreatedAt.UTC(), "re-upserting an existing (agent_id, version) must keep the original created_at")

	reread, err := s.AgentDefinitions().GetVersion(ctx, "research-agent", 1)
	require.NoError(t, err)
	require.NotNil(t, reread.Model)
	assert.Equal(t, "test-model-replaced", *reread.Model)
}

// TestAgentDefinitionStore_Domain_RoundTripsThroughGetVersion proves an
// AgentDefinition written with a Domain (issue #2424 FR1) round-trips
// through GetVersion unchanged, and that CurrentAssignment -> GetVersion
// (the exact path worker/activities.go's ResolveAgentDefinition walks to
// find the grant key's input, FR4) resolves a non-empty Domain.
func TestAgentDefinitionStore_Domain_RoundTripsThroughGetVersion(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	sess := createTestSession(t, ctx, s)

	def := newTestAgentDefinition("domain-agent", 1)
	def.Domain = "audience_score_system"
	require.NoError(t, s.AgentDefinitions().Upsert(ctx, def))

	got, err := s.AgentDefinitions().GetVersion(ctx, "domain-agent", 1)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "audience_score_system", got.Domain)

	require.NoError(t, s.AgentDefinitions().AssignToSession(ctx, sess.SessionID, "domain-agent", 1))
	assignment, err := s.AgentDefinitions().CurrentAssignment(ctx, sess.SessionID)
	require.NoError(t, err)
	require.NotNil(t, assignment)

	resolved, err := s.AgentDefinitions().GetVersion(ctx, assignment.AgentID, assignment.AgentVersion)
	require.NoError(t, err)
	require.NotNil(t, resolved)
	assert.NotEmpty(t, resolved.Domain, "CurrentAssignment -> GetVersion must resolve a non-empty Domain")
	assert.Equal(t, "audience_score_system", resolved.Domain)
}

// TestAgentDefinitionStore_GetLatest_UnknownAgent_ReturnsNilNotError proves
// GetLatest's documented "no rows -> nil, nil" contract.
func TestAgentDefinitionStore_GetLatest_UnknownAgent_ReturnsNilNotError(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)

	got, err := s.AgentDefinitions().GetLatest(ctx, "does-not-exist")
	require.NoError(t, err)
	assert.Nil(t, got)
}

// TestAgentDefinitionStore_AssignToSession_TwiceClosesFirstOpensOne proves
// the SCD2 close-and-open pair: assigning twice leaves exactly one row with
// valid_to IS NULL, and the first row's valid_to is set.
func TestAgentDefinitionStore_AssignToSession_TwiceClosesFirstOpensOne(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	sess := createTestSession(t, ctx, s)

	require.NoError(t, s.AgentDefinitions().Upsert(ctx, newTestAgentDefinition("agent-a", 1)))
	require.NoError(t, s.AgentDefinitions().Upsert(ctx, newTestAgentDefinition("agent-b", 1)))

	require.NoError(t, s.AgentDefinitions().AssignToSession(ctx, sess.SessionID, "agent-a", 1))
	first, err := s.AgentDefinitions().CurrentAssignment(ctx, sess.SessionID)
	require.NoError(t, err)
	require.NotNil(t, first)
	assert.Equal(t, "agent-a", first.AgentID)
	assert.Nil(t, first.ValidTo)

	require.NoError(t, s.AgentDefinitions().AssignToSession(ctx, sess.SessionID, "agent-b", 1))

	current, err := s.AgentDefinitions().CurrentAssignment(ctx, sess.SessionID)
	require.NoError(t, err)
	require.NotNil(t, current)
	assert.Equal(t, "agent-b", current.AgentID)
	assert.Nil(t, current.ValidTo)

	// Exactly one open row must exist for the session.
	var openCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM session_agent WHERE session_id = $1 AND valid_to IS NULL
	`, sess.SessionID).Scan(&openCount))
	assert.Equal(t, 1, openCount, "exactly one open session_agent row must survive a second AssignToSession")

	// The first (agent-a) row must now be closed.
	var closedCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM session_agent WHERE session_id = $1 AND agent_id = 'agent-a' AND valid_to IS NOT NULL
	`, sess.SessionID).Scan(&closedCount))
	assert.Equal(t, 1, closedCount, "the first assignment (agent-a) must be closed (valid_to set), not deleted")
}

// TestAgentDefinitionStore_PartialUniqueIndex_RejectsTwoOpenRows proves the
// partial unique index itself is real DB enforcement (NFR6): inserting a
// second open (valid_to IS NULL) session_agent row directly -- bypassing
// AssignToSession's close-and-open transaction entirely -- must be
// rejected.
func TestAgentDefinitionStore_PartialUniqueIndex_RejectsTwoOpenRows(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	sess := createTestSession(t, ctx, s)

	require.NoError(t, s.AgentDefinitions().Upsert(ctx, newTestAgentDefinition("agent-a", 1)))
	require.NoError(t, s.AgentDefinitions().AssignToSession(ctx, sess.SessionID, "agent-a", 1))

	_, err := db.Pool.Exec(ctx, `
		INSERT INTO session_agent (session_id, agent_id, agent_version) VALUES ($1, 'agent-a', 1)
	`, sess.SessionID)
	assert.Error(t, err, "a second open session_agent row for the same session must be rejected by the partial unique index")

	var openCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM session_agent WHERE session_id = $1 AND valid_to IS NULL
	`, sess.SessionID).Scan(&openCount))
	assert.Equal(t, 1, openCount, "the rejected insert must not leave a second open row behind")
}
