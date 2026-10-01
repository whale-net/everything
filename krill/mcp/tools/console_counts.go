// This file (FR c4ab6c68) is the console count MCP surface: the twins of
// krill/api/handlers/console_counts.go's count endpoints, mounted on the
// same operator surface as the list tools they count (PersonaSwarmOperator
// only, enforced by the mount -- see console.go's own doc comment).
//
// Each count tool reuses its HTTP twin's exported wire types
// (handlers.ConsoleCountWire, handlers.ConsoleOverviewWire) rather than
// declaring a local shape, so the two surfaces cannot drift (LB7).
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

// consoleCountInput is every count_* tool's argument schema: the same
// scope_id the matching list tool takes (required -- a console query has
// no single entity to resolve scope from), plus the page_size and
// page_token parameters the list tools accept. A count describes the whole
// filtered set, so a caller supplying those has them ignored rather than
// answered as a page length.
type consoleCountInput struct {
	ScopeID   string `json:"scope_id" jsonschema:"The scope to count, as a UUID string (NFR1)."`
	PageSize  int    `json:"page_size,omitempty" jsonschema:"Accepted for parity with the matching list tool and ignored: a count is of the whole filtered set, never of one page."`
	PageToken string `json:"page_token,omitempty" jsonschema:"Accepted for parity with the matching list tool and ignored: a count is of the whole filtered set, never of one page."`
}

// parseConsoleCountScopeID is consoleCountInput's one validation step --
// the same parse the list tools above it do, so a scope a list accepts is
// a scope a count accepts.
func parseConsoleCountScopeID(in consoleCountInput) (uuid.UUID, error) {
	scopeID, err := uuid.Parse(in.ScopeID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("scope_id: invalid or missing UUID")
	}
	return scopeID, nil
}

// RegisterCountClaimedTasks registers count_claimed_tasks (FR c4ab6c68):
// how many rows list_claimed_tasks would return unpaged for the same
// scope. Mirrors GET /console/claimed/count (LB7).
func RegisterCountClaimedTasks(reg *server.Registry, tasks store.TaskStore) {
	server.RegisterOpsRead(reg, &mcp.Tool{
		Name:        "count_claimed_tasks",
		Description: "Return how many tasks are currently claimed in a scope -- the full size of the claimed queue, never a page's length. Takes the same scope_id as list_claimed_tasks (FR c4ab6c68).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in consoleCountInput) (*mcp.CallToolResult, handlers.ConsoleCountWire, error) {
		scopeID, err := parseConsoleCountScopeID(in)
		if err != nil {
			return nil, handlers.ConsoleCountWire{}, err
		}
		count, err := tasks.CountClaimedTasks(ctx, store.ListClaimedTasksParams{ScopeID: scopeID})
		if err != nil {
			return nil, handlers.ConsoleCountWire{}, err
		}
		return nil, handlers.ConsoleCountWire{Count: count}, nil
	})
}

// RegisterCountCancelledTasks registers count_cancelled_tasks (FR
// c4ab6c68) -- GET /console/cancelled/count's twin.
func RegisterCountCancelledTasks(reg *server.Registry, tasks store.TaskStore) {
	server.RegisterOpsRead(reg, &mcp.Tool{
		Name:        "count_cancelled_tasks",
		Description: "Return how many tasks are cancelled in a scope -- the full size of the cancelled queue, never a page's length. Takes the same scope_id as list_cancelled_tasks (FR c4ab6c68).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in consoleCountInput) (*mcp.CallToolResult, handlers.ConsoleCountWire, error) {
		scopeID, err := parseConsoleCountScopeID(in)
		if err != nil {
			return nil, handlers.ConsoleCountWire{}, err
		}
		count, err := tasks.CountCancelledTasks(ctx, store.ListCancelledTasksParams{ScopeID: scopeID})
		if err != nil {
			return nil, handlers.ConsoleCountWire{}, err
		}
		return nil, handlers.ConsoleCountWire{Count: count}, nil
	})
}

