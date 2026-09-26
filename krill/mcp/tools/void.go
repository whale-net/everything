// This file is krill's void MCP tool group: the tombstone verb -- void_entity,
// the SCD2 close-WITHOUT-successor that is the other half of the boundary
// call amend draws (LB3). It mirrors the HTTP surface in
// krill/api/handlers/void.go (LB7), where the kind is a path segment and
// here it is the entity_kind argument.
//
// ONE tool, not seven: the seven void-able kinds differ in nothing a caller
// can act on -- the arguments, the refusals, and the returned id are
// identical. voidEntity is one shared body parameterised by a kind constant;
// the caller's switch does the dispatch, passing that constant through. Seven
// tools would put seven near-identical descriptions in the model's context and
// make it pick between them on nothing. This mirrors record_note, which
// already takes an entity_kind discriminator over the same five spec-axis
// kinds, rather than inventing a second convention. The HTTP twin keeps one
// route per kind because there the path genuinely is the discriminator.
//
// Milestone is deliberately absent: a milestone is a delivery-axis fact
// whose delivery records are append-only, and FR 39373553 gives it amend
// for its authoring fields instead.
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

// voidEntityKinds is the closed set of entity_kind values void_entity
// accepts, in the order the tool description lists them.
var voidEntityKinds = []string{
	string(store.VoidedProduct),
	string(store.VoidedFeatureSet),
	string(store.VoidedFeature),
	string(store.VoidedRequirement),
	string(store.VoidedPersona),
	string(store.VoidedNonGoal),
	string(store.VoidedLoadBearingDecision),
}

// voidEntityInput is void_entity's argument schema: which kind of entity to
// tombstone, its surrogate id, and an optional reason recorded on the
// tombstone. The krill session id travels as an ordinary input field
// (krillSessionInput) -- the LB4 subject pair is resolved from it, never
// from a caller-supplied field.
type voidEntityInput struct {
	krillSessionInput
	EntityKind string  `json:"entity_kind" jsonschema:"Which kind of entity to void: one of product, feature_set, feature, requirement, persona, non_goal, load_bearing_decision."`
	EntityID   string  `json:"entity_id" jsonschema:"The surrogate id (LB2) of the entity to void, as a UUID string."`
	Reason     *string `json:"reason,omitempty" jsonschema:"Optional free-text reason, recorded on the tombstone for the list_void_events audit read."`
}

// listVoidEventsInput is list_void_events's argument schema. Like
// list_products, it takes scope_id explicitly rather than deriving it from
// a session: it is a read, and every read tool on this mount is ungated.
type listVoidEventsInput struct {
	ScopeID    string `json:"scope_id" jsonschema:"The scope to list void events for, as a UUID string. Use init_session's scope_id, or list_products to discover one."`
	EntityKind string `json:"entity_kind,omitempty" jsonschema:"Narrow to one entity kind. Omit for every kind."`
}

// voidPersonas matches amend's: void is the Requirement Contributor's
// correction path for a create they should not have made.
var voidPersonas = []server.Persona{server.PersonaRequirementContributor, server.PersonaAgent, server.PersonaSwarmOperator}

