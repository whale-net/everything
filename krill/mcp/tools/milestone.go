// This file (issue #2683, FR1/FR2, C13) is krill's milestone authoring MCP
// tool group: three thin wrappers over store.MilestoneAuthoringStore --
// create_milestone and set_fr_budget (write, Requirement Contributor and
// Agent personas) and get_milestone (read) -- mirroring
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

// RegisterMilestoneAll registers every milestone-authoring tool this
// milestone exposes against reg -- create_milestone/set_fr_budget
// (write) and get_milestone (read). The caller (../main.go) mounts reg at
// the design mount (server.designMountPath via designReg), never the
// read-only spec mount: create_milestone/set_fr_budget need a resolved
// krill session for their LB4 subject pair, the same shape every other
// write tool on that mount already requires (see this file's package doc
// comment).
func RegisterMilestoneAll(reg *server.Registry, sessions store.SessionStore, milestones store.MilestoneAuthoringStore) {
	RegisterCreateMilestone(reg, sessions, milestones)
	RegisterSetFRBudget(reg, sessions, milestones)
	RegisterGetMilestone(reg, milestones)
}
