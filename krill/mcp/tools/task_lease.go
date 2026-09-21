// This file (issue #2723, FR6, C14) is krill's work-axis heartbeat MCP
// tool: heartbeat_task, a thin wrapper over store.TaskStore.Heartbeat
// (krill/store/task_lease.go), mirroring krill/api/handlers/task_lease.go's
// HTTP surface for the same capability (LB7). Originally restricted to
// PersonaAgent only, same as claim_task -- the Agent, not the Swarm
// Operator, is the one actively working a claimed task -- but also
// allow-listed to PersonaSwarmOperator as of issue #2926's follow-up (see
// task_claim.go's doc comment for why: no whagent-net JWT-minting path
// exists for krill-work yet, issue #2932).
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

// heartbeatTaskInput is heartbeat_task's argument schema (FR6): the krill
// session heartbeating, the task's own surrogate id, and the claim id the
// caller believes it still holds.
type heartbeatTaskInput struct {
	krillSessionInput
	TaskID  string `json:"task_id" jsonschema:"The task whose lease to extend, as a UUID string."`
	ClaimID string `json:"claim_id" jsonschema:"The claim id the caller believes it still holds, as a UUID string."`
}

// RegisterHeartbeatTask registers heartbeat_task (FR6): extends a live
// claim's lease and appends one append-only task_lease_event row via
// store.TaskStore.Heartbeat, mirroring
// krill/api/handlers/task_lease.go's HeartbeatHandler for the same
// capability (LB7). A stale claim id (reclaimed, or already released by
// complete/abandon) surfaces as a tool error naming
// store.ErrClaimNotCurrent, never a silently-accepted no-op.
func RegisterHeartbeatTask(reg *server.Registry, sessions store.SessionStore, tasks store.TaskStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name:        "heartbeat_task",
		Description: "Extend a currently-held claim's lease (FR6). Rejected if the caller's claim is no longer the task's current, live claim -- a slow run must not resume writing to a task another run now owns.",
	}, []server.Persona{server.PersonaAgent, server.PersonaSwarmOperator}, func(ctx context.Context, _ *mcp.CallToolRequest, in heartbeatTaskInput) (*mcp.CallToolResult, handlers.HeartbeatResponse, error) {
		var zero handlers.HeartbeatResponse

		sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
		if err != nil {
			return nil, zero, err
		}

		taskID, err := uuid.Parse(in.TaskID)
		if err != nil {
			return nil, zero, fmt.Errorf("task_id: invalid or missing UUID")
		}
		claimID, err := uuid.Parse(in.ClaimID)
		if err != nil {
			return nil, zero, fmt.Errorf("claim_id: invalid or missing UUID")
		}

		lease, err := tasks.Heartbeat(ctx, store.HeartbeatParams{
			ScopeID:    sess.ScopeID,
			TaskID:     taskID,
			ClaimID:    claimID,
			Acting:     sess.Acting,
			OnBehalfOf: sess.OnBehalfOf,
		})
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.ToHeartbeatResponse(lease), nil
	})
}
