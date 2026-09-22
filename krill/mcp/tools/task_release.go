// This file (issue #2872, root plan #2851's M5, FR8) is krill's work-axis
// release MCP tool: release_task, a thin wrapper over
// store.TaskStore.ReleaseLease (krill/store/task_release.go), mirroring
// krill/api/handlers/task_release.go's HTTP surface for the same
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

// releaseTaskInput is release_task's argument schema (FR8): the krill
// session, the task whose active lease is being force-closed, and an
// optional free-text rationale.
type releaseTaskInput struct {
	krillSessionInput
	TaskID string  `json:"task_id" jsonschema:"The task to release, as a UUID string."`
	Reason *string `json:"reason,omitempty" jsonschema:"Optional free-text rationale for releasing."`
}

// RegisterReleaseTask registers release_task (FR8): force-closes the
// active lease on a claimed task directly, independent of lease expiry,
// counting as an attempt against the same DefaultAttemptCap M4's claim/
// reclaim/abandon paths enforce, via store.TaskStore.ReleaseLease,
// mirroring krill/api/handlers/task_release.go's ReleaseTaskHandler for
// the same capability (LB7). Returns the same work.Payload document
// get_task/claim_task/complete_task/abandon_task/cancel_task return.
func RegisterReleaseTask(reg *server.Registry, sessions store.SessionStore, tasks store.TaskStore, assembler *work.Assembler) {
	server.RegisterOpsWrite(reg, &mcp.Tool{
		Name:         "release_task",
		Description:  "Force-close the active lease on a claimed task directly, independent of lease expiry (FR8). Counts as an attempt against the same run-attempt cap claim/reclaim/abandon enforce. Returns the task's full payload document.",
		OutputSchema: workPayloadOutputSchema,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in releaseTaskInput) (*mcp.CallToolResult, work.Payload, error) {
		var zero work.Payload

		sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
		if err != nil {
			return nil, zero, err
		}

		taskID, err := uuid.Parse(in.TaskID)
		if err != nil {
			return nil, zero, fmt.Errorf("task_id: invalid or missing UUID")
		}

		if _, err := tasks.ReleaseLease(ctx, store.ReleaseParams{
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
