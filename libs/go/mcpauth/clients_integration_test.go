//go:build integration

// This file only builds under the "integration" build tag — see
// credential_integration_test.go's header comment for the full rationale
// (mirrored here for the OAuth client registry instead of the credential
// store).
//
// These tests exercise exactly what clients_test.go's pure-Go unit tests
// cannot: real PostgreSQL preflight failure/success against a real
// mcp_oauth_client-shaped table, and — the guarantee NewMemoryClientRegistry
// cannot give at all — a client registered via one *pgxClientRegistry
// instance being retrievable via a second, separately constructed instance
// sharing the same database (the "replica A / replica B" scenario
// README.md documents).
package mcpauth

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/dbtest"
)

const oauthClientSchema = `
	CREATE TABLE mcp_oauth_client (
		client_id  TEXT        PRIMARY KEY,
		metadata   JSONB       NOT NULL,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);
`

// ── preflight ────────────────────────────────────────────────────────────

func TestClientRegistryPreflight_MissingTable_FailsNamingTable(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{
		// No Schema — mcp_oauth_client does not exist.
	})

	reg, err := NewPostgresClientRegistry(ctx, ClientRegistryConfig{Pool: db.Pool})

	require.Error(t, err, "preflight must fail when mcp_oauth_client does not exist")
	assert.Nil(t, reg, "on preflight failure, no ClientRegistry must be handed back")
	assert.Contains(t, err.Error(), "mcp_oauth_client", "error must name the table")
}

func TestClientRegistryPreflight_PresentTable_BootProceeds(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: oauthClientSchema})

	reg, err := NewPostgresClientRegistry(ctx, ClientRegistryConfig{Pool: db.Pool})

	require.NoError(t, err)
	require.NotNil(t, reg)
}

// ── multi-replica round trip ─────────────────────────────────────────────

// TestPostgresClientRegistry_MultiReplica_RegisterOnOneGetOnAnother proves
// the guarantee memoryClientRegistry cannot give: a client dynamically
// registered against one *pgxClientRegistry instance ("replica A") is
// retrievable via a second, entirely separate instance ("replica B")
// sharing the same underlying database.
func TestPostgresClientRegistry_MultiReplica_RegisterOnOneGetOnAnother(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: oauthClientSchema})

	replicaA, err := NewPostgresClientRegistry(ctx, ClientRegistryConfig{Pool: db.Pool})
	require.NoError(t, err)
	replicaB, err := NewPostgresClientRegistry(ctx, ClientRegistryConfig{Pool: db.Pool})
	require.NoError(t, err)

	meta := oauthex.ClientRegistrationMetadata{
		RedirectURIs: []string{"https://client.example.com/callback"},
		ClientName:   "Cross-Replica Client",
	}
	client, err := replicaA.Register(ctx, meta)
	require.NoError(t, err)
	require.NotEmpty(t, client.ClientID)

	got, err := replicaB.Get(ctx, client.ClientID)
	require.NoError(t, err)
	assert.Equal(t, client.ClientID, got.ClientID)
	assert.Equal(t, meta.RedirectURIs, got.RedirectURIs)
	assert.Equal(t, meta.ClientName, got.Metadata.ClientName)
}

func TestPostgresClientRegistry_Get_UnknownClientID_ReturnsErrClientNotFound(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: oauthClientSchema})

	reg, err := NewPostgresClientRegistry(ctx, ClientRegistryConfig{Pool: db.Pool})
	require.NoError(t, err)

	_, err = reg.Get(ctx, "never-registered")
	assert.ErrorIs(t, err, ErrClientNotFound)
}

// TestPostgresClientRegistry_ConcurrentRegistrations_AllSucceedWithDistinctIDs
// proves NewPostgresClientRegistry.Register is safe under concurrent load
// against the pool's connections — a real MCP client population hits
// /register from many replicas/goroutines around the same moment, and
// generateClientID's crypto/rand + a PRIMARY KEY-constrained INSERT must
// not race into either a lost registration or a client_id collision.
func TestPostgresClientRegistry_ConcurrentRegistrations_AllSucceedWithDistinctIDs(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: oauthClientSchema})

	reg, err := NewPostgresClientRegistry(ctx, ClientRegistryConfig{Pool: db.Pool})
	require.NoError(t, err)

	const n = 25
	var wg sync.WaitGroup
	ids := make([]string, n)
	errs := make([]error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			meta := oauthex.ClientRegistrationMetadata{
				RedirectURIs: []string{"https://client.example.com/callback"},
				ClientName:   fmt.Sprintf("Concurrent Client %d", i),
			}
			client, err := reg.Register(ctx, meta)
			ids[i] = client.ClientID
			errs[i] = err
		}(i)
	}
	wg.Wait()

	seen := make(map[string]bool, n)
	for i, err := range errs {
		require.NoError(t, err, "registration %d must not fail under concurrent load", i)
		require.NotEmpty(t, ids[i])
		require.False(t, seen[ids[i]], "client_id %q must not be issued to two concurrent registrations", ids[i])
		seen[ids[i]] = true
	}

	// Every concurrently minted client_id must actually be retrievable
	// afterward — proves the race didn't silently drop a row.
	for i, id := range ids {
		got, err := reg.Get(ctx, id)
		require.NoError(t, err, "registration %d's client_id must be retrievable", i)
		assert.Equal(t, id, got.ClientID)
	}

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, "SELECT COUNT(*) FROM mcp_oauth_client").Scan(&count))
	assert.Equal(t, n, count, "every concurrent registration must persist exactly one row")
}

func TestPostgresClientRegistry_TwoRegistrations_GetDistinctClientIDs(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: oauthClientSchema})

	reg, err := NewPostgresClientRegistry(ctx, ClientRegistryConfig{Pool: db.Pool})
	require.NoError(t, err)

	meta := oauthex.ClientRegistrationMetadata{RedirectURIs: []string{"https://client.example.com/callback"}}
	c1, err := reg.Register(ctx, meta)
	require.NoError(t, err)
	c2, err := reg.Register(ctx, meta)
	require.NoError(t, err)

	assert.NotEqual(t, c1.ClientID, c2.ClientID)

	// And the row really is persisted with the metadata JSON, not just
	// held in-process.
	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, "SELECT COUNT(*) FROM mcp_oauth_client").Scan(&count))
	assert.Equal(t, 2, count)
}
