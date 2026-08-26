package main

import (
	"testing"

	configpb "github.com/whale-net/everything/firmware/proto/config"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	firmwarepb "github.com/whale-net/everything/firmware/proto"
)


func testValidChipSensorPair(t *testing.T, validator *Validator, name string, chipType configpb.ChipType, sensorTypeVal int32, wantValid bool) {
	config := &configpb.DeviceConfig{
		DeviceId: "test",
		Sensors: []*configpb.SensorConfig{
			{
				Name:           "sensor-1",
				I2CAddress:     0x23,
				ChipType:       chipType,
				SensorType:     firmwarepb.SensorType(sensorTypeVal),
				PollIntervalMs: 1000,
			},
		},
	}

	failures := validator.Validate(config, nil, &pb.PushDeviceConfigRequest{
		DeviceId: "test",
		Scope:    pb.ConfigScope_CONFIG_SCOPE_COMPLETE,
	})

	foundFailure := false
	for _, f := range failures {
		if f.Field == "chip_type" && f.EntryIdentifier == "0" {
			foundFailure = true
			break
		}
	}

	if wantValid && foundFailure {
		t.Errorf("%s: valid pair should not fail, but did", name)
	}
	if !wantValid && !foundFailure {
		t.Errorf("%s: invalid pair should fail, but didn't", name)
	}
}

// TestValidatorChipSensorTypePair tests that invalid chip/sensor-type pairs are rejected.
func TestValidatorChipSensorTypePair(t *testing.T) {
	validator, err := NewValidator()
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}

	// BH1750 - single-virtual chip
	testValidChipSensorPair(t, validator, "BH1750 with UNKNOWN", configpb.ChipType_CHIP_TYPE_BH1750, 0, true)
	testValidChipSensorPair(t, validator, "BH1750 with ILLUMINANCE", configpb.ChipType_CHIP_TYPE_BH1750, 1, false)
	testValidChipSensorPair(t, validator, "BH1750 with TEMPERATURE", configpb.ChipType_CHIP_TYPE_BH1750, 2, false)

	// SHT3x - multi-virtual chip
	testValidChipSensorPair(t, validator, "SHT3x with TEMPERATURE", configpb.ChipType_CHIP_TYPE_SHT3X, 2, true)
	testValidChipSensorPair(t, validator, "SHT3x with HUMIDITY", configpb.ChipType_CHIP_TYPE_SHT3X, 3, true)
	testValidChipSensorPair(t, validator, "SHT3x with UNKNOWN", configpb.ChipType_CHIP_TYPE_SHT3X, 0, false)
	testValidChipSensorPair(t, validator, "SHT3x with ILLUMINANCE", configpb.ChipType_CHIP_TYPE_SHT3X, 1, false)

	// CCS811 - multi-virtual chip
	testValidChipSensorPair(t, validator, "CCS811 with ECO2", configpb.ChipType_CHIP_TYPE_CCS811, 4, true)
	testValidChipSensorPair(t, validator, "CCS811 with TVOC", configpb.ChipType_CHIP_TYPE_CCS811, 5, true)
	testValidChipSensorPair(t, validator, "CCS811 with UNKNOWN", configpb.ChipType_CHIP_TYPE_CCS811, 0, false)
	testValidChipSensorPair(t, validator, "CCS811 with TEMPERATURE", configpb.ChipType_CHIP_TYPE_CCS811, 2, false)

	// Unknown chip type
	testValidChipSensorPair(t, validator, "UNKNOWN chip", configpb.ChipType_CHIP_TYPE_UNKNOWN, 0, false)
}

