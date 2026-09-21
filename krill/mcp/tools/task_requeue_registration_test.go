// No-database unit test for RegisterRequeueTask (task_requeue.go, issue
// #2876, FR6). store.New(nil) is safe here (see krill/store/store.go's
// New): it holds the *pgxpool.Pool but never dials it at construction,
// and mcp.AddTool registration never queries -- it only builds the tool's
// schema and handler closure -- so no Postgres is required for this file,
// mirroring task_cancel_registration_test.go's own reasoning.
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

// TestRegisterRequeueTask_RegistersExactlyOneTool is this file's
// structural proof: a real *mcp.Server/Registry, given RegisterRequeueTask,
// ends up with exactly {requeue_task} registered -- listed over a real
// in-memory MCP client/server connection, mirroring
// TestRegisterCancelTaskAndListCancelledTasks_RegistersExactlyTwoTools's
// own technique.
func TestRegisterRequeueTask_RegistersExactlyOneTool(t *testing.T) {
	ctx := context.Background()
	entities := store.New(nil)

	srv := mcp.NewServer(server.Implementation, nil)
	reg := server.NewRegistry(srv)
	tools.RegisterRequeueTask(reg, nil, entities.Tasks(), nil)

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

	require.Len(t, registered, 1, "must register exactly one tool -- nothing more, nothing fewer")
	assert.True(t, registered["requeue_task"], "requeue_task must be registered")
}
