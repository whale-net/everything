package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/leaflab/api/attribution"
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
// Implementation-phase algorithm (as actually implemented below):
//
//  1. Resolve the plant's current region R (PlantRow.RegionID -- kept in
//     sync with the open plant_region_history interval by
//     placement.Writer.Move, per migration 017's doc comment).
//  2. Find every region in R's subtree (R and every descendant) that has
//     at least one sensor registered in it -- sensorRegionsInSubtree below
//     mirrors plants.go's sensorsInRegionSubtrees, but reports the
//     distinct region_ids rather than sensor_ids, since attribution
//     resolves per-region: every sensor sharing a region resolves
//     identically, so this calls attribution.Resolver once per candidate
//     region rather than once per sensor.
//  3. For every candidate region, call
//     attribution.Resolver.ResolvePlants(candidate, time.Now()) and inspect
//     the attributing region_id it returns:
//       - attributes to R -> this plant IS monitored (R's active plants,
//         this one included per FR23's sibling fan-out, are all monitored
//         by that one sensor's region). Short-circuit: one such candidate
//         is enough.
//       - attributes to some descendant D of R (D != R) -> that candidate
//         is intercepted; record D and D's attributed plant(s) as a
//         candidate "intercepting" detail, but keep scanning -- another
//         candidate region in the subtree that is NOT behind any
//         active-plant descendant would still make this plant monitored
//         (e.g. one sensor placed directly in R alongside a child region C
//         that itself holds its own plant: C's sensors are intercepted,
//         but a sensor in R itself is not -- FR23's sibling fan-out is a
//         same-region concept; nothing about C changes what attributes to
//         R).
//     If the subtree has zero sensor-bearing regions, or every candidate's
//     reading attributes to a descendant (no candidate reaches R), the
//     plant is unmonitored.
//  4. Intercepting case: name the (region, plant) pair. When more than one
//     descendant intercepts, the shallowest intercepting descendant (the
//     one nearest R, by v_region_path.depth) is named, tie-broken by
//     lowest region_id and then lowest plant_id when a region hosts more
//     than one active plant (FR23 sibling fan-out) -- deterministic, not a
//     claim that this is the only defensible choice (see this file's
//     Implementation-phase issue text).
//  5. Since when: the later of (a) this plant's own placement interval's
//     valid_from (plant_region_history, open interval for R) and (b) the
//     intercepting plant's placement interval's valid_from -- i.e. the
//     instant the interception actually took effect, never earlier than
//     when either side of it began. Where the plant was never intercepted
//     (PLANT_MONITORING_UNMONITORED_REASON_NO_ATTRIBUTABLE_SENSOR: no
//     sensor ever existed in the subtree, or all were later removed/
//     relocated), fall back to the instant of the last reading that WAS
//     attributable to this plant (MAX(recorded_at) via
//     v_sensor_reading_with_plant, joined on this plant's plant_id) -- and
//     where there never was one either, this plant's own placement
//     valid_from (it has never, since being placed, had an attributable
//     reading). lastAttributableReadingAt/currentPlacementValidFrom below
//     implement this.
//  6. FR25 wiring ("never an empty chart"): the bounded read path
//     (GetReadingSeries and friends) needs to carry this same
//     unmonitored_reason so an unmonitored plant's series response never
//     renders as an empty/outage-looking chart. That RPC set does not
//     exist on this branch's dependency lineage (#1377, #1358) -- it lives
//     in plan/1166-v2-1362 (FR25/FR27/FR28/NFR3.2), which is not an
//     ancestor of this branch. #1450 (filed by the Scaffold phase) already
//     records this gap as a scope note; this Implementation phase does not
//     re-file it and does not attempt the wiring -- it is out of scope
//     until #1362's branch is integrated with this one.
//  7. Household scoping (FR5): ListPlantMonitoringStatus applies
//     scope.Filter() inside the plant query, same shape as ListPlants;
//     GetPlantMonitoringStatus authorizes via authorizePlantWrite (a plant
//     outside scope collapses to "not found", NFR2 -- mirrors
//     authorizePlantWrite's shape for a read, reusing it directly since it
//     already returns ErrPlantNotFound uniformly for both "doesn't exist"
//     and "out of scope").
//
// No server.go handler wiring yet -- pb.UnimplementedLeafLabAPIServer
// still covers both RPCs; every plant RPC (#1377's CreatePlant et al.)
// shares this same deferred-wiring state today. The methods below are
// exercised directly by the Testing phase, the same way #1377's plant
// lifecycle methods are.