// RegisterVoidEntity registers void_entity (FR d38d726e / FR 2a3a8eef):
// closes the entity's current row and inserts NO successor, so it drops
// out of every current-slice read and from render while its unique NAME is
// freed for reuse. Its DISPLAY NUMBER is retired and never reissued, so a
// create that reuses the freed name is handed a fresh number and a `C7`
// citation already rendered for the voided entity can never silently
// repoint (LB2).
//
// It refuses -- writing nothing at all -- on a delivered or shipped entity,
// and on an entity with a live child. Both refusals name the correct path:
// amend/supersession, or void the children first.
func RegisterVoidEntity(reg *server.Registry, sessions store.SessionStore, voids store.VoidStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "void_entity",
		Description: "Void a mistaken create: tombstone a Product, FeatureSet, Feature, Requirement, Persona, NonGoal, or LoadBearingDecision " +
			"as an SCD2 close-WITHOUT-successor. It disappears from every current read and from render, its unique NAME is freed for " +
			"reuse, and its display number is retired and never reissued -- a create reusing the name gets a fresh number, so an " +
			"already-rendered Cn/LBn citation can never silently repoint. Refuses, writing nothing, on a delivered or shipped entity " +
			"(amend it instead) and on an entity with a live child (void the children first). Use list_void_events to audit past voids. " +
			"There is no void_milestone: a milestone's delivery axis is append-only and its authoring fields are amendable.",
	}, voidPersonas, func(ctx context.Context, _ *mcp.CallToolRequest, in voidEntityInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		var zero handlers.IDResponse

		sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
		if err != nil {
			return nil, zero, err
		}
		id, err := uuid.Parse(in.EntityID)
		if err != nil {
			return nil, zero, fmt.Errorf("entity_id: invalid or missing UUID")
		}

		// Dispatch on kind, not on a per-kind tool: every branch is one
		// line into the same store method, so the seven tools' behaviour
		// cannot drift apart and the refusal is identical across kinds.
		switch store.VoidedEntityKind(in.EntityKind) {
		case store.VoidedProduct:
			err = voids.VoidProduct(ctx, sess.ScopeID, id, in.Reason, sess.Acting, sess.OnBehalfOf)
		case store.VoidedFeatureSet:
			err = voids.VoidFeatureSet(ctx, sess.ScopeID, id, in.Reason, sess.Acting, sess.OnBehalfOf)
		case store.VoidedFeature:
			err = voids.VoidFeature(ctx, sess.ScopeID, id, in.Reason, sess.Acting, sess.OnBehalfOf)
		case store.VoidedRequirement:
			err = voids.VoidRequirement(ctx, sess.ScopeID, id, in.Reason, sess.Acting, sess.OnBehalfOf)
		case store.VoidedPersona:
			err = voids.VoidPersona(ctx, sess.ScopeID, id, in.Reason, sess.Acting, sess.OnBehalfOf)
		case store.VoidedNonGoal:
			err = voids.VoidNonGoal(ctx, sess.ScopeID, id, in.Reason, sess.Acting, sess.OnBehalfOf)
		case store.VoidedLoadBearingDecision:
			err = voids.VoidLoadBearingDecision(ctx, sess.ScopeID, id, in.Reason, sess.Acting, sess.OnBehalfOf)
		default:
			return nil, zero, fmt.Errorf("entity_kind: must be one of %v", voidEntityKinds)
		}
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.IDResponse{ID: id.String()}, nil
	})
}

// RegisterListVoidEvents registers list_void_events: the audit read
// (FR d38d726e (c)), the only way to reach a tombstoned entity's original
// surrogate id and original display number. Ungated, like every other read
// on this mount.
func RegisterListVoidEvents(reg *server.Registry, voids store.VoidStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name:        "list_void_events",
		Description: "Audit read for voids: every tombstone in a scope, oldest first, optionally narrowed to one entity_kind. Each entry carries the voided entity's ORIGINAL surrogate id and its ORIGINAL (retired) display number -- the identity a rendered Cn/LBn citation resolved to before the void. This is the only way to reach a voided entity; no current read can see one.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listVoidEventsInput) (*mcp.CallToolResult, handlers.ListVoidEventsResponse, error) {
		var zero handlers.ListVoidEventsResponse

		scopeID, err := uuid.Parse(in.ScopeID)
		if err != nil {
			return nil, zero, fmt.Errorf("scope_id: invalid or missing UUID")
		}

		events, err := voids.ListVoidEvents(ctx, scopeID, store.VoidedEntityKind(in.EntityKind))
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.NewListVoidEventsResponse(events), nil
	})
}

// RegisterVoidAll registers every void tool this milestone exposes.
func RegisterVoidAll(reg *server.Registry, sessions store.SessionStore, voids store.VoidStore) {
	RegisterVoidEntity(reg, sessions, voids)
	RegisterListVoidEvents(reg, voids)
}
