// This file is krill's non-mediated entity creation MCP tool group:
// create_product, create_feature_set, create_load_bearing_decision,
// create_persona, create_non_goal, create_feature, and create_requirement --
// thin wrappers over store.ProductStore.Create/store.FeatureSetStore.Create/
// store.LoadBearingDecisionStore.Create/store.PersonaStore.Create/
// store.NonGoalStore.Create/store.FeatureStore.Create/
// store.RequirementStore.Create, mirroring krill/api/handlers/product.go's,
// featureset.go's, decision.go's, feature.go's, and requirement.go's HTTP
// surface for those five capabilities (LB7), the same way milestone.go
// mirrors krill/api/handlers/milestone.go. Persona and NonGoal have no HTTP
// handler of their own to mirror -- before this file, the only way to
// create either was krill/importer/write.go's one-shot import path, never a
// live session; those two tools wrap store.PersonaStore/store.NonGoalStore
// directly, same as the other tools wrap their store, just with no
// handlers.IDResponse producer in between to reuse. Before this file, a
// Product, FeatureSet, or LoadBearingDecision could only be created over
// HTTP (POST /products, POST /feature-sets, POST /load-bearing-decisions);
// create_feature/create_requirement close the same gap for Feature/
// Requirement (issue #2961) -- design.go's propose_entities remains the
// only mediated-intake path (FR9/FR10, acting != on_behalf_of required),
// but a self-authoring caller (acting == on_behalf_of, e.g. an architect
// persona or a straight import/replay with no Requirement Contributor in
// the loop) now has a non-mediated way to create Feature/Requirement rows
// too, exactly like every other entity kind in this file.
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

// ── create_product (write) ──────────────────────────────────────────────────

// createProductInput is create_product's argument schema -- mirrors
// api/handlers/product.go's createProductRequest, plus the krill session id
// that request's HTTP twin takes via a header instead.
type createProductInput struct {
	krillSessionInput
	Name   string `json:"name" jsonschema:"The Product's name, unique within the caller's scope (LB1)."`
	Vision string `json:"vision" jsonschema:"The Product's vision statement."`
}

// RegisterCreateProduct registers create_product: mints a new Product row
// via store.ProductStore.Create -- Products have no parent (product.go's
// ProductStore doc comment), so this is the one create tool in this file
// with no parent id field to validate.
func RegisterCreateProduct(reg *server.Registry, sessions store.SessionStore, products store.ProductStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name:        "create_product",
		Description: "Create a Product -- the top of the spec chain, with no parent (FR1).",
	}, []server.Persona{server.PersonaRequirementContributor, server.PersonaAgent, server.PersonaSwarmOperator}, func(ctx context.Context, _ *mcp.CallToolRequest, in createProductInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		var zero handlers.IDResponse

		sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
		if err != nil {
			return nil, zero, err
		}

		if in.Name == "" {
			return nil, zero, fmt.Errorf("name: required")
		}
		if in.Vision == "" {
			return nil, zero, fmt.Errorf("vision: required")
		}

		product, err := products.Create(ctx, sess.ScopeID, in.Name, in.Vision)
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.IDResponse{ID: product.ID.String()}, nil
	})
}

// ── create_feature_set (write) ──────────────────────────────────────────────

// createFeatureSetInput is create_feature_set's argument schema -- mirrors
// api/handlers/featureset.go's createFeatureSetRequest, plus the krill
// session id that request's HTTP twin takes via a header instead.
type createFeatureSetInput struct {
	krillSessionInput
	ProductID   string  `json:"product_id" jsonschema:"The Product surrogate id this FeatureSet belongs to, as a UUID string (LB2's single required parent reference)."`
	Name        string  `json:"name" jsonschema:"The FeatureSet's name, unique within its parent Product (LB1)."`
	Description *string `json:"description,omitempty" jsonschema:"Optional FeatureSet description."`
}

