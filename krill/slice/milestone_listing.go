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
	"sort"

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
	milestoneRefs, err := q.store.Milestones().ListRefsByProduct(ctx, scopeID, productID)
	if err != nil {
		return DeliveryListing{}, fmt.Errorf("list milestone_ref by product: %w", err)
	}

	// ListRefsByProduct orders by Name (lexicographic, for the renderer's
	// own use -- see that method's doc comment); this listing needs
	// Position order instead, so re-sort here in Go rather than touching
	// that method's SQL ORDER BY.
	sort.SliceStable(milestoneRefs, func(i, j int) bool {
		return milestoneRefs[i].Position < milestoneRefs[j].Position
	})

	milepebblesByMilestone := make(map[uuid.UUID][]store.MilestoneRef, len(milestoneRefs))
	statusIDs := make([]uuid.UUID, 0, len(milestoneRefs)*2)
	for _, m := range milestoneRefs {
		statusIDs = append(statusIDs, m.ID)

		milepebbles, err := q.store.MilestoneAuthoring().ListMilepebblesByMilestone(ctx, m.ID)
		if err != nil {
			return DeliveryListing{}, fmt.Errorf("list milepebbles by milestone %s: %w", m.ID, err)
		}
		milepebblesByMilestone[m.ID] = milepebbles
		for _, mp := range milepebbles {
			statusIDs = append(statusIDs, mp.ID)
		}
	}

	// One batched call for every milestone and milepebble id under
	// productID (see this method's own doc comment) -- never one
	// CurrentStatus call per container.
	statusByID, err := q.store.MilestoneStatus().CurrentStatuses(ctx, statusIDs)
	if err != nil {
		return DeliveryListing{}, fmt.Errorf("current statuses: %w", err)
	}

	matchesFilter := statusMatcher(statuses)

	var entries []MilestoneListingEntry
	for _, m := range milestoneRefs {
		milestoneStatus := statusByID[m.ID]

		var milepebbleEntries []MilepebbleListingEntry
		for _, mp := range milepebblesByMilestone[m.ID] {
			mpStatus := statusByID[mp.ID]
			if !matchesFilter(mpStatus) {
				continue
			}

			entry, err := q.buildMilepebbleListingEntry(ctx, mp, mpStatus)
			if err != nil {
				return DeliveryListing{}, err
			}
			milepebbleEntries = append(milepebbleEntries, entry)
		}

		// A milestone is returned if it itself matches the filter, or if
		// at least one of its milepebbles does -- see this method's doc
		// comment for why dropping the parent in that second case would
		// make the response unreadable.
		if !matchesFilter(milestoneStatus) && len(milepebbleEntries) == 0 {
			continue
		}

		entry, err := q.buildMilestoneListingEntry(ctx, m, milestoneStatus, milepebbleEntries)
		if err != nil {
			return DeliveryListing{}, err
		}
		entries = append(entries, entry)
	}

	return DeliveryListing{Milestones: entries}, nil
}

// statusMatcher returns a predicate reporting whether a given
// store.MilestoneStatus satisfies statuses -- an empty statuses means
// "all" (the predicate always reports true), never "none".
func statusMatcher(statuses []store.MilestoneStatus) func(store.MilestoneStatus) bool {
	if len(statuses) == 0 {
		return func(store.MilestoneStatus) bool { return true }
	}
	set := make(map[store.MilestoneStatus]struct{}, len(statuses))
	for _, s := range statuses {
		set[s] = struct{}{}
	}
	return func(s store.MilestoneStatus) bool {
		_, ok := set[s]
		return ok
	}
}

// milestoneRelationSets splits containerID's own `entity_milestone`
// associations (store.MilestoneStore.ListAssociationsByMilestone) into
// Delivers and Must-not-foreclose entity id lists -- the same split
// store.MilestoneAuthoringStore.GetMilestone already performs, reused
// here so both call sites agree on which relation wins a row with an
// unexpected value (default: Delivers).
func milestoneRelationSets(associations []store.EntityMilestone) (delivers, mustNotForeclose []uuid.UUID) {
	for _, a := range associations {
		switch a.Relation {
		case store.MilestoneRelationMustNotForeclose:
			mustNotForeclose = append(mustNotForeclose, a.EntityID)
		default:
			delivers = append(delivers, a.EntityID)
		}
	}
	return delivers, mustNotForeclose
}

