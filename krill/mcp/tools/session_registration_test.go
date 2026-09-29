// No-database unit test for RegisterInitSession's tool set (session.go,
// issue #2827). store.New(nil) is safe here (see krill/store/store.go's
// New): it holds the *pgxpool.Pool but never dials it at construction, and
// mcp.AddTool registration never queries -- it only builds the tool's
// schema and handler closure -- so no Postgres is required for this file,
// mirroring milestone_status_registration_test.go's own reasoning.
// sessions/scopes are passed as nil store.SessionStore/store.ScopeStore
// interface values for the same reason: registration only closes over
// them, it never calls a method on either.
package tools_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/mcp/tools"
	"github.com/whale-net/everything/krill/store"
)

// TestRegisterInitSession_RegistersExactlyOneTool proves RegisterInitSession
// wires exactly {init_session} -- an MCP-only caller has exactly one entry
// point to mint a krill_session_id, not a second one this milestone forgot
// to name. Listed over a real in-memory MCP client/server connection
// (mcp.NewInMemoryTransports), not by inspecting Go source.
func TestRegisterInitSession_RegistersExactlyOneTool(t *testing.T) {
	ctx := context.Background()
	var sessions store.SessionStore
	var scopes store.ScopeStore

	srv := mcp.NewServer(server.Implementation, nil)
	reg := server.NewRegistry(srv)
	tools.RegisterInitSession(reg, sessions, scopes)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	_, err := srv.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })

	registered := map[string]bool{}
	for tool, err := range cs.Tools(ctx, nil) {
		require.NoError(t, err)
		registered[tool.Name] = true
	}

	require.Len(t, registered, 1, "RegisterInitSession must register exactly one tool")
	assert.True(t, registered["init_session"], "init_session must be registered")
}

// init_session's input schema carries no identity or scope fields, and a
// call supplying them is rejected before the handler runs.
func TestRegisterInitSession_SchemaHasNoIdentityFields(t *testing.T) {
	ctx := context.Background()
	var sessions store.SessionStore
	var scopes store.ScopeStore

	srv := mcp.NewServer(server.Implementation, nil)
	reg := server.NewRegistry(srv)
	tools.RegisterInitSession(reg, sessions, scopes)
	tools.RegisterGetScope(reg, scopes)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	_, err := srv.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })

	found := map[string]*mcp.Tool{}
	for tool, err := range cs.Tools(ctx, nil) {
		require.NoError(t, err)
		found[tool.Name] = tool
	}
	require.Contains(t, found, "get_scope")
	require.Contains(t, found, "init_session")

	for _, name := range []string{"init_session", "get_scope"} {
		raw, err := json.Marshal(found[name].InputSchema)
		require.NoError(t, err)
		for _, field := range []string{"acting", "on_behalf_of", "scope_id", "whagent_session_id"} {
			assert.NotContains(t, string(raw), `"`+field+`"`, "%s schema must not carry %s", name, field)
		}
	}

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "init_session",
		Arguments: map[string]any{"acting": map[string]string{"iss": "x", "sub": "y", "kind": "human"}},
	})
	if err == nil {
		assert.True(t, res.IsError, "a supplied identity field must be rejected")
	}
}
