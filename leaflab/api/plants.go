package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/whale-net/everything/leaflab/api/audit"
	"github.com/whale-net/everything/leaflab/api/authz"
	"github.com/whale-net/everything/leaflab/api/capture"
	"github.com/whale-net/everything/leaflab/api/contract"
	"github.com/whale-net/everything/leaflab/api/placement"
)

// This file is #1377's implementation of FR54/FR24/FR22.3/FR22.1 (plant
// lifecycle) -- api.proto's Plant RPC set (CreatePlant, CorrectPlant,
// MovePlant, RetirePlant, GetPlant, ListPlants, GetPlantPlacementTimeline).
// It follows 015_ownership's scaffold/feat split, the same one
// PushDeviceConfig/RetireBoard already follow in repository.go and
// #1376's regions.go: read paths select against real SQL with no
// business-rule gate beyond default-listing exclusion; the write paths
// below additionally enforce:
//
//   - CreatePlant/MovePlant write through leaflab/api/placement.MoveTx --
//     FR19's SCD2 close-and-open path, applying FR59.3's no-back-dating
//     refusal, run inside this file's own transaction rather than
//     placement.Writer.Move's single-shot begin/commit, so the write can
//     be combined with the two steps below in one commit. #1360
//     (plan/1166-v2-1360, capture.Recorder/leaflab/api/capture) is merged
//     into this branch's lineage for this -- it was not among #1377's
//     stated dependencies (#1376, #1357), the same gap #1431 recorded for
//     #1379's AssignSensorRegion; #1379 solved it the same way (merging
//     plan/1166-v2-1360 in directly) and this is the first production
//     caller of placement.MoveTx, following that precedent;
//   - FR20's phase-one boundary capture (capture.Recorder.Record), in the
//     same transaction as every placement write -- see
//     sensorsInRegionSubtrees below for how "affected sensors" is computed
//     for a plant move (the union of the old and new region's sensor
//     subtrees) versus a create (just the entered region's subtree);
//   - CorrectPlant/MovePlant are distinct operations (FR24): CorrectPlant
//     never writes a plant_region_history interval, MovePlant never
//     touches name/plant_type_id -- enforced at the *request* shape
//     already (CorrectPlantRequest has no region_id field,
//     MovePlantRequest has no name/plant_type_id field) and mirrored here
//     by each function's SQL only ever touching its own columns;
//   - FR1.2's cross-household guard: a plant may not be placed into a
//     region belonging to another household. The issue text names
//     authz.AssertSameHousehold, which does not exist on this branch
//     lineage even after the #1360 merge above (grep the repo: no
//     "AssertSameHousehold" symbol -- it lives in leaflab/api/authz/
//     invariant.go, introduced by plan/1166-v2-1340, not an ancestor of
//     this branch and not merged in, per the scaffold's noted substitute).
//     assertRegionHousehold below is that substitute: it compares
//     Repository.CurrentHouseholdForRegion against the writer's own
//     household directly (Repository.CurrentHouseholdForPlant for
//     MovePlant, the newly-authorized region's own household for
//     CreatePlant), returning the same contract.InvalidArgument shape and
//     "belongs to a different household" reason AssertSameHousehold uses
//     on #1379's branch, so the two stay observationally identical to a
//     caller even though this file can't import that symbol yet;
//   - member-or-grantee authorization (FR7) via authz.MemberOrGrantee --
//     does not exist on this branch lineage yet (#1344 landed on a
//     divergent v2 branch, same gap #1417/#1427 already recorded).
//     Standing in with the member-only check authorizePlantWrite already
//     provides (mirroring authorizeRegionWrite's shape); the
//     grantee-can-<verb> test this gap blocks is the same one #1417/#1427
//     already noted -- no new scope note filed here, this is that same
//     gap;
//   - FR8 audit recording, via Repository.auditedWrite (see RetireBoard's
//     use of it for the shape) -- CreatePlant/CorrectPlant/MovePlant/
//     RetirePlant's full method names still need adding to
//     audit_registry.go's declaredWriteMethods/auditRegistrations once
//     server.go wires these RPCs onto the wire, matching RetireRegion's
//     own deferred-wiring precedent (regions.go).
//
// No server.go handler wiring yet -- pb.UnimplementedLeafLabAPIServer
// still covers all seven RPCs on the wire; every function below is
// exercised directly by the Testing phase, the same way RetireBoard and
// the region RPC set are today.

