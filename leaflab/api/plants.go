package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/whale-net/everything/leaflab/api/audit"
	"github.com/whale-net/everything/leaflab/api/authz"
)

// This file is #1377's scaffolded wire surface for FR54/FR24/FR22.3/FR22.1
// (plant lifecycle) -- api.proto's Plant RPC set (CreatePlant, CorrectPlant,
// MovePlant, RetirePlant, GetPlant, ListPlants, GetPlantPlacementTimeline).
// It follows 015_ownership's scaffold/feat split, the same one PushDeviceConfig/
// RetireBoard already follow in repository.go and #1376's own scaffold set
// for regions.go: read paths are fully implemented below against real SQL;
// the four write paths are signature-only skeletons that return
// ErrPlantOpNotImplemented until the Implementation phase wires in:
//
//   - CreatePlant/MovePlant writing through leaflab/api/placement.Writer
//     (FR19's SCD2 close-and-open path, FR59.3's no-back-dating refusal);
//   - FR20's phase-one boundary capture (leaflab/api/capture.Recorder.Record),
//     in the same transaction as every placement write. That package does
//     not exist on this branch lineage yet -- #1377 depends on #1376 and
//     #1357, neither of which is #1360 (plan/1166-v2-1360, which
//     introduces leaflab/api/capture) or an ancestor of it. This is the
//     same gap #1431 already recorded for #1379's AssignSensorRegion; see
//     the scope note filed alongside this commit for #1377's instance of
//     it;
//   - CorrectPlant/MovePlant being distinct operations (FR24): CorrectPlant
//     must never write a plant_region_history interval, MovePlant must
//     never touch name/plant_type_id -- both skeletons already enforce
//     this at the *request* shape (CorrectPlantRequest has no region_id
//     field, MovePlantRequest has no name/plant_type_id field), so the
//     Implementation phase only needs to route each request's fields into
//     the right write, not re-derive which operation is which;
//   - FR1.2's cross-household guard: a plant may not be placed into a
//     region belonging to another household. The issue text names
//     authz.AssertSameHousehold, which does not exist on this branch
//     lineage (grep the repo: no "AssertSameHousehold" symbol anywhere,
//     the same kind of gap #1417/#1427 recorded for authz.MemberOrGrantee).
//     Unlike that gap, this one has a direct substitute already in scope:
//     Repository.CurrentHouseholdForRegion and
//     Repository.CurrentHouseholdForPlant (repository.go) resolve the two
//     household ids to compare directly, the same primitive
//     authz.AssertSameHousehold would presumably wrap -- Implementation
//     should reach for those rather than block on the missing helper;
//   - member-or-grantee authorization (FR7) via authz.MemberOrGrantee --
//     which also does not exist on this branch lineage yet (#1344 landed on
//     a divergent v2 branch, same gap #1417/#1427 already recorded).
//     Implementation should stand in with the member-only check
//     authorizePlantWrite below already provides (mirroring
//     authorizeRegionWrite's shape) and file a scope note for the
//     grantee-can-<verb> test, exactly as #1417/#1427 did;
//   - FR8 audit recording, via Repository.auditedWrite (see RetireBoard's
//     use of it for the shape) -- and adding CreatePlant/CorrectPlant/
//     MovePlant/RetirePlant's full method names to audit_registry.go's
//     declaredWriteMethods/auditRegistrations once they're wired into
//     server.go.
//
// No server.go handler wiring yet -- pb.UnimplementedLeafLabAPIServer
// covers all seven new RPCs for now, same as the region RPC set's own
// scaffold commit (43df7c41).

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

// ErrPlantOpNotImplemented is returned by CreatePlant, CorrectPlant,
// MovePlant and RetirePlant until the Implementation phase wires in the
// business rules named in this file's doc comment above. Signature-only
// for now, mirroring ErrRegionOpNotImplemented's role in #1376's scaffold.
var ErrPlantOpNotImplemented = errors.New("plant operation not implemented")

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

// CreatePlant creates a plant and places it into a region in one operation
// (FR54). Signature-only until Implementation wires in: name/region
// validation, FR1.2's cross-household guard (region must belong to the
// caller's household), the Phase 3 SCD2 placement writer
// (leaflab/api/placement.Writer.Move) opening the plant's first
// plant_region_history interval, FR20's phase-one boundary capture in the
// same transaction, FR7 authorization and FR8 audit recording. See this
// file's doc comment.
func (r *Repository) CreatePlant(ctx context.Context, regionID, plantTypeID int64, name string, scope authz.Scope, entry audit.Entry) (PlantRow, error) {
	return PlantRow{}, ErrPlantOpNotImplemented
}

// CorrectPlant fixes a plant's name and/or plant type (FR24) -- never
// writes a plant_region_history interval; that is MovePlant's job.
// Signature-only until Implementation wires in: validation, FR7
// authorization and FR8 audit recording. See this file's doc comment.
func (r *Repository) CorrectPlant(ctx context.Context, plantID int64, name *string, plantTypeID *int64, scope authz.Scope, entry audit.Entry) (PlantRow, error) {
	return PlantRow{}, ErrPlantOpNotImplemented
}

// MovePlant relocates a plant to a new region (FR54, FR19) -- never
// renames the plant or changes its plant_type; that is CorrectPlant's job
// (FR24). Signature-only until Implementation wires in: the Phase 3 SCD2
// placement writer (leaflab/api/placement.Writer.Move, including its
// no-back-dating refusal), FR20's phase-one boundary capture in the same
// transaction, FR1.2's cross-household guard, FR7 authorization and FR8
// audit recording. See this file's doc comment.
func (r *Repository) MovePlant(ctx context.Context, plantID, newRegionID int64, scope authz.Scope, entry audit.Entry) (PlantRow, error) {
	return PlantRow{}, ErrPlantOpNotImplemented
}

// RetirePlant soft-retires a plant (FR22.1, FR22.3): excluded from default
// listings from this point on, accepts no new writes; its readings and
// placement timeline remain reachable through GetPlant and
// GetPlantPlacementTimeline (already implemented above, unconditionally on
// retired state) -- nothing is hard-deleted. Signature-only until
// Implementation wires in: FR7 authorization and FR8 audit recording, and
// the not-idempotent ErrPlantAlreadyRetired distinction (mirroring
// RetireBoard/RetireRegion's shape). See this file's doc comment.
func (r *Repository) RetirePlant(ctx context.Context, plantID int64, scope authz.Scope, entry audit.Entry) (PlantRow, error) {
	return PlantRow{}, ErrPlantOpNotImplemented
}