// RegisterCreateFeatureSet registers create_feature_set: mints a new
// FeatureSet row under an existing Product via store.FeatureSetStore.Create.
func RegisterCreateFeatureSet(reg *server.Registry, sessions store.SessionStore, featureSets store.FeatureSetStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name:        "create_feature_set",
		Description: "Create a FeatureSet under a Product (FR2).",
	}, []server.Persona{server.PersonaRequirementContributor, server.PersonaAgent, server.PersonaSwarmOperator}, func(ctx context.Context, _ *mcp.CallToolRequest, in createFeatureSetInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		var zero handlers.IDResponse

		sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
		if err != nil {
			return nil, zero, err
		}

		productID, err := uuid.Parse(in.ProductID)
		if err != nil {
			return nil, zero, fmt.Errorf("product_id: invalid or missing UUID")
		}
		if in.Name == "" {
			return nil, zero, fmt.Errorf("name: required")
		}

		featureSet, err := featureSets.Create(ctx, sess.ScopeID, productID, in.Name, in.Description)
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.IDResponse{ID: featureSet.ID.String()}, nil
	})
}

// ── create_load_bearing_decision (write) ────────────────────────────────────

// createLoadBearingDecisionInput is create_load_bearing_decision's argument
// schema -- mirrors api/handlers/decision.go's
// attachLoadBearingDecisionRequest, plus the krill session id that
// request's HTTP twin takes via a header instead.
type createLoadBearingDecisionInput struct {
	krillSessionInput
	FeatureSetID string  `json:"feature_set_id" jsonschema:"The FeatureSet surrogate id this decision is attached to, as a UUID string (C2: attached to the area it constrains, not the Product or a Feature)."`
	Name         string  `json:"name" jsonschema:"The decision's name, unique within its parent FeatureSet (LB1)."`
	Body         *string `json:"body,omitempty" jsonschema:"Optional decision body."`
}

// RegisterCreateLoadBearingDecision registers create_load_bearing_decision:
// mints a new LoadBearingDecision row attached to an existing FeatureSet via
// store.LoadBearingDecisionStore.Create -- per that store method's doc
// comment there is no separate create-then-attach step, Create against a
// FeatureSet id is the attach for a brand-new decision.
func RegisterCreateLoadBearingDecision(reg *server.Registry, sessions store.SessionStore, decisions store.LoadBearingDecisionStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name:        "create_load_bearing_decision",
		Description: "Create a Load-Bearing Decision attached to a FeatureSet (FR4) -- PRODUCT.md's LB<n> entries.",
	}, []server.Persona{server.PersonaRequirementContributor, server.PersonaAgent, server.PersonaSwarmOperator}, func(ctx context.Context, _ *mcp.CallToolRequest, in createLoadBearingDecisionInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		var zero handlers.IDResponse

		sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
		if err != nil {
			return nil, zero, err
		}

		featureSetID, err := uuid.Parse(in.FeatureSetID)
		if err != nil {
			return nil, zero, fmt.Errorf("feature_set_id: invalid or missing UUID")
		}
		if in.Name == "" {
			return nil, zero, fmt.Errorf("name: required")
		}

		decision, err := decisions.Create(ctx, sess.ScopeID, featureSetID, in.Name, in.Body)
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.IDResponse{ID: decision.ID.String()}, nil
	})
}

// ── create_persona (write) ──────────────────────────────────────────────────

// createPersonaInput is create_persona's argument schema -- there is no
// HTTP request type to mirror (Persona has no HTTP handler), so this
// follows createFeatureSetInput's shape: a single required parent id plus
// name/description.
type createPersonaInput struct {
	krillSessionInput
	ProductID   string  `json:"product_id" jsonschema:"The Product surrogate id this Persona belongs to, as a UUID string (single required parent reference)."`
	Name        string  `json:"name" jsonschema:"The Persona's name, unique within its parent Product (LB1)."`
	Description *string `json:"description,omitempty" jsonschema:"Optional Persona description."`
}

// RegisterCreatePersona registers create_persona: mints a new Persona row
// under an existing Product via store.PersonaStore.Create -- previously only
// reachable through krill/importer/write.go's one-shot import path, never a
// live session.
func RegisterCreatePersona(reg *server.Registry, sessions store.SessionStore, personas store.PersonaStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name:        "create_persona",
		Description: "Create a Persona under a Product.",
	}, []server.Persona{server.PersonaRequirementContributor, server.PersonaAgent, server.PersonaSwarmOperator}, func(ctx context.Context, _ *mcp.CallToolRequest, in createPersonaInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		var zero handlers.IDResponse

		sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
		if err != nil {
			return nil, zero, err
		}

		productID, err := uuid.Parse(in.ProductID)
		if err != nil {
			return nil, zero, fmt.Errorf("product_id: invalid or missing UUID")
		}
		if in.Name == "" {
			return nil, zero, fmt.Errorf("name: required")
		}

		persona, err := personas.Create(ctx, sess.ScopeID, productID, in.Name, in.Description)
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.IDResponse{ID: persona.ID.String()}, nil
	})
}