// ErrPlantNotFound is returned by plant lookups/operations when plant_id
// names no row, and also -- per NFR2's "no existence oracle" -- when
// plant_id names a row outside the caller's authz.Scope. The two cases are
// indistinguishable to a caller by design; see authorizeRegionWrite's
// identical doc comment for the region case this mirrors.
var ErrPlantNotFound = errors.New("plant not found")

// ErrPlantAlreadyRetired is returned by RetirePlant when the plant is
// already retired -- retirement is not idempotent-by-design, mirroring
// ErrBoardAlreadyRetired/ErrRegionAlreadyRetired.
var ErrPlantAlreadyRetired = errors.New("plant already retired")

// PlantRow is one plant row as read from the plant table. region_id is the
// current-value cache plant.region_id (kept in sync by
// leaflab/api/placement.Writer.Move on every recorded move -- see
// migration 017's doc comment), not a plant_region_history join; a
// caller wanting the full placement history uses GetPlantPlacementTimeline.
type PlantRow struct {
	PlantID     int64
	RegionID    int64
	PlantTypeID int64
	Name        string
	CreatedAt   time.Time
	RemovedAt   *time.Time
}

// ListPlants returns up to limit plants ordered by plant_id,
// keyset-paginated on (plant_id) per FR61 -- same shape as
// Repository.ListRegions. Retired plants are excluded (FR22.1's default-
// listing guard), and scope is applied inside the query via scope.Filter()
// (FR5.1/FR5.2) against plant.household_id directly -- unlike region,
// plant carries household_id on every row (migration 015), so no
// household-resolving view is needed here.
func (r *Repository) ListPlants(ctx context.Context, afterPlantID int64, hasAfter bool, limit int32, scope authz.Scope) ([]PlantRow, error) {
	var sqlQuery string
	var args []any
	if hasAfter {
		filter, filterArgs := scope.Filter(3)
		sqlQuery = fmt.Sprintf(`
			SELECT plant_id, region_id, plant_type_id, name, created_at, removed_at
			FROM plant
			WHERE plant_id > $1
			  AND removed_at IS NULL
			  AND (%s)
			ORDER BY plant_id
			LIMIT $2
		`, filter)
		args = append([]any{afterPlantID, limit}, filterArgs...)
	} else {
		filter, filterArgs := scope.Filter(2)
		sqlQuery = fmt.Sprintf(`
			SELECT plant_id, region_id, plant_type_id, name, created_at, removed_at
			FROM plant
			WHERE removed_at IS NULL
			  AND (%s)
			ORDER BY plant_id
			LIMIT $1
		`, filter)
		args = append([]any{limit}, filterArgs...)
	}

	rows, err := r.db.Query(ctx, sqlQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("list plants: %w", err)
	}
	defer rows.Close()

	var plants []PlantRow
	for rows.Next() {
		var row PlantRow
		if err := rows.Scan(&row.PlantID, &row.RegionID, &row.PlantTypeID, &row.Name, &row.CreatedAt, &row.RemovedAt); err != nil {
			return nil, fmt.Errorf("scan plant: %w", err)
		}
		plants = append(plants, row)
	}
	return plants, rows.Err()
}

// GetPlantByID returns a plant by its numeric id regardless of retired
// state -- the FR22.3/FR22.1 "remains readable by explicit id" half of the
// retired-plant guard; ListPlants is the half that excludes it from
// default listings. Mirrors Repository.GetRegionByID.
func (r *Repository) GetPlantByID(ctx context.Context, plantID int64) (PlantRow, error) {
	var row PlantRow
	err := r.db.QueryRow(ctx, `
		SELECT plant_id, region_id, plant_type_id, name, created_at, removed_at
		FROM plant
		WHERE plant_id = $1
	`, plantID).Scan(&row.PlantID, &row.RegionID, &row.PlantTypeID, &row.Name, &row.CreatedAt, &row.RemovedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PlantRow{}, ErrPlantNotFound
		}
		return PlantRow{}, fmt.Errorf("get plant %d: %w", plantID, err)
	}
	return row, nil
}

