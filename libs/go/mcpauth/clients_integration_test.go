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
