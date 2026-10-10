//go:build integration

// See //whagent_net/session:session_integration_test's BUILD comment for
// why this file only builds under the "integration" tag.
package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/whagent_net/session"
)

func pinnedTestDefinition(agentID, model string, maxTurns int) *session.AgentDefinition {
	return &session.AgentDefinition{AgentID: agentID, Model: &model, MaxTurns: maxTurns}
}

// TestActivities_ResolveAgentDefinition_SupersededMidSession_ResolvesPinnedID
// proves a session keeps running the definition it is pinned to after the
// agent is edited (SCD2 close-and-open), and picks up a new definition only
// when re-assigned.
func TestActivities_ResolveAgentDefinition_SupersededMidSession_ResolvesPinnedID(t *testing.T) {
	ctx := context.Background()
	store, db := newTestStore(t)
	sess := newTestSessionRow(t, ctx, store)
	a := &Activities{Store: store}

	pinned := pinnedTestDefinition("pin-agent", "model-v1", 10)
	require.NoError(t, store.AgentDefinitions().Upsert(ctx, pinned))
	require.NoError(t, store.AgentDefinitions().AssignToSession(ctx, sess.SessionID, pinned.ID))

	res, err := a.ResolveAgentDefinition(ctx, sess.SessionID)
	require.NoError(t, err)
	assert.Equal(t, pinned.ID, res.Definition.ID)
	assert.Equal(t, "model-v1", res.Model)

	// The agent is edited: the pinned row is closed and a new current row opens.
	_, err = db.Pool.Exec(ctx, `UPDATE agent_definition SET valid_to = NOW() WHERE id = $1`, pinned.ID)
	require.NoError(t, err)
	edited := pinnedTestDefinition("pin-agent", "model-v2", 99)
	require.NoError(t, store.AgentDefinitions().Upsert(ctx, edited))

	// The next turn still runs the pinned (now superseded) definition.
	res, err = a.ResolveAgentDefinition(ctx, sess.SessionID)
	require.NoError(t, err)
	assert.Equal(t, pinned.ID, res.Definition.ID, "a superseded pinned definition must still be what the session runs")
	assert.Equal(t, "model-v1", res.Model)
	assert.Equal(t, 10, res.Definition.MaxTurns)
	require.NotNil(t, res.Definition.ValidTo)

	// Re-assigning picks up the edit.
	require.NoError(t, store.AgentDefinitions().AssignToSession(ctx, sess.SessionID, edited.ID))
	res, err = a.ResolveAgentDefinition(ctx, sess.SessionID)
	require.NoError(t, err)
	assert.Equal(t, edited.ID, res.Definition.ID)
	assert.Equal(t, "model-v2", res.Model)
}

// TestActivities_ResolveAgentDefinition_NoAssignment_ErrorsWithoutVersion
// proves an unassigned session errors and the message cites no version.
func TestActivities_ResolveAgentDefinition_NoAssignment_ErrorsWithoutVersion(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)
	sess := newTestSessionRow(t, ctx, store)

	_, err := (&Activities{Store: store}).ResolveAgentDefinition(ctx, sess.SessionID)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "version")
}
