// This file (issue #2869, FR4, NFR6) is M5's console query MCP surface:
// the home for every console read tool this milestone adds, mounted on
// the operator surface (../server/registry.go's RegisterOpsRead,
// transport.go's opsMountPath, /mcp/ops, issue #2867) -- PersonaSwarmOperator
// only, enforced by the mount itself rather than a per-tool allow-list.
// This task ships list_claimed_tasks (FR4), mirroring
// krill/api/handlers/console.go's HTTP surface for the same capability
// 1:1 (LB7) by reusing its exported wire types rather than an MCP-local
// mirror. list_open_notes (issue #2874, FR12) is the sibling read tool
// over store.TaskStore.ListOpenNotes, mirroring
// krill/api/handlers/console.go's ListOpenNotesHandler the same way.
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

// listClaimedTasksInput is list_claimed_tasks' argument schema (FR4).
// ScopeID is explicit input, not session-derived -- this is a read tool
// (NFR6's gate is write-only), mirroring GET /console/claimed's own
// required scope_id query parameter, since this query has no single
// entity to resolve scope from.
type listClaimedTasksInput struct {
	ScopeID   string `json:"scope_id" jsonschema:"The scope to query, as a UUID string (NFR1)."`
	PageSize  int    `json:"page_size" jsonschema:"Optional page size, up to the server-enforced maximum (NFR6); absent or zero applies the default."`
	PageToken string `json:"page_token" jsonschema:"Optional continuation token from a prior page's next_token (NFR6)."`
}

// listClaimedTasksOutput mirrors krill/api/handlers/console.go's HTTP
// response for the same query 1:1 (LB7), reusing its exported
// handlers.ClaimedTaskWire rather than a bespoke MCP-local shape.
type listClaimedTasksOutput struct {
	Tasks     []handlers.ClaimedTaskWire `json:"tasks"`
	NextToken string                     `json:"next_token,omitempty"`
}

// RegisterListClaimedTasks registers list_claimed_tasks (FR4): every
// currently-claimed task in a scope -- claimant, current lane, lease
// expiry, attempt count, title, and delivery reference -- via
// store.TaskStore.ListClaimedTasks, mirroring
// krill/api/handlers/console.go's ListClaimedTasksHandler for the same
// capability. Mounted via server.RegisterOpsRead -- PersonaSwarmOperator
// only.
func RegisterListClaimedTasks(reg *server.Registry, tasks store.TaskStore) {
	server.RegisterOpsRead(reg, &mcp.Tool{
		Name:        "list_claimed_tasks",
		Description: "Return every currently-claimed task in a scope: claimant, current lane, lease expiry, attempt count, title, and delivery reference (FR4). Bounded and continuable (NFR6).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listClaimedTasksInput) (*mcp.CallToolResult, listClaimedTasksOutput, error) {
		var zero listClaimedTasksOutput

		scopeID, err := uuid.Parse(in.ScopeID)
		if err != nil {
			return nil, zero, fmt.Errorf("scope_id: invalid or missing UUID")
		}

		page, err := tasks.ListClaimedTasks(ctx, store.ListClaimedTasksParams{
			ScopeID: scopeID,
			Page: store.PageParams{
				PageSize:          in.PageSize,
				ContinuationToken: in.PageToken,
			},
		})
		if err != nil {
			return nil, zero, err
		}

		out := listClaimedTasksOutput{
			Tasks:     make([]handlers.ClaimedTaskWire, len(page.Items)),
			NextToken: page.NextToken,
		}
		for i, row := range page.Items {
			out.Tasks[i] = handlers.ToClaimedTaskWire(row)
		}
		return nil, out, nil
	})
}

// listCancelledTasksInput is list_cancelled_tasks' argument schema (FR10,
// issue #2873) -- mirrors listClaimedTasksInput's own shape: ScopeID is
// explicit input, not session-derived, since this is a read tool (NFR6's
// gate is write-only).
type listCancelledTasksInput struct {
	ScopeID   string `json:"scope_id" jsonschema:"The scope to query, as a UUID string (NFR1)."`
	PageSize  int    `json:"page_size" jsonschema:"Optional page size, up to the server-enforced maximum (NFR6); absent or zero applies the default."`
	PageToken string `json:"page_token" jsonschema:"Optional continuation token from a prior page's next_token (NFR6)."`
}

// listCancelledTasksOutput mirrors krill/api/handlers/console.go's HTTP
// response for the same query 1:1 (LB7), reusing its exported
// handlers.CancelledTaskWire rather than a bespoke MCP-local shape.
type listCancelledTasksOutput struct {
	Tasks     []handlers.CancelledTaskWire `json:"tasks"`
	NextToken string                       `json:"next_token,omitempty"`
}

