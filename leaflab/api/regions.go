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

// This file is #1376's scaffolded wire surface for FR50/FR22.2/FR22.5/
// NFR6.2 (region lifecycle) -- migration 020's schema and
// api.proto's Region RPC set (CreateRegion, RenameRegion, RetireRegion,
// ListRegions, GetRegionPath). It follows 015_ownership's scaffold/feat
// split, the same one PushDeviceConfig/RetireBoard already follow in
// repository.go: read paths are fully implemented against real SQL below;
// the three write paths are signature-only skeletons that return
// ErrRegionOpNotImplemented until the Implementation phase wires in:
//
//   - the structural rules (FR50.1: max 12 children per parent, minimum
//     depth Room / Shelf / Pot), enforced server-side with a contract.Refuse
//     naming the offending region and field (FR59);
//   - the FR59.3 caller-facing refusal in front of migration 020's
//     parentage-immutability trigger, naming FR74/subtree relocation as the
//     alternative -- the trigger itself is the backstop (NFR6.2), this is
//     the experience;
//   - member-or-grantee authorization (FR7) via authz.MemberOrGrantee --
//     which does not exist on this branch lineage yet (grep the repo: no
//     "Grantee" symbol anywhere). #1344 defines it on a different,
//     divergent v2 branch not reachable from this task's stated
//     dependencies (#1339, #1358, #1338), the same gap #1417 already
//     recorded for #1349's SetBoardDisplayName. Whoever implements these
//     three write paths must either integrate #1344's branch first, or
//     stand in with the member-only check every other write RPC in this
//     file already uses (authorizeBoardAccess's shape) and file a scope
//     note for the grantee-can-<verb> test, exactly as #1417 did;
//   - FR8 audit recording, via Repository.auditedWrite (see RetireBoard's
//     use of it below for the shape) -- and adding CreateRegion/
//     RenameRegion/RetireRegion's full method names to audit_registry.go's
//     declaredWriteMethods/auditRegistrations once they're wired into
//     server.go.
//
// No server.go handler wiring yet -- pb.UnimplementedLeafLabAPIServer
// covers all five new RPCs for now, same as the households/membership RPC
// set's own scaffold commit.

// ErrRegionNotFound is returned by region lookups/operations when
// region_id names no row.
var ErrRegionNotFound = errors.New("region not found")

// ErrRegionOpNotImplemented is returned by CreateRegion, RenameRegion and
// RetireRegion until the Implementation phase wires in the business rules
// named in this file's doc comment above. Signature-only for now, same
// pattern as households.go's ErrHouseholdOpNotImplemented on the sibling
// v2 branch that scaffolded FR75/FR7's household RPCs.
var ErrRegionOpNotImplemented = errors.New("region operation not implemented")

// RegionRow is one region row as read from v_region_household (migration
// 020), which resolves household_id by walking to the tree root -- see
// that view's doc comment for why household-scoped region reads must
// select from it alone rather than joining the raw `region` table.
type RegionRow struct {
	RegionID          int64
	ParentRegionID    *int64
	Name              string
	Description       *string
	RetiredAt         *time.Time
	SuccessorRegionID *int64
}

// ListRegions returns up to limit regions ordered by region_id,
// keyset-paginated on (region_id) per FR61 -- same shape as
// Repository.ListBoards. Retired regions are excluded (FR22.5's default-
// listing guard, backed by idx_region_active), and scope is applied inside
// the query via scope.Filter() (FR5.1/FR5.2) against v_region_household's
// single household_id column, never as a Go-side post-filter.
func (r *Repository) ListRegions(ctx context.Context, afterRegionID int64, hasAfter bool, limit int32, scope authz.Scope) ([]RegionRow, error) {
	var sqlQuery string
	var args []any
	if hasAfter {
		filter, filterArgs := scope.Filter(3)
		sqlQuery = fmt.Sprintf(`
			SELECT region_id, parent_region_id, name, description, retired_at, successor_region_id
			FROM v_region_household
			WHERE region_id > $1
			  AND retired_at IS NULL
			  AND (%s)
			ORDER BY region_id
			LIMIT $2
		`, filter)
		args = append([]any{afterRegionID, limit}, filterArgs...)
	} else {
		filter, filterArgs := scope.Filter(2)
		sqlQuery = fmt.Sprintf(`
			SELECT region_id, parent_region_id, name, description, retired_at, successor_region_id
			FROM v_region_household
			WHERE retired_at IS NULL
			  AND (%s)
			ORDER BY region_id
			LIMIT $1
		`, filter)
		args = append([]any{limit}, filterArgs...)
	}

	rows, err := r.db.Query(ctx, sqlQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("list regions: %w", err)
	}
	defer rows.Close()

	var regions []RegionRow
	for rows.Next() {
		var row RegionRow
		if err := rows.Scan(&row.RegionID, &row.ParentRegionID, &row.Name, &row.Description, &row.RetiredAt, &row.SuccessorRegionID); err != nil {
			return nil, fmt.Errorf("scan region: %w", err)
		}
		regions = append(regions, row)
	}
	return regions, rows.Err()
}

