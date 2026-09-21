// This file (issue #2872, root plan #2851's M5, FR9) is krill's work-axis
// manual-escalate MCP tool: escalate_task, a thin wrapper over
// store.TaskStore.EscalateTask (krill/store/task_escalate.go), mirroring
// krill/api/handlers/task_escalate.go's HTTP surface for the same
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

// escalateTaskInput is escalate_task's argument schema (FR9): the krill
// session, the task to escalate, and an optional free-text rationale.
type escalateTaskInput struct {
	krillSessionInput
	TaskID string  `json:"task_id" jsonschema:"The task to escalate, as a UUID string."`
	Reason *string `json:"reason,omitempty" jsonschema:"Optional free-text rationale for escalating."`
}

// RegisterEscalateTask registers escalate_task (FR9): manually escalates a
// task at any time, recording the same reasoned escalation event FR2/FR3
// record automatically but with reason 'manual', via
// store.TaskStore.EscalateTask, mirroring
// krill/api/handlers/task_escalate.go's EscalateTaskHandler for the same
// capability (LB7). Returns the same work.Payload document
// get_task/claim_task/.../release_task return.
func RegisterEscalateTask(reg *server.Registry, sessions store.SessionStore, tasks store.TaskStore, assembler *work.Assembler) {
	server.RegisterOpsWrite(reg, &mcp.Tool{
		Name:        "escalate_task",
		Description: "Manually escalate a task at any time (FR9), recording the same reasoned escalation event automatic thrash-cap/attempt-cap escalation record, but with reason 'manual'. Returns the task's full payload document.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in escalateTaskInput) (*mcp.CallToolResult, work.Payload, error) {
		var zero work.Payload

		sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
		if err != nil {
			return nil, zero, err
		}

		taskID, err := uuid.Parse(in.TaskID)
		if err != nil {
			return nil, zero, fmt.Errorf("task_id: invalid or missing UUID")
		}

		if _, err := tasks.EscalateTask(ctx, store.EscalateParams{
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
