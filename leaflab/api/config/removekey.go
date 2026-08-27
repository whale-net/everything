package config

import (
	"errors"

	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/leaflab/hwkey"
)

// RemoveForm records which of FR82.4's two removal forms a RemoveKey used,
// for the "which form was used" statement the caller is owed back in the
// response.
type RemoveForm int

const (
	// RemoveFormFullKey: sensor_type was set on the wire -- drops exactly
	// the one entry at (i2c_address, mux_path, sensor_type).
	RemoveFormFullKey RemoveForm = iota
	// RemoveFormChipKey: sensor_type was absent on the wire -- drops every
	// entry sharing (i2c_address, mux_path) regardless of sensor_type, the
	// form that matches the physical action of unsoldering a part (e.g. a
	// CCS811 chip key drops both its eCO2 and TVOC entries).
	RemoveFormChipKey
)

// String renders f for logs and caller-visible "which form was used" text.
func (f RemoveForm) String() string {
	switch f {
	case RemoveFormFullKey:
		return "full_key"
	case RemoveFormChipKey:
		return "chip_key"
	default:
		return "unknown"
	}
}

// RemoveKey is FR82.4's removal descriptor, canonicalised from the wire
// pb.RemoveKey via RemoveKeyFromProto. HasSensorType distinguishes a full
// canonical key (true) from a chip key (false) -- see api.proto's
// RemoveKey doc comment for why the wire field is `optional` specifically
// so this distinction survives.
type RemoveKey struct {
	Chip          hwkey.ChipKey
	SensorTypeID  hwkey.SensorTypeID
	HasSensorType bool
}

// Form reports which of FR82.4's two removal forms k represents.
func (k RemoveKey) Form() RemoveForm {
	if k.HasSensorType {
		return RemoveFormFullKey
	}
	return RemoveFormChipKey
}

// ErrUnaddressableRemove is returned by Match (and by Materialise, which
// calls it) when a RemoveKey names the legacy manifests' "unknown address"
// sentinel (i2c_address == 0, FR18.2) -- FR82.4's stated EDIT limitation:
// such an entry has no removal path via EDIT at all, only via a COMPLETE
// push that omits it. This package stays free of leaflab/api/contract, so
// it has no FailureClass of its own to attach -- the caller
// (PushDeviceConfig's handler) is the one place that both sees this error
// and knows FR39's distinct failure class and the "push scope=COMPLETE"
// remedy text to state.
var ErrUnaddressableRemove = errors.New("config: entry has no addressable i2c_address and cannot be named by a remove key")

// Match reports which of base's entries k selects: for a full canonical
// key, at most one entry (i2c_address, mux_path and sensor_type all
// equal); for a chip key, every entry sharing (i2c_address, mux_path)
// regardless of sensor_type. Returns ErrUnaddressableRemove -- matching
// nothing -- if k names the unknown-address sentinel.
func (k RemoveKey) Match(base []Entry) ([]Entry, error) {
	if k.Chip.I2CAddress.IsUnknownSentinel() {
		return nil, ErrUnaddressableRemove
	}
	var matched []Entry
	for _, e := range base {
		if !e.Key.Chip().Equal(k.Chip) {
			continue
		}
		if k.HasSensorType && e.Key.SensorTypeID != k.SensorTypeID {
			continue
		}
		matched = append(matched, e)
	}
	return matched, nil
}

// RemoveKeyFromProto canonicalises a wire pb.RemoveKey into a RemoveKey,
// given its already-resolved sensor_type_id and whether the wire
// message's optional sensor_type was actually set. Both are the caller's
// job (translating proto3 `optional` presence and resolving the wire
// firmware.SensorType against the sensor_type catalog) -- this package
// has no catalog dependency of its own, matching CanonicalKey's split.
func RemoveKeyFromProto(rk *pb.RemoveKey, sensorTypeID hwkey.SensorTypeID, hasSensorType bool) RemoveKey {
	return RemoveKey{
		Chip: hwkey.ChipKey{
			I2CAddress: hwkey.Address(uint16(rk.GetI2CAddress())),
			MuxPath:    canonicalMuxPath(rk.GetMuxPath()),
		},
		SensorTypeID:  sensorTypeID,
		HasSensorType: hasSensorType,
	}
}
