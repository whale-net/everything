// This file (issue #2941) is krill's work-axis per-milestone task
// discovery MCP tool: list_tasks, a thin wrapper over
// store.TaskStore.ListTasksByMilestone, mirroring
// krill/api/handlers/task_list.go's ListTasksHandler for the same
// capability (LB7). Ungated like every other read tool
// (server.RegisterRead) -- NFR6's gate is write-only, so no
// krillSessionInput here.
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

// listTasksInput is list_tasks' argument schema: a single milestone_ref
// surrogate id (a milepebble, or a milestone with no milepebble cut).
type listTasksInput struct {
	MilestoneID string `json:"milestone_id" jsonschema:"The milepebble's (or uncut milestone's) surrogate id, as a UUID string."`
}

// RegisterListTasks registers list_tasks: every task scoped to one
// milestone/milepebble, oldest-created first, with each task's lane,
// attempt count, and live-claim flag -- the same
// handlers.ListTasksResponse GET /milestones/{id}/tasks returns (LB7).
// Use get_task on a returned id for the full payload.
func RegisterListTasks(reg *server.Registry, tasks store.TaskStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name:        "list_tasks",
		Description: "List every task scoped to a milestone or milepebble (id, title, current_lane, attempt_count, has_live_claim), oldest-created first. Use get_task on a returned id for its full payload.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listTasksInput) (*mcp.CallToolResult, handlers.ListTasksResponse, error) {
		var zero handlers.ListTasksResponse

		milestoneID, err := uuid.Parse(in.MilestoneID)
		if err != nil {
			return nil, zero, fmt.Errorf("milestone_id: invalid or missing UUID")
		}

		list, err := tasks.ListTasksByMilestone(ctx, milestoneID)
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.NewListTasksResponse(list), nil
	})
}
