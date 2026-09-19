// This file (issue #2683, FR1/FR2, C13) is krill's milestone authoring MCP
// tool group: thin wrappers over store.MilestoneAuthoringStore --
// create_milestone, set_fr_budget, add_delivers, add_must_not_foreclose,
// and add_deferral (write, Requirement Contributor and Agent personas)
// and get_milestone (read) -- mirroring
// krill/api/handlers/milestone.go's HTTP surface for the same capability,
// via the same handlers.IDResponse/handlers.NewMilestoneResponse this
// package's other write/read tools reuse (LB7). Registered from
// RegisterMilestoneAll (below) onto the design mount
// (../main.go's designReg) -- these write tools attribute the LB4
// subject pair from a resolved krill session (krillSessionInput,
// requireKrillSession -- design.go), the same shape every other write
// tool on that mount already uses, never from a caller-supplied field.
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

// createMilestoneInput is create_milestone's argument schema (FR1).
type createMilestoneInput struct {
	krillSessionInput
	ProductID string `json:"product_id" jsonschema:"The Product surrogate id this milestone belongs to, as a UUID string."`
	Name      string `json:"name" jsonschema:"The bare milestone identifier, e.g. \"M3\"."`
	Outcome   string `json:"outcome" jsonschema:"The milestone's outcome sentence (FR1)."`
	FRBudget  *int   `json:"fr_budget" jsonschema:"Optional FR budget (FR2) -- may be set later via set_fr_budget."`
}

// RegisterCreateMilestone registers create_milestone (FR1): mints a new
// milestone_ref row via store.MilestoneAuthoringStore.CreateMilestone.
func RegisterCreateMilestone(reg *server.Registry, sessions store.SessionStore, milestones store.MilestoneAuthoringStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name:        "create_milestone",
		Description: "Create a milestone with an outcome sentence and optional FR budget (FR1, FR2).",
	}, []server.Persona{server.PersonaRequirementContributor, server.PersonaAgent}, func(ctx context.Context, _ *mcp.CallToolRequest, in createMilestoneInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
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
		if in.Outcome == "" {
			return nil, zero, fmt.Errorf("outcome: required")
		}

		milestone, err := milestones.CreateMilestone(ctx, sess.ScopeID, productID, in.Name, in.Outcome, in.FRBudget, sess.Acting, sess.OnBehalfOf)
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.IDResponse{ID: milestone.ID.String()}, nil
	})
}

// setFRBudgetInput is set_fr_budget's argument schema (FR2's revise path).
type setFRBudgetInput struct {
	krillSessionInput
	MilestoneID string `json:"milestone_id" jsonschema:"The milestone surrogate id, as a UUID string."`
	FRBudget    int    `json:"fr_budget" jsonschema:"The milestone's revised FR budget -- the current value after two calls is always the latest."`
}

// RegisterSetFRBudget registers set_fr_budget (FR2): revises a milestone's
// FR budget via store.MilestoneAuthoringStore.SetFRBudget.
func RegisterSetFRBudget(reg *server.Registry, sessions store.SessionStore, milestones store.MilestoneAuthoringStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name:        "set_fr_budget",
		Description: "Revise a milestone's FR budget (FR2).",
	}, []server.Persona{server.PersonaRequirementContributor, server.PersonaAgent}, func(ctx context.Context, _ *mcp.CallToolRequest, in setFRBudgetInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		var zero handlers.IDResponse

		sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
		if err != nil {
			return nil, zero, err
		}

		milestoneID, err := uuid.Parse(in.MilestoneID)
		if err != nil {
			return nil, zero, fmt.Errorf("milestone_id: invalid or missing UUID")
		}

		if err := milestones.SetFRBudget(ctx, milestoneID, in.FRBudget, sess.Acting, sess.OnBehalfOf); err != nil {
			return nil, zero, err
		}
		return nil, handlers.IDResponse{ID: milestoneID.String()}, nil
	})
}

// addDeliversInput is add_delivers's argument schema (LB6).
type addDeliversInput struct {
	krillSessionInput
	MilestoneID string `json:"milestone_id" jsonschema:"The milestone surrogate id, as a UUID string."`
	EntityID    string `json:"entity_id" jsonschema:"The delivered entity's surrogate id (feature or FR), as a UUID string."`
}

