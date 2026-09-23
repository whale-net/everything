// This file is krill's top-of-chain entity creation MCP tool group:
// create_product, create_feature_set, create_load_bearing_decision,
// create_persona, and create_non_goal -- thin wrappers over
// store.ProductStore.Create/store.FeatureSetStore.Create/
// store.LoadBearingDecisionStore.Create/store.PersonaStore.Create/
// store.NonGoalStore.Create, mirroring krill/api/handlers/product.go's,
// featureset.go's, and decision.go's HTTP surface for the first three
// capabilities (LB7), the same way milestone.go mirrors
// krill/api/handlers/milestone.go. Persona and NonGoal have no HTTP handler
// of their own to mirror -- before this file, the only way to create either
// was krill/importer/write.go's one-shot import path, never a live session;
// these two tools wrap store.PersonaStore/store.NonGoalStore directly, same
// as the other three tools wrap their store, just with no handlers.IDResponse
// producer in between to reuse. propose_entities (design.go) is explicitly
// scoped to Feature/Requirement only and never reaches this high in the spec
// chain.
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

// ── entrypoint ───────────────────────────────────────────────────────────────

// RegisterEntityCreateAll registers every top-of-chain entity creation tool
// this file exposes against reg -- create_product, create_feature_set,
// create_load_bearing_decision, create_persona, and create_non_goal. The
// caller (../main.go) mounts reg at the design mount
// (server.designMountPath via designReg), the same mount every other write
// tool in this package registers on.
func RegisterEntityCreateAll(reg *server.Registry, sessions store.SessionStore, products store.ProductStore, featureSets store.FeatureSetStore, decisions store.LoadBearingDecisionStore, personas store.PersonaStore, nonGoals store.NonGoalStore) {
	RegisterCreateProduct(reg, sessions, products)
	RegisterCreateFeatureSet(reg, sessions, featureSets)
	RegisterCreateLoadBearingDecision(reg, sessions, decisions)
	RegisterCreatePersona(reg, sessions, personas)
	RegisterCreateNonGoal(reg, sessions, nonGoals)
}
