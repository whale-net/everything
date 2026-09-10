//go:build integration

package session_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/whagent_net/session"
)

// TestModelDefinitionStore_Upsert_GetByID_GetByName_RoundTrip proves the
// basic write/read paths, including that Upsert is a true upsert by Name
// (ModelDefinitionStore is deliberately not versioned like
// AgentDefinitionStore -- see modeldef.go's doc comment).
func TestModelDefinitionStore_Upsert_GetByID_GetByName_RoundTrip(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)

	allowFallbacks := false
	def := &session.ModelDefinition{
		Name:  "sonnet-together-only",
		Model: "anthropic/claude-sonnet-4.5",
		Provider: session.ProviderPreferences{
			Only:           []string{"together"},
			AllowFallbacks: &allowFallbacks,
		},
	}
	require.NoError(t, s.ModelDefinitions().Upsert(ctx, def))
	assert.NotEqual(t, "", def.ID.String())
	assert.False(t, def.CreatedAt.IsZero())

	byID, err := s.ModelDefinitions().GetByID(ctx, def.ID)
	require.NoError(t, err)
	require.NotNil(t, byID)
	assert.Equal(t, "anthropic/claude-sonnet-4.5", byID.Model)
	assert.Equal(t, []string{"together"}, byID.Provider.Only)
	require.NotNil(t, byID.Provider.AllowFallbacks)
	assert.False(t, *byID.Provider.AllowFallbacks)

	byName, err := s.ModelDefinitions().GetByName(ctx, "sonnet-together-only")
	require.NoError(t, err)
	require.NotNil(t, byName)
	assert.Equal(t, def.ID, byName.ID)

	// Re-upsert by the same Name replaces model/provider in place --
	// Upsert's documented behavior, distinct from AgentDefinitionStore's
	// version-minting Upsert.
	def.Model = "anthropic/claude-sonnet-4.5-replaced"
	def.Provider = session.ProviderPreferences{Sort: "price"}
	require.NoError(t, s.ModelDefinitions().Upsert(ctx, def))

	reread, err := s.ModelDefinitions().GetByName(ctx, "sonnet-together-only")
	require.NoError(t, err)
	require.NotNil(t, reread)
	assert.Equal(t, def.ID, reread.ID, "re-upserting an existing name must keep the same id")
	assert.Equal(t, "anthropic/claude-sonnet-4.5-replaced", reread.Model)
	assert.Equal(t, "price", reread.Provider.Sort)
	assert.Empty(t, reread.Provider.Only, "the replaced provider block must not merge with the prior one")
}

// TestModelDefinitionStore_GetByID_GetByName_UnknownReturnsNilNotError
// proves both getters' documented "no row -> nil, nil" contract.
func TestModelDefinitionStore_GetByID_GetByName_UnknownReturnsNilNotError(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)

	byName, err := s.ModelDefinitions().GetByName(ctx, "does-not-exist")
	require.NoError(t, err)
	assert.Nil(t, byName)
}

// TestAgentDefinitionStore_ModelDefinitionID_RoundTrips proves an
// AgentDefinition seeded with ModelDefinitionID (not Model) round-trips
// correctly through Upsert/GetLatest -- Model stays nil, exactly the
// shape worker/activities.go's resolveModel expects (migration 006's XOR
// CHECK constraint is exercised implicitly: this insert would be rejected
// by Postgres itself if both were set).
func TestAgentDefinitionStore_ModelDefinitionID_RoundTrips(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)

	modelDef := &session.ModelDefinition{Name: "sonnet-default", Model: "anthropic/claude-sonnet-4.5"}
	require.NoError(t, s.ModelDefinitions().Upsert(ctx, modelDef))

	def := &session.AgentDefinition{
		AgentID:           "agent-with-model-definition",
		Version:           1,
		ModelDefinitionID: &modelDef.ID,
		ToolSet:           []session.ToolServerRef{{ServerURL: "https://mcp.example.com/research"}},
		MaxTurns:          100,
	}
	require.NoError(t, s.AgentDefinitions().Upsert(ctx, def))

	latest, err := s.AgentDefinitions().GetLatest(ctx, "agent-with-model-definition")
	require.NoError(t, err)
	require.NotNil(t, latest)
	assert.Nil(t, latest.Model)
	require.NotNil(t, latest.ModelDefinitionID)
	assert.Equal(t, modelDef.ID, *latest.ModelDefinitionID)
}
