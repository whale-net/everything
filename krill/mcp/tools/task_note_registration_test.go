// No-database unit test (issue #2874's own MCP-layer structural proof,
// FR11): RegisterRecordNote/RegisterTransitionNoteLifecycle together
// register exactly {record_note, transition_note_lifecycle} over a real
// in-memory MCP client/server connection. store.New(nil) is safe here (see
// krill/store/store.go's New): it holds the *pgxpool.Pool but never dials
// it at construction, and mcp.AddTool registration never queries -- it
// only builds the tool's schema and handler closure -- so no Postgres is
// required for this file, mirroring task_cancel_registration_test.go's own
// reasoning. Both tools are registered on the same registry (mirroring
// mcp/main.go's designReg placement of both) -- the regression guard this
// proves is that transition_note_lifecycle is registered exactly once and
// never duplicated onto a second mount.
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

// TestRegisterRecordNoteAndTransitionNoteLifecycle_RegistersExactlyTwoTools
// is this file's structural proof: a real *mcp.Server/Registry, given
// RegisterRecordNote and RegisterTransitionNoteLifecycle, ends up with
// exactly {record_note, transition_note_lifecycle} registered.
func TestRegisterRecordNoteAndTransitionNoteLifecycle_RegistersExactlyTwoTools(t *testing.T) {
	ctx := context.Background()
	entities := store.New(nil)

	srv := mcp.NewServer(server.Implementation, nil)
	reg := server.NewRegistry(srv)
	tools.RegisterRecordNote(reg, nil, entities.Tasks())
	tools.RegisterTransitionNoteLifecycle(reg, nil, entities.Tasks())

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
	assert.True(t, registered["record_note"], "record_note must be registered")
	assert.True(t, registered["transition_note_lifecycle"], "transition_note_lifecycle must be registered")
}
