// No-database unit test for list_products (product.go) and list_tasks
// (task_list.go), issue #2941's discovery tools: both register as exactly
// {list_products, list_tasks}, and each call maps its store result onto
// the shared handlers wire shape. Stores are in-memory fakes, driven over
// a real in-memory MCP client/server connection
// (mcp.NewInMemoryTransports), mirroring console_registration_test.go.
// The real server.PersonaMiddleware gates every call; operatorPersona
// stands in for the mcpauth bearer check transport.go performs over HTTP.
package tools_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/mcp/tools"
	"github.com/whale-net/everything/krill/store"
)

// discoveryProductStore overrides only ListCurrentByScope; any other
// ProductStore method panics via the nil embedded interface.
type discoveryProductStore struct {
	store.ProductStore
	byScope map[uuid.UUID][]store.Product
}

func (f discoveryProductStore) ListCurrentByScope(_ context.Context, scopeID uuid.UUID) ([]store.Product, error) {
	return f.byScope[scopeID], nil
}

// discoveryTaskStore overrides only ListTasksByMilestone.
type discoveryTaskStore struct {
	store.TaskStore
	byMilestone map[uuid.UUID][]store.TaskSummary
}

func (f discoveryTaskStore) ListTasksByMilestone(_ context.Context, milestoneID uuid.UUID) ([]store.TaskSummary, error) {
	return f.byMilestone[milestoneID], nil
}

// operatorPersona runs every tools/call through the real
// server.PersonaMiddleware as an mcpauth-verified caller, so it resolves
// PersonaSwarmOperator; other methods (initialize, tools/list) pass through.
func operatorPersona(next mcp.MethodHandler) mcp.MethodHandler {
	gated := server.PersonaMiddleware()(next)
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		call, ok := req.(*mcp.CallToolRequest)
		if !ok {
			return next(ctx, method, req)
		}
		if call.Extra == nil {
			call.Extra = &mcp.RequestExtra{}
		}
		call.Extra.TokenInfo = &auth.TokenInfo{UserID: "operator-1"}
		return gated(ctx, method, req)
	}
}

func connectDiscoveryTools(t *testing.T, products store.ProductStore, tasks store.TaskStore) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()

	srv := mcp.NewServer(server.Implementation, nil)
	srv.AddReceivingMiddleware(operatorPersona)
	reg := server.NewRegistry(srv)
	tools.RegisterListProducts(reg, products)
	tools.RegisterListTasks(reg, tasks)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	_, err := srv.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// discoveryTextOf flattens a CallToolResult's text content for failure
// messages.
func discoveryTextOf(res *mcp.CallToolResult) string {
	var out string
	for _, c := range res.Content {
		if text, ok := c.(*mcp.TextContent); ok {
			out += text.Text
		}
	}
	return out
}

func TestRegisterListProductsAndListTasks_RegistersExactlyTwoTools(t *testing.T) {
	cs := connectDiscoveryTools(t, discoveryProductStore{}, discoveryTaskStore{})

	registered := map[string]bool{}
	for tool, err := range cs.Tools(context.Background(), nil) {
		require.NoError(t, err)
		registered[tool.Name] = true
	}

	require.Len(t, registered, 2, "must register exactly two tools -- nothing more, nothing fewer")
	assert.True(t, registered["list_products"], "list_products must be registered")
	assert.True(t, registered["list_tasks"], "list_tasks must be registered")
}

func TestListProducts_ReturnsScopeProducts(t *testing.T) {
	scopeID, productID := uuid.New(), uuid.New()
	cs := connectDiscoveryTools(t, discoveryProductStore{byScope: map[uuid.UUID][]store.Product{
		scopeID: {{ID: productID, ScopeID: scopeID, Name: "krill", Vision: "spec of record"}},
	}}, discoveryTaskStore{})

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_products",
		Arguments: map[string]any{"scope_id": scopeID.String()},
	})
	require.NoError(t, err)
	require.False(t, res.IsError, "unexpected tool error: %s", discoveryTextOf(res))

	structured, ok := res.StructuredContent.(map[string]any)
	require.True(t, ok)
	products, ok := structured["products"].([]any)
	require.True(t, ok)
	require.Len(t, products, 1)
	assert.Equal(t, map[string]any{"id": productID.String(), "name": "krill", "vision": "spec of record"}, products[0])
}

// TestListProducts_EmptyScope_ReturnsEmptyList proves an unknown/empty
// scope is an empty list, not a tool error or a null.
func TestListProducts_EmptyScope_ReturnsEmptyList(t *testing.T) {
	cs := connectDiscoveryTools(t, discoveryProductStore{}, discoveryTaskStore{})

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_products",
		Arguments: map[string]any{"scope_id": uuid.New().String()},
	})
	require.NoError(t, err)
	require.False(t, res.IsError, "unexpected tool error: %s", discoveryTextOf(res))
	assert.Equal(t, map[string]any{"products": []any{}}, res.StructuredContent)
}

func TestListTasks_ReturnsMilestoneTasks(t *testing.T) {
	milestoneID, taskID := uuid.New(), uuid.New()
	cs := connectDiscoveryTools(t, discoveryProductStore{}, discoveryTaskStore{byMilestone: map[uuid.UUID][]store.TaskSummary{
		milestoneID: {{ID: taskID, Title: "wire it", CurrentLane: store.LaneTesting, AttemptCount: 1, HasLiveClaim: true}},
	}})

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_tasks",
		Arguments: map[string]any{"milestone_id": milestoneID.String()},
	})
	require.NoError(t, err)
	require.False(t, res.IsError, "unexpected tool error: %s", discoveryTextOf(res))

	structured, ok := res.StructuredContent.(map[string]any)
	require.True(t, ok)
	tasks, ok := structured["tasks"].([]any)
	require.True(t, ok)
	require.Len(t, tasks, 1)
	assert.Equal(t, map[string]any{
		"id":             taskID.String(),
		"title":          "wire it",
		"current_lane":   "Testing",
		"attempt_count":  float64(1),
		"has_live_claim": true,
	}, tasks[0])
}

// TestDiscoveryTools_InvalidUUID_IsToolError proves a malformed id is a
// tool-level error, never a store call.
func TestDiscoveryTools_InvalidUUID_IsToolError(t *testing.T) {
	cs := connectDiscoveryTools(t, discoveryProductStore{}, discoveryTaskStore{})

	for tool, arg := range map[string]string{"list_products": "scope_id", "list_tasks": "milestone_id"} {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      tool,
			Arguments: map[string]any{arg: "not-a-uuid"},
		})
		require.NoError(t, err)
		assert.True(t, res.IsError, "%s must reject a non-UUID %s", tool, arg)
	}
}
