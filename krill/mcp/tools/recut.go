// This file (issue #2687, FR5, C13) is krill's delivery-axis re-cut MCP
// tool group: thin wrappers over store.RecutStore and //krill/slice's
// GetBacklog -- move_delivery_scope (write, Requirement Contributor and
// Agent personas) and get_backlog (read) -- mirroring
// krill/api/handlers/recut.go's HTTP surface for the same capability
// (LB7), via the same handlers.MoveScopeResponse/slice.Backlog shapes
// this package's other write/read tools reuse. Registered from
// RegisterRecutAll (below) onto the design mount (../main.go's
// designReg), same as delivery_shipment.go's tools:
// move_delivery_scope attributes the LB4 subject pair from a resolved
// krill session (krillSessionInput, requireKrillSession -- design.go),
// never from a caller-supplied field.
package tools

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

// moveDeliveryScopeInput is move_delivery_scope's argument schema (FR5):
// the entities to move, and the from/to container ids -- each a
// milestone, a milepebble, or the backlog bucket (any `milestone_ref`
// row).
type moveDeliveryScopeInput struct {
	krillSessionInput
	EntityIDs []string `json:"entity_ids" jsonschema:"The Feature or Requirement surrogate ids to move, each as a UUID string."`
	From      string   `json:"from" jsonschema:"The milestone, milepebble, or backlog-bucket surrogate id entity_ids currently deliver, as a UUID string."`
	To        string   `json:"to" jsonschema:"The milestone, milepebble, or backlog-bucket surrogate id entity_ids should deliver instead, as a UUID string."`
}

// RegisterMoveDeliveryScope registers move_delivery_scope (FR5): re-points
// entity_ids' Delivers association from `from` to `to` via
// store.RecutStore.MoveScope, in one all-or-nothing transaction. Rejects
// (never a silent skip or a partial apply) an entity_id that is not a
// `from` Delivers association (store.ErrEntityNotInContainer), one already
// shipped in `from` (store.ErrEntityShipped, NFR3), or a move into a
// milepebble that would violate the FR3 subset invariant
// (store.ErrMilepebbleDeliversNotSubset).
func RegisterMoveDeliveryScope(reg *server.Registry, sessions store.SessionStore, recut store.RecutStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name:        "move_delivery_scope",
		Description: "Re-cut not-yet-shipped scope: move entities to a different milestone, milepebble, or the backlog bucket (FR5) -- refuses anything already shipped (NFR3), all-or-nothing over the batch.",
	}, []server.Persona{server.PersonaRequirementContributor, server.PersonaAgent, server.PersonaSwarmOperator}, func(ctx context.Context, _ *mcp.CallToolRequest, in moveDeliveryScopeInput) (*mcp.CallToolResult, handlers.MoveScopeResponse, error) {
		var zero handlers.MoveScopeResponse

		sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
		if err != nil {
			return nil, zero, err
		}

		if len(in.EntityIDs) == 0 {
			return nil, zero, fmt.Errorf("entity_ids: at least one entity id is required")
		}
		entityIDs := make([]uuid.UUID, len(in.EntityIDs))
		for i, raw := range in.EntityIDs {
			id, err := uuid.Parse(raw)
			if err != nil {
				return nil, zero, fmt.Errorf("entity_ids: invalid or missing UUID %q", raw)
			}
			entityIDs[i] = id
		}

		from, err := uuid.Parse(in.From)
		if err != nil {
			return nil, zero, fmt.Errorf("from: invalid or missing UUID")
		}
		to, err := uuid.Parse(in.To)
		if err != nil {
			return nil, zero, fmt.Errorf("to: invalid or missing UUID")
		}

		if err := recut.MoveScope(ctx, sess.ScopeID, entityIDs, from, to, sess.Acting, sess.OnBehalfOf); err != nil {
			return nil, zero, err
		}
		return nil, handlers.MoveScopeResponse{EntityIDs: in.EntityIDs, From: in.From, To: in.To}, nil
	})
}

// backlogInput is get_backlog's argument schema: a single product
// surrogate id.
type backlogInput struct {
	ProductID string `json:"product_id" jsonschema:"The product surrogate id whose backlog bucket to read, as a UUID string."`
}

// backlogQuerier is the one method of *slice.Querier this tool calls,
// narrowed to an interface mirroring
// krill/api/handlers/recut.go's own backlogQuerier, so this file's tests
// can supply a fake without a real *store.Store.
type backlogQuerier interface {
	GetBacklog(ctx context.Context, productID uuid.UUID) (slice.Backlog, error)
}

var _ backlogQuerier = (*slice.Querier)(nil)

// backlogOutputSchema is get_backlog's own advertised output schema,
// built the same way as sliceDocumentOutputSchema (slice.go) but over
// slice.Backlog: the response adds the bucket's milestone_ref id to the
// document, so it is no longer the document type that shared schema
// describes.
var backlogOutputSchema = mustSliceOutputSchema[slice.Backlog]("slice.Backlog")

// RegisterGetBacklog registers get_backlog (FR5/FR6): productID's backlog
// bucket via querier.GetBacklog -- the bucket's own milestone_ref id
// alongside its entities, so a caller holding no session can name the
// bucket as move_delivery_scope's `to`. The read creates that bucket on a
// product's first call, so the id is nameable whether or not anything has
// ever been moved into it.
func RegisterGetBacklog(reg *server.Registry, querier backlogQuerier) {
	server.RegisterRead(reg, &mcp.Tool{
		Name:         "get_backlog",
		Description:  "Return a product's backlog bucket (FR5/FR6): its milestone_ref id -- pass it to move_delivery_scope as `to` to retract planned scope -- plus the not-yet-shipped scope currently parked there.",
		OutputSchema: backlogOutputSchema,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in backlogInput) (*mcp.CallToolResult, slice.Backlog, error) {
		var zero slice.Backlog

		productID, err := uuid.Parse(in.ProductID)
		if err != nil {
			return nil, zero, fmt.Errorf("product_id: invalid or missing UUID")
		}

		backlog, err := querier.GetBacklog(ctx, productID)
		if err != nil {
			return nil, zero, err
		}
		return nil, backlog, nil
	})
}

// RegisterRecutAll registers every delivery-axis re-cut tool this
// milestone exposes against reg -- move_delivery_scope (write) and
// get_backlog (read).
func RegisterRecutAll(reg *server.Registry, sessions store.SessionStore, recut store.RecutStore, querier backlogQuerier) {
	RegisterMoveDeliveryScope(reg, sessions, recut)
	RegisterGetBacklog(reg, querier)
}
