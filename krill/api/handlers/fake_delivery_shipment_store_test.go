// fakeDeliveryShipmentStore/fakeDeliveryBreakdownQuerier back
// delivery_shipment_test.go: MarkShippedHandler/GetDeliveryBreakdownHandler
// (delivery_shipment.go, issue #2686) depend only on store.DeliveryShipmentStore
// and the package's own unexported deliveryBreakdownQuerier interface, so
// handler-level tests never need a real Postgres or //krill/slice (that is
// krill/store/delivery_shipment_integration_test.go's and
// krill/slice/delivery_breakdown_integration_test.go's job).
package handlers_test

import (
	"context"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

// fakeDeliveryShipmentStore records every MarkShipped call it sees (so a
// test can assert the write gate reached, or never reached, the store) and
// can be told to fail with a fixed error.
type fakeDeliveryShipmentStore struct {
	markShippedErr error

	markShippedCalled bool
	gotScopeID        uuid.UUID
	gotMilestoneID    uuid.UUID
	gotEntityID       uuid.UUID
	gotNote           *string
	gotActing         store.Subject
	gotOnBehalfOf     store.Subject
}

func (f *fakeDeliveryShipmentStore) MarkShipped(ctx context.Context, scopeID, milestoneID, entityID uuid.UUID, note *string, acting, onBehalfOf store.Subject) error {
	f.markShippedCalled = true
	f.gotScopeID, f.gotMilestoneID, f.gotEntityID, f.gotNote = scopeID, milestoneID, entityID, note
	f.gotActing, f.gotOnBehalfOf = acting, onBehalfOf
	return f.markShippedErr
}

func (f *fakeDeliveryShipmentStore) ShippedEntityIDs(ctx context.Context, milestoneID uuid.UUID) (map[uuid.UUID]bool, error) {
	return nil, nil
}

func (f *fakeDeliveryShipmentStore) DeliveryBreakdown(ctx context.Context, milestoneID uuid.UUID) ([]uuid.UUID, []uuid.UUID, error) {
	return nil, nil, nil
}

var _ store.DeliveryShipmentStore = (*fakeDeliveryShipmentStore)(nil)

// fakeDeliveryBreakdownQuerier stands in for //krill/slice's
// GetDeliveryBreakdown (deliveryBreakdownQuerier, delivery_shipment.go's
// unexported narrowing of *slice.Querier), so
// TestGetDeliveryBreakdownHandler_* never needs a real store/querier pair.
type fakeDeliveryBreakdownQuerier struct {
	shipped, unshipped slice.Document
	err                error
}

func (f *fakeDeliveryBreakdownQuerier) GetDeliveryBreakdown(ctx context.Context, milestoneID uuid.UUID) (slice.Document, slice.Document, error) {
	return f.shipped, f.unshipped, f.err
}
