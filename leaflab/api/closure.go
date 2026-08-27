package main

// Ownership closure, self-service adoption, and transfer (FR70.2-.4, FR77)
// -- scaffold for #1343.
//
// FR70's ownership closure of a board B is a FIXED SIX-STEP ENUMERATION over
// current placement, not a transitive closure:
//
//  1. B itself.
//  2. B's sensors.
//  3. The regions those sensors currently occupy.
//  4. The root subtree(s) containing those regions.
//  5. Every OTHER board with a sensor currently in that subtree (entangled
//     boards).
//  6. Every plant currently placed in that subtree.
//
// It deliberately does NOT recurse into step 5's entangled boards' OTHER
// sensors (which may sit in a different subtree entirely) -- doing so would
// make the closure a transitive closure, which the requirement text calls
// out as a defect, not an optimisation: "does not follow entangled boards'
// sensors into other subtrees, so adoption may leave an owned board with a
// sensor in an unowned region. That is not a live cross-household reference
// and is dischargeable." ComputeClosure below is written as six sequential
// queries, in this exact order, specifically so that shape stays visible in
// the code -- do not refactor this into a recursive walk over "boards
// reachable from B".
//
// ComputeClosure is fully implemented here (Scaffold phase) because it is
// read-only, matching households.go's precedent of implementing read paths
// in Scaffold and leaving writes for Implementation. ReleaseBoard and
// TransferClosure below are signature-only skeletons returning
// ErrClosureOpNotImplemented until the Implementation phase wires in FR77's
// evidence gating (release token vs. elevated admin + discharged FR76
// challenge), the departure_record write (migration
// 022_departure_record.up.sql), the household-owned-plant-type copy seam,
// and the single-transaction move of board_ownership/region.household_id/
// plant.household_id -- matching 015_ownership's and households.go's
// scaffold/feat split precedent.

import (
	"context"
	"errors"
	"fmt"
)

// ErrClosureOpNotImplemented is returned by every closure write path below
// until the Implementation phase wires in its business logic -- matching
// households.go's ErrHouseholdOpNotImplemented precedent.
var ErrClosureOpNotImplemented = errors.New("closure operation not implemented until the Implementation phase")

// Closure is FR70's ownership closure of a board, as computed by
// ComputeClosure. Every id slice is deduplicated; a slice may be empty (but
// never nil-vs-empty-significant -- callers should treat both the same way)
// when a step finds nothing, e.g. a board with no sensors placed anywhere
// yields an empty closure beyond BoardID/SensorIDs.
type Closure struct {
	// BoardID is B (step 1).
	BoardID int64
	// SensorIDs are B's sensors (step 2), regardless of whether they are
	// currently placed in a region.
	SensorIDs []int64
	// RegionIDs are the regions B's sensors currently occupy (step 3) --
	// only sensors with a non-NULL region_id contribute here.
	RegionIDs []int64
	// SubtreeRootIDs are the root(s) of the region tree(s) containing
	// RegionIDs (step 4). Usually one root, but a board's sensors may be
	// scattered across more than one physical root region (e.g. one sensor
	// in "Greenhouse", another in "Garage") -- the closure spans every such
	// root's subtree, not just the first one found.
	SubtreeRootIDs []int64
	// SubtreeRegionIDs is the full region id membership of every subtree
	// named by SubtreeRootIDs (each root plus all of its descendants) --
	// the population EntangledBoardIDs and PlantIDs are computed against.
	// Not itself one of the six named closure members, but exposed since
	// FR70's refusal path needs to name "the shared subtree" (FR59.3).
	SubtreeRegionIDs []int64
	// EntangledBoardIDs are every OTHER board (never B itself) with a
	// sensor currently in SubtreeRegionIDs (step 5) -- the fixed
	// enumeration stops here; their own other sensors are never followed.
	EntangledBoardIDs []int64
	// PlantIDs are every plant currently placed in SubtreeRegionIDs (step
	// 6) -- removed plants (removed_at IS NOT NULL) are excluded, since
	// "currently placed" is present-tense.
	PlantIDs []int64
}

