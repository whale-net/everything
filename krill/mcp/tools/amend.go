// This file is krill's amend MCP tool group: amend_requirement and
// amend_load_bearing_decision -- thin wrappers over store.AmendStore,
// mirroring krill/api/handlers/amend.go's HTTP surface (LB7): same
// {name, body} replacement, same handlers.IDResponse output. Amend never
// reparents; it replaces an existing entity's name/body under its
// unchanged surrogate id (SCD2 close-and-open, store/amend.go).
package tools

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/store"
)

// amendInput is the argument schema both amend tools share -- mirrors
// api/handlers/amend.go's amendRequest, plus the entity id (a path value on
// the HTTP twin) and the krill session id (a header there).
type amendInput struct {
	krillSessionInput
	ID   string  `json:"id" jsonschema:"The surrogate id (LB2) of the entity to amend, as a UUID string. Unchanged by the amend."`
	Name string  `json:"name" jsonschema:"The entity's replacement name. Required."`
	Body *string `json:"body,omitempty" jsonschema:"The entity's replacement body. Omit to clear it."`
}

// parseAmendInput resolves the session gate and validates in exactly as
// the HTTP amend handlers do, before any store mutation runs.
func parseAmendInput(ctx context.Context, sessions store.SessionStore, in amendInput) (uuid.UUID, error) {
	if _, err := requireKrillSession(ctx, sessions, in.KrillSessionID); err != nil {
		return uuid.Nil, err
	}
	id, err := uuid.Parse(in.ID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("id: invalid or missing UUID")
	}
	if err := handlers.RequireNonEmpty("name", in.Name); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// amendPersonas matches entity.go's create tools: amend is the correction
// path for the same design-axis entities those tools create.
var amendPersonas = []server.Persona{server.PersonaRequirementContributor, server.PersonaAgent, server.PersonaSwarmOperator}

// RegisterAmendRequirement registers amend_requirement: closes the
// Requirement's current row and opens a new revision with the given
// name/body via store.AmendStore.AmendRequirement.
func RegisterAmendRequirement(reg *server.Registry, sessions store.SessionStore, amend store.AmendStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "amend_requirement",
		Description: "Amend an existing Requirement (FR/NFR): replace its name and body as a new SCD2 revision under the same id. " +
			"Never reparents; the Requirement's kind, feature, and position are unchanged.",
	}, amendPersonas, func(ctx context.Context, _ *mcp.CallToolRequest, in amendInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		var zero handlers.IDResponse
		id, err := parseAmendInput(ctx, sessions, in)
		if err != nil {
			return nil, zero, err
		}
		requirement, err := amend.AmendRequirement(ctx, id, in.Name, in.Body)
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.IDResponse{ID: requirement.ID.String()}, nil
	})
}

// RegisterAmendLoadBearingDecision registers amend_load_bearing_decision:
// closes the LoadBearingDecision's current row and opens a new revision
// with the given name/body via store.AmendStore.AmendLoadBearingDecision.
func RegisterAmendLoadBearingDecision(reg *server.Registry, sessions store.SessionStore, amend store.AmendStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "amend_load_bearing_decision",
		Description: "Amend an existing load-bearing decision: replace its name and body as a new SCD2 revision under the same id. " +
			"Never reparents; the decision's feature set and position are unchanged.",
	}, amendPersonas, func(ctx context.Context, _ *mcp.CallToolRequest, in amendInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		var zero handlers.IDResponse
		id, err := parseAmendInput(ctx, sessions, in)
		if err != nil {
			return nil, zero, err
		}
		decision, err := amend.AmendLoadBearingDecision(ctx, id, in.Name, in.Body)
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.IDResponse{ID: decision.ID.String()}, nil
	})
}

// RegisterAmendAll registers amend_requirement and
// amend_load_bearing_decision against reg.
func RegisterAmendAll(reg *server.Registry, sessions store.SessionStore, amend store.AmendStore) {
	RegisterAmendRequirement(reg, sessions, amend)
	RegisterAmendLoadBearingDecision(reg, sessions, amend)
}
