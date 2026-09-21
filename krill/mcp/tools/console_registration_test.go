// No-database unit test (issue #2869/#2874's own MCP-layer structural
// proof, FR4/FR12): RegisterListClaimedTasks/RegisterListOpenNotes
// together register exactly {list_claimed_tasks, list_open_notes} over a
// real in-memory MCP client/server connection. Mirrors
// task_cancel_registration_test.go's own store.New(nil)/in-memory
// transport technique, so it needs no Docker and is part of
// `bazel test //...`. Both tools are registered on the same registry
// (mirroring mcp/main.go's opsReg placement of both) -- this is the
// operator-mount console-read pair list_open_notes joins.
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

// TestRegisterListClaimedTasksAndListOpenNotes_RegistersExactlyTwoTools is
// this file's structural proof: a real *mcp.Server/Registry, given
// RegisterListClaimedTasks and RegisterListOpenNotes, ends up with exactly
// {list_claimed_tasks, list_open_notes} registered.
func TestRegisterListClaimedTasksAndListOpenNotes_RegistersExactlyTwoTools(t *testing.T) {
	ctx := context.Background()
	entities := store.New(nil)

	srv := mcp.NewServer(server.Implementation, nil)
	reg := server.NewRegistry(srv)
	tools.RegisterListClaimedTasks(reg, entities.Tasks())
	tools.RegisterListOpenNotes(reg, entities.Tasks())

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
	assert.True(t, registered["list_claimed_tasks"], "list_claimed_tasks must be registered")
	assert.True(t, registered["list_open_notes"], "list_open_notes must be registered")
}
