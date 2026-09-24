// This file (issue #2686, FR10) is krill's per-delivered-item shipment
// MCP tool group: thin wrappers over store.DeliveryShipmentStore and
// //krill/slice's GetDeliveryBreakdown -- mark_delivered_item_shipped
// (write, Requirement Contributor and Agent personas) and
// get_delivery_breakdown (read) -- mirroring
// krill/api/handlers/delivery_shipment.go's HTTP surface for the same
// capability (LB7), via the same handlers.IDResponse/
// handlers.DeliveryBreakdownResponse shapes this package's other
// write/read tools reuse. Registered from RegisterDeliveryShipmentAll
// (below) onto the design mount (../main.go's designReg), same as
// milestone_status.go's tools: mark_delivered_item_shipped attributes the
// LB4 subject pair from a resolved krill session (krillSessionInput,
// requireKrillSession -- design.go), never from a caller-supplied field.
package tools

import (
	"context"
	"fmt"
	"reflect"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

// markDeliveredItemShippedInput is mark_delivered_item_shipped's argument
// schema (FR10). Note is optional.
type markDeliveredItemShippedInput struct {
	krillSessionInput
	MilestoneID string  `json:"milestone_id" jsonschema:"The milestone or milepebble surrogate id, as a UUID string."`
	EntityID    string  `json:"entity_id" jsonschema:"The Feature or Requirement surrogate id shipping as part of milestone_id's delivered scope, as a UUID string."`
	Note        *string `json:"note" jsonschema:"Optional free-text note attached to this shipment record."`
}

// RegisterMarkDeliveredItemShipped registers mark_delivered_item_shipped
// (FR10): appends one delivery_shipment row via
// store.DeliveryShipmentStore.MarkShipped. Rejects an entity_id that is
// not one of milestone_id's `delivers` associations
// (store.ErrEntityNotDelivered) rather than silently accepting it.
// Idempotent only at the read level -- a second call for the same pair
// still succeeds, appending a second row (NFR2/NFR3).
func RegisterMarkDeliveredItemShipped(reg *server.Registry, sessions store.SessionStore, shipments store.DeliveryShipmentStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name:        "mark_delivered_item_shipped",
		Description: "Record that a Feature or Requirement shipped as part of a milestone or milepebble's delivered scope (FR10) -- appended to history, never an overwrite (NFR2/NFR3).",
	}, []server.Persona{server.PersonaRequirementContributor, server.PersonaAgent, server.PersonaSwarmOperator}, func(ctx context.Context, _ *mcp.CallToolRequest, in markDeliveredItemShippedInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		var zero handlers.IDResponse

		sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
		if err != nil {
			return nil, zero, err
		}

		milestoneID, err := uuid.Parse(in.MilestoneID)
		if err != nil {
			return nil, zero, fmt.Errorf("milestone_id: invalid or missing UUID")
		}
		entityID, err := uuid.Parse(in.EntityID)
		if err != nil {
			return nil, zero, fmt.Errorf("entity_id: invalid or missing UUID")
		}

		if err := shipments.MarkShipped(ctx, sess.ScopeID, milestoneID, entityID, in.Note, sess.Acting, sess.OnBehalfOf); err != nil {
			return nil, zero, err
		}
		return nil, handlers.IDResponse{ID: milestoneID.String()}, nil
	})
}

// deliveryBreakdownInput is get_delivery_breakdown's argument schema: a
// single milestone or milepebble surrogate id.
type deliveryBreakdownInput struct {
	MilestoneID string `json:"milestone_id" jsonschema:"The milestone or milepebble surrogate id, as a UUID string."`
}

// deliveryBreakdownOutputSchema is get_delivery_breakdown's advertised
// output schema for handlers.DeliveryBreakdownResponse, computed once.
// Required for the same reason slice.go's sliceDocumentOutputSchema is
// (see its own doc comment): handlers.DeliveryBreakdownResponse embeds
// two slice.Document values, and jsonschema-go's default reflection over
// uuid.UUID (an [16]byte array) does not match encoding/json's real
// string marshaling of it, so every call would otherwise fail its own
// output-schema validation.
var deliveryBreakdownOutputSchema = mustDeliveryBreakdownOutputSchema()

func mustDeliveryBreakdownOutputSchema() *jsonschema.Schema {
	s, err := jsonschema.For[handlers.DeliveryBreakdownResponse](&jsonschema.ForOptions{
		TypeSchemas: map[reflect.Type]*jsonschema.Schema{
			reflect.TypeFor[uuid.UUID](): {Type: "string"},
		},
	})
	if err != nil {
		panic(fmt.Errorf("krill/mcp/tools: building DeliveryBreakdownResponse output schema: %w", err))
	}
	return s
}

// deliveryBreakdownQuerier is the one method of *slice.Querier this tool
// calls, narrowed to an interface mirroring
// krill/api/handlers/delivery_shipment.go's own deliveryBreakdownQuerier,
// so this file's tests can supply a fake without a real *store.Store.
type deliveryBreakdownQuerier interface {
	GetDeliveryBreakdown(ctx context.Context, milestoneID uuid.UUID) (shipped, unshipped slice.Document, err error)
}

var _ deliveryBreakdownQuerier = (*slice.Querier)(nil)

// RegisterGetDeliveryBreakdown registers get_delivery_breakdown (FR10):
// combines a milestone or milepebble's current status
// (store.MilestoneStatusEventStore.CurrentStatus) with its per-item
// shipped/unshipped split (querier), mirroring
// GetDeliveryBreakdownHandler's own combination via the same
// handlers.DeliveryBreakdownResponse shape (LB7). shipped/unshipped are
// two sets of the same typed entities every other read tool in this
// package returns, never a bare id list.
func RegisterGetDeliveryBreakdown(reg *server.Registry, statuses store.MilestoneStatusEventStore, querier deliveryBreakdownQuerier) {
	server.RegisterRead(reg, &mcp.Tool{
		Name:         "get_delivery_breakdown",
		Description:  "Return a milestone or milepebble's current status alongside its per-item shipped/not-shipped breakdown (FR10) -- the 'partially complete' status made concrete.",
		OutputSchema: deliveryBreakdownOutputSchema,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in deliveryBreakdownInput) (*mcp.CallToolResult, handlers.DeliveryBreakdownResponse, error) {
		var zero handlers.DeliveryBreakdownResponse

		milestoneID, err := uuid.Parse(in.MilestoneID)
		if err != nil {
			return nil, zero, fmt.Errorf("milestone_id: invalid or missing UUID")
		}

		status, err := statuses.CurrentStatus(ctx, milestoneID)
		if err != nil {
			return nil, zero, err
		}

		shipped, unshipped, err := querier.GetDeliveryBreakdown(ctx, milestoneID)
		if err != nil {
			return nil, zero, err
		}

		return nil, handlers.DeliveryBreakdownResponse{
			Status:    string(status),
			Shipped:   shipped,
			Unshipped: unshipped,
		}, nil
	})
}

// RegisterDeliveryShipmentAll registers every per-delivered-item shipment
// tool this milestone exposes against reg --
// mark_delivered_item_shipped (write) and get_delivery_breakdown (read).
func RegisterDeliveryShipmentAll(reg *server.Registry, sessions store.SessionStore, shipments store.DeliveryShipmentStore, statuses store.MilestoneStatusEventStore, querier deliveryBreakdownQuerier) {
	RegisterMarkDeliveredItemShipped(reg, sessions, shipments)
	RegisterGetDeliveryBreakdown(reg, statuses, querier)
}