// ErrPlantMonitoringNotImplemented is no longer returned by
// GetPlantMonitoringStatus/ListPlantMonitoringStatus -- kept as a named
// error for any caller still matching on it defensively during the
// Implementation-phase transition. Mirrors ErrRegionOpNotImplemented's
// retirement precedent once a region RPC's implementation landed.
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
func (r *Repository) GetPlantMonitoringStatus(ctx context.Context, plantID int64, scope authz.Scope) (status PlantMonitoringStatus, found bool, err error) {
	if _, err := r.authorizePlantWrite(ctx, plantID, scope); err != nil {
		if errors.Is(err, ErrPlantNotFound) {
			return PlantMonitoringStatus{}, false, nil
		}
		return PlantMonitoringStatus{}, false, err
	}

	plant, err := r.GetPlantByID(ctx, plantID)
	if err != nil {
		if errors.Is(err, ErrPlantNotFound) {
			// authorizePlantWrite above just confirmed the plant exists
			// and is in scope; a NotFound here would mean it was removed
			// between the two reads -- collapse to the same not-found
			// shape a caller would see either way.
			return PlantMonitoringStatus{}, false, nil
		}
		return PlantMonitoringStatus{}, false, fmt.Errorf("get plant %d for monitoring status: %w", plantID, err)
	}

	pmStatus, err := r.computeMonitoringStatus(ctx, plant)
	if err != nil {
		return PlantMonitoringStatus{}, false, err
	}
	return pmStatus, true, nil
}

// ListPlantMonitoringStatus returns up to limit plants' FR56 status,
// ordered and keyset-paginated on (plant_id) exactly like ListPlants
// (FR61), household-scoped via scope.Filter() (FR5.1/FR5.2). Retired
// plants are excluded, mirroring ListPlants' default-listing guard
// (FR22.1) -- a retired plant is not part of the grower surface this RPC
// serves.
func (r *Repository) ListPlantMonitoringStatus(ctx context.Context, afterPlantID int64, hasAfter bool, limit int32, scope authz.Scope) ([]PlantMonitoringStatus, error) {
	plants, err := r.ListPlants(ctx, afterPlantID, hasAfter, limit, scope)
	if err != nil {
		return nil, err
	}

	statuses := make([]PlantMonitoringStatus, 0, len(plants))
	for _, plant := range plants {
		pmStatus, err := r.computeMonitoringStatus(ctx, plant)
		if err != nil {
			return nil, err
		}
		statuses = append(statuses, pmStatus)
	}
	return statuses, nil
}

