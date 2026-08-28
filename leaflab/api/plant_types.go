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

// This file is #1378's Implementation of FR55 (A24) -- the plant-type
// catalog's ownership split -- migration 034's household_id/retired_at
// columns and api.proto's PlantType RPC set (ListPlantTypes,
// CreatePlantType, RenamePlantType, RetirePlantType), plus
// CreatePlantWithNewType (backing CreatePlantRequest's new_plant_type
// oneof branch, SB-1.10's acquire-and-place).
//
// Follows 015_ownership's / plants.go's / regions.go's scaffold/feat split:
// ListPlantTypes and GetPlantTypeByID (the read paths) were already
// implemented against real SQL by the scaffold; this file adds:
//
//   - member-or-grantee authorization (FR7) for an owned-row write --
//     authz.MemberOrGrantee does not exist on this branch lineage (the
//     same gap #1417/#1427 already recorded for plants.go/regions.go).
//     Standing in with the member-only check authorizePlantTypeWrite
//     already provides, mirroring authorizePlantWrite/authorizeRegionWrite;
//   - FR10 elevation for a global-row write, via the new
//     Repository.AnyActiveElevation (repository.go): unlike every existing
//     elevation check (ActiveElevation, scoped to one target household), a
//     global plant_type write is against no household at all, so this
//     checks only that adminSubject holds *some* unexpired elevation right
//     now -- see requireGlobalWriteElevation and AnyActiveElevation's doc
//     comments;
//   - FR8 audit recording for all three writes, via Repository.auditedWrite:
//     the global case's audit row carries TargetHouseholdID = nil and a
//     self-explanatory Reason when the caller didn't already supply one
//     ("audit it with a null target household, and say so in the audit
//     row" -- see requireGlobalWriteElevation's call sites below); the
//     owned case carries the owning household;
//   - CreatePlantWithNewType: SB-1.10's acquire-and-place -- creates a
//     household-owned plant type and places a new plant against it in one
//     transaction, with no elevated principal anywhere in the call. Backs
//     CreatePlantRequest's new_plant_type oneof branch once a later task
//     wires server.go's CreatePlant handler onto it (no RPC handler wiring
//     exists yet for CreatePlant or any PlantType RPC -- matches every
//     other scaffold's precedent in this package: plants.go/regions.go's
//     writes are exercised directly by integration tests today, not
//     through a live gRPC handler);
//   - FR59.3's referenced-type retirement refusal (plantsReferencingType),
//     naming every active plant still referencing the type, for both
//     ownership classes.
//
// FR77's copyOwnedPlantTypes hook is deliberately NOT implemented here --
// see #1454 (filed by this branch's scaffold commit): the no-op it must
// replace lives on #1343's branch (plan/1166-v2-1343, ownership closure
// transfer), which is not an ancestor of this branch's dependency chain
// (#1377, #1345). Neither branch alone can land the real hook body -- it
// needs both plant_type.household_id (this branch) and the TransferClosure
// call site + departure-record plumbing (#1343's branch). That remains
// whoever integrates the two branches' job, per #1454.
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
// authenticated caller here -- the elevation check that must additionally
// gate an actual global-row write is a separate step
// (requireGlobalWriteElevation), applied by each write function after this
// one succeeds, never folded in here, since GetPlantTypeByID/this function
// are also used by paths that only need to resolve a global row, not write
// it. An owned row requires scope to cover its household. "Doesn't exist"
// and "exists, owned by a household outside the caller's scope" collapse
// to the same ErrPlantTypeNotFound (NFR2), mirroring
// authorizePlantWrite/authorizeRegionWrite -- except a global row is never
// collapsed this way, since FR55 makes it unconditionally readable.
func (r *Repository) authorizePlantTypeWrite(ctx context.Context, plantTypeID int64, scope authz.Scope) (PlantTypeRow, error) {
	row, err := r.GetPlantTypeByID(ctx, plantTypeID)
	if err != nil {
		return PlantTypeRow{}, err
	}
	if row.HouseholdID == nil {
		// Global row: resolvable by anyone. requireGlobalWriteElevation is
		// the gate an actual write applies next.
		return row, nil
	}
	for _, hh := range authz.HouseholdIDs(scope) {
		if hh == *row.HouseholdID {
			return row, nil
		}
	}
	return PlantTypeRow{}, ErrPlantTypeNotFound
}

