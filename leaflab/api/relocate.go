package main

import (
	"context"
	"errors"

	"github.com/whale-net/everything/leaflab/api/audit"
	"github.com/whale-net/everything/leaflab/api/authz"
)

// This file is #1380's scaffold for FR74/FR24 (relocation-induced half) --
// api.proto's RelocateSubtree RPC, this Phase 5 task's atomic composition
// of operations the plan already requires ("introducing no new concept"):
//
//  1. mirror the subtree under new_parent_region_id, preserving relative
//     structure and names, validated against FR50's structural rules
//     (maxRegionChildren/maxRegionDepth, regions.go) before anything is
//     written (FR59.3) -- a 13th child or a Shelf under a Pot refuses the
//     whole operation, naming the violation;
//  2. move every current sensor placement (FR51) and every current plant
//     placement (FR54) into the mirrored regions -- reusing the existing
//     writers, not a third placement path: leaflab/api/placement.MoveTx
//     for plants, and AssignSensorRegion's own sensor_region_history
//     close-and-open write (sensor_region.go) for sensors. Both writers
//     currently have no way to mark the interval they open
//     relocation_induced = TRUE (placement.MoveTx always inserts the
//     column's FALSE default; AssignSensorRegion's inline INSERT has no
//     such column to set at all before migration 034 below) -- extending
//     both call shapes to accept that flag, without disturbing their
//     existing non-relocation callers (CreatePlant/MovePlant,
//     AssignSensorRegion itself), is Implementation-phase work this
//     scaffold defers, named here so it isn't rediscovered from scratch;
//  3. retire the original regions in place (FR22.2) with
//     successor_region_id naming their replacements (region.
//     successor_region_id, migration 020), so a region-keyed series joins
//     across the move.
//
// The whole operation is one transaction (Repository.auditedWrite, same
// shape as every other write in this package) -- computed and validated in
// full before the first write, so a structural refusal never leaves a
// partially mirrored subtree, and no intermediate state is observable
// (FR1.2 never violated, FR56 never reports a plant unmonitored
// mid-relocation -- this is testable by reading concurrently during the
// operation, per this task's Testing section). FR20 captures
// (leaflab/api/capture.Recorder.Record) run for every moved placement
// boundary in the same transaction; FR73 invalidations
// (leaflab/invalidation) publish after commit for every affected sensor,
// mirroring AssignSensorRegion's own publish-after-commit discipline. One
// audit record covers the whole operation (FR8), carrying reason, built
// via audit.NewRelocationEntry (leaflab/api/audit/reason.go) -- not one
// record per moved region/sensor/plant.
//
// FR50.5: SetRegionParent's refusal (regions.go) already names "Relocate
// the subtree instead (FR74)" as the alternative -- this RPC is the path
// that names, and the Testing phase's FR50.5 round-trip test asserts the
// refusal's named alternative actually succeeds end to end.
//
// No server.go handler wiring yet -- pb.UnimplementedLeafLabAPIServer
// still covers RelocateSubtree on the wire, matching every other Phase 5
// RPC's own deferred-wiring precedent (regions.go, plants.go,
// sensor_region.go's doc comments). RelocateSubtree below is a
// signature-only skeleton, returning ErrRelocateSubtreeNotImplemented,
// until Implementation wires in the three-step composition above.

// ErrRelocateSubtreeNotImplemented is returned by RelocateSubtree until the
// Implementation phase wires in the mirror/move/retire composition
// described in this file's doc comment. Mirrors ErrPlantOpNotImplemented
// (plants.go).
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

// RelocateSubtree relocates rootRegionID's subtree under
// newParentRegionID in one atomic operation (FR74): see this file's doc
// comment for the mirror/move/retire composition and why each step reuses
// an existing writer rather than introducing a new one. reason is
// required -- it becomes the one audit record's Reason via
// audit.NewRelocationEntry, mirroring RetireRegion's use of
// Repository.auditedWrite for the transactional/audit shape.
func (r *Repository) RelocateSubtree(ctx context.Context, rootRegionID, newParentRegionID int64, reason string, scope authz.Scope, entry audit.Entry) (RelocationResult, error) {
	return RelocationResult{}, ErrRelocateSubtreeNotImplemented
}
