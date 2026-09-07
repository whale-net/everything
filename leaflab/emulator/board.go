package main

import (
	"crypto/sha256"
	"fmt"
	"strconv"

	firmwarepb "github.com/whale-net/everything/firmware/proto"
	configpb "github.com/whale-net/everything/firmware/proto/config"
)

// Sensor is one resolved sensor instance on a simulated Board.
type Sensor struct {
	Name           string                // logical name; also the MQTT topic suffix
	ChipType       configpb.ChipType
	SensorType     firmwarepb.SensorType // resolved, never UNKNOWN after resolveSensorType
	I2CAddress     uint32
	MuxAddress     uint32 // 0 if directly on root bus
	MuxChannel     uint32
	Enabled        bool
	PollIntervalMS uint32 // 0 = use board default
}

// Board is one simulated leaflab board: its derived device_id, the scenario
// it was built from, and its resolved sensors.
type Board struct {
	DeviceID string
	Scenario string
	Sensors  []Sensor
}

// resolveSensorType resolves the SensorType a sensor should report: infers
// it for single-ISensor chips (declared left UNKNOWN), validates it for
// multi-virtual chips (SHT3x, CCS811), and rejects a declared type the chip
// cannot produce. Mirrors firmware/sensor/catalog/chips.yaml's
// `sensor_types:` mapping.
//
// TODO(#1764 Implementation): explicit chip -> sensor-type Go table
// (comment pointing at chips.yaml), inference and validation logic.
func resolveSensorType(chip configpb.ChipType, declared firmwarepb.SensorType) (firmwarepb.SensorType, error) {
	return firmwarepb.SensorType_SENSOR_TYPE_UNKNOWN, fmt.Errorf("resolveSensorType(%v, %v): not implemented", chip, declared)
}

// deriveDeviceID computes the deterministic emulator device_id for the
// index'th board built from scenarioName, mirroring the real board's
// "leaflab-<12 hex chars>" eFuse-MAC-derived format (leaflab/MQTT.md).
// Deterministic and stable across restarts so a simulated board keeps the
// same DB identity, board name, and ownership row across `tilt down` /
// `tilt up`.
func deriveDeviceID(scenarioName string, index int) string {
	sum := sha256.Sum256([]byte(scenarioName + ":" + strconv.Itoa(index)))
	return fmt.Sprintf("leaflab-%x", sum[:6])
}

// BuildBoards resolves an EMULATOR_BOARDS/--board spec string
// ("<scenario>[:<count>][@<device_id>]", comma-separated; empty spec means
// one board per loaded scenario) against the loaded scenarios into concrete
// Boards.
//
// TODO(#1764 Implementation): spec grammar parsing, per-board Sensor
// resolution via resolveSensorType, validation (unknown scenario name,
// count < 1, duplicate explicit device_id).
func BuildBoards(cfg *Config, scenarios map[string]*Scenario) ([]Board, error) {
	return nil, fmt.Errorf("BuildBoards: not implemented")
}
