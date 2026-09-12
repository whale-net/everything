//go:build integration

// This file only builds under the "integration" build tag (see
// //libs/go/dbtest's README and whagent_net/session/store_integration_test.go
// for the pattern) so `bazel test //...` stays Docker-independent. It spins
// up a throwaway Postgres via dbtest, applies whagent-net's own real
// embedded migrations (//whagent_net/migrate/schema), and exercises
// mcpscope.Resolver against a real whagent_net/session.Store -- proving
// issue #2427's Testing section end to end: ScopeForAgent/ScopeForSession
// resolve a real seeded Scope, an unknown agent/session id is a
// distinguishable not-found error, and a session's already-recorded
// assignment wins over a newer version of the same agent definition with a
// different scope.
package mcpscope_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
	"github.com/whale-net/everything/whagent_net/mcpscope"
	"github.com/whale-net/everything/whagent_net/migrate/schema"
	"github.com/whale-net/everything/whagent_net/session"
)

// newStore provisions an isolated, migrated Postgres database (dbtest) and
// returns a ready *session.Store with publishing disabled -- mirrors
// whagent_net/session/store_integration_test.go's own newStore, kept as a
// separate copy here since that one is unexported from package
// session_test and this file lives in package mcpscope_test.
func newStore(t *testing.T) *session.Store {
	t.Helper()
	ctx := context.Background()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "apply every migration from the real embedded schema")

	return session.New(db.Pool, nil)
}

func strPtr(s string) *string { return &s }

func newTestAgentDefinition(agentID string, scope *string, version int) *session.AgentDefinition {
	return &session.AgentDefinition{
		AgentID:  agentID,
		Scope:    scope,
		Version:  version,
		Model:    strPtr("test-model"),
		ToolSet:  []session.ToolServerRef{{ServerURL: "https://mcp.example.com/research"}},
		MaxTurns: 100,
	}
}

func createTestSession(t *testing.T, ctx context.Context, s *session.Store) *session.Session {
	t.Helper()
	subject := session.Subject{
		Iss:  "https://issuer.example.com",
		Sub:  "user-" + uuid.NewString(),
		Kind: session.SubjectKindHuman,
	}
	sess := &session.Session{
		SessionID:  uuid.New(),
		Subject:    subject,
		OnBehalfOf: subject,
		AgentID:    "test-agent",
		Model:      "test-model",
		Status:     session.StatusRunning,
	}
	require.NoError(t, s.Sessions().Create(ctx, sess))
	return sess
}

// TestScopeForAgent_ReturnsSeededDefinitionsScope proves ScopeForAgent
// resolves a real seeded agent_definition row's Scope end to end.
func TestScopeForAgent_ReturnsSeededDefinitionsScope(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	resolver := mcpscope.New(store.AgentDefinitions())

	require.NoError(t, store.AgentDefinitions().Upsert(ctx, newTestAgentDefinition("research-agent", strPtr("audience_score_system"), 1)))

	scope, err := resolver.ScopeForAgent(ctx, "research-agent")
	require.NoError(t, err)
	require.NotNil(t, scope)
	assert.Equal(t, "audience_score_system", *scope)
}

// TestScopeForAgent_NilScope_ResolvesToNilWithoutError proves a real
// seeded agent_definition row with no scope resolves to a nil *string, not
// an error.
func TestScopeForAgent_NilScope_ResolvesToNilWithoutError(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	resolver := mcpscope.New(store.AgentDefinitions())

	require.NoError(t, store.AgentDefinitions().Upsert(ctx, newTestAgentDefinition("scopeless-agent", nil, 1)))

	scope, err := resolver.ScopeForAgent(ctx, "scopeless-agent")
	require.NoError(t, err)
	assert.Nil(t, scope)
}

// TestScopeForAgent_UnknownAgent_ReturnsErrNotFound proves an unknown
// agent id returns mcpscope.ErrNotFound, not a nil scope with a nil
// error.
func TestScopeForAgent_UnknownAgent_ReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	resolver := mcpscope.New(store.AgentDefinitions())

	scope, err := resolver.ScopeForAgent(ctx, "does-not-exist")
	assert.Nil(t, scope)
	require.Error(t, err)
	assert.True(t, errors.Is(err, mcpscope.ErrNotFound))
}

// TestScopeForSession_ReturnsAssignedVersionsScope_NotNewerVersions
// proves the core FR7 promise: ScopeForSession resolves the Scope of the
// exact (AgentID, Version) recorded in the session's session_agent
// assignment -- even when a *newer* version of the same agent definition
// has since been upserted with a different Scope.
func TestScopeForSession_ReturnsAssignedVersionsScope_NotNewerVersions(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	resolver := mcpscope.New(store.AgentDefinitions())
	sess := createTestSession(t, ctx, store)

	require.NoError(t, store.AgentDefinitions().Upsert(ctx, newTestAgentDefinition("research-agent", strPtr("audience_score_system"), 1)))
	require.NoError(t, store.AgentDefinitions().AssignToSession(ctx, sess.SessionID, "research-agent", 1))

	// A newer version of the same agent, with a different scope, is
	// upserted after the assignment above -- ScopeForSession must still
	// resolve version 1's scope, never this one.
	require.NoError(t, store.AgentDefinitions().Upsert(ctx, newTestAgentDefinition("research-agent", strPtr("manmanv2"), 2)))

	scope, err := resolver.ScopeForSession(ctx, sess.SessionID.String())
	require.NoError(t, err)
	require.NotNil(t, scope)
	assert.Equal(t, "audience_score_system", *scope, "ScopeForSession must resolve the session's recorded assignment, not the agent's current/latest version")

	// Sanity: ScopeForAgent (which does resolve "current") disagrees,
	// proving the two really do take different paths.
	latestScope, err := resolver.ScopeForAgent(ctx, "research-agent")
	require.NoError(t, err)
	require.NotNil(t, latestScope)
	assert.Equal(t, "manmanv2", *latestScope)
}

// TestScopeForSession_UnknownSession_ReturnsErrNotFound proves an unknown
// (but well-formed) session id returns mcpscope.ErrNotFound.
func TestScopeForSession_UnknownSession_ReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	resolver := mcpscope.New(store.AgentDefinitions())

	scope, err := resolver.ScopeForSession(ctx, uuid.NewString())
	assert.Nil(t, scope)
	require.Error(t, err)
	assert.True(t, errors.Is(err, mcpscope.ErrNotFound))
}

// TestScopeForSession_MalformedSessionID_ReturnsErrNotFound proves a
// not-a-uuid session id is treated as not-found rather than surfacing a
// raw parse error.
func TestScopeForSession_MalformedSessionID_ReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	resolver := mcpscope.New(store.AgentDefinitions())

	scope, err := resolver.ScopeForSession(ctx, "not-a-uuid")
	assert.Nil(t, scope)
	require.Error(t, err)
	assert.True(t, errors.Is(err, mcpscope.ErrNotFound))
}