// ── create_non_goal (write) ─────────────────────────────────────────────────

// createNonGoalInput is create_non_goal's argument schema -- there is no
// HTTP request type to mirror (NonGoal has no HTTP handler), so this
// follows createFeatureSetInput's shape plus the kind discriminator
// krill/importer/write.go already parses from its own source format.
type createNonGoalInput struct {
	krillSessionInput
	ProductID string  `json:"product_id" jsonschema:"The Product surrogate id this Non-Goal belongs to, as a UUID string (single required parent reference)."`
	Kind      string  `json:"kind" jsonschema:"Either 'permanent' or 'deferred' -- PRODUCT.md's two Non-goals buckets."`
	Name      string  `json:"name" jsonschema:"The Non-Goal's name, unique within its parent Product (LB1)."`
	Body      *string `json:"body,omitempty" jsonschema:"Optional Non-Goal body."`
}

// RegisterCreateNonGoal registers create_non_goal: mints a new NonGoal row
// under an existing Product via store.NonGoalStore.Create -- previously only
// reachable through krill/importer/write.go's one-shot import path, never a
// live session.
func RegisterCreateNonGoal(reg *server.Registry, sessions store.SessionStore, nonGoals store.NonGoalStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name:        "create_non_goal",
		Description: "Create a Non-Goal (permanent or deferred) under a Product.",
	}, []server.Persona{server.PersonaRequirementContributor, server.PersonaAgent, server.PersonaSwarmOperator}, func(ctx context.Context, _ *mcp.CallToolRequest, in createNonGoalInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		var zero handlers.IDResponse

		sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
		if err != nil {
			return nil, zero, err
		}

		productID, err := uuid.Parse(in.ProductID)
		if err != nil {
			return nil, zero, fmt.Errorf("product_id: invalid or missing UUID")
		}
		var kind store.NonGoalKind
		switch in.Kind {
		case string(store.NonGoalKindPermanent):
			kind = store.NonGoalKindPermanent
		case string(store.NonGoalKindDeferred):
			kind = store.NonGoalKindDeferred
		default:
			return nil, zero, fmt.Errorf("kind: must be %q or %q", store.NonGoalKindPermanent, store.NonGoalKindDeferred)
		}
		if in.Name == "" {
			return nil, zero, fmt.Errorf("name: required")
		}

		nonGoal, err := nonGoals.Create(ctx, sess.ScopeID, productID, kind, in.Name, in.Body)
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.IDResponse{ID: nonGoal.ID.String()}, nil
	})
}

// ── create_feature (write) ──────────────────────────────────────────────────

// createFeatureInput is create_feature's argument schema -- mirrors
// api/handlers/feature.go's createFeatureRequest, plus the krill session id
// that request's HTTP twin takes via a header instead.
type createFeatureInput struct {
	krillSessionInput
	FeatureSetID string  `json:"feature_set_id" jsonschema:"The FeatureSet surrogate id this Feature belongs to, as a UUID string (LB2's single required parent reference)."`
	Name         string  `json:"name" jsonschema:"The Feature's name, unique within its parent FeatureSet (LB1)."`
	Description  *string `json:"description,omitempty" jsonschema:"Optional Feature description."`
}

// RegisterCreateFeature registers create_feature: mints a new Feature row
// under an existing FeatureSet via store.FeatureStore.Create -- a
// non-mediated path (issue #2961), distinct from design.go's propose_entities
// (FR9/FR10's mediated intake, which requires acting != on_behalf_of): the
// caller here is attributed to the resolved krill session's own Acting/
// OnBehalfOf identities exactly like every other tool in this file, with no
// distinctness requirement.
func RegisterCreateFeature(reg *server.Registry, sessions store.SessionStore, features store.FeatureStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name:        "create_feature",
		Description: "Create a Feature under a FeatureSet (FR2) -- a non-mediated alternative to propose_entities for a self-authoring caller.",
	}, []server.Persona{server.PersonaRequirementContributor, server.PersonaAgent, server.PersonaSwarmOperator}, func(ctx context.Context, _ *mcp.CallToolRequest, in createFeatureInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		var zero handlers.IDResponse

		sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
		if err != nil {
			return nil, zero, err
		}

		featureSetID, err := uuid.Parse(in.FeatureSetID)
		if err != nil {
			return nil, zero, fmt.Errorf("feature_set_id: invalid or missing UUID")
		}
		if in.Name == "" {
			return nil, zero, fmt.Errorf("name: required")
		}

		feature, err := features.Create(ctx, sess.ScopeID, featureSetID, in.Name, in.Description)
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.IDResponse{ID: feature.ID.String()}, nil
	})
}

