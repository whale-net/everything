package main

import (
	"encoding/json"
	"fmt"
	"strconv"

	configpb "github.com/whale-net/everything/firmware/proto/config"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/leaflab/canonkey"
	"github.com/whale-net/everything/firmware/sensor/catalog"
)

// ValidationFailure represents a single validation failure against an entry and field.
type ValidationFailure struct {
	// EntryIdentifier is either the entry index (for sensors) or the remove key for removals.
	EntryIdentifier string
	// Field is the field name that failed validation.
	Field string
	// FailureClass is the gRPC failure class for this failure.
	FailureClass pb.FailureClass
	// MessageKey is the UI-facing message key.
	MessageKey string
}

// Validator validates device configs against FR39 requirements.
type Validator struct {
	// chipCatalog maps chip name to sensor types it supports.
	chipCatalog map[string][]string
	// i2cAddressMap maps chip name to supported I2C addresses.
	i2cAddressMap map[string][]uint32
}

// NewValidator creates a new validator, loading the catalog.
func NewValidator() (*Validator, error) {
	chips, err := catalog.Load()
	if err != nil {
		return nil, fmt.Errorf("load catalog: %w", err)
	}

	v := &Validator{
		chipCatalog:   make(map[string][]string),
		i2cAddressMap: make(map[string][]uint32),
	}

	// Build chip catalog from loaded chips.
	for _, chip := range chips {
		v.chipCatalog[chip.Name] = chip.SensorTypes
		addresses := make([]uint32, 0, len(chip.Addresses))
		for _, addr := range chip.Addresses {
			addresses = append(addresses, uint32(addr.I2CAddress))
		}
		v.i2cAddressMap[chip.Name] = addresses
	}

	return v, nil
}

// Validate validates an effective config payload after materialisation,
// returning all validation failures found.
func (v *Validator) Validate(
	effectiveConfig *configpb.DeviceConfig,
	baseConfig *configpb.DeviceConfig,
	editPayload *pb.PushDeviceConfigRequest,
) []ValidationFailure {
	var failures []ValidationFailure

	// Track canonical keys to detect collisions.
	keyToIndices := make(map[string][]int)

	// Validate each sensor entry.
	for i, sensor := range effectiveConfig.Sensors {
		entryID := strconv.Itoa(i)

		// Validate I2C address range (0-127 for standard I2C).
		if sensor.I2cAddress > 127 {
			failures = append(failures, ValidationFailure{
				EntryIdentifier: entryID,
				Field:           "i2c_address",
				FailureClass:    pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT,
				MessageKey:      "INVALID_I2C_ADDRESS",
			})
		}

		// Validate chip/sensor-type pair.
		if !v.isValidChipSensorTypePair(sensor.ChipType, sensor.SensorType) {
			failures = append(failures, ValidationFailure{
				EntryIdentifier: entryID,
				Field:           "chip_type",
				FailureClass:    pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT,
				MessageKey:      "INVALID_CHIP_SENSOR_TYPE_PAIR",
			})
		}

		// Validate poll_interval_ms.
		if !v.isValidPollInterval(sensor.PollIntervalMs) {
			failures = append(failures, ValidationFailure{
				EntryIdentifier: entryID,
				Field:           "poll_interval_ms",
				FailureClass:    pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT,
				MessageKey:      "INVALID_POLL_INTERVAL",
			})
		}

		// Track canonical key for collision detection.
		key, err := canonkey.CanonicalizeKey(sensor)
		if err == nil {
			keyStr := keyString(key)
			keyToIndices[keyStr] = append(keyToIndices[keyStr], i)
		}
	}

	// Detect hardware key collisions.
	for _, indices := range keyToIndices {
		if len(indices) > 1 {
			// Multiple entries share the same hardware key.
			for _, idx := range indices {
				entryID := strconv.Itoa(idx)
				failures = append(failures, ValidationFailure{
					EntryIdentifier: entryID,
					Field:           "hardware_key",
					FailureClass:    pb.FailureClass_FAILURE_CLASS_PRECONDITION,
					MessageKey:      "DUPLICATE_HARDWARE_KEY",
				})
			}
		}
	}

	// Validate removals only for EDIT scope (removals are meaningless for COMPLETE).
	if editPayload.Scope == pb.ConfigScope_CONFIG_SCOPE_EDIT {
		failures = append(failures, v.validateRemovals(editPayload, baseConfig)...)
	}

	return failures
}

// isValidChipSensorTypePair checks if a chip/sensor-type pair is in the catalog.
func (v *Validator) isValidChipSensorTypePair(chipType configpb.ChipType, sensorType configpb.SensorType) bool {
	// ChipType values don't directly correspond to chip names; we need to map them.
	// For now, we check that the chip type is not UNKNOWN and the sensor type is valid for that chip.
	// Note: This requires the ChipType enum values to map to chip names.
	// For scaffolding, we'll accept all non-unknown chip types with non-unknown sensor types.
	// Implementation will refine this with proper catalog lookup.
	if chipType == configpb.CHIP_TYPE_UNKNOWN {
		return false
	}
	// TODO: Implement proper catalog lookup using chipType → chip name mapping.
	return true
}

