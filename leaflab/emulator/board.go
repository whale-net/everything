package main

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strconv"
	"strings"

	firmwarepb "github.com/whale-net/everything/firmware/proto"
	configpb "github.com/whale-net/everything/firmware/proto/config"
)

// Sensor is one resolved sensor instance on a simulated Board.
type Sensor struct {
	Name           string // logical name; also the MQTT topic suffix
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

// chipSensorTypes mirrors firmware/sensor/catalog/chips.yaml's
// `sensor_types:` mapping (chip -> the SensorType values it can produce).
// Duplicated here as an explicit Go table rather than pulling in a YAML
// parser dependency; keep in sync with chips.yaml when a chip is added.
var chipSensorTypes = map[configpb.ChipType][]firmwarepb.SensorType{
	configpb.ChipType_CHIP_TYPE_BH1750: {firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE},
	configpb.ChipType_CHIP_TYPE_SHT3X: {
		firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE,
		firmwarepb.SensorType_SENSOR_TYPE_HUMIDITY,
	},
	configpb.ChipType_CHIP_TYPE_CCS811: {
		firmwarepb.SensorType_SENSOR_TYPE_ECO2,
		firmwarepb.SensorType_SENSOR_TYPE_TVOC,
	},
}

// resolveSensorType resolves the SensorType a sensor should report: infers
// it for single-ISensor chips (declared left UNKNOWN), validates it for
// multi-virtual chips (SHT3x, CCS811), and rejects a declared type the chip
// cannot produce. Mirrors firmware/sensor/catalog/chips.yaml's
// `sensor_types:` mapping.
func resolveSensorType(chip configpb.ChipType, declared firmwarepb.SensorType) (firmwarepb.SensorType, error) {
	produced, ok := chipSensorTypes[chip]
	if !ok {
		return firmwarepb.SensorType_SENSOR_TYPE_UNKNOWN, fmt.Errorf("chip type %v has no known sensor types", chip)
	}

	if declared == firmwarepb.SensorType_SENSOR_TYPE_UNKNOWN {
		if len(produced) == 1 {
			return produced[0], nil
		}
		return firmwarepb.SensorType_SENSOR_TYPE_UNKNOWN, fmt.Errorf("chip type %v is multi-virtual; sensorType must be declared explicitly", chip)
	}

	for _, t := range produced {
		if t == declared {
			return declared, nil
		}
	}
	return firmwarepb.SensorType_SENSOR_TYPE_UNKNOWN, fmt.Errorf("chip type %v cannot produce declared sensor type %v", chip, declared)
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

// boardSpecEntry is one parsed "<scenario>[:<count>][@<device_id>]" element
// from an EMULATOR_BOARDS/--board spec.
type boardSpecEntry struct {
	scenario string
	count    int
	deviceID string // "" unless explicitly overridden; only legal when count == 1
}

// parseBoardSpec parses the comma-separated EMULATOR_BOARDS/--board grammar:
// "<scenario>[:<count>][@<device_id>]", e.g.
// "mux-light-temp:3,single-light@leaflab-deadbeef0001".
func parseBoardSpec(spec string) ([]boardSpecEntry, error) {
	var entries []boardSpecEntry
	for _, raw := range strings.Split(spec, ",") {
		item := strings.TrimSpace(raw)
		if item == "" {
			continue
		}

		deviceID := ""
		rest := item
		if idx := strings.Index(item, "@"); idx >= 0 {
			rest = item[:idx]
			deviceID = item[idx+1:]
			if deviceID == "" {
				return nil, fmt.Errorf("board spec %q: empty device_id override after '@'", item)
			}
		}

		scenario := rest
		count := 1
		if idx := strings.Index(rest, ":"); idx >= 0 {
			scenario = rest[:idx]
			countStr := rest[idx+1:]
			n, err := strconv.Atoi(countStr)
			if err != nil {
				return nil, fmt.Errorf("board spec %q: invalid count %q: %w", item, countStr, err)
			}
			count = n
		}

		if scenario == "" {
			return nil, fmt.Errorf("board spec %q: empty scenario name", item)
		}
		if count < 1 {
			return nil, fmt.Errorf("board spec %q: count must be >= 1, got %d", item, count)
		}
		if deviceID != "" && count != 1 {
			return nil, fmt.Errorf("board spec %q: @device_id override is only legal with count 1, got count %d", item, count)
		}

		entries = append(entries, boardSpecEntry{scenario: scenario, count: count, deviceID: deviceID})
	}
	return entries, nil
}

// BuildBoards resolves an EMULATOR_BOARDS/--board spec string
// ("<scenario>[:<count>][@<device_id>]", comma-separated; empty spec means
// one board per loaded scenario) against the loaded scenarios into concrete
// Boards.
func BuildBoards(cfg *Config, scenarios map[string]*Scenario) ([]Board, error) {
	var entries []boardSpecEntry

	if strings.TrimSpace(cfg.EmulatorBoards) == "" {
		// Empty spec: every scenario in SCENARIO_DIR, one board each (NFR5
		// default load). Sort for deterministic board ordering.
		names := make([]string, 0, len(scenarios))
		for name := range scenarios {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			entries = append(entries, boardSpecEntry{scenario: name, count: 1})
		}
	} else {
		parsed, err := parseBoardSpec(cfg.EmulatorBoards)
		if err != nil {
			return nil, err
		}
		entries = parsed
	}

	var boards []Board
	seenDeviceIDs := make(map[string]string) // device_id -> scenario:index that claimed it

	for _, entry := range entries {
		scenario, ok := scenarios[entry.scenario]
		if !ok {
			return nil, fmt.Errorf("board spec: unknown scenario %q", entry.scenario)
		}

		sensors, err := resolveBoardSensors(scenario)
		if err != nil {
			return nil, err
		}

		for i := 0; i < entry.count; i++ {
			deviceID := entry.deviceID
			if deviceID == "" {
				deviceID = deriveDeviceID(entry.scenario, i)
			}

			claimKey := fmt.Sprintf("%s:%d", entry.scenario, i)
			if prev, exists := seenDeviceIDs[deviceID]; exists {
				return nil, fmt.Errorf("board spec: device_id %q claimed by both %q and %q", deviceID, prev, claimKey)
			}
			seenDeviceIDs[deviceID] = claimKey

			boards = append(boards, Board{
				DeviceID: deviceID,
				Scenario: entry.scenario,
				Sensors:  sensors,
			})
		}
	}

	return boards, nil
}

// resolveBoardSensors converts a Scenario's raw configpb sensors into
// resolved Sensor values.
func resolveBoardSensors(scenario *Scenario) ([]Sensor, error) {
	rawSensors := scenario.Config.GetSensors()
	sensors := make([]Sensor, 0, len(rawSensors))

	for _, s := range rawSensors {
		resolvedType, err := resolveSensorType(s.GetChipType(), s.GetSensorType())
		if err != nil {
			return nil, fmt.Errorf("scenario %s sensor %s: %w", scenario.Name, s.GetName(), err)
		}

		var muxAddress, muxChannel uint32
		if hops := s.GetMuxPath(); len(hops) > 0 {
			muxAddress = hops[0].GetMuxAddress()
			muxChannel = hops[0].GetMuxChannel()
		}

		sensors = append(sensors, Sensor{
			Name:           s.GetName(),
			ChipType:       s.GetChipType(),
			SensorType:     resolvedType,
			I2CAddress:     s.GetI2CAddress(),
			MuxAddress:     muxAddress,
			MuxChannel:     muxChannel,
			Enabled:        s.GetEnabled(),
			PollIntervalMS: s.GetPollIntervalMs(),
		})
	}

	return sensors, nil
}