// computeMonitoringStatus resolves plant's FR56 status via
// attribution.Resolver -- see this file's doc comment for the full
// algorithm. Callers are responsible for authorization/scoping before
// calling this; it performs none of its own.
func (r *Repository) computeMonitoringStatus(ctx context.Context, plant PlantRow) (PlantMonitoringStatus, error) {
	now := time.Now()
	resolver := attribution.NewResolver(r.db)

	candidateRegionIDs, err := sensorRegionsInSubtree(ctx, r.db, plant.RegionID)
	if err != nil {
		return PlantMonitoringStatus{}, fmt.Errorf("compute monitoring status for plant %d: %w", plant.PlantID, err)
	}

	var best *interceptCandidate
	for _, candidateRegionID := range candidateRegionIDs {
		plants, attributedRegionID, err := resolver.ResolvePlants(ctx, candidateRegionID, now)
		if err != nil {
			return PlantMonitoringStatus{}, fmt.Errorf("resolve attribution for region %d (plant %d's subtree): %w", candidateRegionID, plant.PlantID, err)
		}
		if attributedRegionID == plant.RegionID {
			// At least one sensor's reading reaches R directly -- FR23's
			// sibling fan-out means every active plant in R, this one
			// included, is monitored. Short-circuit: no interception
			// detail is needed.
			return PlantMonitoringStatus{PlantID: plant.PlantID, Monitored: true}, nil
		}
		if attributedRegionID == 0 || len(plants) == 0 {
			// No region on this candidate's path -- including R -- has an
			// active plant. Can only happen if R's own attributing plant
			// interval and this plant's cached region_id have drifted out
			// of agreement (see this file's doc comment, step 1); treat
			// defensively as "not an interception" and keep scanning.
			continue
		}

		depth, name, err := regionDepthAndName(ctx, r.db, attributedRegionID)
		if err != nil {
			return PlantMonitoringStatus{}, fmt.Errorf("resolve intercepting region %d for plant %d: %w", attributedRegionID, plant.PlantID, err)
		}
		// plants is ordered by plant_id ascending (attribution.ResolvePlants'
		// doc comment); take the lowest for a deterministic name when the
		// intercepting region itself fans out to more than one plant.
		named := plants[0]
		candidate := interceptCandidate{
			regionID:   attributedRegionID,
			regionName: name,
			depth:      depth,
			plantID:    named.PlantID,
			plantName:  named.Name,
		}
		if best == nil || candidate.depth < best.depth ||
			(candidate.depth == best.depth && candidate.regionID < best.regionID) ||
			(candidate.depth == best.depth && candidate.regionID == best.regionID && candidate.plantID < best.plantID) {
			best = &candidate
		}
	}

	ownValidFrom, ok, err := currentPlacementValidFrom(ctx, r.db, plant.PlantID)
	if err != nil {
		return PlantMonitoringStatus{}, fmt.Errorf("resolve placement valid_from for plant %d: %w", plant.PlantID, err)
	}
	if !ok {
		// No open plant_region_history interval -- should not occur post
		// FR19/FR21 (every plant has exactly one open interval), but fall
		// back to created_at rather than fail the whole status lookup.
		ownValidFrom = plant.CreatedAt
	}

	if best != nil {
		interceptingValidFrom, ok, err := currentPlacementValidFrom(ctx, r.db, best.plantID)
		if err != nil {
			return PlantMonitoringStatus{}, fmt.Errorf("resolve placement valid_from for intercepting plant %d: %w", best.plantID, err)
		}
		since := ownValidFrom
		if ok && interceptingValidFrom.After(since) {
			since = interceptingValidFrom
		}
		return PlantMonitoringStatus{
			PlantID:   plant.PlantID,
			Monitored: false,
			Reason:    UnmonitoredReasonIntercepted,
			Intercepting: &InterceptingPlant{
				RegionID:   best.regionID,
				RegionName: best.regionName,
				PlantID:    best.plantID,
				PlantName:  best.plantName,
			},
			Since: &since,
		}, nil
	}

	// Not monitored, and no interception found: either the subtree has no
	// sensors at all, or every sensor once here has since been removed or
	// relocated. Since carries the meaningful distinction (this file's
	// doc comment, step 5): the last instant a reading WAS attributable to
	// this plant, or (if there never was one) this plant's own placement
	// valid_from.
	since := ownValidFrom
	lastReading, err := lastAttributableReadingAt(ctx, r.db, plant.PlantID)
	if err != nil {
		return PlantMonitoringStatus{}, fmt.Errorf("resolve last attributable reading for plant %d: %w", plant.PlantID, err)
	}
	if lastReading != nil {
		since = *lastReading
	}
	return PlantMonitoringStatus{
		PlantID:   plant.PlantID,
		Monitored: false,
		Reason:    UnmonitoredReasonNoAttributableSensor,
		Since:     &since,
	}, nil
}

// interceptCandidate is one region-with-an-active-plant found intercepting
// a sensor reading somewhere in the evaluated plant's region subtree, as
// computeMonitoringStatus scans candidate regions. depth is v_region_path's
// depth (root = 0), used to pick the shallowest -- i.e. nearest R --
// candidate deterministically when more than one interception exists.
type interceptCandidate struct {
	regionID   int64
	regionName string
	depth      int
	plantID    int64
	plantName  string
}

