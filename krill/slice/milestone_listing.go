// This file (issue #2689, FR11, C28) is krill's product-wide delivery
// listing query: every milestone and milepebble under a Product, its
// current status (#2685's derived, batched MilestoneStatusEventStore.
// CurrentStatuses), and its delivery-axis relations (#2683's Delivers/
// Must-not-foreclose, #2684's milepebbles, #2686's shipped/unshipped
// breakdown) -- one call answering "what is planned versus what is merely
// spec'd" for a whole product, the read the `Agent` persona (root plan
// issue #2681 -- Personas) actually calls.
//
// A projection over //krill/slice's existing typed Document (LB7), not a
// second, hand-rolled entity shape: MilestoneListingEntry.Delivers/
// MustNotForeclose/MilepebbleListingEntry.Delivers are all Document
// values, assembled the same GetEntitySetSlice way GetDeliveryBreakdown
// (query.go, issue #2686) already does. Only the milestone/milepebble
// container fields themselves (id, name, outcome, FR budget, status,
// deferrals) are new -- store.MilestoneRef/store.MilestoneDeferral have
// no Document-shaped equivalent, since a milestone_ref row is not one of
// the four M1 spec-entity kinds Document's fields cover.
package slice

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// MilepebbleListingEntry is one milepebble's row in a
// MilestoneListingEntry.Milepebbles list (issue #2684's kind='milepebble'
// milestone_ref rows). A milepebble carries no Must-not-foreclose
// associations or deferrals of its own (FR3) -- only a Delivers subset of
// its parent milestone's own Delivers set, so unlike
// MilestoneListingEntry this type has no MustNotForeclose or Deferrals
// field.
type MilepebbleListingEntry struct {
	ID       uuid.UUID             `json:"id"`
	Name     string                `json:"name"`
	Outcome  *string               `json:"outcome"`
	Status   store.MilestoneStatus `json:"status"`
	Delivers Document              `json:"delivers"`

	// ShippedCount/UnshippedCount mirror MilestoneListingEntry's own
	// fields -- see that field's doc comment. Both nil unless Status is
	// store.MilestoneStatusPartiallyComplete.
	ShippedCount   *int `json:"shipped_count,omitempty"`
	UnshippedCount *int `json:"unshipped_count,omitempty"`
}

// MilestoneListingEntry is one milestone's row in a DeliveryListing
// (issue #2689, FR11). Delivers/MustNotForeclose reuse this package's
// existing typed entities (LB7) via GetEntitySetSlice, never a bare id
// list. Milepebbles is this milestone's own children, in Position order
// (issue #2682) -- present even when this milestone itself did not match
// the caller's status filter, so long as at least one milepebble did; see
// ListProductDelivery's own doc comment for that rule.
type MilestoneListingEntry struct {
	ID               uuid.UUID                 `json:"id"`
	Name             string                    `json:"name"`
	Outcome          *string                   `json:"outcome"`
	FRBudget         *int                      `json:"fr_budget"`
	Status           store.MilestoneStatus     `json:"status"`
	Delivers         Document                  `json:"delivers"`
	MustNotForeclose Document                  `json:"must_not_foreclose"`
	Deferrals        []store.MilestoneDeferral `json:"deferrals"`

	// ShippedCount/UnshippedCount are populated only when Status is
	// store.MilestoneStatusPartiallyComplete -- issue #2686's per-item
	// shipped/unshipped breakdown, inlined here so FR11's listing answers
	// FR10's question for these rows without a second
	// GET /milestones/{id}/delivery round trip. Nil for every other
	// status, never a pair of zero-value ints (a nil pair means "not
	// applicable", not "zero shipped, zero unshipped").
	ShippedCount   *int `json:"shipped_count,omitempty"`
	UnshippedCount *int `json:"unshipped_count,omitempty"`

	Milepebbles []MilepebbleListingEntry `json:"milepebbles"`
}

// DeliveryListing is ListProductDelivery's result (FR11): every milestone
// under a product, in Position order (issue #2682), matching the
// caller's status filter -- see ListProductDelivery's own doc comment for
// exactly what "matching" means once milepebbles are involved.
type DeliveryListing struct {
	Milestones []MilestoneListingEntry `json:"milestones"`
}

// ListProductDelivery is FR11 (issue #2689, C28): every milestone and
// milepebble under productID, filtered to statuses -- an empty statuses
// means "all", never "none".
//
// kind='backlog' rows (issue #2687, not yet landed as of this method)
// never appear here: only MilestoneKindMilestone rows are ever a
// top-level DeliveryListing.Milestones entry, mirroring
// store.MilestoneStore.ListRefsByProduct's own kind filter -- a future
// backlog kind is excluded by that same filter, not a separate check this
// method would need to add later.
//
// The status filter applies to milestones and milepebbles independently:
// a milestone that does not itself match but has a matching milepebble is
// still returned, carrying only its matching milepebbles -- dropping the
// parent instead would leave a matching milepebble with no visible
// container, which makes the response unreadable. A filter that includes
// store.MilestoneStatusNotStarted must still surface a container with
// zero MilestoneStatusEvent rows (store.MilestoneStatusEventStore.
// CurrentStatuses' own derivation, milestone_status.go) -- this method
// never issues a naive `WHERE status = ...` query, which a zero-row
// container would silently fail to match.
//
// Status derivation is batched: every milestone and milepebble id under
// productID is resolved through one CurrentStatuses call, never one
// CurrentStatus call per id -- listing N containers must not cost N+1
// status queries.
func (q *Querier) ListProductDelivery(ctx context.Context, scopeID, productID uuid.UUID, statuses []store.MilestoneStatus) (DeliveryListing, error) {
	return DeliveryListing{}, fmt.Errorf("ListProductDelivery: not implemented")
}
