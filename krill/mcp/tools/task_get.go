// This file (issue #2721, FR4/FR10, C14/C16, LB7) is krill's work-axis
// by-task-id fetch MCP tool: get_task, a thin wrapper over
// work.Assembler.Assemble (krill/work/payload.go), mirroring
// krill/api/handlers/task_payload.go's HTTP surface for the same
// capability. Ungated like every other read tool (server.RegisterRead) --
// NFR6's gate is write-only, so no krillSessionInput here.
//
// get_task advertises workPayloadOutputSchema (work_payload_schema.go) --
// see that file's own doc comment for why.
package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/work"
)

// getTaskInput is get_task's argument schema (FR4/FR10): a single Task
// surrogate id.
type getTaskInput struct {
	ID string `json:"id" jsonschema:"The task's surrogate id, as a UUID string."`
}

// RegisterGetTaskPayload registers get_task (FR4, FR10): a task's payload
// document -- its embedded spec slice plus lane/dependency/attempt state --
// via work.Assembler.Assemble, mirroring
// krill/api/handlers/task_payload.go's GetTaskPayloadHandler for the same
// capability (LB7). Returns the same work.Payload a successful claim
// (#2722) will, never a bespoke MCP-local shape.
func RegisterGetTaskPayload(reg *server.Registry, tasks store.TaskStore, assembler *work.Assembler) {
	server.RegisterRead(reg, &mcp.Tool{
		Name:         "get_task",
		Description:  "Return a task's payload document: its embedded spec slice plus lane, dependency, and attempt state (FR4/FR10). Ungated -- returns whether or not a claim on the task is currently live.",
		OutputSchema: workPayloadOutputSchema,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getTaskInput) (*mcp.CallToolResult, work.Payload, error) {
		var zero work.Payload

		taskID, err := uuid.Parse(in.ID)
		if err != nil {
			return nil, zero, fmt.Errorf("id: invalid or missing UUID")
		}

		task, err := tasks.GetTaskByID(ctx, taskID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, zero, fmt.Errorf("task not found")
			}
			return nil, zero, err
		}

		payload, err := assembler.Assemble(ctx, task.ScopeID, taskID)
		if err != nil {
			return nil, zero, err
		}
		return nil, payload, nil
	})
}
