// This file (issue #2719, FR1, C14) is krill's work-axis task-create MCP
// tool: create_task, a thin wrapper over store.TaskStore.CreateTask
// (krill/store/task.go), mirroring krill/api/handlers/task.go's HTTP
// surface for the same capability. Restricted to PersonaSwarmOperator
// (unlike the milestone-authoring tools' Requirement Contributor/Agent
// allow-list): root plan issue #2717's Personas section states plainly
// that the Swarm Operator, not the Agent, "creates tasks and their
// dependency edges" in this milestone -- the Agent's role starts at
// claim (a later M4 task), not creation.
//
// Scaffold stage: the tool handler is a stub returning a not-implemented
// error; RegisterCreateTask's caller wiring (../main.go) lands in this
// issue's Implementation phase.
package tools

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/store"
)

// createTaskInput is create_task's argument schema (FR1). LaneSequence/
// StartingLane mirror store.Lane's fixed five-value vocabulary as wire
// strings, the same shape createTaskRequest (api/handlers/task.go) uses.
type createTaskInput struct {
	krillSessionInput
	MilestoneID  string   `json:"milestone_id" jsonschema:"The milepebble or milestone (no cut) surrogate id this task belongs to, as a UUID string (NFR7)."`
	Title        string   `json:"title" jsonschema:"The task's short title."`
	Body         *string  `json:"body" jsonschema:"Optional task body/description."`
	LaneSequence []string `json:"lane_sequence" jsonschema:"Ordered subset of {Scaffold, Implementation, Testing, Validation, Done}, lanes skippable (FR1)."`
	StartingLane string   `json:"starting_lane" jsonschema:"The lane this task starts in -- must be a member of lane_sequence."`
}

// RegisterCreateTask registers create_task (FR1): mints a new `task` row
// via store.TaskStore.CreateTask. Scaffold stub -- see this file's
// doc comment.
func RegisterCreateTask(reg *server.Registry, sessions store.SessionStore, tasks store.TaskStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name:        "create_task",
		Description: "Create a task scoped to a milepebble or a milestone with no milepebble cut, with its own lane sequence and starting lane (FR1).",
	}, []server.Persona{server.PersonaSwarmOperator}, func(ctx context.Context, _ *mcp.CallToolRequest, in createTaskInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		return nil, handlers.IDResponse{}, fmt.Errorf("create_task: not implemented -- see issue #2719's Implementation phase")
	})
}
