//go:build integration

package session_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/whagent_net/session"
)

// TestIdleTimeout_DefinitionRoundTripsThroughUpsertAndSupersede proves the
// SCD2 agent_definition row stores idle_timeout_seconds on Upsert, returns it
// from GetCurrent and GetByID, and carries it into a superseding version.
func TestIdleTimeout_DefinitionRoundTripsThroughUpsertAndSupersede(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)

	def := newTestAgentDefinition("idle-agent")
	secs := int32(86400)
	def.IdleTimeoutSeconds = &secs
	require.NoError(t, s.AgentDefinitions().Upsert(ctx, def))

	current, err := s.AgentDefinitions().GetCurrent(ctx, "idle-agent")
	require.NoError(t, err)
	require.NotNil(t, current)
	require.NotNil(t, current.IdleTimeoutSeconds)
	assert.Equal(t, int32(86400), *current.IdleTimeoutSeconds)

	// A superseding version that leaves idle timeout unset must store NULL.
	next := newTestAgentDefinition("idle-agent")
	supersedeAgentDefinition(t, ctx, s, db, next)
	current, err = s.AgentDefinitions().GetCurrent(ctx, "idle-agent")
	require.NoError(t, err)
	assert.Nil(t, current.IdleTimeoutSeconds, "unset idle timeout must be NULL, i.e. platform default")

	old, err := s.AgentDefinitions().GetByID(ctx, def.ID)
	require.NoError(t, err)
	require.NotNil(t, old)
	require.NotNil(t, old.IdleTimeoutSeconds, "superseded version keeps its pinned idle timeout")
	assert.Equal(t, int32(86400), *old.IdleTimeoutSeconds)
}

// TestIdleTimeout_SessionOverridePinnedWithoutMutatingDefinition proves a
// session's idle timeout override is stored on the sessions row and leaves
// the agent definition untouched.
func TestIdleTimeout_SessionOverridePinnedWithoutMutatingDefinition(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)

	def := newTestAgentDefinition("override-agent")
	defSecs := int32(3600)
	def.IdleTimeoutSeconds = &defSecs
	supersedeAgentDefinition(t, ctx, s, db, def)

	override := int32(90)
	sess := newTestSession()
	sess.AgentID = "override-agent"
	sess.IdleTimeoutOverride = &override
	require.NoError(t, s.Sessions().Create(ctx, sess))

	got, err := s.Sessions().GetByID(ctx, sess.SessionID)
	require.NoError(t, err)
	require.NotNil(t, got.IdleTimeoutOverride)
	assert.Equal(t, int32(90), *got.IdleTimeoutOverride)

	current, err := s.AgentDefinitions().GetCurrent(ctx, "override-agent")
	require.NoError(t, err)
	require.NotNil(t, current.IdleTimeoutSeconds)
	assert.Equal(t, int32(3600), *current.IdleTimeoutSeconds, "definition must not be mutated by a session override")

	// A session without an override stores NULL, so resolution falls through to the definition.
	plain := newTestSession()
	plain.AgentID = "override-agent"
	require.NoError(t, s.Sessions().Create(ctx, plain))
	gotPlain, err := s.Sessions().GetByID(ctx, plain.SessionID)
	require.NoError(t, err)
	assert.Nil(t, gotPlain.IdleTimeoutOverride)

	eff := session.EffectiveIdleTimeout(gotPlain.IdleTimeoutOverride, *current, session.DefaultIdleTimeout)
	assert.Equal(t, int32(3600), session.IdleTimeoutSeconds(eff))
}
