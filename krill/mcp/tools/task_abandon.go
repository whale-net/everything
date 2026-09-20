// This file (issue #2726, FR9, C15) is krill's work-axis abandon MCP tool:
// abandon_task, a thin wrapper over store.TaskStore.AbandonClaim
// (krill/store/task_abandon.go), mirroring
// krill/api/handlers/task_abandon.go's HTTP surface for the same
// capability (LB7). Restricted to PersonaAgent, mirroring claim_task/
// complete_task -- the Agent holding a claim is the one that abandons it.
// abandonTaskInput declares no verdict field of any kind (FR9): abandoning
// reports no outcome.
//
// Named and shaped distinctly from krill's pre-existing delivery-axis
// abandon_milestone tool (krill/mcp/tools/abandon.go) -- neither wraps the
// other.
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

// abandonTaskInput is abandon_task's argument schema (FR9): the krill
// session, the task's own surrogate id, the claim the caller holds, and an
// optional free-text reason.
type abandonTaskInput struct {
	krillSessionInput
	TaskID  string  `json:"task_id" jsonschema:"The task to abandon, as a UUID string."`
	ClaimID string  `json:"claim_id" jsonschema:"The claim this session holds on the task, as a UUID string -- must be the task's current, unreleased claim."`
	Reason  *string `json:"reason,omitempty" jsonschema:"Optional free-text reason for abandoning."`
}

// RegisterAbandonTask registers abandon_task (FR9): releases the claim
// without reporting a verdict, records one abandoned attempt against the
// same DefaultAttemptCap ClaimTask/ReclaimExpired enforce, and leaves the
// task's current lane unchanged, via store.TaskStore.AbandonClaim,
// mirroring krill/api/handlers/task_abandon.go's AbandonTaskHandler for
// the same capability (LB7). Returns the same work.Payload document
// get_task/claim_task/complete_task return -- never a bespoke MCP-local
// shape.
func RegisterAbandonTask(reg *server.Registry, sessions store.SessionStore, tasks store.TaskStore, assembler *work.Assembler) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name:        "abandon_task",
		Description: "Abandon a claimed task without reporting a verdict (FR9): releases the claim immediately, leaves the task's current lane unchanged, and counts as an attempt against the same cap a lease-expiry lapse counts against. Returns the task's full payload document.",
	}, []server.Persona{server.PersonaAgent}, func(ctx context.Context, _ *mcp.CallToolRequest, in abandonTaskInput) (*mcp.CallToolResult, work.Payload, error) {
		var zero work.Payload

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

		if _, err := tasks.AbandonClaim(ctx, store.AbandonParams{
			ScopeID:    sess.ScopeID,
			TaskID:     taskID,
			ClaimID:    claimID,
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
