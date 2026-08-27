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
	"github.com/whale-net/everything/leaflab/api/contract"
)

// This file is #1376's implementation of FR50/FR22.2/FR22.5/NFR6.2 (region
// lifecycle) -- migration 020's schema and api.proto's Region RPC set
// (CreateRegion, RenameRegion, RetireRegion, ListRegions, GetRegionPath).
// It follows 015_ownership's scaffold/feat split, the same one
// PushDeviceConfig/RetireBoard already follow in repository.go: read paths
// (ListRegions, GetRegionByID, GetRegionPath) select against real SQL with
// no business-rule gate beyond default-listing exclusion; the write paths
// below additionally enforce:
//
//   - the structural rules (FR50.1: max 12 children per parent, minimum
//     depth Room / Shelf / Pot), enforced server-side with a
//     contract.InvalidArgument naming the offending region and field
//     (FR59);
//   - the FR59.3 caller-facing refusal in front of migration 020's
//     parentage-immutability trigger (see SetRegionParent below), naming
//     FR74/subtree relocation as the alternative -- the trigger itself is
//     the backstop (NFR6.2), this is the experience;
//   - FR8 audit recording, via Repository.auditedWrite (see RetireBoard's
//     use of it for the shape).
//
// Authorization stands in with a member-only check (authorizeRegionWrite
// below), not authz.MemberOrGrantee (FR7's member-or-grantee capability):
// that symbol does not exist anywhere on this branch lineage (grep the
// repo: no "Grantee" symbol). #1344 defines it on a different, divergent
// v2 branch not reachable from this task's stated dependencies (#1339,
// #1358, #1338) -- the same gap #1417 recorded for #1349's
// SetBoardDisplayName. See the scope note filed alongside this commit for
// the grantee-can-<verb> test this gap blocks.
//
// No server.go RPC handler wiring yet, matching RetireBoard's own
// precedent ("has no RPC surface yet ... it will be added here in the
// task that gives it one", repository.go) -- pb.UnimplementedLeafLabAPIServer
// still covers all five Region RPCs on the wire; every function below is
// exercised directly by the Testing phase, the same way RetireBoard is
// today. Audit registry wiring (audit_registry.go's declaredWriteMethods/
// auditRegistrations) is deferred to whichever task adds that server.go
// wiring, per this file's original scaffold note.

// ErrRegionNotFound is returned by region lookups/operations when
// region_id names no row, and also -- per NFR2's "no existence oracle" --
// when region_id names a row outside the caller's authz.Scope. The two
// cases are indistinguishable to a caller by design; see
// authorizeRegionWrite.
var ErrRegionNotFound = errors.New("region not found")

// ErrRegionAlreadyRetired is returned by RetireRegion when the region is
// already retired -- retirement is not idempotent-by-design, mirroring
// ErrBoardAlreadyRetired (repository.go).
var ErrRegionAlreadyRetired = errors.New("region already retired")

// maxRegionChildren is FR50.1's structural cap: at most 12 (active)
// children per parent region.
const maxRegionChildren = 12

// maxRegionDepth is FR50.1's structural cap expressed as a depth limit:
// Room (depth 1, a root region) / Shelf (depth 2) / Pot (depth 3, the
// deepest level a reading can be attributed against). A region already at
// depth 3 may not have children -- that would be a 4th level, which the
// Room/Shelf/Pot taxonomy has no name for.
const maxRegionDepth = 3

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

// authorizeRegionWrite resolves regionID against scope and collapses
// "doesn't exist" and "exists, out of caller's scope" into the same
// ErrRegionNotFound (NFR2) -- the member-only stand-in for FR7's
// member-or-grantee capability (authz.MemberOrGrantee), which does not
// exist on this branch lineage; see this file's doc comment. This mirrors
// server.go's authorizeBoardAccess/boardNotFoundFailure shape: one
// resolve, a cheap in-memory Permits check, no second query and no
// existence probe ahead of it.
//
// Deliberately does not filter on retired_at: a retired region must still
// resolve and authorize (FR22.5 "remains readable by explicit id"); a
// caller writing to it is refused later, by the specific write path's own
// retired-state check, with a reason naming that state -- not by this
// function collapsing it into "not found".
func (r *Repository) authorizeRegionWrite(ctx context.Context, regionID int64, scope authz.Scope) (authz.Resolution, error) {
	resolver := authz.NewPGResolver(r.db)
	ref := authz.EntityRef{Kind: authz.EntityRegion, ID: regionID}
	res, err := resolver.Resolve(ctx, ref)
	if err != nil {
		if errors.Is(err, authz.ErrNotFound) {
			return authz.Resolution{}, ErrRegionNotFound
		}
		return authz.Resolution{}, fmt.Errorf("resolve region %d: %w", regionID, err)
	}
	if !scope.Permits(ref, res) {
		return authz.Resolution{}, ErrRegionNotFound
	}
	return res, nil
}