// PlacementInterval is one plant_region_history row (FR19), as returned by
// GetPlantPlacementTimeline.
type PlacementInterval struct {
	RegionID          int64
	ValidFrom         time.Time
	ValidTo           *time.Time
	RelocationInduced bool
}

// GetPlantPlacementTimeline returns plantID's plant_region_history
// intervals ordered oldest-to-newest (FR19), each carrying the
// relocation_induced flag FR24's second half needs a home for (populated
// by FR74's sibling task -- always false until then). ok is false when
// plantID names no row, mirroring GetRegionPath's shape. Works for a
// retired plant too (FR22.3's "placement timeline remains readable").
func (r *Repository) GetPlantPlacementTimeline(ctx context.Context, plantID int64) (intervals []PlacementInterval, ok bool, err error) {
	if _, getErr := r.GetPlantByID(ctx, plantID); getErr != nil {
		if errors.Is(getErr, ErrPlantNotFound) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("get plant %d for placement timeline: %w", plantID, getErr)
	}

	rows, queryErr := r.db.Query(ctx, `
		SELECT region_id, valid_from, valid_to, relocation_induced
		FROM plant_region_history
		WHERE plant_id = $1
		ORDER BY valid_from
	`, plantID)
	if queryErr != nil {
		return nil, false, fmt.Errorf("get placement timeline for plant %d: %w", plantID, queryErr)
	}
	defer rows.Close()

	for rows.Next() {
		var iv PlacementInterval
		if scanErr := rows.Scan(&iv.RegionID, &iv.ValidFrom, &iv.ValidTo, &iv.RelocationInduced); scanErr != nil {
			return nil, false, fmt.Errorf("scan placement interval for plant %d: %w", plantID, scanErr)
		}
		intervals = append(intervals, iv)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, false, fmt.Errorf("get placement timeline for plant %d: %w", plantID, rowsErr)
	}
	return intervals, true, nil
}

// authorizePlantWrite resolves plantID against scope and collapses
// "doesn't exist" and "exists, out of caller's scope" into the same
// ErrPlantNotFound (NFR2) -- the member-only stand-in for FR7's
// member-or-grantee capability (authz.MemberOrGrantee), which does not
// exist on this branch lineage; see this file's doc comment. Mirrors
// authorizeRegionWrite's identical shape for the region case.
func (r *Repository) authorizePlantWrite(ctx context.Context, plantID int64, scope authz.Scope) (authz.Resolution, error) {
	resolver := authz.NewPGResolver(r.db)
	ref := authz.EntityRef{Kind: authz.EntityPlant, ID: plantID}
	res, err := resolver.Resolve(ctx, ref)
	if err != nil {
		if errors.Is(err, authz.ErrNotFound) {
			return authz.Resolution{}, ErrPlantNotFound
		}
		return authz.Resolution{}, fmt.Errorf("resolve plant %d: %w", plantID, err)
	}
	if !scope.Permits(ref, res) {
		return authz.Resolution{}, ErrPlantNotFound
	}
	return res, nil
}

// validatePlantName returns a persona-appropriate reason (FR59.2) if name
// is invalid once trimmed, or "" if it's valid. Mirrors validateRegionName
// (regions.go).
func validatePlantName(name string) string {
	if strings.TrimSpace(name) == "" {
		return "A plant name is required."
	}
	return ""
}

// assertRegionHousehold is FR1.2's enforcement point for CreatePlant/
// MovePlant: regionID must resolve to writerHousehold or the write is
// refused, naming region_id as the offending field. This is the substitute
// for the missing authz.AssertSameHousehold this file's doc comment
// describes -- same contract.InvalidArgument shape and "belongs to a
// different household" reason, built from
// Repository.CurrentHouseholdForRegion instead. ErrNoHousehold (region
// does not exist, or resolves to no household -- should not occur
// post-backfill) becomes the same invalid_argument a caller sees for any
// other unresolvable reference; everything else is wrapped as an internal
// error for the caller above to translate.
func (r *Repository) assertRegionHousehold(ctx context.Context, writerHousehold, regionID int64) error {
	regionHousehold, err := r.CurrentHouseholdForRegion(ctx, regionID)
	if err != nil {
		if errors.Is(err, ErrNoHousehold) {
			return contract.InvalidArgument("plant", "region_id", "This references something that doesn't exist.")
		}
		return fmt.Errorf("assert plant region household: check region %d: %w", regionID, err)
	}
	if regionHousehold != writerHousehold {
		return contract.InvalidArgument("plant", "region_id", "This references something that belongs to a different household.")
	}
	return nil
}

