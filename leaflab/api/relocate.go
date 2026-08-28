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
	"github.com/whale-net/everything/leaflab/invalidation"
)

// This file is #1380's implementation of FR74/FR24 (relocation-induced
// half): api.proto's RelocateSubtree RPC, the atomic composition of
// operations this plan already requires --
//
//  1. mirror the subtree under new_parent_region_id, preserving relative
//     structure and names, validated against FR50's structural rules
//     (maxRegionChildren/maxRegionDepth, regions.go) before anything is
//     written (FR59.3) -- a 13th child or a Shelf under a Pot refuses the
//     whole operation, naming the violation;
//  2. move every current sensor placement (FR51) and every current plant
//     placement (FR54) into the mirrored regions -- reusing the existing
//     writers, not a third placement path: leaflab/api/placement.
//     MoveRelocatedTx for plants, sensor_region.go's own
//     assignSensorRegionTx for sensors -- both extended (this task) to
//     accept a relocation_induced flag without disturbing their existing
//     non-relocation callers (CreatePlant/MovePlant, AssignSensorRegion
//     itself);
//  3. retire the original regions in place (FR22.2) with
//     successor_region_id naming their replacements, so a region-keyed
//     series joins across the move.
//
// Planning and structural validation (computeRelocationPlan below) run
// against r.db, before any write -- the same precedent CreateRegion's own
// depth/child-count checks already set (regions.go): a refusal never opens
// the write transaction, so "refused whole, nothing written" holds by
// construction, not by rollback. The write itself -- mirror, move, retire,
// FR20 captures, the one FR8 audit record -- is one Repository.auditedWrite
// transaction (mirroring every other write in this package); Postgres never
// exposes another transaction's uncommitted writes regardless of isolation
// level, so "no intermediate state is observable" (FR1.2 never violated,
// FR56 never reports a plant unmonitored mid-relocation) holds for the same
// reason every other auditedWrite in this package already relies on.
// FR73 invalidations publish only after that transaction has committed,
// mirroring AssignSensorRegion's own publish-after-commit discipline
// (sensor_region.go's doc comment).
//
// FR50.5: SetRegionParent's refusal (regions.go) already names "Relocate
// the subtree instead (FR74)" as the alternative -- this RPC is the path
// that names, and the Testing phase's FR50.5 round-trip test asserts the
// refusal's named alternative actually succeeds end to end.
//
// No server.go handler wiring yet -- pb.UnimplementedLeafLabAPIServer
// still covers RelocateSubtree on the wire, matching every other Phase 5
// RPC's own deferred-wiring precedent (regions.go, plants.go,
// sensor_region.go's doc comments).

// ErrRelocateSubtreeNotImplemented is kept only because a handful of other
// files' doc comments still reference it by name; RelocateSubtree itself
// no longer returns it. Retained rather than deleted so a stray reference
// elsewhere in this branch's history still resolves.
var ErrRelocateSubtreeNotImplemented = errors.New("relocate subtree not implemented")

// RelocationResult is RelocateSubtree's result: the mirrored subtree's
// root region (root_region_id's replacement beneath new_parent_region_id)
// plus counts of what moved, so a caller and the audit trail's entity_id
// (see this file's doc comment) both have something concrete to point at.
// Counts are informational only -- FR74 requires one audit record for the
// whole operation, not one per moved entity; these fields are not a
// substitute for that and must never drive a second, per-entity audit
// write.
type RelocationResult struct {
	NewRoot               RegionRow
	RegionsMirrored       int
	SensorPlacementsMoved int
	PlantPlacementsMoved  int
}

// relocationSubtreeNode is one region in the subtree being relocated, as
// read by computeRelocationPlan's recursive walk -- RelativeDepth is 0 for
// rootRegionID itself, 1 for its direct children, and so on, letting the
// structural depth check below compute each mirrored region's eventual
// depth without a second query per level.
type relocationSubtreeNode struct {
	RegionID       int64
	ParentRegionID *int64
	Name           string
	Description    *string
	RelativeDepth  int
}

