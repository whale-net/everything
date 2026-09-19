// This file (issue #2683, FR1/FR2, C13) is krill's milestone authoring MCP
// tool group: three thin wrappers over store.MilestoneAuthoringStore --
// create_milestone and set_fr_budget (write, Requirement Contributor
// persona) and get_milestone (read) -- mirroring
// krill/api/handlers/milestone.go's HTTP surface for the same capability.
//
// Scaffold stage: tool handlers are stubs returning a not-implemented
// error; RegisterAll wiring lands in this issue's Implementation phase.
package tools

import (
	"context"
	"fmt"

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
// Scaffold stub -- see this file's doc comment.
func RegisterCreateMilestone(reg *server.Registry, sessions store.SessionStore, milestones store.MilestoneAuthoringStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name:        "create_milestone",
		Description: "Create a milestone with an outcome sentence and optional FR budget (FR1, FR2).",
	}, []server.Persona{server.PersonaRequirementContributor, server.PersonaAgent}, func(ctx context.Context, _ *mcp.CallToolRequest, in createMilestoneInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		return nil, handlers.IDResponse{}, fmt.Errorf("create_milestone: not implemented -- see issue #2683's Implementation phase")
	})
}

// setFRBudgetInput is set_fr_budget's argument schema (FR2's revise path).
type setFRBudgetInput struct {
	krillSessionInput
	MilestoneID string `json:"milestone_id" jsonschema:"The milestone surrogate id, as a UUID string."`
	FRBudget    int    `json:"fr_budget" jsonschema:"The milestone's revised FR budget -- the current value after two calls is always the latest."`
}

// RegisterSetFRBudget registers set_fr_budget (FR2): revises a milestone's
// FR budget via store.MilestoneAuthoringStore.SetFRBudget. Scaffold
// stub -- see this file's doc comment.
func RegisterSetFRBudget(reg *server.Registry, sessions store.SessionStore, milestones store.MilestoneAuthoringStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name:        "set_fr_budget",
		Description: "Revise a milestone's FR budget (FR2).",
	}, []server.Persona{server.PersonaRequirementContributor, server.PersonaAgent}, func(ctx context.Context, _ *mcp.CallToolRequest, in setFRBudgetInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		return nil, handlers.IDResponse{}, fmt.Errorf("set_fr_budget: not implemented -- see issue #2683's Implementation phase")
	})
}

// milestoneIDInput is get_milestone's argument schema: a single
// MilestoneRef surrogate id.
type milestoneIDInput struct {
	ID string `json:"id" jsonschema:"The milestone surrogate id, as a UUID string."`
}

// RegisterGetMilestone registers get_milestone: a milestone's authoring
// fields plus its Delivers/Must-not-foreclose association lists and its
// deferrals -- mirrors GetMilestoneHandler. Scaffold stub -- see this
// file's doc comment.
func RegisterGetMilestone(reg *server.Registry, milestones store.MilestoneAuthoringStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name:        "get_milestone",
		Description: "Return a milestone's authoring fields plus its Delivers/Must-not-foreclose lists and deferrals.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in milestoneIDInput) (*mcp.CallToolResult, handlers.MilestoneResponse, error) {
		return nil, handlers.MilestoneResponse{}, fmt.Errorf("get_milestone: not implemented -- see issue #2683's Implementation phase")
	})
}
