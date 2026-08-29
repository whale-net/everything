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
	"github.com/whale-net/everything/leaflab/invalidation"
)

// This file is #1379's implementation of FR51/FR52 (Phase 5 -- Plants,
// regions, and the grower surface): AssignSensorRegion and RenameSensor,
// api.proto's two newest RPCs. It follows 015_ownership's scaffold/feat
// split, the same one #1376's regions.go already followed for
// CreateRegion/RenameRegion/RetireRegion (see that file's doc comment):
// both writes below authorize like every other sensor-scoped write
// (authorizeSensorWrite, mirroring authorizeRegionWrite), write inside
// Repository.auditedWrite (FR8), and -- like regions.go's writes -- have no
// server.go RPC handler wiring yet; every function below is exercised
// directly by the Testing phase, matching CreateRegion/RenameRegion/
// RetireRegion's own precedent (regions.go's doc comment) and RetireBoard's
// before them (repository.go).
//
// Unlike regions.go's writes, both of these change what a cached view of a
// sensor resolves to (its region or its name), so FR73 applies: each
// publishes an invalidation.Event after its own transaction commits, via
// Repository.invalidationPub (repository.go). Repository.invalidationPub
// is nil in every test fixture that doesn't explicitly wire one (via
// SetInvalidationPublisher) -- every publish call site below checks for
// nil first and treats "no publisher" as "don't publish", not an error;
// see repository.go's field doc comment. A publish failure once a
// publisher exists is also non-fatal: the write has already committed by
// that point, and FR73's bounded staleness backstop
// (leaflab/processor's RunCacheBackstop) self-heals a dropped event within
// its own bound. Repository has no logger to record the failure with (it
// never has; this is a pure persistence layer, matching every other file
// in this package) -- swallowing it silently here is a deliberate,
// documented trade-off, not an oversight.
//
// AssignSensorRegion (FR51) is the load-bearing case FR1.3's staleness
// clause was written for: it commits immediately -- closing the open
// sensor_region_history interval, opening a new one, and updating
// sensor.region_id, all in one transaction -- with no device round trip, no
// config version bump and no board availability requirement. FR1.2 is
// enforced via authz.AssertSameHousehold, mirroring
// LeafLabAPIServer.validatePushRegions' error-shape handling (server.go)
// exactly, translated to a single region_id rather than a batch. FR20's
// phase-one boundary capture (leaflab/api/capture.Recorder.Record) runs in
// the same transaction, for the sensor whose region just changed, at the
// instant sensor_region_history's new interval opened (assigned_at) --
// this is the first production caller of that package (#1360 introduced
// it but never wired it into a writer; see the scope note filed on this
// task's scaffold commit, #1431).
//
// RenameSensor (FR52) writes only sensor_name_history (and the sensor.name
// cache) -- no config version bump, no publish to the device. The response
// states whether a config push is still needed: never true for the rename
// alone, only when the sensor's board already has a config push pending
// (device_config.acked_at IS NULL) queued separately -- see
// hasPendingDeviceConfig.

// ErrSensorNotFound is returned by AssignSensorRegion/RenameSensor when
// sensor_id names no row, and also -- per NFR2's "no existence oracle" --
// when sensor_id names a row outside the caller's authz.Scope. Mirrors
// ErrRegionNotFound (regions.go).
var ErrSensorNotFound = errors.New("sensor not found")

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

// authorizeSensorWrite resolves sensorID against scope and collapses
// "doesn't exist" and "exists, out of caller's scope" into the same
// ErrSensorNotFound (NFR2). Mirrors authorizeRegionWrite (regions.go)
// exactly, over authz.EntitySensor instead of authz.EntityRegion.
func (r *Repository) authorizeSensorWrite(ctx context.Context, sensorID int64, scope authz.Scope) (authz.Resolution, error) {
	resolver := authz.NewPGResolver(r.db)
	ref := authz.EntityRef{Kind: authz.EntitySensor, ID: sensorID}
	res, err := resolver.Resolve(ctx, ref)
	if err != nil {
		if errors.Is(err, authz.ErrNotFound) {
			return authz.Resolution{}, ErrSensorNotFound
		}
		return authz.Resolution{}, fmt.Errorf("resolve sensor %d: %w", sensorID, err)
	}
	if !scope.Permits(ref, res) {
		return authz.Resolution{}, ErrSensorNotFound
	}
	return res, nil
}

