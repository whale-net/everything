package main

import (
	"context"
	"fmt"
	"strings"

	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/api/contract"
	"github.com/whale-net/everything/leaflab/hwkey"
)

// checkPushConfigIdentity is FR17's pre-write identity check: it runs
// before PushDeviceConfig writes or publishes anything (the "real push
// path" -- FR17 explicitly is not satisfied by a dry-run-only check, per
// FR82), and decides whether every entry in sensors continues an existing
// sensor's identity (FR16 cases 1/2) or whether any entry would establish
// a genuinely new one (case 3), or would require an unresolved swap
// (FR16.4).
//
// PushDeviceConfig entries are overrides matched against sensors a device
// manifest has already registered (see api.proto's doc comment on
// PushDeviceConfigRequest.sensors) -- this method never writes a sensor
// row itself, on either outcome; it only decides whether the push may
// proceed at all. The actual identity write for a genuine rewire happens
// on the device manifest path (leaflab/processor/repository.go's
// UpsertSensor / RewireAndRenameSensor) once the device reports it.
func (s *LeafLabAPIServer) checkPushConfigIdentity(ctx context.Context, boardID int64, sensors []*configpb.SensorConfig) error {
	if len(sensors) == 0 {
		return nil
	}

	existing, err := s.repo.LoadBoardSensorIdentities(ctx, boardID)
	if err != nil {
		s.logger.Error("load board sensor identities failed", "board_id", boardID, "error", err)
		return contract.Internal("device_config", "", "Could not process this request right now. Please try again.")
	}
	if len(existing) == 0 {
		// Nothing has been manifested for this board yet: there is no
		// existing identity for any entry to continue, collide with, or
		// be refused against. FR16/FR17 have nothing to protect here.
		return nil
	}

	// Resolves a proto SensorType's DB sensor_type_id, memoised per call
	// since a payload commonly repeats the same type across entries.
	typeIDCache := make(map[string]int64)
	resolveTypeID := func(typeName string) (int64, bool, error) {
		if id, ok := typeIDCache[typeName]; ok {
			return id, true, nil
		}
		id, ok, err := s.repo.resolveSensorTypeID(ctx, typeName)
		if err != nil || !ok {
			return 0, ok, err
		}
		typeIDCache[typeName] = id
		return id, true, nil
	}

	type entryMatch struct {
		name      string
		hwMatch   *BoardSensorIdentity // FR16 case 1 candidate
		nameMatch *BoardSensorIdentity // FR16 case 2 candidate
	}
	matches := make([]entryMatch, len(sensors))

	for i, sc := range sensors {
		m := entryMatch{name: sc.Name}

		if hw := HardwareAddressFromSensorConfig(sc.MuxPath, sc.I2CAddress); hw.hasKnownAddress() {
			typeName := sensorTypeNameFromConfig(sc.SensorType)
			sensorTypeID, ok, err := resolveTypeID(typeName)
			if err != nil {
				s.logger.Error("resolve sensor_type_id failed", "type", typeName, "error", err)
				return contract.Internal("device_config", "", "Could not process this request right now. Please try again.")
			}
			if ok {
				key := hwkey.Key{I2CAddress: hw.I2CAddress, MuxPath: hw.MuxPath, SensorTypeID: hwkey.SensorTypeID(sensorTypeID)}
				for j := range existing {
					bsi := &existing[j]
					if bsi.HW == nil || bsi.SensorTypeID != sensorTypeID {
						continue
					}
					exKey := hwkey.Key{I2CAddress: bsi.HW.I2CAddress, MuxPath: bsi.HW.MuxPath, SensorTypeID: hwkey.SensorTypeID(bsi.SensorTypeID)}
					if key.Equal(exKey) {
						m.hwMatch = bsi
						break
					}
				}
			}
		}

		for j := range existing {
			if existing[j].Name == sc.Name {
				m.nameMatch = &existing[j]
				break
			}
		}

		matches[i] = m
	}

	// FR16.4: swap detection. An entry whose hardware-key match and name
	// match resolve to two *different* existing sensors is exactly the
	// "exchange two sensors' hardware keys" scenario FR16.4 describes:
	// applying FR16's normal case-1-first priority would silently move a
	// different sensor's identity onto this entry's name rather than
	// continuing the sensor this entry's own name currently identifies.
	// FR39's within-payload collision check cannot see this -- each
	// entry's own address/name is unique within the payload; the
	// collision is against pre-push DB state, not against another entry.
	// Refused naming every entry involved, rather than attempting a
	// same-transaction atomic swap: simpler, and a partial application is
	// explicitly disallowed by FR16.4.
	var conflicted []string
	seenConflict := make(map[string]bool)
	addConflict := func(name string) {
		if name != "" && !seenConflict[name] {
			seenConflict[name] = true
			conflicted = append(conflicted, name)
		}
	}
	for _, m := range matches {
		if m.hwMatch != nil && m.nameMatch != nil && m.hwMatch.SensorID != m.nameMatch.SensorID {
			addConflict(m.name)
			addConflict(m.hwMatch.Name)
		}
	}
	if len(conflicted) > 0 {
		return contract.Refuse(
			"device_config",
			"sensors",
			fmt.Sprintf(
				"This push would exchange hardware addresses between existing sensors (%s); it cannot be applied without risking a misattributed sensor identity.",
				strings.Join(conflicted, ", "),
			),
			"Push each sensor's hardware move separately with RewireSensor, one at a time, or push a config that does not reassign another sensor's current hardware address.",
		)
	}

	// FR17 case 3: an entry that continues neither an existing hardware
	// key nor an existing name establishes a genuinely new sensor
	// identity. Refuse before anything is written or published -- this
	// check runs on PushDeviceConfig's real push path (its only caller),
	// not a dry run, so FR82's requirement that the real push compute the
	// same effective payload as a dry run is satisfied by construction:
	// there is only one code path.
	for _, m := range matches {
		if m.hwMatch == nil && m.nameMatch == nil {
			return contract.Refuse(
				"device_config",
				"sensors",
				fmt.Sprintf(
					"%q does not match any existing sensor on this device by hardware address or name; pushing it would create a new sensor identity, and its reading history would not follow.",
					m.name,
				),
				"Use RewireSensor if this hardware address belongs to a sensor that already exists under a different name.",
			)
		}
	}

	return nil
}
