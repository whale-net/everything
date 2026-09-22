// This file (issue #2873, root plan #2851's M5, FR7) is krill's work-axis
// cancel MCP tool: cancel_task, a thin wrapper over
// store.TaskStore.CancelTask (krill/store/task_cancel.go), mirroring
// krill/api/handlers/task_cancel.go's HTTP surface for the same
// capability (LB7). Registered on the operator surface (../server/
// transport.go's opsMountPath, /mcp/ops, issue #2867) via
// server.RegisterOpsWrite -- PersonaSwarmOperator only, enforced by the
// mount itself.
package tools

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/work"
)

// cancelTaskInput is cancel_task's argument schema (FR7): the krill
// session, the task to cancel, and an optional free-text rationale.
type cancelTaskInput struct {
	krillSessionInput
	TaskID string  `json:"task_id" jsonschema:"The task to cancel, as a UUID string."`
	Reason *string `json:"reason,omitempty" jsonschema:"Optional free-text rationale for cancelling."`
}

// RegisterCancelTask registers cancel_task (FR7): moves any task --
// escalated or not -- into a dead-lettered terminal state (distinct from
// lane Done) that claim_task never again returns and that a later
// requeue cannot reopen, via store.TaskStore.CancelTask, mirroring
// krill/api/handlers/task_cancel.go's CancelTaskHandler for the same
// capability (LB7). Returns the same work.Payload document get_task/
// claim_task/complete_task/abandon_task return.
func RegisterCancelTask(reg *server.Registry, sessions store.SessionStore, tasks store.TaskStore, assembler *work.Assembler) {
	server.RegisterOpsWrite(reg, &mcp.Tool{
		Name:         "cancel_task",
		Description:  "Cancel a task -- escalated or not -- into a dead-lettered terminal state, distinct from Done, that claim_task never again returns and requeue cannot reopen (FR7). Returns the task's full payload document.",
		OutputSchema: workPayloadOutputSchema,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in cancelTaskInput) (*mcp.CallToolResult, work.Payload, error) {
		var zero work.Payload

		sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
		if err != nil {
			return nil, zero, err
		}

		taskID, err := uuid.Parse(in.TaskID)
		if err != nil {
			return nil, zero, fmt.Errorf("task_id: invalid or missing UUID")
		}

		if _, err := tasks.CancelTask(ctx, store.CancelTaskParams{
			ScopeID:    sess.ScopeID,
			TaskID:     taskID,
			Reason:     in.Reason,
			Acting:     sess.Acting,
			OnBehalfOf: sess.OnBehalfOf,
		}); err != nil {
			return nil, zero, err
		}

		payload, err := assembler.Assemble(ctx, sess.ScopeID, taskID)
		if err != nil {
			return nil, zero, err
		}
		return nil, payload, nil
	})
}
