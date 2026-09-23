// No-database unit test for RegisterEntityCreateAll's tool set
// (entity.go). store.New(nil) is safe here (see krill/store/store.go's
// New): it holds the *pgxpool.Pool but never dials it at construction, and
// mcp.AddTool registration never queries -- it only builds the tool's
// schema and handler closure -- so no Postgres is required for this file,
// mirroring milestone_status_registration_test.go's own reasoning.
// sessions is passed as a nil store.SessionStore interface value for the
// same reason: registration only closes over it, it never calls a method
// on it.
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

// expectedEntityCreateToolNames is exactly the five tools
// RegisterEntityCreateAll wires -- see krill/mcp/tools/entity.go.
var expectedEntityCreateToolNames = []string{
	"create_product",
	"create_feature_set",
	"create_load_bearing_decision",
	"create_persona",
	"create_non_goal",
}

// TestRegisterEntityCreateAll_RegistersExactlyThreeTools proves
// RegisterEntityCreateAll wires exactly {create_product, create_feature_set,
// create_load_bearing_decision, create_persona, create_non_goal} -- the
// top-of-chain spec entities that, before this file, could only be created
// over HTTP (POST /products, POST /feature-sets,
// POST /load-bearing-decisions) or, for Persona/NonGoal, only through
// krill/importer's one-shot import path -- never through an MCP tool.
// Listed over a real in-memory MCP client/server connection
// (mcp.NewInMemoryTransports), not by inspecting Go source.
func TestRegisterEntityCreateAll_RegistersExactlyFiveTools(t *testing.T) {
	ctx := context.Background()
	entities := store.New(nil)

	srv := mcp.NewServer(server.Implementation, nil)
	reg := server.NewRegistry(srv)
	tools.RegisterEntityCreateAll(reg, nil, entities.Products(), entities.FeatureSets(), entities.Decisions(), entities.Personas(), entities.NonGoals())

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

	require.Len(t, registered, len(expectedEntityCreateToolNames), "RegisterEntityCreateAll must register exactly these five tools -- nothing more, nothing fewer")
	for _, name := range expectedEntityCreateToolNames {
		assert.True(t, registered[name], "%s must be registered", name)
	}
}