// sensorsInRegionSubtrees returns the distinct sensor_id of every sensor
// whose sensor.region_id falls within any of rootRegionIDs' subtrees (a
// root itself or any descendant) -- FR20's "affected sensors" computation
// for a placement boundary (leaflab/api/capture.Recorder.Record's doc
// comment: "the sensors in the region subtree the plant left or entered").
// A plant move passes both the old and new region as roots in one call (a
// sensor with no closer plant of its own re-attributes on either side of
// the boundary); a plant create passes just the entered region. Mirrors
// SetRegionParent's WITH RECURSIVE subtree query (regions.go) over sensor
// instead of sensor_reading, run inside tx so it observes the same
// snapshot the placement write and capture rows are recorded against.
func sensorsInRegionSubtrees(ctx context.Context, tx pgx.Tx, rootRegionIDs []int64) ([]int64, error) {
	rows, err := tx.Query(ctx, `
		WITH RECURSIVE subtree AS (
			SELECT region_id FROM region WHERE region_id = ANY($1)

			UNION ALL

			SELECT r.region_id
			FROM region r
			JOIN subtree s ON r.parent_region_id = s.region_id
		)
		SELECT DISTINCT sensor_id FROM sensor WHERE region_id IN (SELECT region_id FROM subtree)
	`, rootRegionIDs)
	if err != nil {
		return nil, fmt.Errorf("compute affected sensors for regions %v: %w", rootRegionIDs, err)
	}
	defer rows.Close()

	var sensorIDs []int64
	for rows.Next() {
		var sensorID int64
		if err := rows.Scan(&sensorID); err != nil {
			return nil, fmt.Errorf("scan affected sensor for regions %v: %w", rootRegionIDs, err)
		}
		sensorIDs = append(sensorIDs, sensorID)
	}
	return sensorIDs, rows.Err()
}

// CreatePlant creates a plant and places it into a region in one operation
// (FR54). Validates name, authorizes regionID like any other region write
// (authorizeRegionWrite -- this is also FR1.2's guard for the create case,
// since a region the caller's own scope does not permit resolves to
// ErrRegionNotFound, never reaching the insert below), opens the plant's
// first plant_region_history interval through placement.MoveTx (FR19), and
// records FR20's phase-one boundary capture for the entered region's
// sensor subtree in the same transaction as both.
func (r *Repository) CreatePlant(ctx context.Context, regionID, plantTypeID int64, name string, scope authz.Scope, entry audit.Entry) (PlantRow, error) {
	if reason := validatePlantName(name); reason != "" {
		return PlantRow{}, contract.InvalidArgument("plant", "name", reason)
	}
	name = strings.TrimSpace(name)

	regionRes, err := r.authorizeRegionWrite(ctx, regionID, scope)
	if err != nil {
		if errors.Is(err, ErrRegionNotFound) {
			return PlantRow{}, contract.InvalidArgument("plant", "region_id", "This references something that doesn't exist.")
		}
		return PlantRow{}, err
	}
	householdID := regionRes.HouseholdID

	var row PlantRow
	writeErr := r.auditedWrite(ctx, func(tx pgx.Tx) (audit.Entry, error) {
		if err := tx.QueryRow(ctx, `
			INSERT INTO plant (region_id, plant_type_id, name, household_id)
			VALUES ($1, $2, $3, $4)
			RETURNING plant_id, region_id, plant_type_id, name, created_at, removed_at
		`, regionID, plantTypeID, name, householdID).Scan(
			&row.PlantID, &row.RegionID, &row.PlantTypeID, &row.Name, &row.CreatedAt, &row.RemovedAt,
		); err != nil {
			return audit.Entry{}, fmt.Errorf("insert plant: %w", err)
		}

		// Opens the plant's first plant_region_history interval through
		// the Phase 3 SCD2 writer (FR19) -- a brand-new plant has no prior
		// interval to close, so this closes zero rows and opens exactly
		// one, the same shape as any later MovePlant.
		validFrom, err := placement.MoveTx(ctx, tx, row.PlantID, regionID, time.Now())
		if err != nil {
			return audit.Entry{}, err
		}
		row.RegionID = regionID

		affectedSensors, err := sensorsInRegionSubtrees(ctx, tx, []int64{regionID})
		if err != nil {
			return audit.Entry{}, err
		}
		if err := capture.NewRecorder().Record(ctx, tx, affectedSensors, validFrom); err != nil {
			return audit.Entry{}, fmt.Errorf("record boundary capture for new plant %d: %w", row.PlantID, err)
		}

		idStr := strconv.FormatInt(row.PlantID, 10)
		entry.EntityID = &idStr
		entry.TargetHouseholdID = &householdID
		return entry, nil
	})
	if writeErr != nil {
		return PlantRow{}, writeErr
	}
	return row, nil
}