// RegisterAddDelivers registers add_delivers (LB6): attaches a
// Delivers association between a milestone and a feature/FR via
// store.MilestoneAuthoringStore.AddDelivers.
func RegisterAddDelivers(reg *server.Registry, sessions store.SessionStore, milestones store.MilestoneAuthoringStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name:        "add_delivers",
		Description: "Record that a milestone delivers a given feature or FR (LB6).",
	}, []server.Persona{server.PersonaRequirementContributor, server.PersonaAgent}, func(ctx context.Context, _ *mcp.CallToolRequest, in addDeliversInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		var zero handlers.IDResponse

		sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
		if err != nil {
			return nil, zero, err
		}

		milestoneID, err := uuid.Parse(in.MilestoneID)
		if err != nil {
			return nil, zero, fmt.Errorf("milestone_id: invalid or missing UUID")
		}
		entityID, err := uuid.Parse(in.EntityID)
		if err != nil {
			return nil, zero, fmt.Errorf("entity_id: invalid or missing UUID")
		}

		if err := milestones.AddDelivers(ctx, sess.ScopeID, milestoneID, entityID, sess.Acting, sess.OnBehalfOf); err != nil {
			return nil, zero, err
		}
		return nil, handlers.IDResponse{ID: milestoneID.String()}, nil
	})
}

// addMustNotForecloseInput is add_must_not_foreclose's argument schema (LB6).
type addMustNotForecloseInput struct {
	krillSessionInput
	MilestoneID string `json:"milestone_id" jsonschema:"The milestone surrogate id, as a UUID string."`
	EntityID    string `json:"entity_id" jsonschema:"The entity's surrogate id this milestone must not foreclose, as a UUID string."`
}

// RegisterAddMustNotForeclose registers add_must_not_foreclose (LB6):
// attaches a Must-not-foreclose association via
// store.MilestoneAuthoringStore.AddMustNotForeclose.
func RegisterAddMustNotForeclose(reg *server.Registry, sessions store.SessionStore, milestones store.MilestoneAuthoringStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name:        "add_must_not_foreclose",
		Description: "Record a decision or entity a milestone must not foreclose (LB6).",
	}, []server.Persona{server.PersonaRequirementContributor, server.PersonaAgent}, func(ctx context.Context, _ *mcp.CallToolRequest, in addMustNotForecloseInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		var zero handlers.IDResponse

		sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
		if err != nil {
			return nil, zero, err
		}

		milestoneID, err := uuid.Parse(in.MilestoneID)
		if err != nil {
			return nil, zero, fmt.Errorf("milestone_id: invalid or missing UUID")
		}
		entityID, err := uuid.Parse(in.EntityID)
		if err != nil {
			return nil, zero, fmt.Errorf("entity_id: invalid or missing UUID")
		}

		if err := milestones.AddMustNotForeclose(ctx, sess.ScopeID, milestoneID, entityID, sess.Acting, sess.OnBehalfOf); err != nil {
			return nil, zero, err
		}
		return nil, handlers.IDResponse{ID: milestoneID.String()}, nil
	})
}

// addDeferralInput is add_deferral's argument schema (FR1). Destination
// must be non-empty -- every deferred entry cites where it went.
type addDeferralInput struct {
	krillSessionInput
	MilestoneID string `json:"milestone_id" jsonschema:"The milestone surrogate id, as a UUID string."`
	Body        string `json:"body" jsonschema:"What was deferred."`
	Destination string `json:"destination" jsonschema:"Where the deferred item went, e.g. a future milestone or issue -- required for every deferral."`
}

// RegisterAddDeferral registers add_deferral (FR1): records a deferral
// via store.MilestoneAuthoringStore.AddDeferral.
func RegisterAddDeferral(reg *server.Registry, sessions store.SessionStore, milestones store.MilestoneAuthoringStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name:        "add_deferral",
		Description: "Record a deferred item and the destination it was pushed to (FR1).",
	}, []server.Persona{server.PersonaRequirementContributor, server.PersonaAgent}, func(ctx context.Context, _ *mcp.CallToolRequest, in addDeferralInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		var zero handlers.IDResponse

		sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
		if err != nil {
			return nil, zero, err
		}

		milestoneID, err := uuid.Parse(in.MilestoneID)
		if err != nil {
			return nil, zero, fmt.Errorf("milestone_id: invalid or missing UUID")
		}
		if in.Destination == "" {
			return nil, zero, fmt.Errorf("destination: required")
		}

		deferral, err := milestones.AddDeferral(ctx, sess.ScopeID, milestoneID, in.Body, in.Destination, sess.Acting, sess.OnBehalfOf)
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.IDResponse{ID: deferral.ID.String()}, nil
	})
}

