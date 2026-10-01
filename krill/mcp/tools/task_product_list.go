// This file (FR cfcd1104-b015-4ffd-a07a-abb7c6e435d3) is the
// product-wide paged task read's MCP surface: list_product_tasks,
// mirroring krill/api/handlers/task_product_list.go's
// GET /products/{id}/tasks for the same capability (LB7) by reusing its
// exported wire type rather than declaring an MCP-local mirror of the same
// data. Ungated like every other read tool (server.RegisterRead) -- NFR6's
// gate is write-only, so no krillSessionInput here.
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

// listProductTasksInput is list_product_tasks' argument schema: the
// Product whose tasks are read, which of its delivery containers to walk,
// and the optional filters and paging pair.
type listProductTasksInput struct {
	ProductID string `json:"product_id" jsonschema:"The Product surrogate id (LB2) whose tasks to read, as a UUID string."`
	Scope     string `json:"scope,omitempty" jsonschema:"Which of the product's containers to walk: \"incomplete\" (every milestone/milepebble not shipped and not abandoned, the default), \"milestone\", or \"milepebble\"."`
	// ContainerID is required for -- and only meaningful to -- the two
	// single-container scopes.
	ContainerID string `json:"container_id,omitempty" jsonschema:"The milestone_ref id for scope=\"milestone\" or scope=\"milepebble\". Required for those two scopes; ignored for \"incomplete\"."`
	Lane        string `json:"lane,omitempty" jsonschema:"Keep only tasks in this current lane (Scaffold, Implementation, Testing, Validation, Done). Omit for every lane."`
	OnlyStuck   bool   `json:"only_stuck,omitempty" jsonschema:"Keep only stuck tasks: expired lease, at the attempt cap, escalated, or cancelled."`
	PageSize    int    `json:"page_size,omitempty" jsonschema:"Rows per page. Omit for the server default; clamped to the server maximum."`
	PageToken   string `json:"page_token,omitempty" jsonschema:"Continuation token from a prior page's next_token. Omit for the first page; a token issued under different filters is refused."`
}

// RegisterListProductTasks registers list_product_tasks: one paged read of
// a product's tasks over its incomplete milestones, one milestone, or one
// milepebble, each row naming its milestone (and milepebble when cut),
// lane, state, attempts/cap, lease expiry and current claim id. Use
// get_task on a returned id for the full payload, and list_tasks for the
// unpaginated per-container read.
func RegisterListProductTasks(reg *server.Registry, tasks store.TaskStore, products store.ProductStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name:        "list_product_tasks",
		Description: "List a product's tasks as one paged list: scope selects every incomplete milestone (default), one milestone (with its milepebbles' tasks), or one milepebble; optional lane and only_stuck filters. Each row carries its milestone (id, name, derived status), milepebble when cut, lane, state with escalation reason, attempts and cap, lease expiry and the current claim id. Returns next_token when more rows remain.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listProductTasksInput) (*mcp.CallToolResult, handlers.ListProductTasksResponse, error) {
		var zero handlers.ListProductTasksResponse

		productID, err := uuid.Parse(in.ProductID)
		if err != nil {
			return nil, zero, fmt.Errorf("product_id: invalid or missing UUID")
		}

		scope := store.ProductTaskScope{Kind: store.ProductTaskScopeIncomplete}
		if in.Scope != "" {
			scope.Kind = store.ProductTaskScopeKind(in.Scope)
		}
		if !validProductTaskScopeKind(scope.Kind) {
			return nil, zero, fmt.Errorf("scope: must be one of incomplete, milestone, milepebble")
		}
		if scope.RequiresContainer() {
			scope.ContainerID, err = uuid.Parse(in.ContainerID)
			if err != nil {
				return nil, zero, fmt.Errorf("container_id: required for scope %q; invalid or missing UUID", in.Scope)
			}
		}

		var lane *store.Lane
		if in.Lane != "" {
			var parsed store.Lane
			for _, candidate := range store.CanonicalLaneOrder {
				if string(candidate) == in.Lane {
					parsed = candidate
					break
				}
			}
			if parsed == "" {
				return nil, zero, fmt.Errorf("lane: must be one of Scaffold, Implementation, Testing, Validation, Done")
			}
			lane = &parsed
		}

		// The store scopes every row by scope_id, which is resolved from
		// the product row itself rather than from a session -- the same
		// LB2 parentage the HTTP surface's handler resolves.
		product, err := products.GetCurrentByID(ctx, productID)
		if err != nil {
			return nil, zero, err
		}

		page, err := tasks.ListProductTasks(ctx, store.ListProductTasksParams{
			ScopeID:   product.ScopeID,
			ProductID: productID,
			Scope:     scope,
			Lane:      lane,
			OnlyStuck: in.OnlyStuck,
			Page: store.PageParams{
				PageSize:          in.PageSize,
				ContinuationToken: in.PageToken,
			},
		})
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.NewListProductTasksResponse(page), nil
	})
}

func validProductTaskScopeKind(kind store.ProductTaskScopeKind) bool {
	for _, k := range store.ValidProductTaskScopeKinds {
		if k == kind {
			return true
		}
	}
	return false
}
