// This file (FR d0a63ffb-8e80-47c1-8a64-2729d4950522) is the
// product-wide design-session aggregate read's MCP surface:
// list_product_design_sessions, mirroring
// krill/api/handlers/design_session_summary.go's
// GET /products/{id}/design-sessions for the same capability (LB7) by
// reusing its exported wire type rather than declaring an MCP-local mirror
// of the same data. Registered on the design-session mount alongside the
// other six tools in design.go -- see that file's package doc comment.
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

// listProductDesignSessionsInput is list_product_design_sessions' argument
// schema: the Product whose sessions are read. A read tool, so no krill
// session -- the id is the product, never session-derived (NFR6's gate is
// write-only).
type listProductDesignSessionsInput struct {
	ProductID string `json:"product_id" jsonschema:"The Product surrogate id (LB2) whose design sessions to read, as a UUID string."`
}

// RegisterListProductDesignSessions registers list_product_design_sessions:
// a product's whole design-session aggregate in one call -- every session
// with its derived stage and open blocking/non-blocking question counts,
// newest first, plus the product-level blocking totals. Mounted via
// server.RegisterRead on the design-session mount.
func RegisterListProductDesignSessions(reg *server.Registry, designSessions store.DesignSessionStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name:        "list_product_design_sessions",
		Description: "Return every design session of a product in one read: opening request, opened time, derived stage, and open blocking/non-blocking question counts, newest first, plus the product total of open blocking questions and how many sessions hold one.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listProductDesignSessionsInput) (*mcp.CallToolResult, handlers.ProductDesignSessionsSummaryWire, error) {
		var zero handlers.ProductDesignSessionsSummaryWire

		productID, err := uuid.Parse(in.ProductID)
		if err != nil {
			return nil, zero, fmt.Errorf("product_id: invalid or missing UUID")
		}

		summary, err := designSessions.SummarizeByProduct(ctx, productID)
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.NewProductDesignSessionsSummaryWire(summary), nil
	})
}
