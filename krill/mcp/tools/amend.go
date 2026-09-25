// This file is krill's amend MCP tool group: one tool per spec-axis entity
// kind -- thin wrappers over store.AmendStore, mirroring
// krill/api/handlers/amend.go's HTTP surface (LB7): same content
// replacement, same handlers.IDResponse output. Amend supersedes an
// existing entity's own content under its unchanged surrogate id, parent,
// and kind (SCD2 close-and-open, store/amend.go); it never reparents and
// never re-kinds.
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

// amendInput is the argument schema shared by every amend tool -- mirrors
// api/handlers/amend.go's request bodies, plus the entity id (a path value
// on the HTTP twin) and the krill session id (a header there).
//
// The placement fields are the same reparent/re-kind guard the HTTP bodies
// carry: no amend tool ever applies them, and passing one is refused by
// name rather than silently ignored (FR f0f6bc18).
type amendInput struct {
	store.AmendPlacementChange
	krillSessionInput
	ID   string  `json:"id" jsonschema:"The surrogate id (LB2) of the entity to amend, as a UUID string. Unchanged by the amend."`
	Name string  `json:"name" jsonschema:"The entity's replacement name. Required."`
	Body *string `json:"body,omitempty" jsonschema:"The entity's replacement body, for the kinds that carry one. Omit to clear it."`
}

// amendDescribedInput is amendInput's shape for the kinds whose amendable
// content is a name plus a description.
type amendDescribedInput struct {
	store.AmendPlacementChange
	krillSessionInput
	ID          string  `json:"id" jsonschema:"The surrogate id (LB2) of the entity to amend, as a UUID string. Unchanged by the amend."`
	Name        string  `json:"name" jsonschema:"The entity's replacement name. Required."`
	Description *string `json:"description,omitempty" jsonschema:"The entity's replacement description. Omit to clear it."`
}

// amendProductInput is amendInput's shape for a Product, whose amendable
// content is its name plus its vision sentence.
type amendProductInput struct {
	store.AmendPlacementChange
	krillSessionInput
	ID     string `json:"id" jsonschema:"The surrogate id (LB2) of the Product to amend, as a UUID string. Unchanged by the amend."`
	Name   string `json:"name" jsonschema:"The Product's replacement name. Required."`
	Vision string `json:"vision" jsonschema:"The Product's replacement vision sentence. Required."`
}

// amendMilestoneInput is amendInput's shape for a Milestone, whose
// amendable content is its name plus its outcome sentence. Its delivery
// axis -- status, Delivers, must-not-foreclose, deferrals -- is not
// addressable from here at all.
type amendMilestoneInput struct {
	store.AmendPlacementChange
	krillSessionInput
	ID      string  `json:"id" jsonschema:"The surrogate id (LB2) of the milestone to amend, as a UUID string. Unchanged by the amend."`
	Name    string  `json:"name" jsonschema:"The milestone's replacement name. Required."`
	Outcome *string `json:"outcome,omitempty" jsonschema:"The milestone's replacement outcome sentence. Omit to clear it."`
}

// parseAmendInput resolves the session gate and the id/name checks exactly
// as the HTTP amend handlers do, before any store mutation runs.
func parseAmendInput(ctx context.Context, sessions store.SessionStore, krillSessionID, id, name string) (uuid.UUID, error) {
	if _, err := requireKrillSession(ctx, sessions, krillSessionID); err != nil {
		return uuid.Nil, err
	}
	parsed, err := uuid.Parse(id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("id: invalid or missing UUID")
	}
	if err := handlers.RequireNonEmpty("name", name); err != nil {
		return uuid.Nil, err
	}
	return parsed, nil
}

// refusePlacement is the FR f0f6bc18 refusal every tool shares.
func refusePlacement(placement store.AmendPlacementChange, entityKind string) error {
	return placement.Refuse(entityKind)
}

// amendPersonas matches entity.go's create tools: amend is the correction
// path for the same design-axis entities those tools create.
var amendPersonas = []server.Persona{server.PersonaRequirementContributor, server.PersonaAgent, server.PersonaSwarmOperator}

