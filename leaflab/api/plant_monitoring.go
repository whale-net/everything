package main

import (
	"context"
	"errors"
	"time"

	"github.com/whale-net/everything/leaflab/api/authz"
)

// This file is #1381's scaffolded wire surface for FR56 (GR-4; SB-1.11) --
// api.proto's GetPlantMonitoringStatus/ListPlantMonitoringStatus RPCs. It
// is the plant-facing consequence of FR23's nearest-ancestor attribution
// rule, which shipped in Phase 3 (leaflab/api/attribution). This file
// REPORTS on that rule; it does not reimplement it -- every method below
// must resolve attribution through attribution.Resolver (or a query that a
// shared-fixture test asserts agrees with it), never by re-deriving FR23's
// stopping condition from scratch. See attribution.go's package doc
// comment for the one Go implementation (and its SQL twin,
// migration 019's attribute_region_plants) this file is required to call.
//
// FR56, restated: a plant is monitored iff at least one reading would
// attribute to it under FR23 -- not merely because a sensor exists
// somewhere in its region's subtree. Since a reading attributes to the
// NEAREST ancestor region (including its own) holding an active plant
// (FR23), a plant in a parent region is unmonitored precisely when a
// descendant region holds an active plant that intercepts every sensor
// beneath the parent before its reading would otherwise reach the parent.
//
// Implementation-phase algorithm (documented here so Implementation does
// not have to re-derive it from the requirement text):
//
//  1. Resolve the plant's current region R (PlantRow.RegionID, or the open
//     plant_region_history interval -- the two must agree; GetPlantByID
//     already exposes the cached column).
//  2. Compute R's subtree (R and every descendant region) -- mirrors
//     plants.go's sensorsInRegionSubtrees, but returning region_ids so
//     each sensor found can be checked as an independent candidate.
//  3. For every sensor whose sensor.region_id falls in that subtree, call
//     attribution.Resolver.ResolvePlants(sensor's region, time.Now()) and
//     inspect the attributing region_id it returns:
//       - attributes to R -> this plant IS monitored (R's active plants,
//         this one included per FR23's sibling fan-out, all monitored by
//         that one sensor). Short-circuit: one such sensor is enough.
//       - attributes to some descendant D of R (D != R) -> that sensor is
//         intercepted; note D and D's attributed plant as a candidate
//         "intercepting" detail, but keep scanning -- another sensor in
//         the subtree that is NOT behind any active-plant descendant would
//         still make this plant monitored (e.g. one sensor placed directly
//         in R alongside a child region C that itself holds its own
//         plant: C's sensors are intercepted, but a sensor in R itself is
//         not (FR23's sibling fan-out is a same-region concept; nothing
//         about C changes what attributes to R)).
//     If the subtree has zero sensors, or every sensor's reading attributes
//     to a descendant (no sensor reaches R), the plant is unmonitored.
//  4. Intercepting case: name the (region, plant) pair. When more than one
//     descendant intercepts, pick deterministically (e.g. the one nearest
//     R -- the shallowest intercepting descendant on the majority of
//     intercepted sensors' paths) and document the tie-break chosen; the
//     requirement is that exactly one is named, not which one when there
//     is a genuine choice.
//  5. Since when: the later of (a) this plant's own placement interval's
//     valid_from (plant_region_history, open interval for R) and (b) the
//     intercepting plant's placement interval's valid_from -- i.e. the
//     instant the interception actually took effect, never earlier than
//     when either side of it began. Where the plant was never intercepted
//     (PLANT_MONITORING_UNMONITORED_REASON_NO_ATTRIBUTABLE_SENSOR: no
//     sensor ever existed in the subtree, or all were later removed/
//     relocated), fall back to the instant of the last reading that WAS
//     attributable to this plant (MAX(recorded_at) via
//     v_sensor_reading_with_plant or equivalent, joined on this plant's
//     plant_id) -- and where there never was one either, this plant's own
//     placement valid_from (it has never, since being placed, had an
//     attributable reading). State whichever formula is actually
//     implemented in a doc comment on the function that computes it; FR56
//     requires the number to be meaningful and defensible, not that it
//     match this comment's exact wording.
//  6. FR25 wiring ("never an empty chart"): the bounded read path
//     (GetReadingSeries and friends) needs to carry this same
//     unmonitored_reason so an unmonitored plant's series response never
//     renders as an empty/outage-looking chart. That RPC set does not
//     exist on this branch's dependency lineage (#1377, #1358) -- it lives
//     in plan/1166-v2-1362 (FR25/FR27/FR28/NFR3.2), which is not an
//     ancestor of this branch. Whoever implements this file must either
//     integrate that branch first, or (following #1377's/plants.go's
//     precedent for an equivalent gap) file a scope note once the read
//     path lands elsewhere, recording that FR56's wiring into it is still
//     outstanding.
//  7. Household scoping (FR5): ListPlantMonitoringStatus applies
//     scope.Filter() inside the query, same shape as ListPlants;
//     GetPlantMonitoringStatus authorizes via authorizePlantWrite's
//     read-equivalent (a plant outside scope collapses to "not found",
//     NFR2 -- mirror authorizePlantWrite's shape but for a read, or reuse
//     it directly since it already returns ErrPlantNotFound uniformly for
//     both "doesn't exist" and "out of scope").
//
// No server.go handler wiring yet -- pb.UnimplementedLeafLabAPIServer
// still covers both RPCs; the methods below are signature-only skeletons
// that return ErrPlantMonitoringNotImplemented until the Implementation
// phase fills in the algorithm above.