// RegisterCountEscalatedTasks registers count_escalated_tasks (FR
// c4ab6c68) -- GET /console/escalated/count's twin.
func RegisterCountEscalatedTasks(reg *server.Registry, tasks store.TaskStore) {
	server.RegisterOpsRead(reg, &mcp.Tool{
		Name:        "count_escalated_tasks",
		Description: "Return how many tasks are escalated in a scope -- the full size of the escalation queue, never a page's length. Takes the same scope_id as list_escalated_tasks (FR c4ab6c68).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in consoleCountInput) (*mcp.CallToolResult, handlers.ConsoleCountWire, error) {
		scopeID, err := parseConsoleCountScopeID(in)
		if err != nil {
			return nil, handlers.ConsoleCountWire{}, err
		}
		count, err := tasks.CountEscalatedTasks(ctx, store.ListEscalatedTasksParams{ScopeID: scopeID})
		if err != nil {
			return nil, handlers.ConsoleCountWire{}, err
		}
		return nil, handlers.ConsoleCountWire{Count: count}, nil
	})
}

// RegisterCountOpenNotes registers count_open_notes (FR c4ab6c68) -- GET
// /console/notes/count's twin. Per-product figures are not additive into
// a scope-wide total; see store.CountOpenNotes' own doc comment.
func RegisterCountOpenNotes(reg *server.Registry, tasks store.TaskStore) {
	server.RegisterOpsRead(reg, &mcp.Tool{
		Name:        "count_open_notes",
		Description: "Return how many notes are still open in a scope -- the full size of the open-notes queue, never a page's length. Takes the same scope_id as list_open_notes (FR c4ab6c68). Per-product figures do not sum to the scope-wide one.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in consoleCountInput) (*mcp.CallToolResult, handlers.ConsoleCountWire, error) {
		scopeID, err := parseConsoleCountScopeID(in)
		if err != nil {
			return nil, handlers.ConsoleCountWire{}, err
		}
		count, err := tasks.CountOpenNotes(ctx, store.ListOpenNotesParams{ScopeID: scopeID})
		if err != nil {
			return nil, handlers.ConsoleCountWire{}, err
		}
		return nil, handlers.ConsoleCountWire{Count: count}, nil
	})
}

// consoleOverviewInput is console_overview_counts' argument schema --
// scope_id, exactly as the four count tools above.
type consoleOverviewInput struct {
	ScopeID string `json:"scope_id" jsonschema:"The scope to count, as a UUID string (NFR1)."`
}

// RegisterConsoleOverviewCounts registers console_overview_counts (FR
// c4ab6c68): the four queue sizes and the three Overview sub-line figures
// in one call -- GET /console/overview's twin (LB7).
func RegisterConsoleOverviewCounts(reg *server.Registry, tasks store.TaskStore) {
	server.RegisterOpsRead(reg, &mcp.Tool{
		Name:        "console_overview_counts",
		Description: "Return every console Overview figure in one call: the escalated, claimed, cancelled and open-notes queue sizes, plus the counts escalated recently, claims expiring soon, and open scope-notes (FR c4ab6c68). Any figure the store cannot count fails the call rather than reporting zero.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in consoleOverviewInput) (*mcp.CallToolResult, handlers.ConsoleOverviewWire, error) {
		scopeID, err := uuid.Parse(in.ScopeID)
		if err != nil {
			return nil, handlers.ConsoleOverviewWire{}, fmt.Errorf("scope_id: invalid or missing UUID")
		}
		counts, err := tasks.CountConsoleOverview(ctx, store.ConsoleOverviewParams{
			Escalated: store.ListEscalatedTasksParams{ScopeID: scopeID},
			Claimed:   store.ListClaimedTasksParams{ScopeID: scopeID},
			Cancelled: store.ListCancelledTasksParams{ScopeID: scopeID},
			Notes:     store.ListOpenNotesParams{ScopeID: scopeID},
		})
		if err != nil {
			return nil, handlers.ConsoleOverviewWire{}, err
		}
		return nil, handlers.ToConsoleOverviewWire(counts), nil
	})
}
