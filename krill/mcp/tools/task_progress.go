// This file (FR 59f664ff-3aa9-4d90-a861-d7758ecee040) is the
// product-wide per-container task-progress read's MCP surface:
// get_product_task_progress, mirroring
// krill/api/handlers/task_progress.go's
// GET /products/{id}/task-progress for the same capability (LB7) by
// reusing its exported wire type rather than declaring an MCP-local mirror
// of the same data. Ungated like every other read tool
// (server.RegisterRead) -- the gate is write-only, so no krillSessionInput
// here.
//
// It is the answer to "how far along is this product", for a persona that
// would otherwise have to page list_product_tasks per container and count
// the rows itself -- the same N+1, over MCP, that this read removes from
// the store.
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

// getProductTaskProgressInput is get_product_task_progress' argument
// schema: the Product whose containers are read and which of them to walk.
// The scope vocabulary is the one list_product_tasks already takes, so a
// persona moves between the two reads without relearning it.
type getProductTaskProgressInput struct {
	ProductID string `json:"product_id" jsonschema:"The Product surrogate id (LB2) whose per-container task progress to read, as a UUID string."`
	Scope     string `json:"scope,omitempty" jsonschema:"Which of the product's containers to report: \"incomplete\" (every milestone/milepebble not shipped and not abandoned, the default), \"milestone\", or \"milepebble\"."`
	// ContainerID is required for -- and only meaningful to -- the two
	// single-container scopes.
	ContainerID string `json:"container_id,omitempty" jsonschema:"The milestone_ref id for scope=\"milestone\" or scope=\"milepebble\". Required for those two scopes; ignored for \"incomplete\"."`
}

// RegisterGetProductTaskProgress registers get_product_task_progress: one
// call returning every in-scope milestone and milepebble of a product with
// its task total, per-lane counts, and Done count. Mounted via
// server.RegisterRead on the work mount.
func RegisterGetProductTaskProgress(reg *server.Registry, tasks store.TaskStore, products store.ProductStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name:        "get_product_task_progress",
		Description: "Return a product's per-container task progress in one read: for every milestone and milepebble in scope, the total task count, the count in each lane (Scaffold, Implementation, Testing, Validation, Done), how many are Done, and how many are cancelled. A milestone's counts include its milepebbles' tasks; a container with no tasks reports total 0 with has_tasks false. Cancelled tasks count in the total and in the lane they were left in, never in Done.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getProductTaskProgressInput) (*mcp.CallToolResult, handlers.ProductTaskProgressWire, error) {
		var zero handlers.ProductTaskProgressWire

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

		// The store scopes every row by scope_id, resolved from the
		// product row itself rather than from a session -- the same LB2
		// parentage the HTTP surface's handler resolves.
		product, err := products.GetCurrentByID(ctx, productID)
		if err != nil {
			return nil, zero, err
		}

		progress, err := tasks.SummarizeProductTaskProgress(ctx, store.ProductTaskProgressParams{
			ScopeID:   product.ScopeID,
			ProductID: productID,
			Scope:     scope,
		})
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.NewProductTaskProgressWire(progress), nil
	})
}