// RegisterAmendProduct registers amend_product: closes the Product's
// current row and opens a new revision with the given name and vision.
func RegisterAmendProduct(reg *server.Registry, sessions store.SessionStore, amend store.AmendStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "amend_product",
		Description: "Amend an existing Product: replace its name and vision as a new SCD2 revision under the same id. " +
			"Never reparents; the Product's scope and position are unchanged.",
	}, amendPersonas, func(ctx context.Context, _ *mcp.CallToolRequest, in amendProductInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		var zero handlers.IDResponse
		if err := refusePlacement(in.AmendPlacementChange, "product"); err != nil {
			return nil, zero, err
		}
		id, err := parseAmendInput(ctx, sessions, in.KrillSessionID, in.ID, in.Name)
		if err != nil {
			return nil, zero, err
		}
		if err := handlers.RequireNonEmpty("vision", in.Vision); err != nil {
			return nil, zero, err
		}
		product, err := amend.AmendProduct(ctx, id, in.Name, in.Vision)
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.IDResponse{ID: product.ID.String()}, nil
	})
}

// RegisterAmendFeatureSet registers amend_feature_set: closes the
// FeatureSet's current row and opens a new revision with the given name and
// description.
func RegisterAmendFeatureSet(reg *server.Registry, sessions store.SessionStore, amend store.AmendStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "amend_feature_set",
		Description: "Amend an existing feature set: replace its name and description as a new SCD2 revision under the same id. " +
			"Never reparents; the set's product and position are unchanged.",
	}, amendPersonas, func(ctx context.Context, _ *mcp.CallToolRequest, in amendDescribedInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		var zero handlers.IDResponse
		if err := refusePlacement(in.AmendPlacementChange, "feature set"); err != nil {
			return nil, zero, err
		}
		id, err := parseAmendInput(ctx, sessions, in.KrillSessionID, in.ID, in.Name)
		if err != nil {
			return nil, zero, err
		}
		amended, err := amend.AmendFeatureSet(ctx, id, in.Name, in.Description)
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.IDResponse{ID: amended.ID.String()}, nil
	})
}

// RegisterAmendFeature registers amend_feature: closes the Feature's
// current row and opens a new revision with the given name and
// description, carrying the `Cn` display number forward unchanged.
func RegisterAmendFeature(reg *server.Registry, sessions store.SessionStore, amend store.AmendStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "amend_feature",
		Description: "Amend an existing capability-map entry: replace its name and description as a new SCD2 revision under the same id. " +
			"Never reparents, and never renumbers -- the entry's Cn display number is carried forward unchanged.",
	}, amendPersonas, func(ctx context.Context, _ *mcp.CallToolRequest, in amendDescribedInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		var zero handlers.IDResponse
		if err := refusePlacement(in.AmendPlacementChange, "feature"); err != nil {
			return nil, zero, err
		}
		id, err := parseAmendInput(ctx, sessions, in.KrillSessionID, in.ID, in.Name)
		if err != nil {
			return nil, zero, err
		}
		amended, err := amend.AmendFeature(ctx, id, in.Name, in.Description)
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.IDResponse{ID: amended.ID.String()}, nil
	})
}

// RegisterAmendRequirement registers amend_requirement: closes the
// Requirement's current row and opens a new revision with the given
// name/body.
func RegisterAmendRequirement(reg *server.Registry, sessions store.SessionStore, amend store.AmendStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "amend_requirement",
		Description: "Amend an existing Requirement (FR/NFR): replace its name and body as a new SCD2 revision under the same id. " +
			"Never reparents; the Requirement's kind, feature, and position are unchanged.",
	}, amendPersonas, func(ctx context.Context, _ *mcp.CallToolRequest, in amendInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		var zero handlers.IDResponse
		if err := refusePlacement(in.AmendPlacementChange, "requirement"); err != nil {
			return nil, zero, err
		}
		id, err := parseAmendInput(ctx, sessions, in.KrillSessionID, in.ID, in.Name)
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

// RegisterAmendPersona registers amend_persona: closes the PersonaRow's
// current row and opens a new revision with the given name and
// description.
func RegisterAmendPersona(reg *server.Registry, sessions store.SessionStore, amend store.AmendStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "amend_persona",
		Description: "Amend an existing persona: replace its name and description as a new SCD2 revision under the same id. " +
			"Never reparents; the persona's product and position are unchanged.",
	}, amendPersonas, func(ctx context.Context, _ *mcp.CallToolRequest, in amendDescribedInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		var zero handlers.IDResponse
		if err := refusePlacement(in.AmendPlacementChange, "persona"); err != nil {
			return nil, zero, err
		}
		id, err := parseAmendInput(ctx, sessions, in.KrillSessionID, in.ID, in.Name)
		if err != nil {
			return nil, zero, err
		}
		amended, err := amend.AmendPersona(ctx, id, in.Name, in.Description)
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.IDResponse{ID: amended.ID.String()}, nil
	})
}