// ── create_requirement (write) ──────────────────────────────────────────────

// createRequirementInput is create_requirement's argument schema -- mirrors
// api/handlers/requirement.go's createRequirementRequest, plus the krill
// session id that request's HTTP twin takes via a header instead. Kind must
// be "FR" or "NFR" -- there is no separate tool per kind, mirroring
// store/requirement.go's one-table-two-kinds design.
type createRequirementInput struct {
	krillSessionInput
	FeatureID string  `json:"feature_id" jsonschema:"The Feature surrogate id this Requirement belongs to, as a UUID string (LB2's single required parent reference)."`
	Kind      string  `json:"kind" jsonschema:"FR or NFR."`
	Name      string  `json:"name" jsonschema:"The Requirement's name, unique within its parent Feature (LB1)."`
	Body      *string `json:"body,omitempty" jsonschema:"Optional Requirement body."`
}

// RegisterCreateRequirement registers create_requirement: mints a new
// Requirement row (FR or NFR) under an existing Feature via
// store.RequirementStore.Create -- a non-mediated path (issue #2961), the
// same distinction from design.go's propose_entities as create_feature
// above.
func RegisterCreateRequirement(reg *server.Registry, sessions store.SessionStore, requirements store.RequirementStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name:        "create_requirement",
		Description: "Create a Requirement (FR or NFR) under a Feature (FR2) -- a non-mediated alternative to propose_entities for a self-authoring caller.",
	}, []server.Persona{server.PersonaRequirementContributor, server.PersonaAgent, server.PersonaSwarmOperator}, func(ctx context.Context, _ *mcp.CallToolRequest, in createRequirementInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		var zero handlers.IDResponse

		sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
		if err != nil {
			return nil, zero, err
		}

		featureID, err := uuid.Parse(in.FeatureID)
		if err != nil {
			return nil, zero, fmt.Errorf("feature_id: invalid or missing UUID")
		}
		if in.Name == "" {
			return nil, zero, fmt.Errorf("name: required")
		}

		kind := store.RequirementKind(in.Kind)
		switch kind {
		case store.RequirementKindFR, store.RequirementKindNFR:
		default:
			return nil, zero, fmt.Errorf("kind must be %q or %q, got %q", store.RequirementKindFR, store.RequirementKindNFR, in.Kind)
		}

		requirement, err := requirements.Create(ctx, sess.ScopeID, featureID, kind, in.Name, in.Body)
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.IDResponse{ID: requirement.ID.String()}, nil
	})
}

// ── entrypoint ───────────────────────────────────────────────────────────────

// RegisterEntityCreateAll registers every non-mediated entity creation tool
// this file exposes against reg -- create_product, create_feature_set,
// create_load_bearing_decision, create_persona, create_non_goal,
// create_feature, and create_requirement. The caller (../main.go) mounts reg
// at the design mount (server.designMountPath via designReg), the same
// mount every other write tool in this package registers on.
func RegisterEntityCreateAll(reg *server.Registry, sessions store.SessionStore, products store.ProductStore, featureSets store.FeatureSetStore, decisions store.LoadBearingDecisionStore, personas store.PersonaStore, nonGoals store.NonGoalStore, features store.FeatureStore, requirements store.RequirementStore) {
	RegisterCreateProduct(reg, sessions, products)
	RegisterCreateFeatureSet(reg, sessions, featureSets)
	RegisterCreateLoadBearingDecision(reg, sessions, decisions)
	RegisterCreatePersona(reg, sessions, personas)
	RegisterCreateNonGoal(reg, sessions, nonGoals)
	RegisterCreateFeature(reg, sessions, features)
	RegisterCreateRequirement(reg, sessions, requirements)
}
