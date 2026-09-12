// Issue #2494's Testing section: "A negative test asserting no write/
// mutation tool is registered on the spec endpoint." External package
// (server_test) rather than an internal (server) test file on purpose:
// this file imports mcp/tools, which imports mcp/server -- an internal
// test file doing the same import is a genuine dependency cycle to
// rules_go's Go compiler (this package's own test archive would import
// mcp/tools importing mcp/server), not just a style preference. Mirrors
// audience_score_system/mcp/server/registry_tools_test.go's same
// external-package-for-the-same-reason choice.
//
// store.New(nil) is safe here (see krill/store/store.go's New): it holds
// the *pgxpool.Pool but never dials it at construction, and mcp.AddTool
// registration never queries -- it only builds the tool's schema and
// handler closure -- so no Postgres is required for this file, and it
// runs as part of `bazel test //...`.
package server_test

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/mcp/tools"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

// expectedSpecSurfaceToolNames is exactly the four FR5-FR8 read tools
// tools.RegisterAll wires -- see krill/mcp/tools/slice.go.
var expectedSpecSurfaceToolNames = []string{
	"get_feature_set_slice",
	"get_feature_slice",
	"get_requirement_slice",
	"get_product_slice",
}

// TestRegistry_SpecSurface_RegistersExactlyTheFourReadTools_NoWriteTool
// mirrors ../main.go's full tool registration for the SPEC surface only
// (tools.RegisterAll against specSrv/specReg, the same call `mcp` wires at
// boot) against a *slice.Querier over a bare *store.Store (nil pool), then
// lists that one registry's tools by name over a real in-memory MCP
// client/server connection (mcp.NewInMemoryTransports). Asserting the
// registered set is EXACTLY these four names -- not merely that they're
// present -- is what makes this a genuine negative test: this test never
// calls tools.RegisterDesignAll or server.RegisterWrite (registry.go,
// issue #2547) against this reg at all, so it proves the spec surface
// specifically stays exactly these four read tools even though
// RegisterWrite now exists elsewhere in this package for the design-session
// surface's own *mcp.Server (registered separately in main.go, and covered
// by krill/mcp/tools/design_test.go, not this file).
func TestRegistry_SpecSurface_RegistersExactlyTheFourReadTools_NoWriteTool(t *testing.T) {
	ctx := context.Background()
	querier := slice.NewQuerier(store.New(nil))

	srv := mcp.NewServer(server.Implementation, nil)
	reg := server.NewRegistry(srv)
	tools.RegisterAll(reg, querier)

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

	require.Len(t, registered, len(expectedSpecSurfaceToolNames), "the spec surface must register exactly the four FR5-FR8 read tools -- nothing more, nothing fewer")
	for _, name := range expectedSpecSurfaceToolNames {
		assert.True(t, registered[name], "%s must be registered", name)
	}
	for name := range registered {
		assert.Contains(t, expectedSpecSurfaceToolNames, name, "%s is not one of the four FR5-FR8 read tools -- no write/mutation tool may be registered on the spec endpoint", name)
	}
}

// expectedDesignSurfaceToolNames is exactly the six FR1-FR10 tools
// tools.RegisterDesignAll wires -- see krill/mcp/tools/design.go.
var expectedDesignSurfaceToolNames = []string{
	"open_design_session",
	"append_revision_event",
	"propose_entities",
	"get_design_session",
	"get_design_session_slice",
	"list_open_questions",
}

// TestRegistry_DesignSurface_RegistersExactlySixTools_NoneOnSpecSurface
// mirrors ../main.go's full tool registration for BOTH mounts (issue
// #2547): tools.RegisterAll against one *mcp.Server/Registry (the
// specMountPath one) and tools.RegisterDesignAll against a SEPARATE
// *mcp.Server/Registry (the designMountPath one), against a
// *slice.Querier/*store.Store/store.SessionStore built over a bare
// *store.Store (nil pool -- registration never queries, see this file's
// package doc comment; sessions is passed as a nil store.SessionStore
// interface value for the same reason: RegisterDesignAll only closes over
// it, it never calls a method on it during registration). Then lists each
// one registry's tools by name over its own real in-memory MCP
// client/server connection, and asserts:
//
//   - the design registry's tool set is EXACTLY the six FR1-FR10 names,
//   - none of those six names appears on the spec registry (proving the
//     write tools this task adds can never end up reachable from
//     /mcp/spec, the same property registry_tools_test.go's original test
//     proves for the read side), and
//   - none of the four FR5-FR8 spec names appears on the design registry
//     (the two surfaces are disjoint, not merely non-overlapping by
//     accident).
func TestRegistry_DesignSurface_RegistersExactlySixTools_NoneOnSpecSurface(t *testing.T) {
	ctx := context.Background()
	entities := store.New(nil)
	querier := slice.NewQuerier(entities)
	var sessions store.SessionStore // nil: registration never calls a method on it

	specSrv := mcp.NewServer(server.Implementation, nil)
	specReg := server.NewRegistry(specSrv)
	tools.RegisterAll(specReg, querier)

	designSrv := mcp.NewServer(server.Implementation, nil)
	designReg := server.NewRegistry(designSrv)
	tools.RegisterDesignAll(designReg, entities, sessions, querier)

	listTools := func(t *testing.T, srv *mcp.Server) map[string]bool {
		t.Helper()
		serverTransport, clientTransport := mcp.NewInMemoryTransports()
		_, err := srv.Connect(ctx, serverTransport, nil)
		require.NoError(t, err)

		client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
		cs, err := client.Connect(ctx, clientTransport, nil)
		require.NoError(t, err)
		t.Cleanup(func() { _ = cs.Close() })

		names := map[string]bool{}
		for tool, err := range cs.Tools(ctx, nil) {
			require.NoError(t, err)
			names[tool.Name] = true
		}
		return names
	}

	specNames := listTools(t, specSrv)
	designNames := listTools(t, designSrv)

	require.Len(t, designNames, len(expectedDesignSurfaceToolNames), "the design surface must register exactly the six FR1-FR10 tools -- nothing more, nothing fewer")
	for _, name := range expectedDesignSurfaceToolNames {
		assert.True(t, designNames[name], "%s must be registered on the design surface", name)
		assert.False(t, specNames[name], "%s (a design-session tool) must never be registered on the spec surface", name)
	}
	for _, name := range expectedSpecSurfaceToolNames {
		assert.False(t, designNames[name], "%s (a spec-surface tool) must never be registered on the design surface", name)
	}
}
