// No-database unit test for RegisterReleaseTask/RegisterEscalateTask/
// RegisterListEscalatedTasks' tool set (task_release.go, task_escalate.go,
// issue #2872, FR8/FR9; console.go, issue #2875, FR5). store.New(nil) is
// safe here (see krill/store/store.go's New): it holds the *pgxpool.Pool
// but never dials it at construction, and mcp.AddTool registration never
// queries -- it only builds the tool's schema and handler closure -- so no
// Postgres is required for this file, mirroring
// task_cancel_registration_test.go's own reasoning.
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

// TestRegisterReleaseTaskAndEscalateTask_RegistersExactlyThreeTools is this
// file's structural proof: a real *mcp.Server/Registry, given
// RegisterReleaseTask, RegisterEscalateTask, and RegisterListEscalatedTasks
// (issue #2875's headline read tool, paired here with the verb that most
// directly produces its rows -- FR9's manual escalate -- mirroring
// TestRegisterCancelTaskAndListCancelledTasks_RegistersExactlyTwoTools's
// own writer-plus-its-list pairing), ends up with exactly {release_task,
// escalate_task, list_escalated_tasks} registered -- listed over a real
// in-memory MCP client/server connection.
func TestRegisterReleaseTaskAndEscalateTask_RegistersExactlyThreeTools(t *testing.T) {
	ctx := context.Background()
	entities := store.New(nil)

	srv := mcp.NewServer(server.Implementation, nil)
	reg := server.NewRegistry(srv)
	tools.RegisterReleaseTask(reg, nil, entities.Tasks(), nil)
	tools.RegisterEscalateTask(reg, nil, entities.Tasks(), nil)
	tools.RegisterListEscalatedTasks(reg, entities.Tasks())

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

	require.Len(t, registered, 3, "must register exactly three tools -- nothing more, nothing fewer")
	assert.True(t, registered["release_task"], "release_task must be registered")
	assert.True(t, registered["escalate_task"], "escalate_task must be registered")
	assert.True(t, registered["list_escalated_tasks"], "list_escalated_tasks must be registered")
}
