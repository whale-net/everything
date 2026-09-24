// No-database unit test for list_personas and list_non_goals
// (persona_nongoal.go): both register as exactly {list_personas,
// list_non_goals}, and each call maps its store result onto its own local
// wire shape. Stores are in-memory fakes, driven over a real in-memory MCP
// client/server connection (mcp.NewInMemoryTransports), mirroring
// discovery_registration_test.go's own technique -- including reusing that
// file's operatorPersona middleware (same package), since RegisterRead
// requires a resolved persona in ctx before a read tool's handler runs.
package tools_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/mcp/tools"
	"github.com/whale-net/everything/krill/store"
)

// personaNonGoalPersonaStore overrides only ListCurrentByProduct; any
// other PersonaStore method panics via the nil embedded interface.
type personaNonGoalPersonaStore struct {
	store.PersonaStore
	byProduct map[uuid.UUID][]store.Persona
}

func (f personaNonGoalPersonaStore) ListCurrentByProduct(_ context.Context, productID uuid.UUID) ([]store.Persona, error) {
	return f.byProduct[productID], nil
}

// personaNonGoalNonGoalStore overrides only ListCurrentByProduct.
type personaNonGoalNonGoalStore struct {
	store.NonGoalStore
	byProduct map[uuid.UUID][]store.NonGoal
}

func (f personaNonGoalNonGoalStore) ListCurrentByProduct(_ context.Context, productID uuid.UUID) ([]store.NonGoal, error) {
	return f.byProduct[productID], nil
}

func connectPersonaNonGoalTools(t *testing.T, personas store.PersonaStore, nonGoals store.NonGoalStore) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()

	srv := mcp.NewServer(server.Implementation, nil)
	srv.AddReceivingMiddleware(operatorPersona)
	reg := server.NewRegistry(srv)
	tools.RegisterListPersonas(reg, personas)
	tools.RegisterListNonGoals(reg, nonGoals)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	_, err := srv.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func TestRegisterListPersonasAndListNonGoals_RegistersExactlyTwoTools(t *testing.T) {
	cs := connectPersonaNonGoalTools(t, personaNonGoalPersonaStore{}, personaNonGoalNonGoalStore{})

	registered := map[string]bool{}
	for tool, err := range cs.Tools(context.Background(), nil) {
		require.NoError(t, err)
		registered[tool.Name] = true
	}

	require.Len(t, registered, 2, "must register exactly two tools -- nothing more, nothing fewer")
	assert.True(t, registered["list_personas"], "list_personas must be registered")
	assert.True(t, registered["list_non_goals"], "list_non_goals must be registered")
}

func TestListPersonas_ReturnsProductPersonas(t *testing.T) {
	productID, personaID := uuid.New(), uuid.New()
	desc := "runs the swarm"
	cs := connectPersonaNonGoalTools(t, personaNonGoalPersonaStore{byProduct: map[uuid.UUID][]store.Persona{
		productID: {{ID: personaID, ProductID: productID, Name: "Swarm Operator", Description: &desc}},
	}}, personaNonGoalNonGoalStore{})

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_personas",
		Arguments: map[string]any{"product_id": productID.String()},
	})
	require.NoError(t, err)
	require.False(t, res.IsError)

	structured, ok := res.StructuredContent.(map[string]any)
	require.True(t, ok)
	personas, ok := structured["personas"].([]any)
	require.True(t, ok)
	require.Len(t, personas, 1)
	assert.Equal(t, map[string]any{"id": personaID.String(), "name": "Swarm Operator", "description": desc}, personas[0])
}

// TestListPersonas_EmptyProduct_ReturnsEmptyList proves an unknown/empty
// product is an empty list, not a tool error or a null.
func TestListPersonas_EmptyProduct_ReturnsEmptyList(t *testing.T) {
	cs := connectPersonaNonGoalTools(t, personaNonGoalPersonaStore{}, personaNonGoalNonGoalStore{})

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_personas",
		Arguments: map[string]any{"product_id": uuid.New().String()},
	})
	require.NoError(t, err)
	require.False(t, res.IsError)
	assert.Equal(t, map[string]any{"personas": []any{}}, res.StructuredContent)
}

func TestListNonGoals_ReturnsProductNonGoals(t *testing.T) {
	productID, nonGoalID := uuid.New(), uuid.New()
	body := "not planned this cycle"
	cs := connectPersonaNonGoalTools(t, personaNonGoalPersonaStore{}, personaNonGoalNonGoalStore{byProduct: map[uuid.UUID][]store.NonGoal{
		productID: {{ID: nonGoalID, ProductID: productID, Kind: store.NonGoalKindDeferred, Name: "Multi-tenant scope", Body: &body}},
	}})

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_non_goals",
		Arguments: map[string]any{"product_id": productID.String()},
	})
	require.NoError(t, err)
	require.False(t, res.IsError)

	structured, ok := res.StructuredContent.(map[string]any)
	require.True(t, ok)
	nonGoals, ok := structured["non_goals"].([]any)
	require.True(t, ok)
	require.Len(t, nonGoals, 1)
	assert.Equal(t, map[string]any{"id": nonGoalID.String(), "kind": "deferred", "name": "Multi-tenant scope", "body": body}, nonGoals[0])
}

// TestListPersonasAndNonGoals_InvalidUUID_IsToolError proves a malformed
// product_id is a tool-level error, never a store call.
func TestListPersonasAndNonGoals_InvalidUUID_IsToolError(t *testing.T) {
	cs := connectPersonaNonGoalTools(t, personaNonGoalPersonaStore{}, personaNonGoalNonGoalStore{})

	for _, tool := range []string{"list_personas", "list_non_goals"} {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      tool,
			Arguments: map[string]any{"product_id": "not-a-uuid"},
		})
		require.NoError(t, err)
		assert.True(t, res.IsError, "%s must reject a non-UUID product_id", tool)
	}
}
