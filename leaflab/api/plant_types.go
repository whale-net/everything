package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/whale-net/everything/leaflab/api/audit"
	"github.com/whale-net/everything/leaflab/api/authz"
)

// This file is #1378's scaffold for FR55 (A24) -- the plant-type catalog's
// ownership split -- migration 034's household_id/retired_at columns and
// api.proto's PlantType RPC set (ListPlantTypes, CreatePlantType,
// RenamePlantType, RetirePlantType), plus CreatePlantRequest's new
// new_plant_type oneof branch (SB-1.10's acquire-and-place).
//
// Follows 015_ownership's / plants.go's / regions.go's scaffold/feat split:
// ListPlantTypes and GetPlantTypeByID (the read paths) are implemented
// fully against real SQL below; the three writes (CreatePlantType,
// RenamePlantType, RetirePlantType) are signature-only skeletons that
// return ErrPlantTypeOpNotImplemented until Implementation wires in:
//
//   - the elevation-for-global / member-or-grantee-for-owned authorization
//     split (FR10, FR7) -- authz.MemberOrGrantee does not exist on this
//     branch lineage, the same gap #1417/#1427 already recorded for every
//     other write path in this package (plants.go, regions.go); standing in
//     with a member-only check when that lands here, mirroring
//     authorizePlantWrite/authorizeRegionWrite;
//   - the global-write elevation check itself has no direct precedent to
//     copy: every existing elevation call site (server.go's
//     elevatedBoardScope, Repository.ActiveElevation) checks an unexpired
//     admin_elevation row against one specific target household
//     (admin_elevation.target_household_id is NOT NULL, migration 029), but
//     a global plant_type write is against no household at all -- the
//     issue text's "require elevation ... against nothing" needs a new
//     repository predicate (something like "does this admin_subject hold
//     any unexpired elevation, against any household, right now" instead of
//     a specific one) that does not exist yet. Left as an open design
//     question for whoever implements CreatePlantType/RenamePlantType/
//     RetirePlantType's global-row branch;
//   - FR8 audit recording for all three writes, via Repository.auditedWrite
//     -- the global case's audit row carries TargetHouseholdID = nil per
//     the issue text ("audit it with a null target household, and say so
//     in the audit row"); the owned case carries the owning household;
//   - CreatePlant's (plants.go) extension to accept new_plant_type inline:
//     no server.go RPC handler wiring exists yet for CreatePlant or any
//     PlantType RPC (matches every other scaffold's precedent in this
//     package), so the branch-on-oneof translation from
//     pb.CreatePlantRequest to Repository.CreatePlant's plantTypeID int64
//     parameter is deferred to whichever task adds that wiring -- it does
//     not require changing CreatePlant's own Go signature, only the
//     handler that will eventually call it, which may need a new
//     Repository method (e.g. CreatePlantWithNewType) that creates the
//     household-owned type and the plant in one transaction (SB-1.10: "one
//     transaction, no elevated principal anywhere");
//   - FR77's copyOwnedPlantTypes hook: #1343 (Phase 2, ownership closure
//     transfer, FR70.2-.4/FR77) left this as a named no-op per its own
//     issue text ("leave a clearly-marked, tested seam ... assert the
//     transfer path calls a copyOwnedPlantTypes hook that is a no-op
//     today"), to be implemented here once plant_type.household_id exists.
//     That no-op does not exist on this branch lineage -- #1378's stated
//     dependencies are #1377 and #1345, neither reaching #1343
//     (plan/1166-v2-1343 is a sibling branch, not an ancestor) -- grep the
//     repo: no "copyOwnedPlantTypes" symbol anywhere on this lineage. Filed
//     as a scope note documenting this cross-branch coordination gap.
//
// No server.go handler wiring yet -- pb.UnimplementedLeafLabAPIServer still
// covers all four PlantType RPCs on the wire; every function below is
// exercised directly by the Testing phase, the same way the plant/region
// RPC sets are today.

// ErrPlantTypeNotFound is returned by plant-type lookups/operations when
// plant_type_id names no row, and also -- per NFR2's "no existence oracle"
// -- when plant_type_id names a household-owned row outside the caller's
// scope (never for a global row, which every authenticated principal can
// read). Mirrors ErrPlantNotFound/ErrRegionNotFound's identical role.
var ErrPlantTypeNotFound = errors.New("plant type not found")