// assertRegionSameHousehold is FR1.2's enforcement point for
// AssignSensorRegion: regionID must resolve to writerHousehold (the
// sensor's board's current household, from authorizeSensorWrite above) or
// the write is refused, naming region_id as the offending field. Mirrors
// LeafLabAPIServer.validatePushRegions' error-shape handling (server.go)
// -- a contract.Failure from AssertSameHousehold passes through verbatim,
// an unresolvable region_id becomes a caller-facing invalid_argument
// rather than an internal error, and everything else is wrapped as an
// internal error for the caller above to translate.
func (r *Repository) assertRegionSameHousehold(ctx context.Context, writerHousehold, regionID int64) error {
	resolver := authz.NewPGResolver(r.db)
	err := authz.AssertSameHousehold(ctx, resolver, writerHousehold, authz.LiveRef{
		EntityRef: authz.EntityRef{Kind: authz.EntityRegion, ID: regionID},
		Field:     "region_id",
	})
	if err == nil {
		return nil
	}
	if _, ok := contract.FromError(err); ok {
		// AssertSameHousehold's own FailureInvalidArgument, already naming
		// region_id (FR1.3-style entry/field naming) -- pass through
		// verbatim.
		return err
	}
	if errors.Is(err, authz.ErrNotFound) {
		return contract.InvalidArgument("assign_sensor_region", "region_id", "This references something that doesn't exist.")
	}
	return fmt.Errorf("assign sensor region: check region %d household: %w", regionID, err)
}

