package main

import (
	"encoding/json"
	"testing"

	configpb "github.com/whale-net/everything/firmware/proto/config"
	pb "github.com/whale-net/everything/leaflab/api/proto"
)

// Sensor type constants (from firmware/proto/firmware.proto)
const (
	SENSOR_TYPE_ILLUMINANCE = 1
	SENSOR_TYPE_TEMPERATURE = 2
	SENSOR_TYPE_HUMIDITY    = 3
)

// TestMaterializer_EDIT_MaterializesFromBase tests that EDIT scope
// materialises unnamed entries from the accepted base config.
func TestMaterializer_EDIT_MaterializesFromBase(t *testing.T) {
	materialiser := NewMaterialiser()

	// Create a base config with 3 sensors
	baseConfig := &configpb.DeviceConfig{
		DeviceId: "device-1",
		Sensors: []*configpb.SensorConfig{
			{
				Name:        "sensor-1",
				I2CAddress:  0x23,
				ChipType:    configpb.ChipType_CHIP_TYPE_BH1750,
				SensorType:  SENSOR_TYPE_ILLUMINANCE,
			},
			{
				Name:        "sensor-2",
				I2CAddress:  0x44,
				ChipType:    configpb.ChipType_CHIP_TYPE_SHT3X,
				SensorType:  SENSOR_TYPE_TEMPERATURE,
			},
			{
				Name:        "sensor-3",
				I2CAddress:  0x44,
				ChipType:    configpb.ChipType_CHIP_TYPE_SHT3X,
				SensorType:  SENSOR_TYPE_HUMIDITY,
			},
		},
	}

	// Create an EDIT request that only adds/changes sensor-2
	editPayload := &pb.PushDeviceConfigRequest{
		DeviceId: "device-1",
		Scope:    pb.ConfigScope_CONFIG_SCOPE_EDIT,
		Sensors: []*configpb.SensorConfig{
			{
				Name:        "sensor-2-updated",
				I2CAddress:  0x44,
				ChipType:    configpb.ChipType_CHIP_TYPE_SHT3X,
				SensorType:  SENSOR_TYPE_TEMPERATURE,
			},
		},
	}

	result, provenance, _, err := materialiser.Materialize(baseConfig, editPayload)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	// Result should have 3 sensors total
	if len(result.Sensors) != 3 {
		t.Errorf("expected 3 sensors, got %d", len(result.Sensors))
	}

	// Check that sensor-1 and sensor-3 are materialised
	var s1Found, s2Found, s3Found bool
	for i, sensor := range result.Sensors {
		if sensor.Name == "sensor-1" {
			s1Found = true
			if provenance[i] != pb.Provenance_PROVENANCE_MATERIALISED {
				t.Errorf("sensor-1: expected MATERIALISED, got %v", provenance[i])
			}
		} else if sensor.Name == "sensor-2-updated" {
			s2Found = true
			if provenance[i] != pb.Provenance_PROVENANCE_AUTHORED {
				t.Errorf("sensor-2-updated: expected AUTHORED, got %v", provenance[i])
			}
		} else if sensor.Name == "sensor-3" {
			s3Found = true
			if provenance[i] != pb.Provenance_PROVENANCE_MATERIALISED {
				t.Errorf("sensor-3: expected MATERIALISED, got %v", provenance[i])
			}
		}
	}

	if !s1Found {
		t.Errorf("expected to find sensor-1 (materialised)")
	}
	if !s2Found {
		t.Errorf("expected to find sensor-2-updated (authored)")
	}
	if !s3Found {
		t.Errorf("expected to find sensor-3 (materialised)")
	}
}

