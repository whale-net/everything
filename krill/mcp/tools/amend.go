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
// carry: no amend tool ever applies them, and sending one that DIFFERS from
// the entity's own current placement is refused by name rather than silently
// ignored (FR f0f6bc18). Sending the placement the entity already has -- a
// client echoing back what it read -- amends normally, which is why all five
// are omitempty and why the guard reads the current row before it decides
// (FR b62ed47a).
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

// amendDeferralInput is amend_deferral's schema. A deferral has no name:
// its amendable content is the body plus the destination FR1 requires.
type amendDeferralInput struct {
	krillSessionInput
	ID          string `json:"id" jsonschema:"The surrogate id (LB2) of the deferral to amend, as a UUID string. Unchanged by the amend."`
	Body        string `json:"body" jsonschema:"The deferral's replacement text. Required."`
	Destination string `json:"destination" jsonschema:"Where the deferred item went (FR1). Required."`
}

// parseAmendID resolves the session gate and the entity id exactly as the
// HTTP amend handlers do. It is parseAmendInput minus the name check, for
// the sibling verb that changes WHERE an entity sits and so has no name to
// require.
func parseAmendID(ctx context.Context, sessions store.SessionStore, krillSessionID, id string) (uuid.UUID, error) {
	if _, err := requireKrillSession(ctx, sessions, krillSessionID); err != nil {
		return uuid.Nil, err
	}
	parsed, err := uuid.Parse(id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("id: invalid or missing UUID")
	}
	return parsed, nil
}

// parseAmendInput resolves the session gate and the id/name checks exactly
// as the HTTP amend handlers do, before any store mutation runs.
func parseAmendInput(ctx context.Context, sessions store.SessionStore, krillSessionID, id, name string) (uuid.UUID, error) {
	parsed, err := parseAmendID(ctx, sessions, krillSessionID, id)
	if err != nil {
		return uuid.Nil, err
	}
	if err := handlers.RequireNonEmpty("name", name); err != nil {
		return uuid.Nil, err
	}
	return parsed, nil
}