// ErrPlantTypeAlreadyRetired is returned by RetirePlantType when the type
// is already retired -- retirement is not idempotent-by-design, mirroring
// ErrPlantAlreadyRetired/ErrRegionAlreadyRetired/ErrBoardAlreadyRetired.
var ErrPlantTypeAlreadyRetired = errors.New("plant type already retired")

// ErrPlantTypeOpNotImplemented is returned by every write below until
// Implementation lands -- see this file's doc comment for what's blocking
// each one.
var ErrPlantTypeOpNotImplemented = errors.New("plant type operation not implemented")

// ErrPlantTypeReferenced is returned by RetirePlantType when one or more
// plants still reference the type (FR59.3) -- in either ownership class.
// Nothing is hard-deleted; see this file's doc comment.
var ErrPlantTypeReferenced = errors.New("plant type is referenced by one or more plants")

// PlantTypeRow is one plant_type row as read from the plant_type table.
// HouseholdID is nil for a global row (migration 034) -- FR55's
// distinguishability requirement lives in this field, not in a separate
// boolean, so a caller (and this row's JSON/proto projection) can name
// *which* household owns an owned row rather than only whether one does.
type PlantTypeRow struct {
	PlantTypeID int64
	CommonName  string
	Species     *string
	HouseholdID *int64
	RetiredAt   *time.Time
}

// ListPlantTypes returns global rows (household_id IS NULL) plus the
// caller's own household-owned rows, keyset-paginated on (plant_type_id)
// per FR61 -- same shape as Repository.ListPlants/ListRegions. Retired
// types are excluded (FR22.1's default-listing guard). Deliberately does
// not use scope.Filter alone: a plain Filter call assumes every listed row
// belongs to *some* household (HouseholdScope.Filter's doc comment), which
// would wrongly exclude every global row -- "household_id IS NULL OR (%s)"
// ORs the global population in ahead of the caller's own scope fragment.
func (r *Repository) ListPlantTypes(ctx context.Context, afterPlantTypeID int64, hasAfter bool, limit int32, scope authz.Scope) ([]PlantTypeRow, error) {
	var sqlQuery string
	var args []any
	if hasAfter {
		filter, filterArgs := scope.Filter(3)
		sqlQuery = fmt.Sprintf(`
			SELECT plant_type_id, common_name, species, household_id, retired_at
			FROM plant_type
			WHERE plant_type_id > $1
			  AND retired_at IS NULL
			  AND (household_id IS NULL OR (%s))
			ORDER BY plant_type_id
			LIMIT $2
		`, filter)
		args = append([]any{afterPlantTypeID, limit}, filterArgs...)
	} else {
		filter, filterArgs := scope.Filter(2)
		sqlQuery = fmt.Sprintf(`
			SELECT plant_type_id, common_name, species, household_id, retired_at
			FROM plant_type
			WHERE retired_at IS NULL
			  AND (household_id IS NULL OR (%s))
			ORDER BY plant_type_id
			LIMIT $1
		`, filter)
		args = append([]any{limit}, filterArgs...)
	}

	rows, err := r.db.Query(ctx, sqlQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("list plant types: %w", err)
	}
	defer rows.Close()

	var plantTypes []PlantTypeRow
	for rows.Next() {
		var row PlantTypeRow
		if err := rows.Scan(&row.PlantTypeID, &row.CommonName, &row.Species, &row.HouseholdID, &row.RetiredAt); err != nil {
			return nil, fmt.Errorf("scan plant type: %w", err)
		}
		plantTypes = append(plantTypes, row)
	}
	return plantTypes, rows.Err()
}

