// TestRegistry_WorkSurface_RegistersExactlyTheWorkTools pins /mcp/work's
// tool set (issue #3028). The work mount is the one the krill-work plugin's
// .mcp.json points at, and it is a deliberate split from /mcp/design: those
// personas must not reach design-session authoring or milestone/delivery
// writes.
//
// The gap this test closes is that before it, the work mount's registration
// lived as 18 inline Register* calls in krill/mcp/main.go. Every existing
// registration test exercises the spec or design mount, so all six of the
// discovery reads added for #3028 could be deleted from main.go with the
// whole suite still green -- and in production a krill-work persona would
// silently stop being able to resolve a Product or read a Milestone, with
// nothing in a log saying so. That is the same silent-failure shape as
// issue #3017, where an unmatched allowlist pattern in a persona's tools:
// frontmatter was ignored with nothing failing.
//
// Asserting the set is EXACTLY these names (not merely that they are
// present) keeps the mount a genuine negative test in both directions: the
// discovery reads cannot quietly drop off it, and a design-session or
// milestone WRITE cannot quietly appear on it.
//
// No database: store.New(nil) is safe here (krill/store/store.go's New holds
// the *pgxpool.Pool but never dials it at construction, and registration
// only builds each tool's schema and handler closure). Mirrors
// krill/mcp/server/registry_tools_test.go's own technique, so this runs
// under `bazel test //...` with no Docker.
package tools_test

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
	"github.com/whale-net/everything/krill/work"
)

func TestRegistry_WorkSurface_RegistersExactlyTheWorkTools(t *testing.T) {
	ctx := context.Background()

	entities := store.New(nil)
	querier := slice.NewQuerier(entities)
	assembler := work.NewAssembler(entities.Tasks(), querier)

	srv := mcp.NewServer(server.Implementation, nil)
	reg := server.NewRegistry(srv)
	tools.RegisterWorkAll(reg, entities, nil, assembler, querier)

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

	expected := tools.ExpectedWorkSurfaceToolNames()
	require.Len(t, registered, len(expected),
		"the work surface must register exactly the expected work-axis tools -- nothing more, nothing fewer")

	for _, name := range expected {
		assert.True(t, registered[name], "%s must be registered on /mcp/work", name)
	}
	for name := range registered {
		assert.Contains(t, expected, name,
			"%s is not a work-axis tool -- no design-session authoring or milestone/delivery write may be reachable from /mcp/work", name)
	}
}

// TestWorkSurface_DiscoveryReadsAreMounted names the #3028 regression
// directly, so a failure says what broke rather than only reporting a count
// mismatch. These six are the reads a krill-work persona needs to resolve the
// product and milestone behind a task id it was handed; without them the
// persona can claim work it has no way to read the spec for.
func TestWorkSurface_DiscoveryReadsAreMounted(t *testing.T) {
	ctx := context.Background()

	entities := store.New(nil)
	querier := slice.NewQuerier(entities)
	assembler := work.NewAssembler(entities.Tasks(), querier)

	srv := mcp.NewServer(server.Implementation, nil)
	reg := server.NewRegistry(srv)
	tools.RegisterWorkAll(reg, entities, nil, assembler, querier)

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

	for _, name := range []string{
		"list_products",
		"get_milestone",
		"list_milepebbles",
		"list_product_delivery",
		"get_milestone_status",
		"get_milestone_status_history",
	} {
		assert.True(t, registered[name],
			"%s must be reachable from /mcp/work (#3028): without it a krill-work persona holding a task or milestone id cannot resolve the product or read the milestone it is working on", name)
	}
}
