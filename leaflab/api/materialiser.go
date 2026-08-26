package main

import (
	"encoding/json"
	"fmt"

	configpb "github.com/whale-net/everything/firmware/proto/config"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/leaflab/canonkey"
)

// Materialiser handles config materialisation for EDIT scope pushes (FR82).
type Materialiser struct{}

// NewMaterialiser creates a new config materialiser.
func NewMaterialiser() *Materialiser {
	return &Materialiser{}
}

// RemovalForm indicates whether a removal was specified as a full key or chip key.
type RemovalForm int

const (
	RemovalFormFull RemovalForm = iota
	RemovalFormChip
)

// RemovalResult tracks which form of removal was used for a removal key.
type RemovalResult struct {
	I2CAddress uint32
	MuxPath    []canonkey.MuxHop
	SensorType int32
	Form       RemovalForm // Full or Chip
}

// Materialize takes a base config (from the accepted version) and an EDIT request
// and produces a complete config by combining:
// - Entries named in the request (AUTHORED)
// - Entries carried forward from the base (MATERIALISED)
// - Minus entries in the remove list
//
// Returns the materialised config, a map of entry indices to provenance,
// a list of removal results showing which form was used, and an error if the base is nil
// (no accepted config to complete from).
func (m *Materialiser) Materialize(
	baseConfig *configpb.DeviceConfig,
	editPayload *pb.PushDeviceConfigRequest,
) (*configpb.DeviceConfig, map[int]pb.Provenance, []RemovalResult, error) {
	if baseConfig == nil {
		return nil, nil, nil, fmt.Errorf("materialiser: no base config to complete edit from; send a complete push")
	}

	// Build a set of authored keys (sensors being added/updated)
	authoredKeys := make(map[string]bool)

	// Track authored sensors by their canonical key
	for _, sensor := range editPayload.Sensors {
		key, err := canonkey.CanonicalizeKey(sensor)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("canonicalize sensor key: %w", err)
		}
		authoredKeys[keyString(key)] = true
	}

	// Parse and categorize removal keys
	removedFullKeys := make(map[string]bool)
	removedChipKeys := make(map[string]bool)
	removals := []RemovalResult{}

	for _, removalKey := range editPayload.Remove {
		muxPath := parseMuxPath(removalKey.MuxPath)

		if removalKey.SensorType == 0 {
			// Chip key removal (no sensor type)
			chipKeyStr := chipKeyString(removalKey.I2CAddress, muxPath)
			removedChipKeys[chipKeyStr] = true
			removals = append(removals, RemovalResult{
				I2CAddress: removalKey.I2CAddress,
				MuxPath:    muxPath,
				SensorType: 0,
				Form:       RemovalFormChip,
			})
		} else {
			// Full key removal
			fullKeyStr := fullKeyString(removalKey.I2CAddress, muxPath, removalKey.SensorType)
			removedFullKeys[fullKeyStr] = true
			removals = append(removals, RemovalResult{
				I2CAddress: removalKey.I2CAddress,
				MuxPath:    muxPath,
				SensorType: removalKey.SensorType,
				Form:       RemovalFormFull,
			})
		}
	}

	// Build materialised config
	result := &configpb.DeviceConfig{
		DeviceId: editPayload.DeviceId,
		Sensors:  make([]*configpb.SensorConfig, 0),
	}
	provenance := make(map[int]pb.Provenance)

	// Track which base sensors were kept or removed
	for _, baseSensor := range baseConfig.Sensors {
		key, err := canonkey.CanonicalizeKey(baseSensor)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("canonicalize base sensor key: %w", err)
		}
		keyStr := keyString(key)
		fullKeyStr := fullKeyString(key.I2CAddress, key.MuxPath, key.SensorType)
		chipKeyStr := chipKeyString(key.I2CAddress, key.MuxPath)

		// Skip if explicitly removed (either full key or chip key)
		if removedFullKeys[fullKeyStr] || removedChipKeys[chipKeyStr] {
			continue
		}

		// Skip if authored (will be added from editPayload)
		if authoredKeys[keyStr] {
			continue
		}

		// Carry forward from base as MATERIALISED
		result.Sensors = append(result.Sensors, baseSensor)
		provenance[len(result.Sensors)-1] = pb.Provenance_PROVENANCE_MATERIALISED
	}

	// Add authored sensors
	for _, sensor := range editPayload.Sensors {
		result.Sensors = append(result.Sensors, sensor)
		provenance[len(result.Sensors)-1] = pb.Provenance_PROVENANCE_AUTHORED
	}

	return result, provenance, removals, nil
}

// keyString returns a string representation of a Key for use in maps.
func keyString(key *canonkey.Key) string {
	return fmt.Sprintf("%d:%s:%d", key.I2CAddress, muxPathString(key.MuxPath), key.SensorType)
}

// fullKeyString returns a string key for (i2c_address, mux_path, sensor_type).
func fullKeyString(i2cAddr uint32, muxPath []canonkey.MuxHop, sensorType int32) string {
	return fmt.Sprintf("%d:%s:%d", i2cAddr, muxPathString(muxPath), sensorType)
}

// chipKeyString returns a string key for (i2c_address, mux_path).
func chipKeyString(i2cAddr uint32, muxPath []canonkey.MuxHop) string {
	return fmt.Sprintf("%d:%s", i2cAddr, muxPathString(muxPath))
}

// muxPathString returns a string representation of a mux path.
func muxPathString(path []canonkey.MuxHop) string {
	if len(path) == 0 {
		return ""
	}
	result := ""
	for i, hop := range path {
		if i > 0 {
			result += ","
		}
		result += fmt.Sprintf("%d:%d", hop.MuxAddress, hop.MuxChannel)
	}
	return result
}

// parseMuxPath decodes a mux_path string from the RemovalKey proto.
// The mux_path is stored as a JSONB-encoded string in the proto wire format.
func parseMuxPath(protoMuxPath string) []canonkey.MuxHop {
	if protoMuxPath == "" {
		return []canonkey.MuxHop{}
	}

	var hops []*configpb.MuxHop
	if err := json.Unmarshal([]byte(protoMuxPath), &hops); err != nil {
		// If parsing fails, return empty (shouldn't happen with valid proto)
		return []canonkey.MuxHop{}
	}

	result := make([]canonkey.MuxHop, 0, len(hops))
	for _, hop := range hops {
		if hop != nil {
			result = append(result, canonkey.MuxHop{
				MuxAddress: hop.MuxAddress,
				MuxChannel: hop.MuxChannel,
			})
		}
	}
	return result
}