// RegisterAmendNonGoal registers amend_non_goal: closes the NonGoal's
// current row and opens a new revision with the given name and body. Kind
// is carried forward, never re-chosen -- resolving a deferred non-goal is
// its own verb.
func RegisterAmendNonGoal(reg *server.Registry, sessions store.SessionStore, amend store.AmendStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "amend_non_goal",
		Description: "Amend an existing non-goal: replace its name and body as a new SCD2 revision under the same id. " +
			"Never reparents or re-kinds; the non-goal's product, kind, and position are unchanged.",
	}, amendPersonas, func(ctx context.Context, _ *mcp.CallToolRequest, in amendInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		var zero handlers.IDResponse
		if err := refusePlacement(in.AmendPlacementChange, "non-goal"); err != nil {
			return nil, zero, err
		}
		id, err := parseAmendInput(ctx, sessions, in.KrillSessionID, in.ID, in.Name)
		if err != nil {
			return nil, zero, err
		}
		amended, err := amend.AmendNonGoal(ctx, id, in.Name, in.Body)
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.IDResponse{ID: amended.ID.String()}, nil
	})
}

// RegisterAmendLoadBearingDecision registers amend_load_bearing_decision:
// closes the LoadBearingDecision's current row and opens a new revision
// with the given name/body.
func RegisterAmendLoadBearingDecision(reg *server.Registry, sessions store.SessionStore, amend store.AmendStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "amend_load_bearing_decision",
		Description: "Amend an existing load-bearing decision: replace its name and body as a new SCD2 revision under the same id. " +
			"Never reparents, and never renumbers -- the decision's LBn display number is carried forward unchanged.",
	}, amendPersonas, func(ctx context.Context, _ *mcp.CallToolRequest, in amendInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		var zero handlers.IDResponse
		if err := refusePlacement(in.AmendPlacementChange, "load-bearing decision"); err != nil {
			return nil, zero, err
		}
		id, err := parseAmendInput(ctx, sessions, in.KrillSessionID, in.ID, in.Name)
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

// RegisterAmendMilestone registers amend_milestone: closes the milestone's
// current row and opens a new revision with the given name and outcome.
// Nothing on the milestone's delivery axis -- status history, Delivers,
// must-not-foreclose, deferrals -- is read or written by it.
func RegisterAmendMilestone(reg *server.Registry, sessions store.SessionStore, amend store.AmendStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "amend_milestone",
		Description: "Amend a milestone's authoring content: replace its name and outcome as a new SCD2 revision under the same id. " +
			"Never reparents, re-kinds, or re-cuts; the milestone's FR budget is carried forward, and its whole delivery axis " +
			"(status history, Delivers, must-not-foreclose, deferrals) is left untouched.",
	}, amendPersonas, func(ctx context.Context, _ *mcp.CallToolRequest, in amendMilestoneInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		var zero handlers.IDResponse
		if err := refusePlacement(in.AmendPlacementChange, "milestone"); err != nil {
			return nil, zero, err
		}
		id, err := parseAmendInput(ctx, sessions, in.KrillSessionID, in.ID, in.Name)
		if err != nil {
			return nil, zero, err
		}
		amended, err := amend.AmendMilestone(ctx, id, in.Name, in.Outcome)
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.IDResponse{ID: amended.ID.String()}, nil
	})
}

// RegisterAmendAll registers every amend_* tool against reg.
func RegisterAmendAll(reg *server.Registry, sessions store.SessionStore, amend store.AmendStore) {
	RegisterAmendProduct(reg, sessions, amend)
	RegisterAmendFeatureSet(reg, sessions, amend)
	RegisterAmendFeature(reg, sessions, amend)
	RegisterAmendRequirement(reg, sessions, amend)
	RegisterAmendPersona(reg, sessions, amend)
	RegisterAmendNonGoal(reg, sessions, amend)
	RegisterAmendLoadBearingDecision(reg, sessions, amend)
	RegisterAmendMilestone(reg, sessions, amend)
}