// CorrectPlant fixes a plant's name and/or plant type (FR24) -- never
// writes a plant_region_history interval; that is MovePlant's job. Either
// field may be nil to leave it unchanged (COALESCE against the existing
// value); at least one of name/plantTypeID should be set by the caller,
// but CorrectPlant does not itself require it -- a no-op correction still
// authorizes, still refuses on a retired plant and still audits, matching
// RenameRegion's precedent of not special-casing "nothing actually
// changed".
func (r *Repository) CorrectPlant(ctx context.Context, plantID int64, name *string, plantTypeID *int64, scope authz.Scope, entry audit.Entry) (PlantRow, error) {
	if name != nil {
		if reason := validatePlantName(*name); reason != "" {
			return PlantRow{}, contract.InvalidArgument("plant", "name", reason)
		}
		trimmed := strings.TrimSpace(*name)
		name = &trimmed
	}

	res, err := r.authorizePlantWrite(ctx, plantID, scope)
	if err != nil {
		return PlantRow{}, err
	}

	var row PlantRow
	writeErr := r.auditedWrite(ctx, func(tx pgx.Tx) (audit.Entry, error) {
		err := tx.QueryRow(ctx, `
			UPDATE plant
			SET name = COALESCE($2, name),
			    plant_type_id = COALESCE($3, plant_type_id)
			WHERE plant_id = $1
			  AND removed_at IS NULL
			RETURNING plant_id, region_id, plant_type_id, name, created_at, removed_at
		`, plantID, name, plantTypeID).Scan(
			&row.PlantID, &row.RegionID, &row.PlantTypeID, &row.Name, &row.CreatedAt, &row.RemovedAt,
		)
		if err == nil {
			idStr := strconv.FormatInt(plantID, 10)
			entry.EntityID = &idStr
			hh := res.HouseholdID
			entry.TargetHouseholdID = &hh
			return entry, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return audit.Entry{}, fmt.Errorf("correct plant %d: %w", plantID, err)
		}
		// No row updated -- the plant exists (authorizePlantWrite above
		// confirmed that) and is retired (FR22.1: "accepts no new
		// writes").
		return audit.Entry{}, contract.InvalidArgument("plant", "plant_id", "This plant has been retired and no longer accepts changes.")
	})
	if writeErr != nil {
		return PlantRow{}, writeErr
	}
	return row, nil
}