// TestValidatorI2CAddressRange tests that out-of-range I2C addresses are rejected.
func TestValidatorI2CAddressRange(t *testing.T) {
	validator, err := NewValidator()
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}

	validAddrs := []uint32{0, 1, 35, 68, 90, 127}
	for _, addr := range validAddrs {
		config := &configpb.DeviceConfig{
			DeviceId: "test",
			Sensors: []*configpb.SensorConfig{
				{
					Name:           "sensor-1",
					I2CAddress:     addr,
					ChipType:       configpb.ChipType_CHIP_TYPE_BH1750,
					SensorType:     0,
					PollIntervalMs: 1000,
				},
			},
		}

		failures := validator.Validate(config, nil, &pb.PushDeviceConfigRequest{
			DeviceId: "test",
			Scope:    pb.ConfigScope_CONFIG_SCOPE_COMPLETE,
		})

		var addrFailures []ValidationFailure
		for _, f := range failures {
			if f.Field == "i2c_address" {
				addrFailures = append(addrFailures, f)
			}
		}

		if len(addrFailures) > 0 {
			t.Errorf("addr %d should be valid but got failure", addr)
		}
	}

	invalidAddrs := []uint32{128, 129, 255, 256}
	for _, addr := range invalidAddrs {
		config := &configpb.DeviceConfig{
			DeviceId: "test",
			Sensors: []*configpb.SensorConfig{
				{
					Name:           "sensor-1",
					I2CAddress:     addr,
					ChipType:       configpb.ChipType_CHIP_TYPE_BH1750,
					SensorType:     0,
					PollIntervalMs: 1000,
				},
			},
		}

		failures := validator.Validate(config, nil, &pb.PushDeviceConfigRequest{
			DeviceId: "test",
			Scope:    pb.ConfigScope_CONFIG_SCOPE_COMPLETE,
		})

		foundFailure := false
		for _, f := range failures {
			if f.Field == "i2c_address" && f.EntryIdentifier == "0" && f.MessageKey == "INVALID_I2C_ADDRESS" {
				foundFailure = true
				break
			}
		}

		if !foundFailure {
			t.Errorf("addr %d should be invalid but passed", addr)
		}
	}
}

// TestValidatorPollInterval tests that invalid poll_interval_ms values are rejected.
func TestValidatorPollInterval(t *testing.T) {
	validator, err := NewValidator()
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}

	validIntervals := []uint32{0, 100, 1000, 3_600_000}
	invalidIntervals := []uint32{50, 99, 3_600_001}

	for _, interval := range validIntervals {
		config := &configpb.DeviceConfig{
			DeviceId: "test",
			Sensors: []*configpb.SensorConfig{
				{
					Name:           "sensor-1",
					I2CAddress:     0x23,
					ChipType:       configpb.ChipType_CHIP_TYPE_BH1750,
					SensorType:     0,
					PollIntervalMs: interval,
				},
			},
		}

		failures := validator.Validate(config, nil, &pb.PushDeviceConfigRequest{
			DeviceId: "test",
			Scope:    pb.ConfigScope_CONFIG_SCOPE_COMPLETE,
		})

		foundFailure := false
		for _, f := range failures {
			if f.Field == "poll_interval_ms" && f.EntryIdentifier == "0" {
				foundFailure = true
				break
			}
		}

		if foundFailure {
			t.Errorf("valid interval %d should not fail, but did", interval)
		}
	}

	for _, interval := range invalidIntervals {
		config := &configpb.DeviceConfig{
			DeviceId: "test",
			Sensors: []*configpb.SensorConfig{
				{
					Name:           "sensor-1",
					I2CAddress:     0x23,
					ChipType:       configpb.ChipType_CHIP_TYPE_BH1750,
					SensorType:     0,
					PollIntervalMs: interval,
				},
			},
		}

		failures := validator.Validate(config, nil, &pb.PushDeviceConfigRequest{
			DeviceId: "test",
			Scope:    pb.ConfigScope_CONFIG_SCOPE_COMPLETE,
		})

		foundFailure := false
		for _, f := range failures {
			if f.Field == "poll_interval_ms" && f.EntryIdentifier == "0" {
				foundFailure = true
				break
			}
		}

		if !foundFailure {
			t.Errorf("invalid interval %d should fail, but didn't", interval)
		}
	}
}