// relocationPlan is computeRelocationPlan's output: everything
// RelocateSubtree's write transaction needs, already validated against
// FR50's structural rules (FR59.3) -- the write transaction below performs
// no structural check of its own, only the plan's instructions.
type relocationPlan struct {
	Root              RegionRow
	NewParent         RegionRow
	HouseholdID       int64
	Subtree           []relocationSubtreeNode // parent-first order (ordered by RelativeDepth ascending)
	OriginalRegionIDs []int64
}

// validateRelocationReason returns a persona-appropriate reason (FR59.2)
// if reason is invalid once trimmed, or "" if it's valid -- mirrors
// validateRegionName/validatePlantName/validateSensorName. FR74's one
// audit record (audit.NewRelocationEntry) requires a reason; refusing an
// empty one here keeps that requirement caller-visible rather than
// surfacing as an audit-layer panic.
func validateRelocationReason(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return "A reason is required to relocate a subtree."
	}
	return ""
}

// computeRelocationPlan resolves and validates a RelocateSubtree call
// before any write: authorization and retirement checks on both endpoints
// (mirroring authorizeRegionWrite's use everywhere else in this package),
// FR1.2 (root's subtree and new_parent_region_id must share one
// household), a cycle check (the destination cannot be the subtree itself
// or one of its own descendants), and FR50's structural rules -- a 13th
// child under new_parent_region_id, or a mirrored depth beyond
// maxRegionDepth (a Shelf under a Pot). Any failure returns a
// contract.InvalidArgument/Refuse naming the violation, and writes
// nothing -- computeRelocationPlan only ever reads.
func (r *Repository) computeRelocationPlan(ctx context.Context, rootRegionID, newParentRegionID int64, scope authz.Scope) (relocationPlan, error) {
	rootRes, err := r.authorizeRegionWrite(ctx, rootRegionID, scope)
	if err != nil {
		if errors.Is(err, ErrRegionNotFound) {
			return relocationPlan{}, contract.InvalidArgument("relocate_subtree", "root_region_id", "This references something that doesn't exist.")
		}
		return relocationPlan{}, err
	}
	root, err := r.GetRegionByID(ctx, rootRegionID)
	if err != nil {
		return relocationPlan{}, fmt.Errorf("get root region %d: %w", rootRegionID, err)
	}
	if root.RetiredAt != nil {
		return relocationPlan{}, contract.InvalidArgument("relocate_subtree", "root_region_id", "This region has been retired and no longer accepts changes.")
	}

	newParentRes, err := r.authorizeRegionWrite(ctx, newParentRegionID, scope)
	if err != nil {
		if errors.Is(err, ErrRegionNotFound) {
			return relocationPlan{}, contract.InvalidArgument("relocate_subtree", "new_parent_region_id", "This references something that doesn't exist.")
		}
		return relocationPlan{}, err
	}
	newParent, err := r.GetRegionByID(ctx, newParentRegionID)
	if err != nil {
		return relocationPlan{}, fmt.Errorf("get new parent region %d: %w", newParentRegionID, err)
	}
	if newParent.RetiredAt != nil {
		return relocationPlan{}, contract.InvalidArgument("relocate_subtree", "new_parent_region_id", "This region has been retired and no longer accepts changes.")
	}

	// FR1.2: the whole subtree and the new parent must belong to one
	// household. Every region in rootRegionID's subtree shares rootRes'
	// resolved household by construction (household boundaries never
	// split mid-tree, migration 015's enforce_region_household_root
	// trigger), so comparing the two resolved households once is
	// sufficient -- no need to re-check every descendant individually.
	if rootRes.HouseholdID != newParentRes.HouseholdID {
		return relocationPlan{}, contract.InvalidArgument("relocate_subtree", "new_parent_region_id", "This references something that belongs to a different household.")
	}

	subtree, err := r.regionSubtree(ctx, rootRegionID)
	if err != nil {
		return relocationPlan{}, fmt.Errorf("compute subtree for region %d: %w", rootRegionID, err)
	}

	// Cycle prevention: the destination cannot be the subtree being moved
	// or any of its own descendants -- relocating a subtree under itself
	// is not expressible as a tree.
	for _, n := range subtree {
		if n.RegionID == newParentRegionID {
			return relocationPlan{}, contract.InvalidArgument("relocate_subtree", "new_parent_region_id", "The destination is inside the subtree being relocated.")
		}
	}

	// FR50.1 structural rules, checked before anything is written
	// (FR59.3):
	newParentPath, ok, err := r.GetRegionPath(ctx, newParentRegionID)
	if err != nil {
		return relocationPlan{}, fmt.Errorf("get new parent region %d path: %w", newParentRegionID, err)
	}
	if !ok {
		return relocationPlan{}, fmt.Errorf("region %d resolved but has no path", newParentRegionID)
	}
	newParentDepth := len(newParentPath.PathIDs)

	// Depth: the mirrored root lands at newParentDepth+1; every other
	// mirrored node lands newParentDepth+1+RelativeDepth deep. A single
	// pass over the subtree finds the shallowest violation, if any --
	// named with the specific region that would be too deep, e.g. a Shelf
	// mirrored beneath a Pot.
	for _, n := range subtree {
		mirroredDepth := newParentDepth + 1 + n.RelativeDepth
		if mirroredDepth > maxRegionDepth {
			return relocationPlan{}, contract.InvalidArgument("relocate_subtree", "new_parent_region_id", fmt.Sprintf(
				"Relocating %q here would place %q beyond the deepest allowed region level (Room / Shelf / Pot).",
				root.Name, n.Name,
			))
		}
	}

	// Children: only new_parent_region_id gains a new child directly (the
	// mirrored subtree root) -- every other mirrored parent is a
	// brand-new region with zero existing children, so its post-mirror
	// child count is identical to the original subtree's own (already
	// FR50.1-valid, since CreateRegion enforced it when those children
	// were created).
	var existingChildCount int
	if err := r.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM region WHERE parent_region_id = $1 AND retired_at IS NULL
	`, newParentRegionID).Scan(&existingChildCount); err != nil {
		return relocationPlan{}, fmt.Errorf("count children of region %d: %w", newParentRegionID, err)
	}
	if existingChildCount+1 > maxRegionChildren {
		return relocationPlan{}, contract.InvalidArgument("relocate_subtree", "new_parent_region_id", fmt.Sprintf(
			"%q already has the maximum of %d child regions.", newParent.Name, maxRegionChildren,
		))
	}

	originalIDs := make([]int64, len(subtree))
	for i, n := range subtree {
		originalIDs[i] = n.RegionID
	}

	return relocationPlan{
		Root:              root,
		NewParent:         newParent,
		HouseholdID:       rootRes.HouseholdID,
		Subtree:           subtree,
		OriginalRegionIDs: originalIDs,
	}, nil
}

// regionSubtree returns rootRegionID's own row and every descendant
// reachable via parent_region_id, in parent-first order (ordered by
// relative depth ascending) -- mirroring is refused or performed on the
// whole subtree regardless of any descendant's own already-retired state
// (see relocate.go's write step: an already-retired descendant only gets
// a successor_region_id, never a new retired_at), so this walk does not
// filter on retired_at.
func (r *Repository) regionSubtree(ctx context.Context, rootRegionID int64) ([]relocationSubtreeNode, error) {
	rows, err := r.db.Query(ctx, `
		WITH RECURSIVE subtree AS (
			SELECT region_id, parent_region_id, name, description, 0 AS depth
			FROM region WHERE region_id = $1

			UNION ALL

			SELECT r.region_id, r.parent_region_id, r.name, r.description, s.depth + 1
			FROM region r
			JOIN subtree s ON r.parent_region_id = s.region_id
		)
		SELECT region_id, parent_region_id, name, description, depth
		FROM subtree
		ORDER BY depth
	`, rootRegionID)
	if err != nil {
		return nil, fmt.Errorf("walk subtree for region %d: %w", rootRegionID, err)
	}
	defer rows.Close()

	var nodes []relocationSubtreeNode
	for rows.Next() {
		var n relocationSubtreeNode
		if err := rows.Scan(&n.RegionID, &n.ParentRegionID, &n.Name, &n.Description, &n.RelativeDepth); err != nil {
			return nil, fmt.Errorf("scan subtree node for region %d: %w", rootRegionID, err)
		}
		nodes = append(nodes, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("walk subtree for region %d: %w", rootRegionID, err)
	}
	return nodes, nil
}

// RelocateSubtree relocates rootRegionID's subtree under
// newParentRegionID in one atomic operation (FR74): see this file's doc
// comment for the mirror/move/retire composition and why each step reuses
// an existing writer rather than introducing a new one. reason is
// required -- it becomes the one audit record's Reason via
// audit.NewRelocationEntry. entry carries the caller's own identity
// (ActorSubject, ActorKind, CorrelationID) -- RelocateSubtree builds the
// actual recorded audit.Entry from it plus reason and the resolved
// household/entity, via audit.NewRelocationEntry, exactly the constructor
// this task's scaffold named for this action's own registered reason
// requirement.
func (r *Repository) RelocateSubtree(ctx context.Context, rootRegionID, newParentRegionID int64, reason string, scope authz.Scope, entry audit.Entry) (RelocationResult, error) {
	if msg := validateRelocationReason(reason); msg != "" {
		return RelocationResult{}, contract.InvalidArgument("relocate_subtree", "reason", msg)
	}

	plan, err := r.computeRelocationPlan(ctx, rootRegionID, newParentRegionID, scope)
	if err != nil {
		return RelocationResult{}, err
	}

	type sensorInvalidation struct {
		sensorID   int64
		sensorName string
		deviceID   string
	}

	var result RelocationResult
	var toInvalidate []sensorInvalidation

	writeErr := r.auditedWrite(ctx, func(tx pgx.Tx) (audit.Entry, error) {
		// Step 1: mirror the subtree under new_parent_region_id,
		// preserving relative structure and names. Parent-first order
		// (plan.Subtree is already sorted that way) guarantees a node's
		// mirrored parent already exists in idMap by the time the node
		// itself is inserted. household_id is left NULL for every
		// mirrored region -- every one of them has a parent
		// (new_parent_region_id at minimum), and only a tree root ever
		// carries household_id (migration 015's
		// enforce_region_household_root trigger).
		idMap := make(map[int64]int64, len(plan.Subtree))
		var newRoot RegionRow
		for _, n := range plan.Subtree {
			var parentArg int64
			if n.RegionID == rootRegionID {
				parentArg = newParentRegionID
			} else {
				mirroredParent, ok := idMap[*n.ParentRegionID]
				if !ok {
					return audit.Entry{}, fmt.Errorf("relocate subtree %d: no mirrored parent for region %d (parent %d)", rootRegionID, n.RegionID, *n.ParentRegionID)
				}
				parentArg = mirroredParent
			}

			var mirrored RegionRow
			if err := tx.QueryRow(ctx, `
				INSERT INTO region (parent_region_id, name, description, household_id)
				VALUES ($1, $2, $3, NULL)
				RETURNING region_id, parent_region_id, name, description, retired_at, successor_region_id
			`, parentArg, n.Name, n.Description).Scan(
				&mirrored.RegionID, &mirrored.ParentRegionID, &mirrored.Name, &mirrored.Description, &mirrored.RetiredAt, &mirrored.SuccessorRegionID,
			); err != nil {
				return audit.Entry{}, fmt.Errorf("mirror region %d: %w", n.RegionID, err)
			}
			idMap[n.RegionID] = mirrored.RegionID
			if n.RegionID == rootRegionID {
				newRoot = mirrored
			}
		}

		// Step 2a: move every current sensor placement (FR51) into the
		// mirrored regions -- reusing assignSensorRegionTx
		// (sensor_region.go), the exact write AssignSensorRegion itself
		// uses, marked relocation_induced = TRUE (FR24).
		rows, err := tx.Query(ctx, `
			SELECT sensor_id, region_id FROM sensor_region_history
			WHERE region_id = ANY($1) AND valid_to IS NULL
		`, plan.OriginalRegionIDs)
		if err != nil {
			return audit.Entry{}, fmt.Errorf("find current sensor placements for region %d relocation: %w", rootRegionID, err)
		}
		type sensorMove struct {
			sensorID    int64
			oldRegionID int64
		}
		var sensorMoves []sensorMove
		for rows.Next() {
			var m sensorMove
			if err := rows.Scan(&m.sensorID, &m.oldRegionID); err != nil {
				rows.Close()
				return audit.Entry{}, fmt.Errorf("scan current sensor placement for region %d relocation: %w", rootRegionID, err)
			}
			sensorMoves = append(sensorMoves, m)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return audit.Entry{}, fmt.Errorf("find current sensor placements for region %d relocation: %w", rootRegionID, err)
		}
		rows.Close()

		var movedSensorIDs []int64
		for _, m := range sensorMoves {
			newRegionID, ok := idMap[m.oldRegionID]
			if !ok {
				return audit.Entry{}, fmt.Errorf("relocate subtree %d: no mirrored region for sensor %d's current region %d", rootRegionID, m.sensorID, m.oldRegionID)
			}
			_, sensorName, deviceID, err := assignSensorRegionTx(ctx, tx, m.sensorID, newRegionID, true)
			if err != nil {
				return audit.Entry{}, fmt.Errorf("move sensor %d during relocation of region %d: %w", m.sensorID, rootRegionID, err)
			}
			movedSensorIDs = append(movedSensorIDs, m.sensorID)
			toInvalidate = append(toInvalidate, sensorInvalidation{sensorID: m.sensorID, sensorName: sensorName, deviceID: deviceID})
		}

		// Step 2b: move every current plant placement (FR54) into the
		// mirrored regions -- reusing placement.MoveRelocatedTx, the same
		// SCD2 close-and-open writer CreatePlant/MovePlant use, marked
		// relocation_induced = TRUE (FR24).
		prows, err := tx.Query(ctx, `
			SELECT plant_id, region_id FROM plant_region_history
			WHERE region_id = ANY($1) AND valid_to IS NULL
		`, plan.OriginalRegionIDs)
		if err != nil {
			return audit.Entry{}, fmt.Errorf("find current plant placements for region %d relocation: %w", rootRegionID, err)
		}
		type plantMove struct {
			plantID     int64
			oldRegionID int64
		}
		var plantMoves []plantMove
		for prows.Next() {
			var m plantMove
			if err := prows.Scan(&m.plantID, &m.oldRegionID); err != nil {
				prows.Close()
				return audit.Entry{}, fmt.Errorf("scan current plant placement for region %d relocation: %w", rootRegionID, err)
			}
			plantMoves = append(plantMoves, m)
		}
		if err := prows.Err(); err != nil {
			prows.Close()
			return audit.Entry{}, fmt.Errorf("find current plant placements for region %d relocation: %w", rootRegionID, err)
		}
		prows.Close()

		now := time.Now()
		var movedPlantIDs []int64
		for _, m := range plantMoves {
			newRegionID, ok := idMap[m.oldRegionID]
			if !ok {
				return audit.Entry{}, fmt.Errorf("relocate subtree %d: no mirrored region for plant %d's current region %d", rootRegionID, m.plantID, m.oldRegionID)
			}
			if _, err := placement.MoveRelocatedTx(ctx, tx, m.plantID, newRegionID, now); err != nil {
				return audit.Entry{}, fmt.Errorf("move plant %d during relocation of region %d: %w", m.plantID, rootRegionID, err)
			}
			movedPlantIDs = append(movedPlantIDs, m.plantID)
		}

		// Step 3: retire the original regions in place (FR22.2) with
		// successor_region_id naming their replacements. An original
		// region already retired before this relocation (an earlier,
		// unrelated RetireRegion) keeps its own original retired_at --
		// COALESCE only fills it in for a region this relocation is the
		// one retiring -- but every original region, retired or not,
		// gets a successor_region_id so a region-keyed series joins
		// across the move.
		for _, n := range plan.Subtree {
			newRegionID := idMap[n.RegionID]
			if _, err := tx.Exec(ctx, `
				UPDATE region
				SET retired_at = COALESCE(retired_at, NOW()),
				    successor_region_id = $2
				WHERE region_id = $1
			`, n.RegionID, newRegionID); err != nil {
				return audit.Entry{}, fmt.Errorf("retire original region %d during relocation: %w", n.RegionID, err)
			}
		}

		// FR20: captures for every moved placement boundary, in the same
		// transaction. Every affected sensor for this relocation's
		// boundary is exactly the set of sensors that themselves moved
		// (movedSensorIDs) -- the mirrored regions are brand new and
		// start with no sensors of their own, and no sensor outside the
		// relocated subtree has its own attribution changed by regions
		// moving strictly beneath it (attribution walks from a sensor's
		// own region toward the root, never into descendants). boundaryAt
		// is read back from the transaction's own NOW() -- Postgres holds
		// a single transaction timestamp for the whole transaction, so
		// this is identical to the valid_from/assigned_at every write
		// above actually recorded, giving the whole relocation one shared
		// boundary instant.
		var boundaryAt time.Time
		if err := tx.QueryRow(ctx, `SELECT NOW()`).Scan(&boundaryAt); err != nil {
			return audit.Entry{}, fmt.Errorf("read relocation boundary instant for region %d: %w", rootRegionID, err)
		}
		if err := capture.NewRecorder().Record(ctx, tx, movedSensorIDs, boundaryAt); err != nil {
			return audit.Entry{}, fmt.Errorf("record boundary capture for region %d relocation: %w", rootRegionID, err)
		}

		// FR8: one audit record for the whole operation, carrying reason
		// -- audit.NewRelocationEntry, not one record per moved
		// region/sensor/plant. entityID names the relocated subtree's
		// original root region_id, mirroring NewTransferEntry's entityID
		// naming the board being transferred.
		entityIDStr := strconv.FormatInt(rootRegionID, 10)
		hh := plan.HouseholdID
		finalEntry := audit.NewRelocationEntry(entry.ActorSubject, entry.ActorKind, &hh, &entityIDStr, reason, entry.CorrelationID)

		result = RelocationResult{
			NewRoot:               newRoot,
			RegionsMirrored:       len(plan.Subtree),
			SensorPlacementsMoved: len(movedSensorIDs),
			PlantPlacementsMoved:  len(movedPlantIDs),
		}
		return finalEntry, nil
	})
	if writeErr != nil {
		return RelocationResult{}, writeErr
	}

	// FR73: publish only after the write above has committed (it has, by
	// construction: auditedWrite only returns nil once tx.Commit
	// succeeded) -- never before, mirroring AssignSensorRegion's own
	// publish-after-commit discipline (sensor_region.go's doc comment),
	// including its nil-publisher and publish-failure handling.
	if r.invalidationPub != nil {
		for _, inv := range toInvalidate {
			_ = r.invalidationPub.Publish(ctx, invalidation.Event{
				Kind:       invalidation.KindRegion,
				DeviceID:   inv.deviceID,
				SensorID:   inv.sensorID,
				SensorName: inv.sensorName,
				ObservedAt: time.Now(),
			})
		}
	}

	return result, nil
}
