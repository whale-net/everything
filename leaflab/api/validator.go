package main

import (
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
	// chipTypeNames maps ChipType enum values to chip names.
	chipTypeNames map[configpb.ChipType]string
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
		chipTypeNames: buildChipTypeNames(),
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

// buildChipTypeNames creates the mapping from ChipType enum to chip names.
func buildChipTypeNames() map[configpb.ChipType]string {
	return map[configpb.ChipType]string{
		configpb.ChipType_CHIP_TYPE_BH1750: "BH1750",
		configpb.ChipType_CHIP_TYPE_SHT3X:  "SHT3x",
		configpb.ChipType_CHIP_TYPE_CCS811: "CCS811",
	}
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
		if sensor.I2CAddress > 127 {
			failures = append(failures, ValidationFailure{
				EntryIdentifier: entryID,
				Field:           "i2c_address",
				FailureClass:    pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT,
				MessageKey:      "INVALID_I2C_ADDRESS",
			})
		}

		// Validate chip/sensor-type pair.
		if !v.isValidChipSensorTypePair(sensor.ChipType, int32(sensor.SensorType)) {
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
func (v *Validator) isValidChipSensorTypePair(chipType configpb.ChipType, sensorType int32) bool {
	// Chip type UNKNOWN is always invalid.
	if chipType == configpb.ChipType_CHIP_TYPE_UNKNOWN {
		return false
	}

	// Get the chip name from the enum value.
	chipName, ok := v.chipTypeNames[chipType]
	if !ok {
		// Unknown chip type enum value.
		return false
	}

	// Get the sensor types supported by this chip.
	supportedTypes, ok := v.chipCatalog[chipName]
	if !ok {
		// Chip not in catalog.
		return false
	}

	// Convert sensor type to int32 for comparison
	sensorTypeInt := int32(sensorType)

	// BH1750 is a single-virtual chip and should have sensor_type = UNKNOWN (0).
	if chipName == "BH1750" {
		return sensorTypeInt == 0 // SENSOR_TYPE_UNKNOWN
	}

	// For multi-virtual chips (SHT3x, CCS811), sensor_type must not be UNKNOWN (0)
	// and must be one of the supported types.
	if sensorTypeInt == 0 { // SENSOR_TYPE_UNKNOWN
		return false
	}

	// Map SensorType enum value to sensor type name.
	sensorTypeName := sensorTypeEnumToName(sensorTypeInt)
	if sensorTypeName == "" {
		return false
	}

	// Check if this sensor type is supported by the chip.
	for _, supportedType := range supportedTypes {
		if supportedType == sensorTypeName {
			return true
		}
	}

	return false
}

// sensorTypeEnumToName maps SensorType enum values to string names used in the catalog.
func sensorTypeEnumToName(sensorType int32) string {
	const (
		SENSOR_TYPE_UNKNOWN     = 0
		SENSOR_TYPE_ILLUMINANCE = 1
		SENSOR_TYPE_TEMPERATURE = 2
		SENSOR_TYPE_HUMIDITY    = 3
		SENSOR_TYPE_ECO2        = 4
		SENSOR_TYPE_TVOC        = 5
	)

	switch sensorType {
	case SENSOR_TYPE_ILLUMINANCE:
		return "illuminance"
	case SENSOR_TYPE_TEMPERATURE:
		return "temperature"
	case SENSOR_TYPE_HUMIDITY:
		return "humidity"
	case SENSOR_TYPE_ECO2:
		return "eco2"
	case SENSOR_TYPE_TVOC:
		return "tvoc"
	default:
		return ""
	}
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
			if baseSensor != nil && baseSensor.I2CAddress == 0 {
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

// removalKeyString returns a string representation of a removal key for error reporting.
func removalKeyString(removalKey *pb.RemovalKey) string {
	muxPath := parseMuxPath(removalKey.MuxPath)
	if removalKey.SensorType == 0 {
		return chipKeyString(removalKey.I2CAddress, muxPath)
	}
	return fullKeyString(removalKey.I2CAddress, muxPath, removalKey.SensorType)
}