// GetPlantTypeByID returns a plant type by its numeric id regardless of
// retired state -- the FR22.1 "remains readable by explicit id" half of the
// retired-type guard; ListPlantTypes is the half that excludes it from
// default listings. Does not itself authorize a household-owned row against
// scope -- see authorizePlantTypeWrite for that -- so this is also usable by
// read paths (e.g. resolving a plant's plant_type_id for display) that must
// work for any global row regardless of caller.
func (r *Repository) GetPlantTypeByID(ctx context.Context, plantTypeID int64) (PlantTypeRow, error) {
	var row PlantTypeRow
	err := r.db.QueryRow(ctx, `
		SELECT plant_type_id, common_name, species, household_id, retired_at
		FROM plant_type
		WHERE plant_type_id = $1
	`, plantTypeID).Scan(&row.PlantTypeID, &row.CommonName, &row.Species, &row.HouseholdID, &row.RetiredAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PlantTypeRow{}, ErrPlantTypeNotFound
		}
		return PlantTypeRow{}, fmt.Errorf("get plant type %d: %w", plantTypeID, err)
	}
	return row, nil
}

// authorizePlantTypeWrite resolves plantTypeID and confirms the caller may
// write to it: a global row (HouseholdID nil) resolves for any
// authenticated caller here (the elevation check itself -- required before
// an actual global write -- is a separate, not-yet-implemented gate; see
// this file's doc comment), an owned row requires scope to cover its
// household. "Doesn't exist" and "exists, owned by a household outside the
// caller's scope" collapse to the same ErrPlantTypeNotFound (NFR2), mirroring
// authorizePlantWrite/authorizeRegionWrite -- except a global row is never
// collapsed this way, since FR55 makes it unconditionally readable.
func (r *Repository) authorizePlantTypeWrite(ctx context.Context, plantTypeID int64, scope authz.Scope) (PlantTypeRow, error) {
	row, err := r.GetPlantTypeByID(ctx, plantTypeID)
	if err != nil {
		return PlantTypeRow{}, err
	}
	if row.HouseholdID == nil {
		// Global row: resolvable by anyone. The elevation check that must
		// gate an actual write to it is not applied here -- see this file's
		// doc comment.
		return row, nil
	}
	for _, hh := range authz.HouseholdIDs(scope) {
		if hh == *row.HouseholdID {
			return row, nil
		}
	}
	return PlantTypeRow{}, ErrPlantTypeNotFound
}

// validatePlantTypeCommonName returns a persona-appropriate reason (FR59.2)
// if commonName is invalid once trimmed, or "" if it's valid. Mirrors
// validatePlantName/validateRegionName's identical shape.
func validatePlantTypeCommonName(commonName string) string {
	if strings.TrimSpace(commonName) == "" {
		return "A plant type name is required."
	}
	return ""
}

// CreatePlantType creates a plant type (FR55, A24) -- a global row
// (household_id NULL) under FR10 elevation, or a household-owned row under
// FR7 member-or-grantee, no elevation. Not yet implemented -- see this
// file's doc comment for what's blocking it (principally: the elevation
// predicate for a global write has no existing repository method to call,
// since every current elevation check is scoped to one specific target
// household and a global row has none).
func (r *Repository) CreatePlantType(ctx context.Context, commonName string, species *string, global bool, scope authz.Scope, entry audit.Entry) (PlantTypeRow, error) {
	return PlantTypeRow{}, ErrPlantTypeOpNotImplemented
}

// RenamePlantType renames a plant type (FR55) -- same elevation-for-global
// / member-or-grantee-for-owned split as CreatePlantType. Not yet
// implemented; see this file's doc comment.
func (r *Repository) RenamePlantType(ctx context.Context, plantTypeID int64, commonName string, scope authz.Scope, entry audit.Entry) (PlantTypeRow, error) {
	return PlantTypeRow{}, ErrPlantTypeOpNotImplemented
}

// RetirePlantType soft-retires a plant type (FR22.1) -- refused (FR59.3),
// naming the referencing plants, while any plant still references it, in
// either ownership class. Nothing is hard-deleted. Not yet implemented; see
// this file's doc comment. (The FR59.3 refusal shape itself --
// ErrPlantTypeReferenced plus a query naming the referencing plants -- is
// straightforward and does not block on the elevation gap
// CreatePlantType/RenamePlantType do, but is left unimplemented here so
// Implementation lands all three writes' authorization story together.)
func (r *Repository) RetirePlantType(ctx context.Context, plantTypeID int64, scope authz.Scope, entry audit.Entry) (PlantTypeRow, error) {
	return PlantTypeRow{}, ErrPlantTypeOpNotImplemented
}
