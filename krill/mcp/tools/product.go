// This file (issue #2941) is krill's Product discovery MCP tool:
// list_products, a thin wrapper over store.ProductStore.ListCurrentByScope,
// mirroring krill/api/handlers/product.go's ListProductsHandler for the
// same capability (LB7). It is the entry point for finding the Product id
// every get_*_slice read requires. Ungated like every other read tool
// (server.RegisterRead) -- NFR6's gate is write-only, so no
// krillSessionInput here.
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

// listProductsInput is list_products' argument schema: the scope to list
// Products in. A Product has no parent entity, so scope_id is explicit.
type listProductsInput struct {
	ScopeID string `json:"scope_id" jsonschema:"The scope to list current Products in, as a UUID string."`
}

// RegisterListProducts registers list_products: every current Product in
// a scope, ordered by position then name, returning the same
// handlers.ListProductsResponse GET /products does (LB7).
func RegisterListProducts(reg *server.Registry, products store.ProductStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name:        "list_products",
		Description: "List every current Product in a scope (id, name, vision), ordered by position then name. Use it to discover the product_id the get_*_slice tools require.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listProductsInput) (*mcp.CallToolResult, handlers.ListProductsResponse, error) {
		var zero handlers.ListProductsResponse

		scopeID, err := uuid.Parse(in.ScopeID)
		if err != nil {
			return nil, zero, fmt.Errorf("scope_id: invalid or missing UUID")
		}

		list, err := products.ListCurrentByScope(ctx, scopeID)
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.NewListProductsResponse(list), nil
	})
}