// TestValidatorHardwareKeyCollision tests that duplicate hardware keys are detected.
func TestValidatorHardwareKeyCollision(t *testing.T) {
	validator, err := NewValidator()
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}

	config := &configpb.DeviceConfig{
		DeviceId: "test",
		Sensors: []*configpb.SensorConfig{
			{
				Name:           "sensor-1",
				I2CAddress:     0x23,
				ChipType:       configpb.ChipType_CHIP_TYPE_BH1750,
				SensorType:     0,
				PollIntervalMs: 1000,
			},
			{
				Name:           "sensor-2",
				I2CAddress:     0x23,
				ChipType:       configpb.ChipType_CHIP_TYPE_BH1750,
				SensorType:     0,
				PollIntervalMs: 1000,
			},
		},
	}

	failures := validator.Validate(config, nil, &pb.PushDeviceConfigRequest{
		DeviceId: "test",
		Scope:    pb.ConfigScope_CONFIG_SCOPE_COMPLETE,
	})

	var collisionFailures []ValidationFailure
	for _, f := range failures {
		if f.Field == "hardware_key" && f.MessageKey == "DUPLICATE_HARDWARE_KEY" {
			collisionFailures = append(collisionFailures, f)
		}
	}

	if len(collisionFailures) != 2 {
		t.Errorf("expected 2 collision failures (one per sensor), got %d", len(collisionFailures))
	}

	foundEntries := make(map[string]bool)
	for _, f := range collisionFailures {
		foundEntries[f.EntryIdentifier] = true
	}
	if !foundEntries["0"] || !foundEntries["1"] {
		t.Errorf("expected failures on entries 0 and 1, got %v", foundEntries)
	}
}

// TestValidatorRemovalKeyNotFound tests that removal keys not in the base are errors.
func TestValidatorRemovalKeyNotFound(t *testing.T) {
	validator, err := NewValidator()
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}

	baseConfig := &configpb.DeviceConfig{
		DeviceId: "test",
		Sensors: []*configpb.SensorConfig{
			{
				Name:           "sensor-1",
				I2CAddress:     0x23,
				ChipType:       configpb.ChipType_CHIP_TYPE_BH1750,
				SensorType:     0,
				PollIntervalMs: 1000,
			},
		},
	}

	effectiveConfig := baseConfig
	editPayload := &pb.PushDeviceConfigRequest{
		DeviceId: "test",
		Scope:    pb.ConfigScope_CONFIG_SCOPE_EDIT,
		Sensors:  []*configpb.SensorConfig{},
		Remove: []*pb.RemovalKey{
			{
				I2CAddress: 0x44,
				MuxPath:    "",
				SensorType: 0,
			},
		},
	}

	failures := validator.Validate(effectiveConfig, baseConfig, editPayload)

	foundFailure := false
	for _, f := range failures {
		if f.Field == "remove" && f.MessageKey == "REMOVAL_KEY_NOT_FOUND" {
			foundFailure = true
			break
		}
	}

	if !foundFailure {
		t.Errorf("should report removal key not found error")
	}
}

// TestValidatorRemovalEntryNoAddress tests that removing an entry with i2c_address=0 is rejected.
func TestValidatorRemovalEntryNoAddress(t *testing.T) {
	validator, err := NewValidator()
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}

	baseConfig := &configpb.DeviceConfig{
		DeviceId: "test",
		Sensors: []*configpb.SensorConfig{
			{
				Name:           "sensor-1",
				I2CAddress:     0,
				ChipType:       configpb.ChipType_CHIP_TYPE_SHT3X,
				SensorType:     2,
				PollIntervalMs: 1000,
			},
		},
	}

	effectiveConfig := baseConfig
	editPayload := &pb.PushDeviceConfigRequest{
		DeviceId: "test",
		Scope:    pb.ConfigScope_CONFIG_SCOPE_EDIT,
		Sensors:  []*configpb.SensorConfig{},
		Remove: []*pb.RemovalKey{
			{
				I2CAddress: 0,
				MuxPath:    "",
				SensorType: 2,
			},
		},
	}

	failures := validator.Validate(effectiveConfig, baseConfig, editPayload)

	foundFailure := false
	for _, f := range failures {
		if f.Field == "remove" && f.MessageKey == "REMOVE_ENTRY_NO_ADDRESS" {
			foundFailure = true
			break
		}
	}

	if !foundFailure {
		t.Errorf("should report removal of entry with no address error")
	}
}