// isValidPollInterval checks if poll_interval_ms is in a valid range.
func (v *Validator) isValidPollInterval(pollIntervalMs uint32) bool {
	// poll_interval_ms == 0 means "use device default", which is valid.
	// Maximum practical value: allow up to 1 hour (3,600,000 ms).
	// Minimum (if non-zero): allow down to 100ms for sanity.
	if pollIntervalMs == 0 {
		return true
	}
	if pollIntervalMs < 100 {
		return false
	}
	if pollIntervalMs > 3_600_000 {
		return false
	}
	return true
}

// validateRemovals checks that removal keys are valid and match existing entries.
func (v *Validator) validateRemovals(
	editPayload *pb.PushDeviceConfigRequest,
	baseConfig *configpb.DeviceConfig,
) []ValidationFailure {
	var failures []ValidationFailure

	if baseConfig == nil {
		// If there's no base config (impossible in EDIT scope, but defensive),
		// all removals are invalid.
		for _, removalKey := range editPayload.Remove {
			keyStr := removalKeyString(removalKey)
			failures = append(failures, ValidationFailure{
				EntryIdentifier: keyStr,
				Field:           "remove",
				FailureClass:    pb.FailureClass_FAILURE_CLASS_PRECONDITION,
				MessageKey:      "REMOVAL_KEY_NOT_FOUND",
			})
		}
		return failures
	}

	// Build a set of removable keys from the base config.
	removableFullKeys := make(map[string]bool)
	removableChipKeys := make(map[string]bool)
	baseSensorsByKey := make(map[string]*configpb.SensorConfig)

	for _, baseSensor := range baseConfig.Sensors {
		key, err := canonkey.CanonicalizeKey(baseSensor)
		if err != nil {
			continue
		}
		keyStr := keyString(key)
		removableFullKeys[keyStr] = true
		baseSensorsByKey[keyStr] = baseSensor

		// Also record chip key.
		chipKeyStr := chipKeyString(key.I2CAddress, key.MuxPath)
		removableChipKeys[chipKeyStr] = true
	}

	// Validate each removal key.
	for _, removalKey := range editPayload.Remove {
		muxPath := parseMuxPath(removalKey.MuxPath)
		keyStr := removalKeyString(removalKey)

		if removalKey.SensorType == 0 {
			// Chip key removal.
			chipKeyStr := chipKeyString(removalKey.I2CAddress, muxPath)
			if !removableChipKeys[chipKeyStr] {
				failures = append(failures, ValidationFailure{
					EntryIdentifier: keyStr,
					Field:           "remove",
					FailureClass:    pb.FailureClass_FAILURE_CLASS_PRECONDITION,
					MessageKey:      "REMOVAL_KEY_NOT_FOUND",
				})
			}
		} else {
			// Full key removal.
			fullKeyStr := fullKeyString(removalKey.I2CAddress, muxPath, removalKey.SensorType)
			if !removableFullKeys[fullKeyStr] {
				failures = append(failures, ValidationFailure{
					EntryIdentifier: keyStr,
					Field:           "remove",
					FailureClass:    pb.FailureClass_FAILURE_CLASS_PRECONDITION,
					MessageKey:      "REMOVAL_KEY_NOT_FOUND",
				})
				continue
			}

			// Check if the sensor being removed has i2c_address == 0.
			baseSensor := baseSensorsByKey[fullKeyStr]
			if baseSensor != nil && baseSensor.I2cAddress == 0 {
				failures = append(failures, ValidationFailure{
					EntryIdentifier: keyStr,
					Field:           "remove",
					FailureClass:    pb.FailureClass_FAILURE_CLASS_PRECONDITION,
					MessageKey:      "REMOVE_ENTRY_NO_ADDRESS",
				})
			}
		}
	}

	return failures
}

// keyString returns a string representation of a canonical key.
func keyString(key *canonkey.Key) string {
	return fmt.Sprintf("%d:%s:%d", key.I2CAddress, muxPathString(key.MuxPath), key.SensorType)
}

// chipKeyString returns a string key for (i2c_address, mux_path).
func chipKeyString(i2cAddr uint32, muxPath []canonkey.MuxHop) string {
	return fmt.Sprintf("%d:%s", i2cAddr, muxPathString(muxPath))
}

// fullKeyString returns a string key for (i2c_address, mux_path, sensor_type).
func fullKeyString(i2cAddr uint32, muxPath []canonkey.MuxHop, sensorType int32) string {
	return fmt.Sprintf("%d:%s:%d", i2cAddr, muxPathString(muxPath), sensorType)
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

// removalKeyString returns a string representation of a removal key for error reporting.
func removalKeyString(removalKey *pb.RemovalKey) string {
	muxPath := parseMuxPath(removalKey.MuxPath)
	if removalKey.SensorType == 0 {
		return chipKeyString(removalKey.I2CAddress, muxPath)
	}
	return fullKeyString(removalKey.I2CAddress, muxPath, removalKey.SensorType)
}

// parseMuxPath decodes a mux_path string from the RemovalKey proto.
func parseMuxPath(protoMuxPath string) []canonkey.MuxHop {
	// This is copied from materialiser.go for consistency.
	if protoMuxPath == "" {
		return []canonkey.MuxHop{}
	}

	var hops []*configpb.MuxHop
	if err := json.Unmarshal([]byte(protoMuxPath), &hops); err != nil {
		// If parsing fails, return empty (shouldn't happen with valid proto).
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
