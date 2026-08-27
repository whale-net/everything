package main

import (
	"context"
	"fmt"

	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/api/config"
	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/leaflab/hwkey"
)

// resolveTypeIDCached wraps s.repo.resolveSensorTypeID with a per-call
// memoisation cache -- shared by resolveConfigEntries and
// resolveRemoveKeys (and mirroring checkPushConfigIdentity's own local
// cache in identity.go), since one payload commonly repeats the same
// sensor type across several entries.
func (s *LeafLabAPIServer) resolveTypeIDCached(ctx context.Context, cache map[string]int64, typeName string) (int64, bool, error) {
	if id, ok := cache[typeName]; ok {
		return id, true, nil
	}
	id, ok, err := s.repo.resolveSensorTypeID(ctx, typeName)
	if err != nil || !ok {
		return 0, ok, err
	}
	cache[typeName] = id
	return id, true, nil
}

// resolveConfigEntries canonicalises every entry in sensors into a
// //leaflab/api/config.Entry (FR18/FR82), resolving each entry's
// sensor_type against the catalog first. It never fails -- and never
// drops an entry -- solely because a sensor_type can't be resolved: a
// single-virtual chip (e.g. BH1750, per
// leaflab/scripts/scenarios/single-light.json) reports no explicit
// SensorType at all, and this package has no chip-type-to-sensor-type
// resolution of its own to fall back on (the same limitation
// checkPushConfigIdentity, identity.go, already has and already tolerates
// by falling back to name-based matching). Such an entry's Key carries
// hwkey.SensorTypeID(0) -- never a real catalog id, since sensor_type is
// BIGSERIAL starting at 1 -- as an "unresolved" sentinel:
// InsertDeviceConfigNextVersion (repository.go) skips writing a
// device_config_entry row for it rather than violate that table's
// sensor_type_id FK, but the entry itself is always still fully present
// in the returned slice and therefore in the stored config_json/materialised
// sensors list -- FR82.2's "a COMPLETE push always works" is never
// compromised by an unresolvable type.
//
// A sensor_type that *is* named (typeName != "") but genuinely unknown to
// the catalog is treated the same way (sentinel 0), not as an error --
// only resolveRemoveKeys below, where a caller explicitly names a
// sensor_type on a *removal*, treats an unresolved name as a caller
// mistake worth refusing.
func (s *LeafLabAPIServer) resolveConfigEntries(ctx context.Context, sensors []*configpb.SensorConfig) ([]config.Entry, error) {
	entries := make([]config.Entry, 0, len(sensors))
	typeIDCache := make(map[string]int64)
	for _, sc := range sensors {
		var sensorTypeID hwkey.SensorTypeID
		if typeName := sensorTypeNameFromConfig(sc.SensorType); typeName != "" {
			id, ok, err := s.resolveTypeIDCached(ctx, typeIDCache, typeName)
			if err != nil {
				s.logger.Error("resolve sensor_type_id failed", "type", typeName, "error", err)
				return nil, contract.Internal("device_config", "", "Could not process this request right now. Please try again.")
			}
			if ok {
				sensorTypeID = hwkey.SensorTypeID(id)
			}
			// !ok falls through with sensorTypeID left at the zero value --
			// see doc comment above.
		}
		entries = append(entries, config.Entry{
			Key:    config.CanonicalKey(sc, sensorTypeID),
			Sensor: sc,
		})
	}
	return entries, nil
}

// resolveRemoveKeys canonicalises every wire pb.RemoveKey in removes into a
// //leaflab/api/config.RemoveKey (FR82.4), resolving sensor_type against
// the catalog when the wire message actually set it (optional presence,
// rk.SensorType != nil -- distinguishing a full canonical key from a chip
// key, per RemoveKey's own proto doc comment).
//
// Unlike resolveConfigEntries, a sensor_type explicitly named on a
// *removal* that the catalog doesn't recognise is refused as caller error
// (invalid_argument) rather than silently falling back to a sentinel:
// naming a sensor_type that doesn't exist can never validly select any
// entry, and letting it through would risk that sentinel accidentally
// colliding with an unrelated single-virtual-chip entry's own sentinel
// key (see resolveConfigEntries' doc comment) instead of visibly refusing
// the request.
func (s *LeafLabAPIServer) resolveRemoveKeys(ctx context.Context, removes []*pb.RemoveKey) ([]config.RemoveKey, error) {
	keys := make([]config.RemoveKey, 0, len(removes))
	typeIDCache := make(map[string]int64)
	for i, rk := range removes {
		hasSensorType := rk.SensorType != nil
		var sensorTypeID int64
		if hasSensorType {
			typeName := sensorTypeNameFromConfig(rk.GetSensorType())
			id, ok, err := s.resolveTypeIDCached(ctx, typeIDCache, typeName)
			if err != nil {
				s.logger.Error("resolve sensor_type_id for remove failed", "type", typeName, "error", err)
				return nil, contract.Internal("device_config", "", "Could not process this request right now. Please try again.")
			}
			if !ok {
				return nil, contract.InvalidArgument(
					"device_config",
					fmt.Sprintf("removes[%d].sensor_type", i),
					"This sensor type is not recognized.",
				)
			}
			sensorTypeID = id
		}
		keys = append(keys, config.RemoveKeyFromProto(rk, hwkey.SensorTypeID(sensorTypeID), hasSensorType))
	}
	return keys, nil
}
