// No-database unit test for RegisterCreateTask's tool set (task.go, issue
// #2719). store.New(nil) is safe here (see krill/store/store.go's New):
// it holds the *pgxpool.Pool but never dials it at construction, and
// mcp.AddTool registration never queries -- it only builds the tool's
// schema and handler closure -- so no Postgres is required for this file,
// mirroring abandon_registration_test.go's own reasoning. sessions is
// passed as a nil store.SessionStore interface value for the same reason:
// registration only closes over it, it never calls a method on it.
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

// TestRegisterCreateTask_RegistersExactlyOneTool is this file's structural
// proof: a real *mcp.Server/Registry, given only RegisterCreateTask, ends
// up with exactly {create_task} registered -- no other work-axis tool
// exists on this registry at all (this task's own scope, FR1). Listed
// over a real in-memory MCP client/server connection
// (mcp.NewInMemoryTransports), not by inspecting Go source, so this
// proves what an actual MCP client would see.
func TestRegisterCreateTask_RegistersExactlyOneTool(t *testing.T) {
	ctx := context.Background()
	tasks := store.New(nil).Tasks()

	srv := mcp.NewServer(server.Implementation, nil)
	reg := server.NewRegistry(srv)
	tools.RegisterCreateTask(reg, nil, tasks)

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

	require.Len(t, registered, 1, "RegisterCreateTask must register exactly one tool -- nothing more, nothing fewer")
	assert.True(t, registered["create_task"], "create_task must be registered")
}
