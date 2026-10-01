// This file (FR c4ab6c68) is the console count MCP surface: the twins of
// krill/api/handlers/console_counts.go's count endpoints, mounted on the
// same operator surface as the list tools they count (PersonaSwarmOperator
// only, enforced by the mount -- see console.go's own doc comment).
//
// Each count tool reuses its HTTP twin's exported wire types
// (handlers.ConsoleCountWire, handlers.ConsoleOverviewWire) rather than
// declaring a local shape, so the two surfaces cannot drift (LB7), and
// each takes the same narrowing its list tool takes -- the criterion is
// that a count and the list beside it are asked the same question, so a
// count that could not be narrowed would answer a question nobody asked.
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

// consoleCountInput is every per-queue count_* tool's argument schema: the
// scope_id its list tool takes (required -- a console query has no single
// entity to resolve scope from), the same optional product_id and
// milestone_id narrowing all four list tools take, and the page_size and
// page_token parameters they accept. A count describes the whole filtered
// set, so a caller supplying the paging pair has them ignored rather than
// answered as a page length.
type consoleCountInput struct {
	ScopeID     string `json:"scope_id" jsonschema:"The scope to count, as a UUID string (NFR1)."`
	ProductID   string `json:"product_id,omitempty" jsonschema:"Optional product to narrow to, exactly as the matching list tool takes it; absent returns every product's rows in the scope."`
	MilestoneID string `json:"milestone_id,omitempty" jsonschema:"Optional milestone or milepebble to narrow to, exactly as the matching list tool takes it; a milestone includes its milepebbles' rows."`
	PageSize    int    `json:"page_size,omitempty" jsonschema:"Accepted for parity with the matching list tool and ignored: a count is of the whole filtered set, never of one page."`
	PageToken   string `json:"page_token,omitempty" jsonschema:"Accepted for parity with the matching list tool and ignored: a count is of the whole filtered set, never of one page."`
}

// consoleEscalatedCountInput is consoleCountInput plus the reason
// narrowing list_escalated_tasks also takes, so count_escalated_tasks is
// asked exactly the question its list is.
type consoleEscalatedCountInput struct {
	ScopeID     string `json:"scope_id" jsonschema:"The scope to count, as a UUID string (NFR1)."`
	ProductID   string `json:"product_id,omitempty" jsonschema:"Optional product to narrow to, exactly as list_escalated_tasks takes it."`
	MilestoneID string `json:"milestone_id,omitempty" jsonschema:"Optional milestone or milepebble to narrow to, exactly as list_escalated_tasks takes it."`
	Reason      string `json:"reason,omitempty" jsonschema:"Optional escalation reason to narrow to: thrash-cap, attempt-cap or manual; absent returns every reason."`
	PageSize    int    `json:"page_size,omitempty" jsonschema:"Accepted for parity with the matching list tool and ignored: a count is of the whole filtered set, never of one page."`
	PageToken   string `json:"page_token,omitempty" jsonschema:"Accepted for parity with the matching list tool and ignored: a count is of the whole filtered set, never of one page."`
}

// parseConsoleCountScopeAndFilter is every per-queue count tool's one
// validation step: the scope parse and the ConsoleFilter parse the list
// tools above perform, so a narrowing a list accepts is a narrowing a
// count accepts, and a malformed one is a tool error rather than a
// silently widened count.
func parseConsoleCountScopeAndFilter(in consoleCountInput) (uuid.UUID, store.ConsoleFilter, error) {
	scopeID, err := uuid.Parse(in.ScopeID)
	if err != nil {
		return uuid.Nil, store.ConsoleFilter{}, fmt.Errorf("scope_id: invalid or missing UUID")
	}
	filter, err := parseConsoleFilter(in.ProductID, in.MilestoneID)
	if err != nil {
		return uuid.Nil, store.ConsoleFilter{}, err
	}
	return scopeID, filter, nil
}

// RegisterCountClaimedTasks registers count_claimed_tasks (FR c4ab6c68):
// how many rows list_claimed_tasks would return unpaged for the same
// scope and narrowing. Mirrors GET /console/claimed/count (LB7).
func RegisterCountClaimedTasks(reg *server.Registry, tasks store.TaskStore) {
	server.RegisterOpsRead(reg, &mcp.Tool{
		Name:        "count_claimed_tasks",
		Description: "Return how many tasks are currently claimed in a scope -- the full size of the claimed queue, never a page's length. Takes the same scope_id, product_id and milestone_id as list_claimed_tasks (FR c4ab6c68).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in consoleCountInput) (*mcp.CallToolResult, handlers.ConsoleCountWire, error) {
		scopeID, filter, err := parseConsoleCountScopeAndFilter(in)
		if err != nil {
			return nil, handlers.ConsoleCountWire{}, err
		}
		count, err := tasks.CountClaimedTasks(ctx, store.ListClaimedTasksParams{
			ScopeID:       scopeID,
			ConsoleFilter: filter,
		})
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
		Description: "Return how many tasks are cancelled in a scope -- the full size of the cancelled queue, never a page's length. Takes the same scope_id, product_id and milestone_id as list_cancelled_tasks (FR c4ab6c68).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in consoleCountInput) (*mcp.CallToolResult, handlers.ConsoleCountWire, error) {
		scopeID, filter, err := parseConsoleCountScopeAndFilter(in)
		if err != nil {
			return nil, handlers.ConsoleCountWire{}, err
		}
		count, err := tasks.CountCancelledTasks(ctx, store.ListCancelledTasksParams{
			ScopeID:       scopeID,
			ConsoleFilter: filter,
		})
		if err != nil {
			return nil, handlers.ConsoleCountWire{}, err
		}
		return nil, handlers.ConsoleCountWire{Count: count}, nil
	})
}

