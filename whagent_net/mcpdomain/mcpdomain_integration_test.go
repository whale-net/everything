//go:build integration

// This file only builds under the "integration" build tag (see
// //libs/go/dbtest's README and whagent_net/session/store_integration_test.go
// for the pattern) so `bazel test //...` stays Docker-independent. It spins
// up a throwaway Postgres via dbtest, applies whagent-net's own real
// embedded migrations (//whagent_net/migrate/schema), and exercises
// mcpdomain.Resolver against a real whagent_net/session.Store -- proving
// issue #2427's Testing section end to end: DomainForAgent/DomainForSession
// resolve a real seeded Domain, an unknown agent/session id is a
// distinguishable not-found error, and a session's already-recorded
// assignment wins over a newer version of the same agent definition with a
// different domain.
package mcpdomain_test

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
	"github.com/whale-net/everything/whagent_net/mcpdomain"
	"github.com/whale-net/everything/whagent_net/migrate/schema"
	"github.com/whale-net/everything/whagent_net/session"
)

// newStore provisions an isolated, migrated Postgres database (dbtest) and
// returns a ready *session.Store with publishing disabled -- mirrors
// whagent_net/session/store_integration_test.go's own newStore, kept as a
// separate copy here since that one is unexported from package
// session_test and this file lives in package mcpdomain_test.
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

func newTestAgentDefinition(agentID, domain string, version int) *session.AgentDefinition {
	return &session.AgentDefinition{
		AgentID:  agentID,
		Domain:   domain,
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

// TestDomainForAgent_ReturnsSeededDefinitionsDomain proves DomainForAgent
// resolves a real seeded agent_definition row's Domain end to end.
func TestDomainForAgent_ReturnsSeededDefinitionsDomain(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	resolver := mcpdomain.New(store.AgentDefinitions())

	require.NoError(t, store.AgentDefinitions().Upsert(ctx, newTestAgentDefinition("research-agent", "audience_score_system", 1)))

	domain, err := resolver.DomainForAgent(ctx, "research-agent")
	require.NoError(t, err)
	assert.Equal(t, "audience_score_system", domain)
}

// TestDomainForAgent_UnknownAgent_ReturnsErrNotFound proves an unknown
// agent id returns mcpdomain.ErrNotFound, not an empty string with a nil
// error.
func TestDomainForAgent_UnknownAgent_ReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	resolver := mcpdomain.New(store.AgentDefinitions())

	domain, err := resolver.DomainForAgent(ctx, "does-not-exist")
	assert.Empty(t, domain)
	require.Error(t, err)
	assert.True(t, errors.Is(err, mcpdomain.ErrNotFound))
}

// TestDomainForSession_ReturnsAssignedVersionsDomain_NotNewerVersions
// proves the core FR7 promise: DomainForSession resolves the Domain of the
// exact (AgentID, Version) recorded in the session's session_agent
// assignment -- even when a *newer* version of the same agent definition
// has since been upserted with a different Domain.
func TestDomainForSession_ReturnsAssignedVersionsDomain_NotNewerVersions(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	resolver := mcpdomain.New(store.AgentDefinitions())
	sess := createTestSession(t, ctx, store)

	require.NoError(t, store.AgentDefinitions().Upsert(ctx, newTestAgentDefinition("research-agent", "audience_score_system", 1)))
	require.NoError(t, store.AgentDefinitions().AssignToSession(ctx, sess.SessionID, "research-agent", 1))

	// A newer version of the same agent, with a different domain, is
	// upserted after the assignment above -- DomainForSession must still
	// resolve version 1's domain, never this one.
	require.NoError(t, store.AgentDefinitions().Upsert(ctx, newTestAgentDefinition("research-agent", "manmanv2", 2)))

	domain, err := resolver.DomainForSession(ctx, sess.SessionID.String())
	require.NoError(t, err)
	assert.Equal(t, "audience_score_system", domain, "DomainForSession must resolve the session's recorded assignment, not the agent's current/latest version")

	// Sanity: DomainForAgent (which does resolve "current") disagrees,
	// proving the two really do take different paths.
	latestDomain, err := resolver.DomainForAgent(ctx, "research-agent")
	require.NoError(t, err)
	assert.Equal(t, "manmanv2", latestDomain)
}

// TestDomainForSession_UnknownSession_ReturnsErrNotFound proves an unknown
// (but well-formed) session id returns mcpdomain.ErrNotFound.
func TestDomainForSession_UnknownSession_ReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	resolver := mcpdomain.New(store.AgentDefinitions())

	domain, err := resolver.DomainForSession(ctx, uuid.NewString())
	assert.Empty(t, domain)
	require.Error(t, err)
	assert.True(t, errors.Is(err, mcpdomain.ErrNotFound))
}

// TestDomainForSession_MalformedSessionID_ReturnsErrNotFound proves a
// not-a-uuid session id is treated as not-found rather than surfacing a
// raw parse error.
func TestDomainForSession_MalformedSessionID_ReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	resolver := mcpdomain.New(store.AgentDefinitions())

	domain, err := resolver.DomainForSession(ctx, "not-a-uuid")
	assert.Empty(t, domain)
	require.Error(t, err)
	assert.True(t, errors.Is(err, mcpdomain.ErrNotFound))
}