// buildMilestoneListingEntry assembles one MilestoneListingEntry: m's own
// Delivers/Must-not-foreclose Documents (via GetEntitySetSlice, LB7), its
// deferrals, and its already-filtered milepebbles. shippedCount/
// unshippedCount are populated only when status is
// store.MilestoneStatusPartiallyComplete (FR10 inlined into FR11, per
// this package's doc comment on MilestoneListingEntry).
func (q *Querier) buildMilestoneListingEntry(ctx context.Context, m store.MilestoneRef, status store.MilestoneStatus, milepebbles []MilepebbleListingEntry) (MilestoneListingEntry, error) {
	associations, err := q.store.Milestones().ListAssociationsByMilestone(ctx, m.ID)
	if err != nil {
		return MilestoneListingEntry{}, fmt.Errorf("list associations for milestone %s: %w", m.ID, err)
	}
	deliversIDs, mustNotForecloseIDs := milestoneRelationSets(associations)

	delivers, err := q.GetEntitySetSlice(ctx, deliversIDs)
	if err != nil {
		return MilestoneListingEntry{}, fmt.Errorf("delivers entity set slice for milestone %s: %w", m.ID, err)
	}
	mustNotForeclose, err := q.GetEntitySetSlice(ctx, mustNotForecloseIDs)
	if err != nil {
		return MilestoneListingEntry{}, fmt.Errorf("must_not_foreclose entity set slice for milestone %s: %w", m.ID, err)
	}

	deferrals, err := q.store.MilestoneAuthoring().ListDeferrals(ctx, m.ID)
	if err != nil {
		return MilestoneListingEntry{}, fmt.Errorf("list deferrals for milestone %s: %w", m.ID, err)
	}

	entry := MilestoneListingEntry{
		ID:               m.ID,
		Name:             m.Name,
		Outcome:          m.Outcome,
		FRBudget:         m.FRBudget,
		Status:           status,
		Delivers:         delivers,
		MustNotForeclose: mustNotForeclose,
		Deferrals:        deferrals,
		Milepebbles:      milepebbles,
	}

	if status == store.MilestoneStatusPartiallyComplete {
		shipped, unshipped, err := q.store.DeliveryShipments().DeliveryBreakdown(ctx, m.ID)
		if err != nil {
			return MilestoneListingEntry{}, fmt.Errorf("delivery breakdown for milestone %s: %w", m.ID, err)
		}
		shippedCount, unshippedCount := len(shipped), len(unshipped)
		entry.ShippedCount = &shippedCount
		entry.UnshippedCount = &unshippedCount
	}

	return entry, nil
}

// buildMilepebbleListingEntry mirrors buildMilestoneListingEntry for one
// milepebble -- a milepebble carries no Must-not-foreclose associations or
// deferrals of its own (FR3), so it needs neither of those two calls.
func (q *Querier) buildMilepebbleListingEntry(ctx context.Context, mp store.MilestoneRef, status store.MilestoneStatus) (MilepebbleListingEntry, error) {
	associations, err := q.store.Milestones().ListAssociationsByMilestone(ctx, mp.ID)
	if err != nil {
		return MilepebbleListingEntry{}, fmt.Errorf("list associations for milepebble %s: %w", mp.ID, err)
	}
	deliversIDs, _ := milestoneRelationSets(associations)

	delivers, err := q.GetEntitySetSlice(ctx, deliversIDs)
	if err != nil {
		return MilepebbleListingEntry{}, fmt.Errorf("delivers entity set slice for milepebble %s: %w", mp.ID, err)
	}

	entry := MilepebbleListingEntry{
		ID:       mp.ID,
		Name:     mp.Name,
		Outcome:  mp.Outcome,
		Status:   status,
		Delivers: delivers,
	}

	if status == store.MilestoneStatusPartiallyComplete {
		shipped, unshipped, err := q.store.DeliveryShipments().DeliveryBreakdown(ctx, mp.ID)
		if err != nil {
			return MilepebbleListingEntry{}, fmt.Errorf("delivery breakdown for milepebble %s: %w", mp.ID, err)
		}
		shippedCount, unshippedCount := len(shipped), len(unshipped)
		entry.ShippedCount = &shippedCount
		entry.UnshippedCount = &unshippedCount
	}

	return entry, nil
}