// TestMaterializer_EDIT_FullKeyRemoval tests that full-key removals
// drop exactly one entry.
func TestMaterializer_EDIT_FullKeyRemoval(t *testing.T) {
	materialiser := NewMaterialiser()

	// Create a base config with 2 sensors on same address (SHT3x)
	baseConfig := &configpb.DeviceConfig{
		DeviceId: "device-1",
		Sensors: []*configpb.SensorConfig{
			{
				Name:        "temperature",
				I2CAddress:  0x44,
				ChipType:    configpb.ChipType_CHIP_TYPE_SHT3X,
				SensorType:  SENSOR_TYPE_TEMPERATURE,
			},
			{
				Name:        "humidity",
				I2CAddress:  0x44,
				ChipType:    configpb.ChipType_CHIP_TYPE_SHT3X,
				SensorType:  SENSOR_TYPE_HUMIDITY,
			},
		},
	}

	// Remove only the temperature entry (full key removal)
	editPayload := &pb.PushDeviceConfigRequest{
		DeviceId: "device-1",
		Scope:    pb.ConfigScope_CONFIG_SCOPE_EDIT,
		Remove: []*pb.RemovalKey{
			{
				I2CAddress: 0x44,
				MuxPath:    "", // no mux
				SensorType: int32(SENSOR_TYPE_TEMPERATURE),
			},
		},
	}

	result, _, removals, err := materialiser.Materialize(baseConfig, editPayload)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	// Should have 1 sensor left (humidity)
	if len(result.Sensors) != 1 {
		t.Errorf("expected 1 sensor after full-key removal, got %d", len(result.Sensors))
	}

	if result.Sensors[0].Name != "humidity" {
		t.Errorf("expected humidity sensor to remain, got %s", result.Sensors[0].Name)
	}

	// Verify removal result shows full key form
	if len(removals) != 1 {
		t.Fatalf("expected 1 removal result, got %d", len(removals))
	}
	if removals[0].Form != RemovalFormFull {
		t.Errorf("expected RemovalFormFull, got %v", removals[0].Form)
	}
}

// TestMaterializer_EDIT_ChipKeyRemoval tests that chip-key removals
// drop all entries at that address and mux path.
func TestMaterializer_EDIT_ChipKeyRemoval(t *testing.T) {
	materialiser := NewMaterialiser()

	// Create a base config with 2 sensors on same address (SHT3x)
	baseConfig := &configpb.DeviceConfig{
		DeviceId: "device-1",
		Sensors: []*configpb.SensorConfig{
			{
				Name:        "temperature",
				I2CAddress:  0x44,
				ChipType:    configpb.ChipType_CHIP_TYPE_SHT3X,
				SensorType:  SENSOR_TYPE_TEMPERATURE,
			},
			{
				Name:        "humidity",
				I2CAddress:  0x44,
				ChipType:    configpb.ChipType_CHIP_TYPE_SHT3X,
				SensorType:  SENSOR_TYPE_HUMIDITY,
			},
			{
				Name:        "light",
				I2CAddress:  0x23,
				ChipType:    configpb.ChipType_CHIP_TYPE_BH1750,
				SensorType:  SENSOR_TYPE_ILLUMINANCE,
			},
		},
	}

	// Remove the entire chip at 0x44 (chip-key removal: no sensor_type)
	editPayload := &pb.PushDeviceConfigRequest{
		DeviceId: "device-1",
		Scope:    pb.ConfigScope_CONFIG_SCOPE_EDIT,
		Remove: []*pb.RemovalKey{
			{
				I2CAddress: 0x44,
				MuxPath:    "", // no mux
				SensorType: 0,  // chip-key: sensor_type = 0
			},
		},
	}

	result, _, removals, err := materialiser.Materialize(baseConfig, editPayload)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	// Should have 1 sensor left (light)
	if len(result.Sensors) != 1 {
		t.Errorf("expected 1 sensor after chip-key removal, got %d", len(result.Sensors))
	}

	if result.Sensors[0].Name != "light" {
		t.Errorf("expected light sensor to remain, got %s", result.Sensors[0].Name)
	}

	// Verify removal result shows chip key form
	if len(removals) != 1 {
		t.Fatalf("expected 1 removal result, got %d", len(removals))
	}
	if removals[0].Form != RemovalFormChip {
		t.Errorf("expected RemovalFormChip, got %v", removals[0].Form)
	}
}

