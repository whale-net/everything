// No-database unit test for RegisterCancelTask/RegisterListCancelledTasks'
// tool set (task_cancel.go, console.go, issue #2873, FR7/FR10).
// store.New(nil) is safe here (see krill/store/store.go's New): it holds
// the *pgxpool.Pool but never dials it at construction, and mcp.AddTool
// registration never queries -- it only builds the tool's schema and
// handler closure -- so no Postgres is required for this file, mirroring
// task_lease_registration_test.go's own reasoning.
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

// TestRegisterCancelTaskAndListCancelledTasks_RegistersExactlyTwoTools is
// this file's structural proof: a real *mcp.Server/Registry, given
// RegisterCancelTask and RegisterListCancelledTasks, ends up with exactly
// {cancel_task, list_cancelled_tasks} registered -- listed over a real
// in-memory MCP client/server connection, mirroring
// TestRegisterHeartbeatTask_RegistersExactlyOneTool's own technique.
func TestRegisterCancelTaskAndListCancelledTasks_RegistersExactlyTwoTools(t *testing.T) {
	ctx := context.Background()
	entities := store.New(nil)

	srv := mcp.NewServer(server.Implementation, nil)
	reg := server.NewRegistry(srv)
	tools.RegisterCancelTask(reg, nil, entities.Tasks(), nil)
	tools.RegisterListCancelledTasks(reg, entities.Tasks())

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

	require.Len(t, registered, 2, "must register exactly two tools -- nothing more, nothing fewer")
	assert.True(t, registered["cancel_task"], "cancel_task must be registered")
	assert.True(t, registered["list_cancelled_tasks"], "list_cancelled_tasks must be registered")
}
