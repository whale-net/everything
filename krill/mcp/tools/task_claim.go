// This file (issue #2722, FR3/FR5, C14) is krill's work-axis claim MCP
// tool: claim_task, a thin wrapper over store.TaskStore.ClaimTask
// (krill/store/task_claim.go), mirroring krill/api/handlers/task_claim.go's
// HTTP surface for the same capability (LB7). Restricted to PersonaAgent,
// unlike create_task/declare_task_dependencies' PersonaSwarmOperator:
// root plan issue #2717's Personas section states the Agent, not the
// Swarm Operator, "claims a task" in this milestone.
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

// claimTaskInput is claim_task's argument schema (FR3, FR5): the krill
// session claiming the task, plus the task's own surrogate id.
type claimTaskInput struct {
	krillSessionInput
	TaskID string `json:"task_id" jsonschema:"The task to claim, as a UUID string."`
}

// RegisterClaimTask registers claim_task (FR3, FR5): mints a lease and
// records one attempt via store.TaskStore.ClaimTask, mirroring
// krill/api/handlers/task_claim.go's ClaimTaskHandler for the same
// capability (LB7). Returns the same work.Payload document get_task
// returns (#2721), enriched with the fresh lease -- never a bespoke
// MCP-local shape.
func RegisterClaimTask(reg *server.Registry, sessions store.SessionStore, tasks store.TaskStore, assembler *work.Assembler) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name:        "claim_task",
		Description: "Claim a claimable task: mints a lease, records one attempt, and returns the task's full payload document (FR3, FR5).",
	}, []server.Persona{server.PersonaAgent}, func(ctx context.Context, _ *mcp.CallToolRequest, in claimTaskInput) (*mcp.CallToolResult, work.Payload, error) {
		var zero work.Payload

		sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
		if err != nil {
			return nil, zero, err
		}

		taskID, err := uuid.Parse(in.TaskID)
		if err != nil {
			return nil, zero, fmt.Errorf("task_id: invalid or missing UUID")
		}

		if _, err := tasks.ClaimTask(ctx, store.ClaimTaskParams{
			ScopeID:    sess.ScopeID,
			TaskID:     taskID,
			SessionID:  sess.ID,
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
