// No-database unit test for RegisterMilestoneStatusAll's tool set
// (milestone_status.go, issue #2685's Testing item 5's MCP-layer half of
// NFR2). store.New(nil) is safe here (see krill/store/store.go's New): it
// holds the *pgxpool.Pool but never dials it at construction, and
// mcp.AddTool registration never queries -- it only builds the tool's
// schema and handler closure -- so no Postgres is required for this file,
// mirroring krill/mcp/server/registry_tools_test.go's own package-doc
// comment for the identical reasoning. sessions is passed as a nil
// store.SessionStore interface value for the same reason: registration
// only closes over it, it never calls a method on it.
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

// expectedMilestoneStatusToolNames is exactly the three tools
// RegisterMilestoneStatusAll wires -- see krill/mcp/tools/milestone_status.go.
var expectedMilestoneStatusToolNames = []string{
	"set_milestone_status",
	"get_milestone_status",
	"get_milestone_status_history",
}

// TestRegisterMilestoneStatusAll_RegistersExactlyThreeTools_NoUpdateOrDeleteTool
// is issue #2685's Testing item 5's MCP-layer structural proof (NFR2): a
// real *mcp.Server/Registry, given only RegisterMilestoneStatusAll, ends up
// with exactly {set_milestone_status, get_milestone_status,
// get_milestone_status_history} registered -- no update_milestone_status,
// delete_milestone_status, or any other mutation-shaped tool exists on
// this registry at all. Listed over a real in-memory MCP client/server
// connection (mcp.NewInMemoryTransports), not by inspecting Go source, so
// this proves what an actual MCP client would see.
func TestRegisterMilestoneStatusAll_RegistersExactlyThreeTools_NoUpdateOrDeleteTool(t *testing.T) {
	ctx := context.Background()
	statuses := store.New(nil).MilestoneStatus()

	srv := mcp.NewServer(server.Implementation, nil)
	reg := server.NewRegistry(srv)
	tools.RegisterMilestoneStatusAll(reg, nil, statuses)

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

	require.Len(t, registered, len(expectedMilestoneStatusToolNames), "RegisterMilestoneStatusAll must register exactly these three tools -- nothing more, nothing fewer")
	for _, name := range expectedMilestoneStatusToolNames {
		assert.True(t, registered[name], "%s must be registered", name)
	}
	for name := range registered {
		assert.Contains(t, expectedMilestoneStatusToolNames, name,
			"%s is not one of the three FR8/FR9/FR12 status tools -- NFR2 forbids any update/delete-shaped tool on this surface", name)
	}
}