// ComputeClosure computes FR70's ownership closure of boardID: the fixed
// six-step enumeration documented on this file's doc comment, executed in
// that exact order. It is read-only -- callers that need the closure locked
// against concurrent mutation (e.g. the Implementation phase's adoption/
// transfer transactions) are responsible for taking their own locks around
// the write; this function itself neither opens a transaction nor locks
// rows, matching its signature (ctx, boardID) -> (Closure, error) with no
// tx parameter.
func (r *Repository) ComputeClosure(ctx context.Context, boardID int64) (Closure, error) {
	// Step 1: B itself. Verifying existence up front means every later step
	// fails fast with ErrBoardNotFound instead of silently returning an
	// empty closure for a boardID that names no row.
	if _, err := r.GetBoardByID(ctx, boardID); err != nil {
		return Closure{}, err
	}

	closure := Closure{BoardID: boardID}

	// Step 2 + 3: B's sensors, and the regions they currently occupy.
	sensorIDs, regionIDs, err := r.boardSensorsAndRegions(ctx, boardID)
	if err != nil {
		return Closure{}, err
	}
	closure.SensorIDs = sensorIDs
	closure.RegionIDs = regionIDs

	if len(regionIDs) == 0 {
		// None of B's sensors are currently placed anywhere -- the closure
		// is B and its sensors alone; steps 4-6 have nothing to enumerate.
		return closure, nil
	}

	// Step 4: the root subtree(s) containing those regions.
	rootIDs, err := r.resolveRegionRoots(ctx, regionIDs)
	if err != nil {
		return Closure{}, err
	}
	closure.SubtreeRootIDs = rootIDs

	subtreeRegionIDs, err := r.subtreeRegionIDs(ctx, rootIDs)
	if err != nil {
		return Closure{}, err
	}
	closure.SubtreeRegionIDs = subtreeRegionIDs

	// Step 5: every OTHER board with a sensor currently in that subtree.
	// Fixed enumeration: this does not recurse into those boards' own other
	// sensors (see this file's doc comment).
	entangledBoardIDs, err := r.boardsWithSensorsInRegions(ctx, subtreeRegionIDs, boardID)
	if err != nil {
		return Closure{}, err
	}
	closure.EntangledBoardIDs = entangledBoardIDs

	// Step 6: every plant currently placed in that subtree.
	plantIDs, err := r.activePlantsInRegions(ctx, subtreeRegionIDs)
	if err != nil {
		return Closure{}, err
	}
	closure.PlantIDs = plantIDs

	return closure, nil
}

// boardSensorsAndRegions returns boardID's sensor ids (step 2) and the
// distinct, non-NULL region ids they currently occupy (step 3).
func (r *Repository) boardSensorsAndRegions(ctx context.Context, boardID int64) (sensorIDs []int64, regionIDs []int64, err error) {
	rows, err := r.db.Query(ctx, `
		SELECT sensor_id, region_id FROM sensor WHERE board_id = $1
	`, boardID)
	if err != nil {
		return nil, nil, fmt.Errorf("list sensors for board %d: %w", boardID, err)
	}
	defer rows.Close()

	seenRegion := make(map[int64]bool)
	for rows.Next() {
		var sensorID int64
		var regionID *int64
		if err := rows.Scan(&sensorID, &regionID); err != nil {
			return nil, nil, fmt.Errorf("scan sensor for board %d: %w", boardID, err)
		}
		sensorIDs = append(sensorIDs, sensorID)
		if regionID != nil && !seenRegion[*regionID] {
			seenRegion[*regionID] = true
			regionIDs = append(regionIDs, *regionID)
		}
	}
	return sensorIDs, regionIDs, rows.Err()
}

// resolveRegionRoots returns the distinct tree-root region ids reached by
// walking each of regionIDs up through parent_region_id -- step 4's "root
// subtree(s) containing those regions". A region that is itself a root
// resolves to itself.
func (r *Repository) resolveRegionRoots(ctx context.Context, regionIDs []int64) ([]int64, error) {
	rows, err := r.db.Query(ctx, `
		WITH RECURSIVE ancestors AS (
			SELECT region_id, parent_region_id
			FROM region
			WHERE region_id = ANY($1)

			UNION ALL

			SELECT r.region_id, r.parent_region_id
			FROM region r
			JOIN ancestors a ON r.region_id = a.parent_region_id
		)
		SELECT DISTINCT region_id FROM ancestors WHERE parent_region_id IS NULL
	`, regionIDs)
	if err != nil {
		return nil, fmt.Errorf("resolve region roots for %v: %w", regionIDs, err)
	}
	defer rows.Close()

	var rootIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan region root for %v: %w", regionIDs, err)
		}
		rootIDs = append(rootIDs, id)
	}
	return rootIDs, rows.Err()
}

