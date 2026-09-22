// This file (issue #2876, root plan #2851's M5, FR6) is krill's work-axis
// requeue MCP tool: requeue_task, a thin wrapper over
// store.TaskStore.RequeueTask (krill/store/task_requeue.go), mirroring
// krill/api/handlers/task_requeue.go's HTTP surface for the same
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

// requeueTaskInput is requeue_task's argument schema (FR6): the krill
// session, the task to requeue, and an optional free-text rationale.
type requeueTaskInput struct {
	krillSessionInput
	TaskID string  `json:"task_id" jsonschema:"The task to requeue, as a UUID string."`
	Reason *string `json:"reason,omitempty" jsonschema:"Optional free-text rationale for requeuing."`
}

// RegisterRequeueTask registers requeue_task (FR6): returns an escalated
// task to claimable, resetting exactly the counter (thrash or attempt)
// whose cap triggered the escalation being resolved, via
// store.TaskStore.RequeueTask, mirroring
// krill/api/handlers/task_requeue.go's RequeueTaskHandler for the same
// capability (LB7). Returns the same work.Payload document
// get_task/claim_task/.../cancel_task/release_task/escalate_task return.
func RegisterRequeueTask(reg *server.Registry, sessions store.SessionStore, tasks store.TaskStore, assembler *work.Assembler) {
	server.RegisterOpsWrite(reg, &mcp.Tool{
		Name:         "requeue_task",
		Description:  "Return an escalated task to claimable (FR6), resetting exactly the counter (thrash or attempt) whose cap triggered the escalation being resolved -- refused on a non-escalated or cancelled task. Returns the task's full payload document.",
		OutputSchema: workPayloadOutputSchema,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in requeueTaskInput) (*mcp.CallToolResult, work.Payload, error) {
		var zero work.Payload

		sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
		if err != nil {
			return nil, zero, err
		}

		taskID, err := uuid.Parse(in.TaskID)
		if err != nil {
			return nil, zero, fmt.Errorf("task_id: invalid or missing UUID")
		}

		if _, err := tasks.RequeueTask(ctx, store.RequeueParams{
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
