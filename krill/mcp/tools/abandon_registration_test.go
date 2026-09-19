// No-database unit test for RegisterAbandonAll's tool set (abandon.go,
// issue #2688). store.New(nil) is safe here (see krill/store/store.go's
// New): it holds the *pgxpool.Pool but never dials it at construction, and
// mcp.AddTool registration never queries -- it only builds the tool's
// schema and handler closure -- so no Postgres is required for this file,
// mirroring milestone_status_registration_test.go's own reasoning.
package tools_test

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/mcp/tools"
	"github.com/whale-net/everything/krill/store"
)

// expectedAbandonToolNames is exactly the one tool RegisterAbandonAll
// wires -- see krill/mcp/tools/abandon.go.
var expectedAbandonToolNames = []string{"abandon_milestone"}

// TestRegisterAbandonAll_RegistersExactlyOneTool is this file's structural
// proof: a real *mcp.Server/Registry, given only RegisterAbandonAll, ends
// up with exactly {abandon_milestone} registered -- no un-abandon tool
// exists on this registry at all (this milestone's own "not reversible"
// design choice, store.AbandonStore's doc comment). Listed over a real
// in-memory MCP client/server connection (mcp.NewInMemoryTransports), not
// by inspecting Go source, so this proves what an actual MCP client would
// see.
func TestRegisterAbandonAll_RegistersExactlyOneTool(t *testing.T) {
	ctx := context.Background()
	abandons := store.New(nil).Abandon()

	srv := mcp.NewServer(server.Implementation, nil)
	reg := server.NewRegistry(srv)
	tools.RegisterAbandonAll(reg, nil, abandons)

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

	require.Len(t, registered, len(expectedAbandonToolNames), "RegisterAbandonAll must register exactly this one tool -- nothing more, nothing fewer")
	for _, name := range expectedAbandonToolNames {
		assert.True(t, registered[name], "%s must be registered", name)
	}
}