// ErrPlantMonitoringNotImplemented is returned by GetPlantMonitoringStatus/
// ListPlantMonitoringStatus until the Implementation phase wires in FR56's
// algorithm (see this file's doc comment). Mirrors ErrRegionOpNotImplemented's
// role from #1376's region-lifecycle scaffold.
var ErrPlantMonitoringNotImplemented = errors.New("plant monitoring status: not implemented")

// UnmonitoredReason is the Go-side mirror of
// PlantMonitoringUnmonitoredReason (api.proto) -- a string enum, following
// leaflab/api/contract.FailureClass's precedent for a wire enum's Go twin,
// rather than the generated protobuf enum type, so this package's business
// logic does not depend on the generated pb package for its own control
// flow.
type UnmonitoredReason string

const (
	// UnmonitoredReasonIntercepted: see InterceptingPlant's doc comment.
	UnmonitoredReasonIntercepted UnmonitoredReason = "intercepted"
	// UnmonitoredReasonNoAttributableSensor covers both "never had a
	// sensor in the subtree" and "had one, since removed/relocated" -- the
	// two are distinguished only by UnmonitoredSince's derivation, not by
	// a separate reason value. See this file's doc comment, step 5.
	UnmonitoredReasonNoAttributableSensor UnmonitoredReason = "no_attributable_sensor"
)

// InterceptingPlant names the descendant region and active plant now
// taking the readings that would otherwise attribute to the plant this
// detail is attached to (FR56). Populated only when Reason ==
// UnmonitoredReasonIntercepted.
type InterceptingPlant struct {
	RegionID   int64
	RegionName string
	PlantID    int64
	PlantName  string
}

// PlantMonitoringStatus is one plant's FR56 status -- the Go-side shape
// GetPlantMonitoringStatus/ListPlantMonitoringStatus return, translated to
// PlantMonitoringStatus (api.proto) at the RPC boundary the same way
// PlantRow is translated to PlantInfo.
type PlantMonitoringStatus struct {
	PlantID int64
	// True iff at least one reading currently attributes to this plant
	// under FR23. When false, Reason, Since and (for the intercepted case)
	// Intercepting are populated.
	Monitored bool
	Reason    UnmonitoredReason
	// Non-nil only when Reason == UnmonitoredReasonIntercepted.
	Intercepting *InterceptingPlant
	// Non-nil only when Monitored is false -- see this file's doc comment,
	// step 5, for the derivation. Never a zero time.Time on an unmonitored
	// plant.
	Since *time.Time
}

// GetPlantMonitoringStatus reports plantID's FR56 status. found is false
// when plantID names no row, or names a row outside scope (NFR2 -- the two
// are indistinguishable to a caller by design, mirroring
// authorizePlantWrite's ErrPlantNotFound collapsing).
//
// Not yet implemented -- see this file's doc comment for the algorithm
// Implementation must wire in.
func (r *Repository) GetPlantMonitoringStatus(ctx context.Context, plantID int64, scope authz.Scope) (status PlantMonitoringStatus, found bool, err error) {
	return PlantMonitoringStatus{}, false, ErrPlantMonitoringNotImplemented
}

// ListPlantMonitoringStatus returns up to limit plants' FR56 status,
// ordered and keyset-paginated on (plant_id) exactly like ListPlants
// (FR61), household-scoped via scope.Filter() (FR5.1/FR5.2). Retired
// plants are excluded, mirroring ListPlants' default-listing guard
// (FR22.1) -- a retired plant is not part of the grower surface this RPC
// serves.
//
// Not yet implemented -- see this file's doc comment for the algorithm
// Implementation must wire in.
func (r *Repository) ListPlantMonitoringStatus(ctx context.Context, afterPlantID int64, hasAfter bool, limit int32, scope authz.Scope) ([]PlantMonitoringStatus, error) {
	return nil, ErrPlantMonitoringNotImplemented
}
