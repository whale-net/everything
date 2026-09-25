// TestProposeEntities_SchemaHasNoCallerSettablePosition is the regression
// check for the position-schema drift (issue #3027's finding; M8's
// c7699a57).
//
// The three layers disagreed. agents/producer.md said "no position field --
// don't set one", because FR7 assigns each proposed entity's position
// server-side. The HTTP twin (api/handlers/mediated.go) accepted a position
// and ignored it, so old request bodies still decoded. But the MCP tool
// schema was generated from a Go struct carrying `Position int` with no
// omitempty, which the schema generator marks REQUIRED. So a caller
// correctly following the documented contract failed validation, while a
// caller who obeyed the schema sent a value that did nothing.
//
// The doc and the server were right; the tool schema was the defect. This
// test pins that the fix is a REMOVAL, not a retyping: a caller-settable
// position is the bug whether it is required, optional, or defaulted, so
// re-adding the field as `*int` + omitempty would still fail here.
//
// Pure Go, no database: registration only builds each tool's schema and
// handler closure, so store.New(nil) over a nil pool is enough.
package tools_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/mcp/tools"
)

func TestProposeEntities_SchemaHasNoCallerSettablePosition(t *testing.T) {
	ctx := context.Background()

	srv := mcp.NewServer(server.Implementation, nil)
	reg := server.NewRegistry(srv)
	tools.RegisterProposeEntities(reg, nil, nil)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	_, err := srv.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })

	var inputSchema any
	found := false
	for tool, err := range cs.Tools(ctx, nil) {
		require.NoError(t, err)
		if tool.Name == "propose_entities" {
			inputSchema = tool.InputSchema
			found = true
			break
		}
	}
	require.True(t, found, "propose_entities must be registered")

	raw, err := json.Marshal(inputSchema)
	require.NoError(t, err)

	var schema map[string]any
	require.NoError(t, json.Unmarshal(raw, &schema))

	// Top level: no caller-settable position.
	props, ok := schema["properties"].(map[string]any)
	require.True(t, ok, "schema has no properties object: %s", raw)
	_, hasTop := props["position"]
	assert.False(t, hasTop,
		"propose_entities must not accept a top-level position: FR7 assigns each proposed entity's position server-side")

	// The nested proposals array is where the drift actually bit.
	proposals, ok := props["proposals"].(map[string]any)
	require.True(t, ok, "schema has no proposals property: %s", raw)
	nested, ok := proposals["items"].(map[string]any)
	require.True(t, ok, "proposals has no items schema: %s", raw)
	itemProps, ok := nested["properties"].(map[string]any)
	require.True(t, ok, "proposals items has no properties: %s", raw)

	_, hasNested := itemProps["position"]
	assert.False(t, hasNested,
		"each proposal must not accept a position field: it is assigned server-side (FR7). Advertising it -- as required, optional, or defaulted -- tells MCP callers to send a value with no effect, and marking it required made a caller correctly following agents/producer.md fail schema validation")

	// Guard the fix against being "resolved" by deletion of something else.
	for _, required := range []string{"kind", "name", "summary_line"} {
		assert.Contains(t, itemProps, required, "proposal must still require %q", required)
	}
}
