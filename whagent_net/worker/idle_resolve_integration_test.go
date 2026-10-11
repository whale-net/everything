//go:build integration

// See //whagent_net/session:session_integration_test's BUILD comment for
// why this file only builds under the "integration" tag.
package main

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/whagent_net/session"
)

// TestActivities_ResolveIdleTimeout_PrecedenceFromStore proves the activity
// reads the session's override and its assigned definition from the store and
// applies override > definition > platform default.
func TestActivities_ResolveIdleTimeout_PrecedenceFromStore(t *testing.T) {
	ctx := context.Background()
	store, db := newTestStore(t)
	platform := 2 * time.Hour
	a := &Activities{Store: store, DefaultIdleTimeout: platform}

	defSecs := int32(3600)
	def := &session.AgentDefinition{AgentID: "idle-agent", Model: strPtrW("model-i"), MaxTurns: 10, IdleTimeoutSeconds: &defSecs}
	require.NoError(t, store.AgentDefinitions().Upsert(ctx, def))

	t.Run("override wins over definition", func(t *testing.T) {
		sess := newTestSessionRow(t, ctx, store)
		require.NoError(t, store.AgentDefinitions().AssignToSession(ctx, sess.SessionID, def.ID))
		_, err := db.Pool.Exec(ctx, `UPDATE sessions SET idle_timeout_seconds = $1 WHERE session_id = $2`, 90, sess.SessionID)
		require.NoError(t, err)

		res, err := a.ResolveIdleTimeout(ctx, sess.SessionID)
		require.NoError(t, err)
		assert.Equal(t, 90*time.Second, res.EffectiveTimeout)
	})

	t.Run("definition wins over platform when no override", func(t *testing.T) {
		sess := newTestSessionRow(t, ctx, store)
		require.NoError(t, store.AgentDefinitions().AssignToSession(ctx, sess.SessionID, def.ID))

		res, err := a.ResolveIdleTimeout(ctx, sess.SessionID)
		require.NoError(t, err)
		assert.Equal(t, time.Hour, res.EffectiveTimeout)
	})

	t.Run("platform default when neither override nor definition is set", func(t *testing.T) {
		bare := &session.AgentDefinition{AgentID: "idle-bare-agent", Model: strPtrW("model-b"), MaxTurns: 10}
		require.NoError(t, store.AgentDefinitions().Upsert(ctx, bare))
		sess := newTestSessionRow(t, ctx, store)
		require.NoError(t, store.AgentDefinitions().AssignToSession(ctx, sess.SessionID, bare.ID))

		res, err := a.ResolveIdleTimeout(ctx, sess.SessionID)
		require.NoError(t, err)
		assert.Equal(t, platform, res.EffectiveTimeout)
	})

	t.Run("unassigned session falls back to platform default", func(t *testing.T) {
		sess := newTestSessionRow(t, ctx, store)

		res, err := a.ResolveIdleTimeout(ctx, sess.SessionID)
		require.NoError(t, err)
		assert.Equal(t, platform, res.EffectiveTimeout)
	})

	t.Run("missing session is an error", func(t *testing.T) {
		_, err := a.ResolveIdleTimeout(ctx, uuid.New())
		require.Error(t, err)
	})
}