// refusePlacement is the FR f0f6bc18 refusal every tool shares, decided
// against the entity's own current placement rather than against the
// presence of a field in the arguments: an argument that echoes the
// placement already there is not a change and amends fine (FR b62ed47a). A
// tool that sends no placement field at all never reads it.
func refusePlacement(ctx context.Context, amend store.AmendStore, placement store.AmendPlacementChange, entityKind string, id uuid.UUID) error {
	if !placement.Sent() {
		return nil
	}
	current, err := amend.CurrentPlacement(ctx, entityKind, id)
	if err != nil {
		return err
	}
	return placement.Refuse(entityKind, current)
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
		id, err := parseAmendInput(ctx, sessions, in.KrillSessionID, in.ID, in.Name)
		if err != nil {
			return nil, zero, err
		}
		if err := refusePlacement(ctx, amend, in.AmendPlacementChange, "product", id); err != nil {
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
		id, err := parseAmendInput(ctx, sessions, in.KrillSessionID, in.ID, in.Name)
		if err != nil {
			return nil, zero, err
		}
		if err := refusePlacement(ctx, amend, in.AmendPlacementChange, "feature set", id); err != nil {
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
		id, err := parseAmendInput(ctx, sessions, in.KrillSessionID, in.ID, in.Name)
		if err != nil {
			return nil, zero, err
		}
		if err := refusePlacement(ctx, amend, in.AmendPlacementChange, "feature", id); err != nil {
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
		id, err := parseAmendInput(ctx, sessions, in.KrillSessionID, in.ID, in.Name)
		if err != nil {
			return nil, zero, err
		}
		if err := refusePlacement(ctx, amend, in.AmendPlacementChange, "requirement", id); err != nil {
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
		id, err := parseAmendInput(ctx, sessions, in.KrillSessionID, in.ID, in.Name)
		if err != nil {
			return nil, zero, err
		}
		if err := refusePlacement(ctx, amend, in.AmendPlacementChange, "persona", id); err != nil {
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
		id, err := parseAmendInput(ctx, sessions, in.KrillSessionID, in.ID, in.Name)
		if err != nil {
			return nil, zero, err
		}
		if err := refusePlacement(ctx, amend, in.AmendPlacementChange, "non-goal", id); err != nil {
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
		id, err := parseAmendInput(ctx, sessions, in.KrillSessionID, in.ID, in.Name)
		if err != nil {
			return nil, zero, err
		}
		if err := refusePlacement(ctx, amend, in.AmendPlacementChange, "load-bearing decision", id); err != nil {
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
		id, err := parseAmendInput(ctx, sessions, in.KrillSessionID, in.ID, in.Name)
		if err != nil {
			return nil, zero, err
		}
		if err := refusePlacement(ctx, amend, in.AmendPlacementChange, "milestone", id); err != nil {
			return nil, zero, err
		}
		amended, err := amend.AmendMilestone(ctx, id, in.Name, in.Outcome)
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.IDResponse{ID: amended.ID.String()}, nil
	})
}

// RegisterAmendDeferral registers amend_deferral: supersedes one milestone
// deferral's text under its unchanged id, keeping its milestone, position
// and original authorship.
func RegisterAmendDeferral(reg *server.Registry, sessions store.SessionStore, amend store.AmendStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "amend_deferral",
		Description: "Amend a milestone deferral: replace its body and destination as a new SCD2 revision under the same id. " +
			"The deferral stays on its milestone at its position, and keeps its original authorship.",
	}, amendPersonas, func(ctx context.Context, _ *mcp.CallToolRequest, in amendDeferralInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		var zero handlers.IDResponse
		id, err := parseAmendID(ctx, sessions, in.KrillSessionID, in.ID)
		if err != nil {
			return nil, zero, err
		}
		if err := handlers.RequireNonEmpty("body", in.Body); err != nil {
			return nil, zero, err
		}
		amended, err := amend.AmendDeferral(ctx, id, in.Body, in.Destination)
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.IDResponse{ID: amended.ID.String()}, nil
	})
}

// reparentFeatureInput is reparent_feature's argument schema: the Feature to
// move and the feature set to move it under. There is no name or body -- a
// reparent changes only WHERE the Feature sits -- so this is not
// amendDescribedInput, and the id parse goes through parseAmendID rather
// than parseAmendInput, whose name requirement a reparent has no answer for.
type reparentFeatureInput struct {
	krillSessionInput
	FeatureID    string `json:"feature_id" jsonschema:"The surrogate id (LB2) of the feature to move, as a UUID string. Unchanged by the move."`
	FeatureSetID string `json:"feature_set_id" jsonschema:"The surrogate id (LB2) of the feature set to move the feature under, as a UUID string."`
}

// RegisterReparentFeature registers reparent_feature: the move half of the
// SCD2 write path, registered from this file because AmendPlacementChange
// names it back to the caller (store/amend.go) and because the verb is
// amend's own sibling -- same close-and-open, different column.
func RegisterReparentFeature(reg *server.Registry, sessions store.SessionStore, reparent store.ReparentStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "reparent_feature",
		Description: "Move a feature between feature sets: close the feature's current row and open a successor under the given feature_set_id, " +
			"under the same id. It is SCD2 -- the prior revision is closed, not deleted, and nothing is created or deleted. The feature keeps its " +
			"Cn display number, its requirements, and its delivery associations, and its name and description come along unchanged; to reword it, " +
			"use amend_feature. The move stays inside one product -- a feature set of another product is refused, since Cn is numbered product-wide.",
	}, amendPersonas, func(ctx context.Context, _ *mcp.CallToolRequest, in reparentFeatureInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		var zero handlers.IDResponse
		id, err := parseAmendID(ctx, sessions, in.KrillSessionID, in.FeatureID)
		if err != nil {
			return nil, zero, err
		}
		featureSetID, err := uuid.Parse(in.FeatureSetID)
		if err != nil {
			return nil, zero, fmt.Errorf("feature_set_id: invalid or missing UUID")
		}
		moved, err := reparent.ReparentFeature(ctx, id, featureSetID)
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.IDResponse{ID: moved.ID.String()}, nil
	})
}

// RegisterAmendAll registers every amend_* tool against reg, plus
// reparent_feature -- the refusal store.AmendPlacementChange reports for a
// Feature's feature_set_id has to name a verb this mount actually has.
func RegisterAmendAll(reg *server.Registry, sessions store.SessionStore, amend store.AmendStore, reparent store.ReparentStore) {
	RegisterAmendProduct(reg, sessions, amend)
	RegisterAmendFeatureSet(reg, sessions, amend)
	RegisterAmendFeature(reg, sessions, amend)
	RegisterAmendRequirement(reg, sessions, amend)
	RegisterAmendPersona(reg, sessions, amend)
	RegisterAmendNonGoal(reg, sessions, amend)
	RegisterAmendLoadBearingDecision(reg, sessions, amend)
	RegisterAmendMilestone(reg, sessions, amend)
	RegisterAmendDeferral(reg, sessions, amend)
	RegisterReparentFeature(reg, sessions, reparent)
}
