// This file is krill's Persona/NonGoal discovery MCP tool group:
// list_personas and list_non_goals, thin wrappers over
// store.PersonaStore.ListCurrentByProduct/store.NonGoalStore.ListCurrentByProduct,
// mirroring product.go's list_products for the same discovery shape (LB7)
// -- Persona and NonGoal have no HTTP handler to mirror (same reasoning as
// entity.go's create_persona/create_non_goal), so their response types are
// defined locally here rather than reused from krill/api/handlers. Before
// this file, a Persona or NonGoal created via create_persona/
// create_non_goal had no MCP-reachable way to be read back at all -- not
// through a standalone list tool, and not as part of the FR5-FR9
// whole-Product slice document (krill/slice/document.go's Document has no
// Personas/NonGoals field).
package tools

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/store"
)

// ── list_personas (read) ────────────────────────────────────────────────────

// listByProductInput is list_personas' and list_non_goals' shared argument
// schema: the single Product to list current rows under.
type listByProductInput struct {
	ProductID string `json:"product_id" jsonschema:"The Product surrogate id to list current rows under, as a UUID string."`
}

// PersonaSummary is one entry of ListPersonasResponse.Personas.
type PersonaSummary struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
}

// ListPersonasResponse is list_personas' response body.
type ListPersonasResponse struct {
	Personas []PersonaSummary `json:"personas"`
}

// newListPersonasResponse maps store.PersonaStore.ListCurrentByProduct's
// result onto its wire shape.
func newListPersonasResponse(personas []store.Persona) ListPersonasResponse {
	summaries := make([]PersonaSummary, len(personas))
	for i, p := range personas {
		summaries[i] = PersonaSummary{ID: p.ID.String(), Name: p.Name, Description: p.Description}
	}
	return ListPersonasResponse{Personas: summaries}
}

// RegisterListPersonas registers list_personas: every current Persona under
// a Product, ordered by position then name -- ungated like every other
// discovery tool (server.RegisterRead), since NFR6's gate is write-only.
func RegisterListPersonas(reg *server.Registry, personas store.PersonaStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name:        "list_personas",
		Description: "List every current Persona under a Product (id, name, description), ordered by position then name.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listByProductInput) (*mcp.CallToolResult, ListPersonasResponse, error) {
		var zero ListPersonasResponse

		productID, err := uuid.Parse(in.ProductID)
		if err != nil {
			return nil, zero, fmt.Errorf("product_id: invalid or missing UUID")
		}

		list, err := personas.ListCurrentByProduct(ctx, productID)
		if err != nil {
			return nil, zero, err
		}
		return nil, newListPersonasResponse(list), nil
	})
}

// ── list_non_goals (read) ───────────────────────────────────────────────────

// NonGoalSummary is one entry of ListNonGoalsResponse.NonGoals.
type NonGoalSummary struct {
	ID   string  `json:"id"`
	Kind string  `json:"kind"`
	Name string  `json:"name"`
	Body *string `json:"body,omitempty"`
}

// ListNonGoalsResponse is list_non_goals' response body.
type ListNonGoalsResponse struct {
	NonGoals []NonGoalSummary `json:"non_goals"`
}

// newListNonGoalsResponse maps store.NonGoalStore.ListCurrentByProduct's
// result onto its wire shape.
func newListNonGoalsResponse(nonGoals []store.NonGoal) ListNonGoalsResponse {
	summaries := make([]NonGoalSummary, len(nonGoals))
	for i, n := range nonGoals {
		summaries[i] = NonGoalSummary{ID: n.ID.String(), Kind: string(n.Kind), Name: n.Name, Body: n.Body}
	}
	return ListNonGoalsResponse{NonGoals: summaries}
}

// RegisterListNonGoals registers list_non_goals: every current NonGoal
// (both kinds) under a Product, ordered by kind then position then name --
// ungated like every other discovery tool (server.RegisterRead), since
// NFR6's gate is write-only.
func RegisterListNonGoals(reg *server.Registry, nonGoals store.NonGoalStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name:        "list_non_goals",
		Description: "List every current Non-Goal (permanent or deferred) under a Product (id, kind, name, body), ordered by kind then position then name.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listByProductInput) (*mcp.CallToolResult, ListNonGoalsResponse, error) {
		var zero ListNonGoalsResponse

		productID, err := uuid.Parse(in.ProductID)
		if err != nil {
			return nil, zero, fmt.Errorf("product_id: invalid or missing UUID")
		}

		list, err := nonGoals.ListCurrentByProduct(ctx, productID)
		if err != nil {
			return nil, zero, err
		}
		return nil, newListNonGoalsResponse(list), nil
	})
}