// TestMaterializer_EDIT_MultipleEditsAndRemovals tests that multiple
// edits and removals in one request produce one version.
func TestMaterializer_EDIT_MultipleEditsAndRemovals(t *testing.T) {
	materialiser := NewMaterialiser()

	// Create a base config with 7 sensors
	baseConfig := &configpb.DeviceConfig{
		DeviceId: "device-1",
		Sensors: []*configpb.SensorConfig{
			{Name: "s1", I2CAddress: 0x10, SensorType: SENSOR_TYPE_ILLUMINANCE},
			{Name: "s2", I2CAddress: 0x20, SensorType: SENSOR_TYPE_TEMPERATURE},
			{Name: "s3", I2CAddress: 0x30, SensorType: SENSOR_TYPE_HUMIDITY},
			{Name: "s4", I2CAddress: 0x40, SensorType: SENSOR_TYPE_TEMPERATURE},
			{Name: "s5", I2CAddress: 0x50, SensorType: SENSOR_TYPE_HUMIDITY},
			{Name: "s6", I2CAddress: 0x60, SensorType: SENSOR_TYPE_ILLUMINANCE},
			{Name: "s7", I2CAddress: 0x70, SensorType: SENSOR_TYPE_TEMPERATURE},
		},
	}

	// Make edits to s3, s4, s5 and remove s1, s2
	editPayload := &pb.PushDeviceConfigRequest{
		DeviceId: "device-1",
		Scope:    pb.ConfigScope_CONFIG_SCOPE_EDIT,
		Sensors: []*configpb.SensorConfig{
			// 3 edits
			{Name: "s3-updated", I2CAddress: 0x30, SensorType: SENSOR_TYPE_HUMIDITY},
			{Name: "s4-updated", I2CAddress: 0x40, SensorType: SENSOR_TYPE_TEMPERATURE},
			{Name: "s5-updated", I2CAddress: 0x50, SensorType: SENSOR_TYPE_HUMIDITY},
		},
		Remove: []*pb.RemovalKey{
			{I2CAddress: 0x10, SensorType: int32(SENSOR_TYPE_ILLUMINANCE)},
			{I2CAddress: 0x20, SensorType: int32(SENSOR_TYPE_TEMPERATURE)},
		},
	}

	result, provenance, _, err := materialiser.Materialize(baseConfig, editPayload)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	// Result should have 5 sensors:
	// - Materialised: s6, s7 (not edited, not removed)
	// - Authored: s3-updated, s4-updated, s5-updated (edited)
	if len(result.Sensors) != 5 {
		t.Errorf("expected 5 sensors, got %d", len(result.Sensors))
	}

	// Count materialized vs authored
	matCount := 0
	authCount := 0
	for _, prov := range provenance {
		if prov == pb.Provenance_PROVENANCE_MATERIALISED {
			matCount++
		} else if prov == pb.Provenance_PROVENANCE_AUTHORED {
			authCount++
		}
	}
	if matCount != 2 {
		t.Errorf("expected 2 materialised sensors, got %d", matCount)
	}
	if authCount != 3 {
		t.Errorf("expected 3 authored sensors, got %d", authCount)
	}
}

// TestMaterializer_EDIT_AllProvenanceTracked tests that provenance
// is correctly tracked for all entries.
func TestMaterializer_EDIT_AllProvenanceTracked(t *testing.T) {
	materialiser := NewMaterialiser()

	baseConfig := &configpb.DeviceConfig{
		DeviceId: "device-1",
		Sensors: []*configpb.SensorConfig{
			{Name: "base-only", I2CAddress: 0x10, SensorType: SENSOR_TYPE_ILLUMINANCE},
			{Name: "base-update", I2CAddress: 0x20, SensorType: SENSOR_TYPE_TEMPERATURE},
		},
	}

	editPayload := &pb.PushDeviceConfigRequest{
		DeviceId: "device-1",
		Scope:    pb.ConfigScope_CONFIG_SCOPE_EDIT,
		Sensors: []*configpb.SensorConfig{
			// Update base-update
			{Name: "base-update-new", I2CAddress: 0x20, SensorType: SENSOR_TYPE_TEMPERATURE},
			// Add new sensor
			{Name: "new-sensor", I2CAddress: 0x30, SensorType: SENSOR_TYPE_HUMIDITY},
		},
	}

	result, provenance, _, err := materialiser.Materialize(baseConfig, editPayload)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	// Result should have 3 sensors: base-only (materialised), base-update-new (authored), new-sensor (authored)
	if len(result.Sensors) != 3 {
		t.Errorf("expected 3 sensors, got %d", len(result.Sensors))
	}

	// Check provenance map has correct keys and values
	if len(provenance) != 3 {
		t.Errorf("expected 3 provenance entries, got %d", len(provenance))
	}

	matCount := 0
	authCount := 0
	for _, prov := range provenance {
		if prov == pb.Provenance_PROVENANCE_MATERIALISED {
			matCount++
		} else if prov == pb.Provenance_PROVENANCE_AUTHORED {
			authCount++
		}
	}
	if matCount != 1 {
		t.Errorf("expected 1 materialised sensor, got %d", matCount)
	}
	if authCount != 2 {
		t.Errorf("expected 2 authored sensors, got %d", authCount)
	}
}

