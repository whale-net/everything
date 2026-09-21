// This file (issue #2725, FR8, C15) is krill's work-axis complete-with-a-
// verdict MCP tool: complete_task, a thin wrapper over
// store.TaskStore.CompleteTask (krill/store/task_complete.go), mirroring
// krill/api/handlers/task_complete.go's HTTP surface for the same
// capability (LB7). Originally restricted to PersonaAgent only, mirroring
// claim_task (task_claim.go) -- root plan issue #2717's Personas section
// names the Agent, not the Swarm Operator, as the one that completes a
// claimed task -- but also allow-listed to PersonaSwarmOperator as of
// issue #2926's follow-up (see task_claim.go's doc comment for why: no
// whagent-net JWT-minting path exists for krill-work yet, issue #2932).
// completeTaskInput declares no lane/destination field of any kind (FR8):
// the completing Agent reports only a verdict, never a destination lane.
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

// completeTaskInput is complete_task's argument schema (FR8): the krill
// session, the task's own surrogate id, the claim the caller holds, and a
// pass/fail verdict.
type completeTaskInput struct {
	krillSessionInput
	TaskID  string  `json:"task_id" jsonschema:"The task to complete, as a UUID string."`
	ClaimID string  `json:"claim_id" jsonschema:"The claim this session holds on the task, as a UUID string -- must be the task's current claim."`
	Verdict string  `json:"verdict" jsonschema:"The completion verdict: \"pass\" or \"fail\". krill, not the caller, decides the resulting lane from this."`
	Summary *string `json:"summary,omitempty" jsonschema:"Optional free-text completion summary."`
}

// RegisterCompleteTask registers complete_task (FR8): releases the claim,
// records one completed attempt, and advances or reverts the task's own
// lane via store.TaskStore.CompleteTask, mirroring
// krill/api/handlers/task_complete.go's CompleteTaskHandler for the same
// capability (LB7). Returns the same work.Payload document get_task/
// claim_task return -- never a bespoke MCP-local shape.
func RegisterCompleteTask(reg *server.Registry, sessions store.SessionStore, tasks store.TaskStore, assembler *work.Assembler) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name:        "complete_task",
		Description: "Complete a claimed task with a pass/fail verdict (FR8): krill, not the caller, decides whether the task advances one lane or reverts one lane in its own lane sequence. Returns the task's full payload document.",
	}, []server.Persona{server.PersonaAgent, server.PersonaSwarmOperator}, func(ctx context.Context, _ *mcp.CallToolRequest, in completeTaskInput) (*mcp.CallToolResult, work.Payload, error) {
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

		if _, err := tasks.CompleteTask(ctx, store.CompleteTaskParams{
			ScopeID:    sess.ScopeID,
			TaskID:     taskID,
			ClaimID:    claimID,
			Verdict:    store.Verdict(in.Verdict),
			Summary:    in.Summary,
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