// milestoneIDInput is get_milestone's argument schema: a single
// MilestoneRef surrogate id.
type milestoneIDInput struct {
	ID string `json:"id" jsonschema:"The milestone surrogate id, as a UUID string."`
}

// RegisterGetMilestone registers get_milestone: a milestone's authoring
// fields plus its Delivers/Must-not-foreclose association lists and its
// deferrals -- mirrors GetMilestoneHandler, via the same
// handlers.NewMilestoneResponse constructor (LB7).
func RegisterGetMilestone(reg *server.Registry, milestones store.MilestoneAuthoringStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name:        "get_milestone",
		Description: "Return a milestone's authoring fields plus its Delivers/Must-not-foreclose lists and deferrals.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in milestoneIDInput) (*mcp.CallToolResult, handlers.MilestoneResponse, error) {
		var zero handlers.MilestoneResponse

		id, err := uuid.Parse(in.ID)
		if err != nil {
			return nil, zero, fmt.Errorf("id: invalid or missing UUID")
		}

		ref, delivers, mustNotForeclose, deferrals, err := milestones.GetMilestone(ctx, id)
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.NewMilestoneResponse(ref, delivers, mustNotForeclose, deferrals), nil
	})
}

// Milepebble tools below (migration 011, issue #2684, FR3/FR4) are
// scaffold-stage skeletons -- registration onto RegisterMilestoneAll/the
// design mount and the actual store calls land in the Implementation
// phase, matching krill/store/milestone_authoring.go's own skeleton
// methods each of these will eventually wrap.

// RegisterCreateMilepebble will register create_milepebble (FR3): cuts a
// milestone into a new milepebble via
// store.MilestoneAuthoringStore.CreateMilepebble. Not yet wired into
// RegisterMilestoneAll.
func RegisterCreateMilepebble(reg *server.Registry, sessions store.SessionStore, milestones store.MilestoneAuthoringStore) {
}

// RegisterAddMilepebbleScope will register add_milepebble_scope (FR3):
// attaches a Delivers association between a milepebble and an entity
// already delivered by its parent milestone, via
// store.MilestoneAuthoringStore.AddMilepebbleDelivers. Not yet wired into
// RegisterMilestoneAll.
func RegisterAddMilepebbleScope(reg *server.Registry, sessions store.SessionStore, milestones store.MilestoneAuthoringStore) {
}

// RegisterAddDiscoveredScope will register add_discovered_scope (FR4):
// creates a real Feature/Requirement row and associates it to a
// milepebble and its parent milestone, via
// store.MilestoneAuthoringStore.AddDiscoveredScope. Not yet wired into
// RegisterMilestoneAll.
func RegisterAddDiscoveredScope(reg *server.Registry, sessions store.SessionStore, milestones store.MilestoneAuthoringStore) {
}

// RegisterListMilepebbles will register list_milepebbles: every milepebble
// cut from a milestone, in Position order, via
// store.MilestoneAuthoringStore.ListMilepebblesByMilestone. Not yet wired
// into RegisterMilestoneAll.
func RegisterListMilepebbles(reg *server.Registry, milestones store.MilestoneAuthoringStore) {
}

// RegisterMilestoneAll registers every milestone-authoring tool this
// milestone exposes against reg -- create_milestone/set_fr_budget/
// add_delivers/add_must_not_foreclose/add_deferral (write) and
// get_milestone (read). The caller (../main.go) mounts reg at the design
// mount (server.designMountPath via designReg), never the read-only spec
// mount: every write tool here needs a resolved krill session for its
// LB4 subject pair, the same shape every other write tool on that mount
// already requires (see this file's package doc comment).
func RegisterMilestoneAll(reg *server.Registry, sessions store.SessionStore, milestones store.MilestoneAuthoringStore) {
	RegisterCreateMilestone(reg, sessions, milestones)
	RegisterSetFRBudget(reg, sessions, milestones)
	RegisterAddDelivers(reg, sessions, milestones)
	RegisterAddMustNotForeclose(reg, sessions, milestones)
	RegisterAddDeferral(reg, sessions, milestones)
	RegisterGetMilestone(reg, milestones)
}
