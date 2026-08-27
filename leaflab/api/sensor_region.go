package main

import (
	"context"
	"errors"
	"time"

	"github.com/whale-net/everything/leaflab/api/audit"
	"github.com/whale-net/everything/leaflab/api/authz"
)

// This file is #1379's scaffold for FR51/FR52 (Phase 5 -- Plants, regions,
// and the grower surface): AssignSensorRegion and RenameSensor, api.proto's
// two newest RPCs. It follows 015_ownership's scaffold/feat split, the same
// one #1376's regions.go already followed for CreateRegion/RenameRegion/
// RetireRegion (see that file's doc comment): read paths, if any are
// needed, would be fully implemented here; the two writes below are
// signature-only skeletons that return ErrSensorOpNotImplemented until
// Implementation wires in the real logic. No migration is needed --
// sensor_region_history (migrations 001/011) and sensor_name_history
// (migrations 001/009/011, renamed from sensor_label) both already carry
// NFR6.1's index pair: a plain index on sensor_id
// (idx_sensor_region_history_sensor_id / idx_sensor_name_history_sensor_id)
// plus a partial index on sensor_id WHERE valid_to IS NULL
// (idx_sensor_region_history_current / idx_sensor_name_history_current).
//
// AssignSensorRegion (FR51) is the load-bearing case FR1.3's staleness
// clause was written for: it commits immediately -- closing the open
// sensor_region_history interval, opening a new one, and updating
// sensor.region_id, all in one transaction -- with no device round trip, no
// config version bump and no board availability requirement. Implementation
// must:
//
//   - route the target region through authz.AssertSameHousehold (FR1.2),
//     resolving the sensor's board's current household as the writer
//     household;
//   - publish an invalidation.Event{Kind: invalidation.KindRegion} on
//     commit (FR73) -- KindRegion already exists in
//     leaflab/invalidation/event.go, documented there as "written by the
//     API's direct region assignment (FR51, Phase 5)";
//   - in the same transaction, record FR20's phase-one boundary capture
//     (a sensor's region change moves which plants its readings attribute
//     to, so straddling buckets must be captured) via
//     leaflab/api/capture.Recorder.Record. That package does not exist on
//     this branch lineage yet -- it lands on #1360
//     (plan/1166-v2-1360, scaffolded at d14b06d1, implemented at 00e23e35),
//     which is not among this task's stated dependencies (#1376, #1356,
//     #1340). Implementation must either pull #1360's capture package in
//     (if it has since merged into this lineage) or, if it has not, file a
//     scope note recording the gap -- do not silently skip the boundary
//     capture call, and do not invent a substitute mechanism here.
//
// RenameSensor (FR52) writes only sensor_name_history (and the sensor.name
// cache) -- no config version bump, no publish to the device.
// Implementation must:
//
//   - publish an invalidation.Event{Kind: invalidation.KindName,
//     PriorSensorName: <old name>} on commit (FR73) -- KindName already
//     exists, documented as "written by a rename (FR52, Phase 5) ...
//     PriorSensorName is always set for this kind";
//   - report in the response whether a config push is still needed: never
//     true for the rename alone, only when the caller also has pending
//     device-behaviour changes (poll interval, sensor set, addressing)
//     queued separately (the "pending vs immediate split" this task's issue
//     describes).
//
// Both writes authorize like every other sensor-scoped write: resolve
// sensor_id via authz.EntitySensor and require scope.Permits, collapsing
// "doesn't exist" and "exists, out of scope" into the same not-found
// (NFR2), mirroring authorizeRegionWrite in regions.go. FR8 audit recording
// goes through Repository.auditedWrite exactly as every other write in
// this package.

// ErrSensorNotFound is returned by AssignSensorRegion/RenameSensor when
// sensor_id names no row, and also -- per NFR2's "no existence oracle" --
// when sensor_id names a row outside the caller's authz.Scope. Mirrors
// ErrRegionNotFound (regions.go).
var ErrSensorNotFound = errors.New("sensor not found")

// ErrSensorOpNotImplemented is returned by every write below until
// Implementation wires in the real logic. Mirrors ErrRegionOpNotImplemented
// as it existed in #1376's own scaffold commit (43df7c41) before that
// task's Implementation phase removed it.
var ErrSensorOpNotImplemented = errors.New("sensor operation not implemented")

// SensorRegionAssignment is AssignSensorRegion's result: the sensor's
// region immediately after the commit, and when that sensor_region_history
// interval opened (FR64) -- push-time for FR1.3's staleness comparison,
// there is no ack on this path.
type SensorRegionAssignment struct {
	SensorID   int64
	RegionID   int64
	AssignedAt time.Time
}

// SensorRename is RenameSensor's result: the sensor's name immediately
// after the commit, and whether a config push is still needed (FR52) --
// never true for the rename alone.
type SensorRename struct {
	SensorID         int64
	Name             string
	ConfigPushNeeded bool
}

// AssignSensorRegion is FR51's server-side-fact region write (Phase 5).
// Not yet implemented -- see this file's doc comment for what
// Implementation must wire (FR1.2's same-household enforcement, FR73
// invalidation, FR20's phase-one boundary capture, FR8 audit).
func (r *Repository) AssignSensorRegion(ctx context.Context, sensorID, regionID int64, scope authz.Scope, entry audit.Entry) (SensorRegionAssignment, error) {
	return SensorRegionAssignment{}, ErrSensorOpNotImplemented
}

// RenameSensor is FR52's rename-without-a-round-trip write (Phase 5). Not
// yet implemented -- see this file's doc comment.
func (r *Repository) RenameSensor(ctx context.Context, sensorID int64, name string, scope authz.Scope, entry audit.Entry) (SensorRename, error) {
	return SensorRename{}, ErrSensorOpNotImplemented
}