// TestValidatorKeySwapNotCaught tests that a swap is not caught here (FR16.4 handles it).
func TestValidatorKeySwapNotCaught(t *testing.T) {
	validator, err := NewValidator()
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}

	baseConfig := &configpb.DeviceConfig{
		DeviceId: "test",
		Sensors: []*configpb.SensorConfig{
			{
				Name:           "sensor-a",
				I2CAddress:     0x23,
				ChipType:       configpb.ChipType_CHIP_TYPE_BH1750,
				SensorType:     0,
				PollIntervalMs: 1000,
			},
			{
				Name:           "sensor-b",
				I2CAddress:     0x44,
				ChipType:       configpb.ChipType_CHIP_TYPE_SHT3X,
				SensorType:     2,
				PollIntervalMs: 1000,
			},
		},
	}

	effectiveConfig := &configpb.DeviceConfig{
		DeviceId: "test",
		Sensors: []*configpb.SensorConfig{
			{
				Name:           "sensor-a",
				I2CAddress:     0x44,
				ChipType:       configpb.ChipType_CHIP_TYPE_SHT3X,
				SensorType:     2,
				PollIntervalMs: 1000,
			},
			{
				Name:           "sensor-b",
				I2CAddress:     0x23,
				ChipType:       configpb.ChipType_CHIP_TYPE_BH1750,
				SensorType:     0,
				PollIntervalMs: 1000,
			},
		},
	}

	failures := validator.Validate(effectiveConfig, baseConfig, &pb.PushDeviceConfigRequest{
		DeviceId: "test",
		Scope:    pb.ConfigScope_CONFIG_SCOPE_EDIT,
	})

	for _, f := range failures {
		if f.Field == "hardware_key" && f.MessageKey == "DUPLICATE_HARDWARE_KEY" {
			t.Errorf("key swap should not be caught as collision by FR39")
		}
	}
}

// TestValidatorEditEffectivePayload tests that validation runs on the effective payload after materialisation.
func TestValidatorEditEffectivePayload(t *testing.T) {
	validator, err := NewValidator()
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}

	baseConfig := &configpb.DeviceConfig{
		DeviceId: "test",
		Sensors: []*configpb.SensorConfig{
			{
				Name:           "sensor-1",
				I2CAddress:     0x23,
				ChipType:       configpb.ChipType_CHIP_TYPE_BH1750,
				SensorType:     0,
				PollIntervalMs: 1000,
			},
		},
	}

	editPayload := &pb.PushDeviceConfigRequest{
		DeviceId: "test",
		Scope:    pb.ConfigScope_CONFIG_SCOPE_EDIT,
		Sensors: []*configpb.SensorConfig{
			{
				Name:           "sensor-1-updated",
				I2CAddress:     0x23,
				ChipType:       configpb.ChipType_CHIP_TYPE_BH1750,
				SensorType:     0,
				PollIntervalMs: 50,
			},
		},
	}

	effectiveConfig := &configpb.DeviceConfig{
		DeviceId: "test",
		Sensors: []*configpb.SensorConfig{
			{
				Name:           "sensor-1-updated",
				I2CAddress:     0x23,
				ChipType:       configpb.ChipType_CHIP_TYPE_BH1750,
				SensorType:     0,
				PollIntervalMs: 50,
			},
		},
	}

	failures := validator.Validate(effectiveConfig, baseConfig, editPayload)

	foundFailure := false
	for _, f := range failures {
		if f.Field == "poll_interval_ms" && f.EntryIdentifier == "0" && f.MessageKey == "INVALID_POLL_INTERVAL" {
			foundFailure = true
			break
		}
	}

	if !foundFailure {
		t.Errorf("should validate the effective payload after materialisation")
	}
}