// sensorRegionsInSubtree returns the distinct region_id of every region in
// rootRegionID's subtree (rootRegionID itself or any descendant) that has
// at least one sensor registered in it. Mirrors plants.go's
// sensorsInRegionSubtrees, but reports which region each candidate sensor
// sits in rather than the sensor_id itself -- FR56's algorithm calls
// attribution.Resolver once per distinct region (sensors sharing a region
// resolve identically), not once per sensor. Runs directly against db
// (read-only, no transaction needed) rather than within a tx, unlike
// sensorsInRegionSubtrees' write-path use.
func sensorRegionsInSubtree(ctx context.Context, db *pgxpool.Pool, rootRegionID int64) ([]int64, error) {
	rows, err := db.Query(ctx, `
		WITH RECURSIVE subtree AS (
			SELECT region_id FROM region WHERE region_id = $1

			UNION ALL

			SELECT r.region_id
			FROM region r
			JOIN subtree s ON r.parent_region_id = s.region_id
		)
		SELECT DISTINCT sensor.region_id
		FROM sensor
		WHERE sensor.region_id IN (SELECT region_id FROM subtree)
	`, rootRegionID)
	if err != nil {
		return nil, fmt.Errorf("compute sensor-bearing regions in subtree of region %d: %w", rootRegionID, err)
	}
	defer rows.Close()

	var regionIDs []int64
	for rows.Next() {
		var regionID int64
		if err := rows.Scan(&regionID); err != nil {
			return nil, fmt.Errorf("scan sensor-bearing region in subtree of region %d: %w", rootRegionID, err)
		}
		regionIDs = append(regionIDs, regionID)
	}
	return regionIDs, rows.Err()
}

// regionDepthAndName returns regionID's v_region_path depth (root = 0) and
// name, for naming/ranking an interception candidate.
func regionDepthAndName(ctx context.Context, db *pgxpool.Pool, regionID int64) (depth int, name string, err error) {
	err = db.QueryRow(ctx, `
		SELECT depth, name FROM v_region_path WHERE region_id = $1
	`, regionID).Scan(&depth, &name)
	if err != nil {
		return 0, "", fmt.Errorf("resolve depth/name for region %d: %w", regionID, err)
	}
	return depth, name, nil
}

// currentPlacementValidFrom returns plantID's currently-open
// plant_region_history interval's valid_from (FR19). ok is false when
// plantID has no open interval (should not occur post FR19/FR21, but
// callers fall back gracefully rather than failing outright).
func currentPlacementValidFrom(ctx context.Context, db *pgxpool.Pool, plantID int64) (validFrom time.Time, ok bool, err error) {
	err = db.QueryRow(ctx, `
		SELECT valid_from FROM plant_region_history
		WHERE plant_id = $1 AND valid_to IS NULL
	`, plantID).Scan(&validFrom)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, fmt.Errorf("resolve current placement valid_from for plant %d: %w", plantID, err)
	}
	return validFrom, true, nil
}

// lastAttributableReadingAt returns the recorded_at of the most recent
// reading that attributed to plantID, per v_sensor_reading_with_plant
// (migration 012) -- nil when no reading ever attributed to it. This is
// FR56's since-when fallback (this file's doc comment, step 5) for a plant
// that was never intercepted: the instant its own subtree last produced an
// attributable reading, before any sensor coverage was removed/relocated.
func lastAttributableReadingAt(ctx context.Context, db *pgxpool.Pool, plantID int64) (*time.Time, error) {
	var maxRecordedAt *time.Time
	err := db.QueryRow(ctx, `
		SELECT MAX(recorded_at) FROM v_sensor_reading_with_plant WHERE plant_id = $1
	`, plantID).Scan(&maxRecordedAt)
	if err != nil {
		return nil, fmt.Errorf("resolve last attributable reading for plant %d: %w", plantID, err)
	}
	return maxRecordedAt, nil
}