// TestMaterializer_EDIT_BaseNil tests that materialiser rejects nil base config.
func TestMaterializer_EDIT_BaseNil(t *testing.T) {
	materialiser := NewMaterialiser()

	editPayload := &pb.PushDeviceConfigRequest{
		DeviceId: "device-1",
		Scope:    pb.ConfigScope_CONFIG_SCOPE_EDIT,
		Sensors: []*configpb.SensorConfig{
			{Name: "s1", I2CAddress: 0x10, SensorType: SENSOR_TYPE_ILLUMINANCE},
		},
	}

	_, _, _, err := materialiser.Materialize(nil, editPayload)
	if err == nil {
		t.Fatalf("expected error for nil base config, got nil")
	}
}

// TestBuildProvenanceJSON tests that provenance JSON is correctly built.
func TestBuildProvenanceJSON(t *testing.T) {
	tests := []struct {
		name       string
		provenance map[int]pb.Provenance
		expected   string
	}{
		{
			name:       "empty",
			provenance: make(map[int]pb.Provenance),
			expected:   "{}",
		},
		{
			name: "single authored",
			provenance: map[int]pb.Provenance{
				0: pb.Provenance_PROVENANCE_AUTHORED,
			},
			expected: `{"0":1}`,
		},
		{
			name: "mixed provenance",
			provenance: map[int]pb.Provenance{
				0: pb.Provenance_PROVENANCE_AUTHORED,
				1: pb.Provenance_PROVENANCE_MATERIALISED,
				2: pb.Provenance_PROVENANCE_AUTHORED,
			},
			expected: `{"0":1,"1":2,"2":1}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := buildProvenanceJSON(tt.provenance)
			if err != nil {
				t.Fatalf("buildProvenanceJSON: %v", err)
			}

			// Unmarshal both to compare (JSON key order may vary)
			var resultMap map[string]int
			if err := json.Unmarshal(result, &resultMap); err != nil {
				t.Fatalf("unmarshal result: %v", err)
			}

			var expectedMap map[string]int
			if err := json.Unmarshal([]byte(tt.expected), &expectedMap); err != nil {
				t.Fatalf("unmarshal expected: %v", err)
			}

			if len(resultMap) != len(expectedMap) {
				t.Errorf("map length mismatch: got %d, expected %d", len(resultMap), len(expectedMap))
			}

			for k, v := range expectedMap {
				if resultMap[k] != v {
					t.Errorf("key %q: got %d, expected %d", k, resultMap[k], v)
				}
			}
		})
	}
}

// TestMaterializer_CanonicalKeysUsedCorrectly tests that canonicalizer
// is used to normalize sensor keys for matching.
func TestMaterializer_CanonicalKeysUsedCorrectly(t *testing.T) {
	materialiser := NewMaterialiser()

	// Create base config
	baseConfig := &configpb.DeviceConfig{
		DeviceId: "device-1",
		Sensors: []*configpb.SensorConfig{
			{
				Name:        "temp",
				I2CAddress:  0x44,
				ChipType:    configpb.ChipType_CHIP_TYPE_SHT3X,
				SensorType:  SENSOR_TYPE_TEMPERATURE,
			},
		},
	}

	// Edit that updates the same sensor (same canonical key)
	editPayload := &pb.PushDeviceConfigRequest{
		DeviceId: "device-1",
		Scope:    pb.ConfigScope_CONFIG_SCOPE_EDIT,
		Sensors: []*configpb.SensorConfig{
			{
				Name:        "temp-updated",
				I2CAddress:  0x44,
				ChipType:    configpb.ChipType_CHIP_TYPE_SHT3X,
				SensorType:  SENSOR_TYPE_TEMPERATURE,
			},
		},
	}

	result, provenance, _, err := materialiser.Materialize(baseConfig, editPayload)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	// Should have exactly one sensor (the updated one)
	if len(result.Sensors) != 1 {
		t.Errorf("expected 1 sensor, got %d", len(result.Sensors))
	}

	// Should be AUTHORED (not materialised), since it was in the edit request
	if provenance[0] != pb.Provenance_PROVENANCE_AUTHORED {
		t.Errorf("expected AUTHORED, got %v", provenance[0])
	}

	if result.Sensors[0].Name != "temp-updated" {
		t.Errorf("expected name temp-updated, got %s", result.Sensors[0].Name)
	}
}