// requireGlobalWriteElevation gates a write to a global (household_id
// NULL) plant_type row on FR10 elevation: adminSubject must currently hold
// an unexpired elevation against *some* household
// (Repository.AnyActiveElevation) -- there is no specific target household
// to check against, since a global row belongs to none. Returns a
// contract.PermissionDenied failure, not the underlying
// ErrNoActiveElevation, when the caller holds no such elevation, so callers
// can return this error directly to a caller.
func (r *Repository) requireGlobalWriteElevation(ctx context.Context, adminSubject string) error {
	if _, err := r.AnyActiveElevation(ctx, adminSubject); err != nil {
		if errors.Is(err, ErrNoActiveElevation) {
			return contract.PermissionDenied("plant_type", "", "Changing a shared plant type requires admin elevation.")
		}
		return fmt.Errorf("check elevation for global plant type write: %w", err)
	}
	return nil
}

// globalWriteAuditNote is stamped onto entry.Reason by every global
// plant_type write when the caller didn't already supply one, so the
// audit row's null TargetHouseholdID reads as an intentional "this write
// has no household to target" rather than a data gap -- the issue text's
// "audit it with a null target household, and say so in the audit row".
const globalWriteAuditNote = "global plant type write: audited with no target household, since a global row belongs to none"

// stampPlantTypeAudit fills entry's EntityID/TargetHouseholdID for a
// plant_type write, and -- for the global case -- Reason, per
// globalWriteAuditNote's doc comment. Shared by
// CreatePlantType/RenamePlantType/RetirePlantType so the three audit rows
// stay identical in shape.
func stampPlantTypeAudit(entry *audit.Entry, plantTypeID int64, householdID *int64) {
	idStr := strconv.FormatInt(plantTypeID, 10)
	entry.EntityID = &idStr
	entry.TargetHouseholdID = householdID
	if householdID == nil && entry.Reason == nil {
		note := globalWriteAuditNote
		entry.Reason = &note
	}
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

// ownHouseholdForCatalogWrite resolves the single household a caller with
// no other entity to inherit from is writing against -- CreatePlantType's
// household-owned branch has no parent entity (unlike CreatePlant's
// region, or CreateRegion's parent region) to resolve a household from, so
// it falls back to the caller's own current membership
// (authz.HouseholdIDs(scope)), exactly like CreateRegion's root-region
// case (regions.go): zero memberships is refused (not a member of any
// household), more than one is refused rather than guessed at (V1 has no
// household-selection field on CreatePlantTypeRequest).
func ownHouseholdForCatalogWrite(scope authz.Scope) (int64, error) {
	households := authz.HouseholdIDs(scope)
	switch len(households) {
	case 0:
		return 0, contract.PermissionDenied("plant_type", "", "You must be a member of a household to create a plant type.")
	case 1:
		return households[0], nil
	default:
		return 0, contract.InvalidArgument("plant_type", "global", "You belong to more than one household; this can't tell which one should own the new type.")
	}
}

// CreatePlantType creates a plant type (FR55, A24) -- a global row
// (household_id NULL) under FR10 elevation (requireGlobalWriteElevation),
// audited with a null target household, or a household-owned row under
// FR7 member-or-grantee (standing in with member-only -- see this file's
// doc comment), no elevation, scoped to the caller's own household
// (ownHouseholdForCatalogWrite).
func (r *Repository) CreatePlantType(ctx context.Context, commonName string, species *string, global bool, adminSubject string, scope authz.Scope, entry audit.Entry) (PlantTypeRow, error) {
	if reason := validatePlantTypeCommonName(commonName); reason != "" {
		return PlantTypeRow{}, contract.InvalidArgument("plant_type", "common_name", reason)
	}
	commonName = strings.TrimSpace(commonName)

	var householdID *int64
	if global {
		if err := r.requireGlobalWriteElevation(ctx, adminSubject); err != nil {
			return PlantTypeRow{}, err
		}
	} else {
		hh, err := ownHouseholdForCatalogWrite(scope)
		if err != nil {
			return PlantTypeRow{}, err
		}
		householdID = &hh
	}

	var row PlantTypeRow
	writeErr := r.auditedWrite(ctx, func(tx pgx.Tx) (audit.Entry, error) {
		if err := tx.QueryRow(ctx, `
			INSERT INTO plant_type (common_name, species, household_id)
			VALUES ($1, $2, $3)
			RETURNING plant_type_id, common_name, species, household_id, retired_at
		`, commonName, species, householdID).Scan(
			&row.PlantTypeID, &row.CommonName, &row.Species, &row.HouseholdID, &row.RetiredAt,
		); err != nil {
			return audit.Entry{}, fmt.Errorf("insert plant type: %w", err)
		}
		stampPlantTypeAudit(&entry, row.PlantTypeID, householdID)
		return entry, nil
	})
	if writeErr != nil {
		return PlantTypeRow{}, writeErr
	}
	return row, nil
}

// RenamePlantType renames a plant type (FR55) -- same elevation-for-global
// / member-or-grantee-for-owned split as CreatePlantType. Refuses on a
// retired type (FR22.1: "accepts no new writes"), mirroring
// CorrectPlant/RenameRegion's identical zero-rows-updated handling.
func (r *Repository) RenamePlantType(ctx context.Context, plantTypeID int64, commonName string, adminSubject string, scope authz.Scope, entry audit.Entry) (PlantTypeRow, error) {
	if reason := validatePlantTypeCommonName(commonName); reason != "" {
		return PlantTypeRow{}, contract.InvalidArgument("plant_type", "common_name", reason)
	}
	commonName = strings.TrimSpace(commonName)

	row, err := r.authorizePlantTypeWrite(ctx, plantTypeID, scope)
	if err != nil {
		return PlantTypeRow{}, err
	}
	if row.HouseholdID == nil {
		if err := r.requireGlobalWriteElevation(ctx, adminSubject); err != nil {
			return PlantTypeRow{}, err
		}
	}

	var updated PlantTypeRow
	writeErr := r.auditedWrite(ctx, func(tx pgx.Tx) (audit.Entry, error) {
		err := tx.QueryRow(ctx, `
			UPDATE plant_type
			SET common_name = $1
			WHERE plant_type_id = $2
			  AND retired_at IS NULL
			RETURNING plant_type_id, common_name, species, household_id, retired_at
		`, commonName, plantTypeID).Scan(
			&updated.PlantTypeID, &updated.CommonName, &updated.Species, &updated.HouseholdID, &updated.RetiredAt,
		)
		if err == nil {
			stampPlantTypeAudit(&entry, plantTypeID, row.HouseholdID)
			return entry, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return audit.Entry{}, fmt.Errorf("rename plant type %d: %w", plantTypeID, err)
		}
		// authorizePlantTypeWrite above confirmed the row exists (and, for
		// an owned row, is in scope); zero rows updated here means it is
		// retired -- retirement accepts no new writes, same shape as
		// CorrectPlant's retired-plant refusal.
		return audit.Entry{}, contract.InvalidArgument("plant_type", "plant_type_id", "This plant type has been retired and no longer accepts changes.")
	})
	if writeErr != nil {
		return PlantTypeRow{}, writeErr
	}
	return updated, nil
}

// plantsReferencingType returns the display name of every active
// (non-removed) plant referencing plantTypeID, for FR59.3's "refuse and
// name the referencing plants" retirement guard -- applies identically to
// a global or an owned type. Run as a pre-check ahead of RetirePlantType's
// UPDATE, not inside the same transaction as it, the same documented
// precedent SetRegionParent's pre-check has (regions.go): a race between
// this SELECT and the UPDATE (a plant created against this type in
// between) is possible in principle but left to a future re-check, not
// guarded against here -- this pre-check exists to make the common case a
// clean FR59.3 refusal instead of an FK violation surfacing as an opaque
// internal error.
func (r *Repository) plantsReferencingType(ctx context.Context, plantTypeID int64) ([]string, error) {
	rows, err := r.db.Query(ctx, `
		SELECT name FROM plant
		WHERE plant_type_id = $1 AND removed_at IS NULL
		ORDER BY plant_id
	`, plantTypeID)
	if err != nil {
		return nil, fmt.Errorf("find plants referencing plant type %d: %w", plantTypeID, err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan plant referencing plant type %d: %w", plantTypeID, err)
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// RetirePlantType soft-retires a plant type (FR22.1) -- refused (FR59.3),
// naming the referencing plants, while any plant still references it, in
// either ownership class. Nothing is hard-deleted: retirement only ever
// sets retired_at, mirroring RetirePlant/RetireRegion/RetireBoard.
func (r *Repository) RetirePlantType(ctx context.Context, plantTypeID int64, adminSubject string, scope authz.Scope, entry audit.Entry) (PlantTypeRow, error) {
	row, err := r.authorizePlantTypeWrite(ctx, plantTypeID, scope)
	if err != nil {
		return PlantTypeRow{}, err
	}
	if row.HouseholdID == nil {
		if err := r.requireGlobalWriteElevation(ctx, adminSubject); err != nil {
			return PlantTypeRow{}, err
		}
	}

	referencingNames, err := r.plantsReferencingType(ctx, plantTypeID)
	if err != nil {
		return PlantTypeRow{}, err
	}
	if len(referencingNames) > 0 {
		return PlantTypeRow{}, contract.Refuse(
			"plant_type", "plant_type_id",
			fmt.Sprintf(
				"This plant type is still used by %s; it can't be retired while any plant references it.",
				strings.Join(referencingNames, ", "),
			),
			"Retire or re-type those plants first, or leave this plant type active.",
		)
	}

	var updated PlantTypeRow
	writeErr := r.auditedWrite(ctx, func(tx pgx.Tx) (audit.Entry, error) {
		err := tx.QueryRow(ctx, `
			UPDATE plant_type
			SET retired_at = NOW()
			WHERE plant_type_id = $1
			  AND retired_at IS NULL
			RETURNING plant_type_id, common_name, species, household_id, retired_at
		`, plantTypeID).Scan(
			&updated.PlantTypeID, &updated.CommonName, &updated.Species, &updated.HouseholdID, &updated.RetiredAt,
		)
		if err == nil {
			stampPlantTypeAudit(&entry, plantTypeID, row.HouseholdID)
			return entry, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return audit.Entry{}, fmt.Errorf("retire plant type %d: %w", plantTypeID, err)
		}
		// No row updated -- the type exists (authorizePlantTypeWrite above
		// confirmed that) but is already retired: retirement is not
		// idempotent-by-design, mirroring RetirePlant/RetireRegion/
		// RetireBoard.
		return audit.Entry{}, ErrPlantTypeAlreadyRetired
	})
	if writeErr != nil {
		return PlantTypeRow{}, writeErr
	}
	return updated, nil
}

// CreatePlantWithNewType creates a household-owned plant type and a plant
// referencing it in one transaction (FR55, SB-1.10's acquire-and-place):
// the owning household is the same one regionID resolves to (via
// authorizeRegionWrite, exactly like CreatePlant's own authorization) --
// never the caller's ambient membership and never a global type, matching
// NewPlantType's proto doc comment ("always creates a household-owned
// type ... there is no elevation path through this message"). No call in
// this function touches admin_elevation -- that is SB-1.10's "no elevated
// principal anywhere in the call" requirement made structural rather than
// merely tested.
//
// Mirrors CreatePlant's placement/capture shape (plants.go) exactly, with
// the plant_type insert added ahead of the plant insert inside the same
// auditedWrite transaction: if placement or capture fails, the whole
// transaction (including the new plant_type row) rolls back, so this never
// leaves a plant_type row acquired-but-unplaced.
func (r *Repository) CreatePlantWithNewType(ctx context.Context, regionID int64, plantName, typeCommonName string, typeSpecies *string, scope authz.Scope, entry audit.Entry) (PlantRow, PlantTypeRow, error) {
	if reason := validatePlantName(plantName); reason != "" {
		return PlantRow{}, PlantTypeRow{}, contract.InvalidArgument("plant", "name", reason)
	}
	plantName = strings.TrimSpace(plantName)
	if reason := validatePlantTypeCommonName(typeCommonName); reason != "" {
		return PlantRow{}, PlantTypeRow{}, contract.InvalidArgument("plant_type", "common_name", reason)
	}
	typeCommonName = strings.TrimSpace(typeCommonName)

	regionRes, err := r.authorizeRegionWrite(ctx, regionID, scope)
	if err != nil {
		if errors.Is(err, ErrRegionNotFound) {
			return PlantRow{}, PlantTypeRow{}, contract.InvalidArgument("plant", "region_id", "This references something that doesn't exist.")
		}
		return PlantRow{}, PlantTypeRow{}, err
	}
	householdID := regionRes.HouseholdID

	var plantRow PlantRow
	var typeRow PlantTypeRow
	writeErr := r.auditedWrite(ctx, func(tx pgx.Tx) (audit.Entry, error) {
		if err := tx.QueryRow(ctx, `
			INSERT INTO plant_type (common_name, species, household_id)
			VALUES ($1, $2, $3)
			RETURNING plant_type_id, common_name, species, household_id, retired_at
		`, typeCommonName, typeSpecies, householdID).Scan(
			&typeRow.PlantTypeID, &typeRow.CommonName, &typeRow.Species, &typeRow.HouseholdID, &typeRow.RetiredAt,
		); err != nil {
			return audit.Entry{}, fmt.Errorf("insert inline plant type: %w", err)
		}

		if err := tx.QueryRow(ctx, `
			INSERT INTO plant (region_id, plant_type_id, name, household_id)
			VALUES ($1, $2, $3, $4)
			RETURNING plant_id, region_id, plant_type_id, name, created_at, removed_at
		`, regionID, typeRow.PlantTypeID, plantName, householdID).Scan(
			&plantRow.PlantID, &plantRow.RegionID, &plantRow.PlantTypeID, &plantRow.Name, &plantRow.CreatedAt, &plantRow.RemovedAt,
		); err != nil {
			return audit.Entry{}, fmt.Errorf("insert plant for inline type: %w", err)
		}

		// Opens the plant's first plant_region_history interval through
		// the Phase 3 SCD2 writer (FR19) -- identical to CreatePlant's own
		// shape (plants.go).
		validFrom, err := placement.MoveTx(ctx, tx, plantRow.PlantID, regionID, time.Now())
		if err != nil {
			return audit.Entry{}, err
		}
		plantRow.RegionID = regionID

		affectedSensors, err := sensorsInRegionSubtrees(ctx, tx, []int64{regionID})
		if err != nil {
			return audit.Entry{}, err
		}
		if err := capture.NewRecorder().Record(ctx, tx, affectedSensors, validFrom); err != nil {
			return audit.Entry{}, fmt.Errorf("record boundary capture for new plant %d: %w", plantRow.PlantID, err)
		}

		idStr := strconv.FormatInt(plantRow.PlantID, 10)
		entry.EntityID = &idStr
		entry.TargetHouseholdID = &householdID
		return entry, nil
	})
	if writeErr != nil {
		return PlantRow{}, PlantTypeRow{}, writeErr
	}
	return plantRow, typeRow, nil
}