// MovePlant relocates a plant to a new region (FR54, FR19) -- never
// renames the plant or changes its plant_type; that is CorrectPlant's job
// (FR24). Authorizes the plant (member-only stand-in for FR7), enforces
// FR1.2 (assertRegionHousehold: the new region must belong to the plant's
// own household), refuses on a retired plant (FR22.1), then writes through
// placement.MoveTx (FR19's SCD2 close-and-open, applying FR59.3's
// no-back-dating refusal) and records FR20's phase-one boundary capture
// for the union of the old and new region's sensor subtrees, all in the
// same transaction.
func (r *Repository) MovePlant(ctx context.Context, plantID, newRegionID int64, scope authz.Scope, entry audit.Entry) (PlantRow, error) {
	res, err := r.authorizePlantWrite(ctx, plantID, scope)
	if err != nil {
		return PlantRow{}, err
	}

	if err := r.assertRegionHousehold(ctx, res.HouseholdID, newRegionID); err != nil {
		return PlantRow{}, err
	}

	current, err := r.GetPlantByID(ctx, plantID)
	if err != nil {
		return PlantRow{}, fmt.Errorf("get plant %d: %w", plantID, err)
	}
	if current.RemovedAt != nil {
		return PlantRow{}, contract.InvalidArgument("plant", "plant_id", "This plant has been retired and no longer accepts changes.")
	}
	oldRegionID := current.RegionID

	var row PlantRow
	writeErr := r.auditedWrite(ctx, func(tx pgx.Tx) (audit.Entry, error) {
		validFrom, err := placement.MoveTx(ctx, tx, plantID, newRegionID, time.Now())
		if err != nil {
			return audit.Entry{}, err
		}

		if err := tx.QueryRow(ctx, `
			SELECT plant_id, region_id, plant_type_id, name, created_at, removed_at
			FROM plant
			WHERE plant_id = $1
		`, plantID).Scan(
			&row.PlantID, &row.RegionID, &row.PlantTypeID, &row.Name, &row.CreatedAt, &row.RemovedAt,
		); err != nil {
			return audit.Entry{}, fmt.Errorf("read moved plant %d: %w", plantID, err)
		}

		// Both sides of the boundary re-evaluate attribution (a sensor
		// with no closer plant of its own in the region it just lost or
		// gained the plant's nearest-ancestor coverage from) -- see
		// sensorsInRegionSubtrees' doc comment.
		affectedSensors, err := sensorsInRegionSubtrees(ctx, tx, []int64{oldRegionID, newRegionID})
		if err != nil {
			return audit.Entry{}, err
		}
		if err := capture.NewRecorder().Record(ctx, tx, affectedSensors, validFrom); err != nil {
			return audit.Entry{}, fmt.Errorf("record boundary capture for plant %d move: %w", plantID, err)
		}

		idStr := strconv.FormatInt(plantID, 10)
		entry.EntityID = &idStr
		hh := res.HouseholdID
		entry.TargetHouseholdID = &hh
		return entry, nil
	})
	if writeErr != nil {
		return PlantRow{}, writeErr
	}
	return row, nil
}

// RetirePlant soft-retires a plant (FR22.1, FR22.3): excluded from default
// listings from this point on, accepts no new writes; its readings and
// placement timeline remain reachable through GetPlant and
// GetPlantPlacementTimeline (already implemented above, unconditionally on
// retired state) -- nothing is hard-deleted. Mirrors
// RetireBoard/RetireRegion's shape exactly: an auditedWrite UPDATE ... SET
// removed_at = NOW() WHERE plant_id = $1 AND removed_at IS NULL,
// distinguishing "does not exist" from "already retired" on the
// zero-rows-updated path. Retirement is not idempotent-by-design.
func (r *Repository) RetirePlant(ctx context.Context, plantID int64, scope authz.Scope, entry audit.Entry) (PlantRow, error) {
	res, err := r.authorizePlantWrite(ctx, plantID, scope)
	if err != nil {
		return PlantRow{}, err
	}

	var row PlantRow
	writeErr := r.auditedWrite(ctx, func(tx pgx.Tx) (audit.Entry, error) {
		err := tx.QueryRow(ctx, `
			UPDATE plant
			SET removed_at = NOW()
			WHERE plant_id = $1
			  AND removed_at IS NULL
			RETURNING plant_id, region_id, plant_type_id, name, created_at, removed_at
		`, plantID).Scan(
			&row.PlantID, &row.RegionID, &row.PlantTypeID, &row.Name, &row.CreatedAt, &row.RemovedAt,
		)
		if err == nil {
			idStr := strconv.FormatInt(plantID, 10)
			entry.EntityID = &idStr
			hh := res.HouseholdID
			entry.TargetHouseholdID = &hh
			return entry, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return audit.Entry{}, fmt.Errorf("retire plant %d: %w", plantID, err)
		}
		// No row updated -- the plant exists (authorizePlantWrite above
		// confirmed that) but is already retired: retirement is not
		// idempotent-by-design, mirroring RetireBoard/RetireRegion.
		return audit.Entry{}, ErrPlantAlreadyRetired
	})
	if writeErr != nil {
		return PlantRow{}, writeErr
	}
	return row, nil
}