// subtreeRegionIDs returns every region id in the subtree(s) rooted at
// rootIDs -- each root plus all of its descendants -- the population steps
// 5 and 6 are computed against.
func (r *Repository) subtreeRegionIDs(ctx context.Context, rootIDs []int64) ([]int64, error) {
	rows, err := r.db.Query(ctx, `
		WITH RECURSIVE subtree AS (
			SELECT region_id FROM region WHERE region_id = ANY($1)

			UNION ALL

			SELECT r.region_id
			FROM region r
			JOIN subtree s ON r.parent_region_id = s.region_id
		)
		SELECT region_id FROM subtree
	`, rootIDs)
	if err != nil {
		return nil, fmt.Errorf("resolve subtree region ids for roots %v: %w", rootIDs, err)
	}
	defer rows.Close()

	var regionIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan subtree region for roots %v: %w", rootIDs, err)
		}
		regionIDs = append(regionIDs, id)
	}
	return regionIDs, rows.Err()
}

// boardsWithSensorsInRegions returns the distinct board ids (excluding
// excludeBoardID) with at least one sensor currently placed in regionIDs --
// step 5's entangled boards.
func (r *Repository) boardsWithSensorsInRegions(ctx context.Context, regionIDs []int64, excludeBoardID int64) ([]int64, error) {
	rows, err := r.db.Query(ctx, `
		SELECT DISTINCT board_id
		FROM sensor
		WHERE region_id = ANY($1) AND board_id != $2
	`, regionIDs, excludeBoardID)
	if err != nil {
		return nil, fmt.Errorf("list entangled boards for regions %v: %w", regionIDs, err)
	}
	defer rows.Close()

	var boardIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan entangled board for regions %v: %w", regionIDs, err)
		}
		boardIDs = append(boardIDs, id)
	}
	return boardIDs, rows.Err()
}

// activePlantsInRegions returns the ids of every plant currently placed
// (removed_at IS NULL) in regionIDs -- step 6.
func (r *Repository) activePlantsInRegions(ctx context.Context, regionIDs []int64) ([]int64, error) {
	rows, err := r.db.Query(ctx, `
		SELECT plant_id
		FROM plant
		WHERE region_id = ANY($1) AND removed_at IS NULL
	`, regionIDs)
	if err != nil {
		return nil, fmt.Errorf("list active plants for regions %v: %w", regionIDs, err)
	}
	defer rows.Close()

	var plantIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan active plant for regions %v: %w", regionIDs, err)
		}
		plantIDs = append(plantIDs, id)
	}
	return plantIDs, rows.Err()
}

// ReleaseBoard is FR77(a)'s evidence path: a member of the losing household
// releases a board's closure, producing a release token TransferClosure can
// later present as evidence. Signature-only skeleton -- returns
// ErrClosureOpNotImplemented until the Implementation phase wires in the
// release-token issuance and its persistence.
func (r *Repository) ReleaseBoard(ctx context.Context, boardID int64, principalSubject string) (releaseToken string, err error) {
	return "", ErrClosureOpNotImplemented
}

// TransferClosure is FR77's transfer operation: moves boardID's ownership
// closure to destinationHouseholdID, gated on evidence -- either a release
// token from ReleaseBoard by a member of the losing household, or an
// elevated admin action carrying a discharged FR76 possession-challenge id.
// Signature-only skeleton -- returns ErrClosureOpNotImplemented until the
// Implementation phase wires in evidence verification, the refusal path for
// an entangled board already owned by a different real household (FR59.3,
// naming FR51/FR54/FR74), the single-transaction SCD2 move of
// board_ownership/region.household_id/plant.household_id, the
// household-owned-plant-type copy (copyOwnedPlantTypes -- see this file's
// doc comment and the issue's Implementation section), and the
// departure_record write (migration 022_departure_record.up.sql) on the
// losing side.
func (r *Repository) TransferClosure(ctx context.Context, boardID, destinationHouseholdID int64, releaseToken, dischargedChallengeHandle, actorSubject, reason string) (HouseholdRow, error) {
	return HouseholdRow{}, ErrClosureOpNotImplemented
}

// PreviewClosure returns boardID's ownership closure without moving
// anything -- the read-only preview a caller uses before deciding whether
// to adopt or transfer. Fully implemented: it is a thin wrapper over
// ComputeClosure, which is itself read-only (see this file's doc comment).
func (r *Repository) PreviewClosure(ctx context.Context, boardID int64) (Closure, error) {
	return r.ComputeClosure(ctx, boardID)
}