// RegisterCountEscalatedTasks registers count_escalated_tasks (FR
// c4ab6c68) -- GET /console/escalated/count's twin, taking the reason
// narrowing alongside the product/milestone one so it is asked the same
// question list_escalated_tasks is.
func RegisterCountEscalatedTasks(reg *server.Registry, tasks store.TaskStore) {
	server.RegisterOpsRead(reg, &mcp.Tool{
		Name:        "count_escalated_tasks",
		Description: "Return how many tasks are escalated in a scope -- the full size of the escalation queue, never a page's length. Takes the same scope_id, product_id, milestone_id and reason as list_escalated_tasks (FR c4ab6c68).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in consoleEscalatedCountInput) (*mcp.CallToolResult, handlers.ConsoleCountWire, error) {
		scopeID, filter, err := parseConsoleCountScopeAndFilter(consoleCountInput{
			ScopeID:     in.ScopeID,
			ProductID:   in.ProductID,
			MilestoneID: in.MilestoneID,
		})
		if err != nil {
			return nil, handlers.ConsoleCountWire{}, err
		}
		var reason *store.EscalationReason
		if in.Reason != "" {
			parsed := store.EscalationReason(in.Reason)
			reason = &parsed
		}
		count, err := tasks.CountEscalatedTasks(ctx, store.ListEscalatedTasksParams{
			ScopeID:       scopeID,
			ConsoleFilter: filter,
			Reason:        reason,
		})
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
		Description: "Return how many notes are still open in a scope -- the full size of the open-notes queue, never a page's length. Takes the same scope_id, product_id and milestone_id as list_open_notes (FR c4ab6c68). Per-product figures do not sum to the scope-wide one.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in consoleCountInput) (*mcp.CallToolResult, handlers.ConsoleCountWire, error) {
		scopeID, filter, err := parseConsoleCountScopeAndFilter(in)
		if err != nil {
			return nil, handlers.ConsoleCountWire{}, err
		}
		count, err := tasks.CountOpenNotes(ctx, store.ListOpenNotesParams{
			ScopeID:       scopeID,
			ConsoleFilter: filter,
		})
		if err != nil {
			return nil, handlers.ConsoleCountWire{}, err
		}
		return nil, handlers.ConsoleCountWire{Count: count}, nil
	})
}

// consoleOverviewInput is console_overview_counts' argument schema --
// scope_id plus the same narrowing every queue count above takes, applied
// to all four queues at once exactly as GET /console/overview applies one
// query string to all four.
type consoleOverviewInput struct {
	ScopeID     string `json:"scope_id" jsonschema:"The scope to count, as a UUID string (NFR1)."`
	ProductID   string `json:"product_id,omitempty" jsonschema:"Optional product to narrow every queue to, exactly as the per-queue list tools take it."`
	MilestoneID string `json:"milestone_id,omitempty" jsonschema:"Optional milestone or milepebble to narrow every queue to, exactly as the per-queue list tools take it."`
	Reason      string `json:"reason,omitempty" jsonschema:"Optional escalation reason, narrowing the escalated queue and its recent figure exactly as list_escalated_tasks takes it."`
}

// RegisterConsoleOverviewCounts registers console_overview_counts (FR
// c4ab6c68): the four queue sizes and the three Overview sub-line figures
// in one call -- GET /console/overview's twin (LB7). Any figure the store
// cannot count fails the call rather than reporting zero.
func RegisterConsoleOverviewCounts(reg *server.Registry, tasks store.TaskStore) {
	server.RegisterOpsRead(reg, &mcp.Tool{
		Name:        "console_overview_counts",
		Description: "Return every console Overview figure in one call: the escalated, claimed, cancelled and open-notes queue sizes, plus the counts escalated recently, claims expiring soon, and open scope-notes (FR c4ab6c68). Takes the same scope_id, product_id, milestone_id and reason the per-queue tools take. Any figure the store cannot count fails the call rather than reporting zero.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in consoleOverviewInput) (*mcp.CallToolResult, handlers.ConsoleOverviewWire, error) {
		scopeID, filter, err := parseConsoleCountScopeAndFilter(consoleCountInput{
			ScopeID:     in.ScopeID,
			ProductID:   in.ProductID,
			MilestoneID: in.MilestoneID,
		})
		if err != nil {
			return nil, handlers.ConsoleOverviewWire{}, err
		}
		var reason *store.EscalationReason
		if in.Reason != "" {
			parsed := store.EscalationReason(in.Reason)
			reason = &parsed
		}
		counts, err := tasks.CountConsoleOverview(ctx, store.ConsoleOverviewParams{
			Escalated: store.ListEscalatedTasksParams{ScopeID: scopeID, ConsoleFilter: filter, Reason: reason},
			Claimed:   store.ListClaimedTasksParams{ScopeID: scopeID, ConsoleFilter: filter},
			Cancelled: store.ListCancelledTasksParams{ScopeID: scopeID, ConsoleFilter: filter},
			Notes:     store.ListOpenNotesParams{ScopeID: scopeID, ConsoleFilter: filter},
		})
		if err != nil {
			return nil, handlers.ConsoleOverviewWire{}, err
		}
		return nil, handlers.ToConsoleOverviewWire(counts), nil
	})
}