// GetRegionByID returns a region by its numeric id regardless of retired
// state -- the FR22.5 "remains readable by explicit id" half of the
// retired-region guard; ListRegions is the half that excludes it from
// default listings. Mirrors Repository.GetBoardByID.
func (r *Repository) GetRegionByID(ctx context.Context, regionID int64) (RegionRow, error) {
	var row RegionRow
	err := r.db.QueryRow(ctx, `
		SELECT region_id, parent_region_id, name, description, retired_at, successor_region_id
		FROM v_region_household
		WHERE region_id = $1
	`, regionID).Scan(&row.RegionID, &row.ParentRegionID, &row.Name, &row.Description, &row.RetiredAt, &row.SuccessorRegionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RegionRow{}, ErrRegionNotFound
		}
		return RegionRow{}, fmt.Errorf("get region %d: %w", regionID, err)
	}
	return row, nil
}

// RegionPath is a region's root-to-leaf path, read from v_region_path
// (migration 012) rather than a second recursive CTE.
type RegionPath struct {
	PathIDs   []int64
	PathNames []string
	PathName  string
}

// GetRegionPath returns regionID's root-to-leaf path (FR50.2). ok is false
// when regionID names no row -- v_region_path has no row for a nonexistent
// region_id, same as its zero-row behavior documented in
// attribute_region_plants's SQL twin (migration 019). Works for a retired
// region too: v_region_path does not filter on retired_at.
func (r *Repository) GetRegionPath(ctx context.Context, regionID int64) (path RegionPath, ok bool, err error) {
	var pathIDs []int64
	var pathNames []string
	var pathName string
	dbErr := r.db.QueryRow(ctx, `
		SELECT path_ids, path_names, path_name
		FROM v_region_path
		WHERE region_id = $1
	`, regionID).Scan(&pathIDs, &pathNames, &pathName)
	if dbErr != nil {
		if errors.Is(dbErr, pgx.ErrNoRows) {
			return RegionPath{}, false, nil
		}
		return RegionPath{}, false, fmt.Errorf("get region path for %d: %w", regionID, dbErr)
	}
	return RegionPath{PathIDs: pathIDs, PathNames: pathNames, PathName: pathName}, true, nil
}

// CreateRegion creates a region under an optional parent (FR50.1). Not yet
// implemented -- see this file's doc comment for what Implementation must
// wire (structural rules, member-or-grantee authorization, FR8 audit).
func (r *Repository) CreateRegion(ctx context.Context, parentRegionID *int64, name, description string, entry audit.Entry) (RegionRow, error) {
	return RegionRow{}, ErrRegionOpNotImplemented
}

// RenameRegion changes a region's name. Never touches parentage (FR50.3).
// Not yet implemented -- see this file's doc comment.
func (r *Repository) RenameRegion(ctx context.Context, regionID int64, name string, entry audit.Entry) (RegionRow, error) {
	return RegionRow{}, ErrRegionOpNotImplemented
}

// RetireRegion soft-retires a region (FR22.2, FR22.5). Not yet implemented
// -- see this file's doc comment. Once wired, this mirrors
// Repository.RetireBoard's shape: an auditedWrite UPDATE ... SET retired_at
// = NOW() WHERE region_id = $1 AND retired_at IS NULL, distinguishing "does
// not exist" from "already retired" on the zero-rows-updated path exactly
// as RetireBoard does.
func (r *Repository) RetireRegion(ctx context.Context, regionID int64, entry audit.Entry) (RegionRow, error) {
	return RegionRow{}, ErrRegionOpNotImplemented
}