// hasPendingDeviceConfig reports whether boardID has a device_config push
// awaiting an ack (acked_at IS NULL) -- FR52's "the response states
// whether a config push is still needed" for RenameSensor: the rename
// itself never needs one, but the caller may already have unrelated
// device-behaviour changes (poll interval, sensor set, addressing) queued
// separately via PushDeviceConfig, and this reports that state rather than
// silently dropping it from the rename's response.
func (r *Repository) hasPendingDeviceConfig(ctx context.Context, boardID int64) (bool, error) {
	var pending bool
	if err := r.db.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM device_config WHERE board_id = $1 AND acked_at IS NULL)
	`, boardID).Scan(&pending); err != nil {
		return false, fmt.Errorf("check pending device_config for board %d: %w", boardID, err)
	}
	return pending, nil
}

// assignSensorRegionTx is the sensor_region_history close-and-open write
// (FR51), extracted from AssignSensorRegion so FR74's atomic subtree
// relocation (leaflab/api/relocate.go) can reuse the exact same write path
// for the "move every current sensor placement into the mirrored regions"
// clause -- not a third placement path, the same discipline
// leaflab/api/placement.MoveTx/MoveRelocatedTx follow for plants.
// relocationInduced marks the opened interval per FR24; AssignSensorRegion
// itself always passes false. Returns the opened interval's valid_from
// (assignedAt), the sensor's name and its board's device_id -- both needed
// by callers to publish an FR73 invalidation.Event after commit.
func assignSensorRegionTx(ctx context.Context, tx pgx.Tx, sensorID, regionID int64, relocationInduced bool) (assignedAt time.Time, sensorName, deviceID string, err error) {
	// Close the sensor's current open sensor_region_history interval, if
	// any -- a sensor getting its first-ever region assignment simply
	// closes zero rows, not an error (mirrors placement.MoveTx's
	// close-and-open, and ApplyConfigRegions' own close step).
	if _, err = tx.Exec(ctx, `
		UPDATE sensor_region_history SET valid_to = NOW()
		WHERE sensor_id = $1 AND valid_to IS NULL
	`, sensorID); err != nil {
		return time.Time{}, "", "", fmt.Errorf("close region history for sensor %d: %w", sensorID, err)
	}

	// Open the new interval. valid_from is left to the column DEFAULT
	// NOW() -- returned here as assignedAt, which is both
	// AssignSensorRegion's response assigned_at (FR64) / FR1.3's staleness
	// comparison point for any later ApplyConfigRegions apply, and FR20's
	// boundaryAt for the caller's own capture call.
	if err = tx.QueryRow(ctx, `
		INSERT INTO sensor_region_history (sensor_id, region_id, relocation_induced)
		VALUES ($1, $2, $3)
		RETURNING valid_from
	`, sensorID, regionID, relocationInduced).Scan(&assignedAt); err != nil {
		return time.Time{}, "", "", fmt.Errorf("open region history for sensor %d: %w", sensorID, err)
	}

	// Sync sensor.region_id (the current-value cache) and read back the
	// sensor's name and its board's device_id in the same statement --
	// both needed for the invalidation.Event a caller publishes after
	// commit (see this file's doc comment), no second query.
	if err = tx.QueryRow(ctx, `
		UPDATE sensor s
		SET region_id = $2
		FROM board b
		WHERE s.sensor_id = $1 AND b.board_id = s.board_id
		RETURNING s.name, b.device_id
	`, sensorID, regionID).Scan(&sensorName, &deviceID); err != nil {
		return time.Time{}, "", "", fmt.Errorf("sync region cache for sensor %d: %w", sensorID, err)
	}

	return assignedAt, sensorName, deviceID, nil
}

// AssignSensorRegion is FR51's server-side-fact region write (Phase 5): it
// commits immediately, with no device round trip, no config version bump
// and no board availability requirement -- see this file's doc comment for
// FR1.2/FR73/FR20's enforcement points.
func (r *Repository) AssignSensorRegion(ctx context.Context, sensorID, regionID int64, scope authz.Scope, entry audit.Entry) (SensorRegionAssignment, error) {
	res, err := r.authorizeSensorWrite(ctx, sensorID, scope)
	if err != nil {
		return SensorRegionAssignment{}, err
	}

	if err := r.assertRegionSameHousehold(ctx, res.HouseholdID, regionID); err != nil {
		return SensorRegionAssignment{}, err
	}

	var result SensorRegionAssignment
	var deviceID, sensorName string
	writeErr := r.auditedWrite(ctx, func(tx pgx.Tx) (audit.Entry, error) {
		assignedAt, name, device, err := assignSensorRegionTx(ctx, tx, sensorID, regionID, false)
		if err != nil {
			return audit.Entry{}, err
		}
		sensorName, deviceID = name, device

		// FR20 phase one: the sensor whose region just changed is the
		// only affected sensor for this boundary (see the doc comment
		// above authorizeSensorWrite) -- unlike a plant placement move
		// (FR19), a sensor's own region assignment has no subtree to walk.
		if err := capture.NewRecorder().Record(ctx, tx, []int64{sensorID}, assignedAt); err != nil {
			return audit.Entry{}, fmt.Errorf("record boundary capture for sensor %d: %w", sensorID, err)
		}

		idStr := strconv.FormatInt(sensorID, 10)
		entry.EntityID = &idStr
		hh := res.HouseholdID
		entry.TargetHouseholdID = &hh
		result = SensorRegionAssignment{SensorID: sensorID, RegionID: regionID, AssignedAt: assignedAt}
		return entry, nil
	})
	if writeErr != nil {
		return SensorRegionAssignment{}, writeErr
	}

	// FR73: publish only after the write above has committed (it has, by
	// construction: auditedWrite only returns nil once tx.Commit
	// succeeded) -- never before. See this file's doc comment for the
	// nil-publisher and publish-failure handling.
	if r.invalidationPub != nil {
		_ = r.invalidationPub.Publish(ctx, invalidation.Event{
			Kind:       invalidation.KindRegion,
			DeviceID:   deviceID,
			SensorID:   sensorID,
			SensorName: sensorName,
			ObservedAt: time.Now(),
		})
	}

	return result, nil
}

// validateSensorName returns a persona-appropriate reason (FR59.2) if name
// is invalid once trimmed, or "" if it's valid. Mirrors validateRegionName
// (regions.go).
func validateSensorName(name string) string {
	if strings.TrimSpace(name) == "" {
		return "A sensor name is required."
	}
	return ""
}

// RenameSensor is FR52's rename-without-a-round-trip write (Phase 5): it
// writes only sensor_name_history and the sensor.name cache -- no config
// version bump, no publish to the device.
func (r *Repository) RenameSensor(ctx context.Context, sensorID int64, name string, scope authz.Scope, entry audit.Entry) (SensorRename, error) {
	if reason := validateSensorName(name); reason != "" {
		return SensorRename{}, contract.InvalidArgument("rename_sensor", "name", reason)
	}
	name = strings.TrimSpace(name)

	res, err := r.authorizeSensorWrite(ctx, sensorID, scope)
	if err != nil {
		return SensorRename{}, err
	}

	var priorName, deviceID string
	var boardID int64
	writeErr := r.auditedWrite(ctx, func(tx pgx.Tx) (audit.Entry, error) {
		// The name immediately before this write -- needed both to close
		// sensor_name_history's current interval implicitly (nothing reads
		// it back for that) and for the invalidation.Event published
		// after commit: a cache keyed device_id -> sensor_name must evict
		// the entry under this *prior* key explicitly, or the rename
		// leaves it orphaned (invalidation.Event.PriorSensorName's doc
		// comment).
		if err := tx.QueryRow(ctx, `
			SELECT s.name, s.board_id, b.device_id
			FROM sensor s
			JOIN board b ON b.board_id = s.board_id
			WHERE s.sensor_id = $1
		`, sensorID).Scan(&priorName, &boardID, &deviceID); err != nil {
			return audit.Entry{}, fmt.Errorf("read current name for sensor %d: %w", sensorID, err)
		}

		if _, err := tx.Exec(ctx, `
			UPDATE sensor_name_history SET valid_to = NOW()
			WHERE sensor_id = $1 AND valid_to IS NULL
		`, sensorID); err != nil {
			return audit.Entry{}, fmt.Errorf("close name history for sensor %d: %w", sensorID, err)
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO sensor_name_history (sensor_id, name) VALUES ($1, $2)
		`, sensorID, name); err != nil {
			return audit.Entry{}, fmt.Errorf("open name history for sensor %d: %w", sensorID, err)
		}

		if _, err := tx.Exec(ctx, `
			UPDATE sensor SET name = $2 WHERE sensor_id = $1
		`, sensorID, name); err != nil {
			return audit.Entry{}, fmt.Errorf("sync name cache for sensor %d: %w", sensorID, err)
		}

		idStr := strconv.FormatInt(sensorID, 10)
		entry.EntityID = &idStr
		hh := res.HouseholdID
		entry.TargetHouseholdID = &hh
		return entry, nil
	})
	if writeErr != nil {
		return SensorRename{}, writeErr
	}

	// FR52: reflects whether the sensor's board already has an unrelated
	// device-behaviour push pending -- never set by the rename itself
	// (see hasPendingDeviceConfig's doc comment).
	pushNeeded, err := r.hasPendingDeviceConfig(ctx, boardID)
	if err != nil {
		return SensorRename{}, err
	}

	// FR73: publish only after commit -- see AssignSensorRegion's
	// identical publish step and this file's doc comment for the
	// nil-publisher and publish-failure handling.
	if r.invalidationPub != nil {
		_ = r.invalidationPub.Publish(ctx, invalidation.Event{
			Kind:            invalidation.KindName,
			DeviceID:        deviceID,
			SensorID:        sensorID,
			SensorName:      name,
			PriorSensorName: priorName,
			ObservedAt:      time.Now(),
		})
	}

	return SensorRename{SensorID: sensorID, Name: name, ConfigPushNeeded: pushNeeded}, nil
}