// RegisterListCancelledTasks registers list_cancelled_tasks (FR10, issue
// #2873): every cancelled task in a scope -- title, delivery reference,
// and the cancellation's own acting/on-behalf-of subjects and timestamp --
// via store.TaskStore.ListCancelledTasks, mirroring
// krill/api/handlers/console.go's ListCancelledTasksHandler for the same
// capability. Mounted via server.RegisterOpsRead -- PersonaSwarmOperator
// only.
func RegisterListCancelledTasks(reg *server.Registry, tasks store.TaskStore) {
	server.RegisterOpsRead(reg, &mcp.Tool{
		Name:        "list_cancelled_tasks",
		Description: "Return every cancelled (dead-lettered) task in a scope: title, delivery reference, and who cancelled it (FR10). Bounded and continuable (NFR6).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listCancelledTasksInput) (*mcp.CallToolResult, listCancelledTasksOutput, error) {
		var zero listCancelledTasksOutput

		scopeID, err := uuid.Parse(in.ScopeID)
		if err != nil {
			return nil, zero, fmt.Errorf("scope_id: invalid or missing UUID")
		}

		page, err := tasks.ListCancelledTasks(ctx, store.ListCancelledTasksParams{
			ScopeID: scopeID,
			Page: store.PageParams{
				PageSize:          in.PageSize,
				ContinuationToken: in.PageToken,
			},
		})
		if err != nil {
			return nil, zero, err
		}

		out := listCancelledTasksOutput{
			Tasks:     make([]handlers.CancelledTaskWire, len(page.Items)),
			NextToken: page.NextToken,
		}
		for i, row := range page.Items {
			out.Tasks[i] = handlers.ToCancelledTaskWire(row)
		}
		return nil, out, nil
	})
}

// listOpenNotesInput is list_open_notes' argument schema (FR12). ScopeID is
// explicit input, not session-derived -- mirrors listClaimedTasksInput's own
// posture for the same reason (see its own doc comment).
type listOpenNotesInput struct {
	ScopeID   string `json:"scope_id" jsonschema:"The scope to query, as a UUID string (NFR1)."`
	PageSize  int    `json:"page_size" jsonschema:"Optional page size, up to the server-enforced maximum (NFR6); absent or zero applies the default."`
	PageToken string `json:"page_token" jsonschema:"Optional continuation token from a prior page's next_token (NFR6)."`
}

// listOpenNotesOutput mirrors krill/api/handlers/console.go's HTTP response
// for the same query 1:1 (LB7), reusing its exported handlers.OpenNoteWire
// rather than a bespoke MCP-local shape.
type listOpenNotesOutput struct {
	Notes     []handlers.OpenNoteWire `json:"notes"`
	NextToken string                  `json:"next_token,omitempty"`
}

// RegisterListOpenNotes registers list_open_notes (FR12): every note still
// at NoteLifecycleStatusNoted in a scope, across both target shapes a note
// can name, via store.TaskStore.ListOpenNotes, mirroring
// krill/api/handlers/console.go's ListOpenNotesHandler for the same
// capability. Mounted via server.RegisterOpsRead -- PersonaSwarmOperator
// only.
func RegisterListOpenNotes(reg *server.Registry, tasks store.TaskStore) {
	server.RegisterOpsRead(reg, &mcp.Tool{
		Name:        "list_open_notes",
		Description: "Return every note still at 'noted' in a scope -- body, kind, and the identifying context of whichever task or spec-axis entity it targets (FR12). Bounded and continuable (NFR6).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listOpenNotesInput) (*mcp.CallToolResult, listOpenNotesOutput, error) {
		var zero listOpenNotesOutput

		scopeID, err := uuid.Parse(in.ScopeID)
		if err != nil {
			return nil, zero, fmt.Errorf("scope_id: invalid or missing UUID")
		}

		page, err := tasks.ListOpenNotes(ctx, store.ListOpenNotesParams{
			ScopeID: scopeID,
			Page: store.PageParams{
				PageSize:          in.PageSize,
				ContinuationToken: in.PageToken,
			},
		})
		if err != nil {
			return nil, zero, err
		}

		out := listOpenNotesOutput{
			Notes:     make([]handlers.OpenNoteWire, len(page.Items)),
			NextToken: page.NextToken,
		}
		for i, row := range page.Items {
			out.Notes[i] = handlers.ToOpenNoteWire(row)
		}
		return nil, out, nil
	})
}