// validateRegionName returns a persona-appropriate reason (FR59.2) if name
// is invalid once trimmed, or "" if it's valid. Mirrors validateDeviceID's
// shape (server.go).
func validateRegionName(name string) string {
	if strings.TrimSpace(name) == "" {
		return "A region name is required."
	}
	return ""
}

// CreateRegion creates a region under an optional parent (FR50.1).
//
// With a parent: the parent must resolve and authorize against scope
// (authorizeRegionWrite) exactly like any other region write; the new
// region's depth (parent's v_region_path length + 1) and the parent's
// active child count are checked against maxRegionDepth/maxRegionChildren
// before anything is written. household_id is left NULL -- only a tree
// root carries it (migration 015's enforce_region_household_root trigger),
// non-root regions inherit.
//
// Without a parent (a new root region, i.e. a new "Room"): there is no
// entity to resolve household from, and CreateRegionRequest carries no
// household field, so household_id comes from the caller's own current
// membership (authz.HouseholdIDs(scope)) instead. Exactly one current
// membership is required: zero means the caller is not a member of any
// household (refused, matching the member-only authorization every other
// region write uses); more than one is ambiguous -- V1 has no household-
// selection field on this request, and guessing wrong would silently file
// a new tree under the wrong household, so it is refused rather than
// resolved by picking one arbitrarily.
func (r *Repository) CreateRegion(ctx context.Context, parentRegionID *int64, name, description string, scope authz.Scope, entry audit.Entry) (RegionRow, error) {
	if reason := validateRegionName(name); reason != "" {
		return RegionRow{}, contract.InvalidArgument("region", "name", reason)
	}
	name = strings.TrimSpace(name)

	var householdID int64
	var parentIDArg any
	if parentRegionID != nil {
		res, err := r.authorizeRegionWrite(ctx, *parentRegionID, scope)
		if err != nil {
			return RegionRow{}, err
		}

		path, ok, err := r.GetRegionPath(ctx, *parentRegionID)
		if err != nil {
			return RegionRow{}, fmt.Errorf("get parent region %d path: %w", *parentRegionID, err)
		}
		if !ok {
			// authorizeRegionWrite just confirmed this row exists; a miss
			// here would mean the region was deleted (never happens --
			// regions are never hard-deleted) or GetRegionPath's view
			// disagrees with the region table, either an internal error.
			return RegionRow{}, fmt.Errorf("region %d resolved but has no path", *parentRegionID)
		}
		parentName := path.PathNames[len(path.PathNames)-1]
		if len(path.PathIDs) >= maxRegionDepth {
			return RegionRow{}, contract.InvalidArgument("region", "parent_region_id", fmt.Sprintf(
				"%q is already at the deepest allowed region level (Room / Shelf / Pot); a region cannot be created beneath it.",
				parentName,
			))
		}

		var childCount int
		if err := r.db.QueryRow(ctx, `
			SELECT COUNT(*) FROM region WHERE parent_region_id = $1 AND retired_at IS NULL
		`, *parentRegionID).Scan(&childCount); err != nil {
			return RegionRow{}, fmt.Errorf("count children of region %d: %w", *parentRegionID, err)
		}
		if childCount >= maxRegionChildren {
			return RegionRow{}, contract.InvalidArgument("region", "parent_region_id", fmt.Sprintf(
				"%q already has the maximum of %d child regions.", parentName, maxRegionChildren,
			))
		}

		householdID = res.HouseholdID
		parentIDArg = *parentRegionID
	} else {
		households := authz.HouseholdIDs(scope)
		switch len(households) {
		case 0:
			return RegionRow{}, contract.PermissionDenied("region", "", "You must be a member of a household to create a region.")
		case 1:
			householdID = households[0]
		default:
			return RegionRow{}, contract.InvalidArgument("region", "parent_region_id", "You belong to more than one household; create this region under an existing region so it's clear which household it belongs to.")
		}
		parentIDArg = nil
	}

	var row RegionRow
	err := r.auditedWrite(ctx, func(tx pgx.Tx) (audit.Entry, error) {
		var householdArg any
		if parentIDArg == nil {
			householdArg = householdID
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO region (parent_region_id, name, description, household_id)
			VALUES ($1, $2, $3, $4)
			RETURNING region_id, parent_region_id, name, description, retired_at, successor_region_id
		`, parentIDArg, name, description, householdArg).Scan(
			&row.RegionID, &row.ParentRegionID, &row.Name, &row.Description, &row.RetiredAt, &row.SuccessorRegionID,
		)
		if err != nil {
			return audit.Entry{}, fmt.Errorf("insert region: %w", err)
		}
		idStr := strconv.FormatInt(row.RegionID, 10)
		entry.EntityID = &idStr
		entry.TargetHouseholdID = &householdID
		return entry, nil
	})
	if err != nil {
		return RegionRow{}, err
	}
	return row, nil
}

// RenameRegion changes a region's name. Never touches parentage (FR50.3).
func (r *Repository) RenameRegion(ctx context.Context, regionID int64, name string, scope authz.Scope, entry audit.Entry) (RegionRow, error) {
	if reason := validateRegionName(name); reason != "" {
		return RegionRow{}, contract.InvalidArgument("region", "name", reason)
	}
	name = strings.TrimSpace(name)

	res, err := r.authorizeRegionWrite(ctx, regionID, scope)
	if err != nil {
		return RegionRow{}, err
	}

	var row RegionRow
	writeErr := r.auditedWrite(ctx, func(tx pgx.Tx) (audit.Entry, error) {
		err := tx.QueryRow(ctx, `
			UPDATE region
			SET name = $2
			WHERE region_id = $1
			  AND retired_at IS NULL
			RETURNING region_id, parent_region_id, name, description, retired_at, successor_region_id
		`, regionID, name).Scan(
			&row.RegionID, &row.ParentRegionID, &row.Name, &row.Description, &row.RetiredAt, &row.SuccessorRegionID,
		)
		if err == nil {
			idStr := strconv.FormatInt(regionID, 10)
			entry.EntityID = &idStr
			hh := res.HouseholdID
			entry.TargetHouseholdID = &hh
			return entry, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return audit.Entry{}, fmt.Errorf("rename region %d: %w", regionID, err)
		}
		// No row updated by the retired_at IS NULL guard -- the row exists
		// (authorizeRegionWrite above confirmed that) and is retired
		// (FR22.5: a retired region "accepts no new writes").
		return audit.Entry{}, contract.InvalidArgument("region", "region_id", "This region has been retired and no longer accepts changes.")
	})
	if writeErr != nil {
		return RegionRow{}, writeErr
	}
	return row, nil
}

// RetireRegion soft-retires a region (FR22.2, FR22.5). Mirrors
// Repository.RetireBoard's shape: an auditedWrite UPDATE ... SET
// retired_at = NOW() WHERE region_id = $1 AND retired_at IS NULL,
// distinguishing "does not exist" from "already retired" on the
// zero-rows-updated path exactly as RetireBoard does. Never touches
// parent_region_id -- FR22.2's "retirement does not unfreeze parentage" is
// enforced by migration 020's trigger regardless (it fires on any
// parent_region_id UPDATE, retired or not), and this statement never
// issues one.
func (r *Repository) RetireRegion(ctx context.Context, regionID int64, scope authz.Scope, entry audit.Entry) (RegionRow, error) {
	res, err := r.authorizeRegionWrite(ctx, regionID, scope)
	if err != nil {
		return RegionRow{}, err
	}

	var row RegionRow
	writeErr := r.auditedWrite(ctx, func(tx pgx.Tx) (audit.Entry, error) {
		err := tx.QueryRow(ctx, `
			UPDATE region
			SET retired_at = NOW()
			WHERE region_id = $1
			  AND retired_at IS NULL
			RETURNING region_id, parent_region_id, name, description, retired_at, successor_region_id
		`, regionID).Scan(
			&row.RegionID, &row.ParentRegionID, &row.Name, &row.Description, &row.RetiredAt, &row.SuccessorRegionID,
		)
		if err == nil {
			idStr := strconv.FormatInt(regionID, 10)
			entry.EntityID = &idStr
			hh := res.HouseholdID
			entry.TargetHouseholdID = &hh
			return entry, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return audit.Entry{}, fmt.Errorf("retire region %d: %w", regionID, err)
		}
		// No row updated -- the region exists (authorizeRegionWrite above
		// confirmed that) but is already retired: retirement is not
		// idempotent-by-design, mirroring RetireBoard.
		return audit.Entry{}, ErrRegionAlreadyRetired
	})
	if writeErr != nil {
		return RegionRow{}, writeErr
	}
	return row, nil
}

// SetRegionParent re-parents a region (FR50.3, FR50.5, FR22.2, NFR6.2).
// Not exposed as an RPC by this task -- FR50.3 names re-parenting "a
// create-time grace window for a mis-typed parent, not a user-facing
// re-parenting capability", and the actual re-parenting product surface is
// FR74's subtree relocation (a sibling task). This is the primitive that
// sibling task's RPC calls into, plus the create-time grace window itself;
// it has no RPC surface yet, the same precedent RetireBoard already set in
// this file's doc comment.
//
// Authorizes exactly like every other region write (member-only stand-in
// for FR7). Then applies FR59.3's caller-facing refusal *in front of*
// migration 020's parentage-immutability trigger, in two parts:
//
//  1. Retirement never unfreezes parentage (FR22.2) -- a retired region's
//     parent_region_id is exactly as frozen as an active one's, checked
//     here explicitly rather than left to the trigger, so the refusal
//     names the actual reason (retired) rather than the reading-attribution
//     one below.
//  2. The trigger's own recursive descendant test, reimplemented here as a
//     SELECT so the refusal can be produced *before* attempting the
//     UPDATE -- a raw trigger EXCEPTION from inside the UPDATE would reach
//     the caller as an opaque database error, not FR59.3's shape. The
//     trigger (020_region_lifecycle.up.sql) is the backstop that still
//     holds if this pre-check and the UPDATE race (e.g. a reading is
//     attributed in between) or if the caller bypasses this function
//     entirely via direct SQL -- that is exactly the case NFR6.2 requires
//     and the Testing phase exercises explicitly; this pre-check exists
//     only to make the common case a clean refusal instead of an internal
//     error.
//
// newParentRegionID may be nil to make regionID a root (rare, but not
// disallowed by FR50 -- the same immutability and structural rules apply
// to becoming rootless as to any other re-parent).
func (r *Repository) SetRegionParent(ctx context.Context, regionID int64, newParentRegionID *int64, scope authz.Scope, entry audit.Entry) (RegionRow, error) {
	res, err := r.authorizeRegionWrite(ctx, regionID, scope)
	if err != nil {
		return RegionRow{}, err
	}

	current, err := r.GetRegionByID(ctx, regionID)
	if err != nil {
		return RegionRow{}, fmt.Errorf("get region %d: %w", regionID, err)
	}
	if current.RetiredAt != nil {
		return RegionRow{}, contract.Refuse("region", "parent_region_id",
			"This region has been retired; retirement does not unfreeze its parentage.",
			"Relocate the subtree instead (FR74).")
	}

	if newParentRegionID != nil {
		if _, err := r.authorizeRegionWrite(ctx, *newParentRegionID, scope); err != nil {
			return RegionRow{}, err
		}
	}

	var hasReading bool
	if err := r.db.QueryRow(ctx, `
		WITH RECURSIVE subtree AS (
			SELECT region_id FROM region WHERE region_id = $1

			UNION ALL

			SELECT r.region_id
			FROM region r
			JOIN subtree s ON r.parent_region_id = s.region_id
		)
		SELECT EXISTS (
			SELECT 1 FROM sensor_reading sr WHERE sr.region_id IN (SELECT region_id FROM subtree)
		)
	`, regionID).Scan(&hasReading); err != nil {
		return RegionRow{}, fmt.Errorf("check region %d subtree for readings: %w", regionID, err)
	}
	if hasReading {
		return RegionRow{}, contract.Refuse("region", "parent_region_id",
			"This region's parentage is frozen: a reading has been attributed to it or a descendant region.",
			"Relocate the subtree instead (FR74).")
	}

	var row RegionRow
	writeErr := r.auditedWrite(ctx, func(tx pgx.Tx) (audit.Entry, error) {
		var parentArg any
		if newParentRegionID != nil {
			parentArg = *newParentRegionID
		}
		err := tx.QueryRow(ctx, `
			UPDATE region
			SET parent_region_id = $2
			WHERE region_id = $1
			RETURNING region_id, parent_region_id, name, description, retired_at, successor_region_id
		`, regionID, parentArg).Scan(
			&row.RegionID, &row.ParentRegionID, &row.Name, &row.Description, &row.RetiredAt, &row.SuccessorRegionID,
		)
		if err != nil {
			// The trigger's own RAISE EXCEPTION lands here if the race
			// described above occurs -- surfaced as an opaque internal
			// error rather than contract.Refuse, since by construction the
			// pre-check above just found no reading. See this function's
			// doc comment.
			return audit.Entry{}, fmt.Errorf("re-parent region %d: %w", regionID, err)
		}
		idStr := strconv.FormatInt(regionID, 10)
		entry.EntityID = &idStr
		hh := res.HouseholdID
		entry.TargetHouseholdID = &hh
		return entry, nil
	})
	if writeErr != nil {
		return RegionRow{}, writeErr
	}
	return row, nil
}
