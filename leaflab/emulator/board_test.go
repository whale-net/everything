package main

import (
	"regexp"
	"testing"

	firmwarepb "github.com/whale-net/everything/firmware/proto"
	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

var deviceIDPattern = regexp.MustCompile(`^leaflab-[0-9a-f]{12}$`)

// TestResolveSensorType_InferSingleISensor covers the single-ISensor
// inference path: declared left UNKNOWN, chip produces exactly one
// SensorType.
func TestResolveSensorType_InferSingleISensor(t *testing.T) {
	got, err := resolveSensorType(configpb.ChipType_CHIP_TYPE_BH1750, firmwarepb.SensorType_SENSOR_TYPE_UNKNOWN)
	require.NoError(t, err)
	assert.Equal(t, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE, got)
}

// TestResolveSensorType_MultiVirtualDeclared covers the multi-virtual chip
// path when the declared type is one the chip can actually produce.
func TestResolveSensorType_MultiVirtualDeclared(t *testing.T) {
	got, err := resolveSensorType(configpb.ChipType_CHIP_TYPE_SHT3X, firmwarepb.SensorType_SENSOR_TYPE_HUMIDITY)
	require.NoError(t, err)
	assert.Equal(t, firmwarepb.SensorType_SENSOR_TYPE_HUMIDITY, got)
}

// TestResolveSensorType_Rejections covers the two documented rejection
// cases: a multi-virtual chip left undeclared (SHT3x with no sensorType),
// and a declared type the chip cannot produce (BH1750 declared as
// SENSOR_TYPE_ECO2).
func TestResolveSensorType_Rejections(t *testing.T) {
	cases := []struct {
		name     string
		chip     configpb.ChipType
		declared firmwarepb.SensorType
	}{
		{
			name:     "SHT3x with no sensorType",
			chip:     configpb.ChipType_CHIP_TYPE_SHT3X,
			declared: firmwarepb.SensorType_SENSOR_TYPE_UNKNOWN,
		},
		{
			name:     "BH1750 declared as SENSOR_TYPE_ECO2",
			chip:     configpb.ChipType_CHIP_TYPE_BH1750,
			declared: firmwarepb.SensorType_SENSOR_TYPE_ECO2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveSensorType(tc.chip, tc.declared)
			require.Error(t, err)
		})
	}
}

// fakeScenarios builds a minimal scenarios map for board-spec tests that
// don't need real scenario-file content, just distinct valid scenario
// names to spawn boards from.
func fakeScenarios(names ...string) map[string]*Scenario {
	scenarios := make(map[string]*Scenario, len(names))
	for _, name := range names {
		scenarios[name] = &Scenario{
			Name: name,
			Config: &configpb.DeviceConfig{
				Sensors: []*configpb.SensorConfig{
					{ChipType: configpb.ChipType_CHIP_TYPE_BH1750, I2CAddress: 35, Name: "light", Enabled: proto.Bool(true)},
				},
			},
		}
	}
	return scenarios
}

// TestBuildBoards_DeviceIDDeterminism covers the hard requirement that
// device IDs are deterministic and stable across restarts: BuildBoards
// twice over the same input yields byte-identical DeviceIDs, different
// scenarios/indices yield distinct ids, and every id matches the
// leaflab-<12 hex> format.
func TestBuildBoards_DeviceIDDeterminism(t *testing.T) {
	scenarios := fakeScenarios("scenario-a", "scenario-b")
	cfg := &Config{EmulatorBoards: "scenario-a:2,scenario-b:1"}

	first, err := BuildBoards(cfg, scenarios)
	require.NoError(t, err)
	second, err := BuildBoards(cfg, scenarios)
	require.NoError(t, err)

	require.Len(t, first, 3)
	require.Len(t, second, 3)

	seen := make(map[string]struct{})
	for i := range first {
		assert.Equal(t, first[i].DeviceID, second[i].DeviceID, "board %d device_id not stable across BuildBoards calls", i)
		assert.Regexp(t, deviceIDPattern, first[i].DeviceID)

		_, dup := seen[first[i].DeviceID]
		assert.False(t, dup, "device_id %q reused across boards", first[i].DeviceID)
		seen[first[i].DeviceID] = struct{}{}
	}
}

// TestBuildBoards_SpecParsing covers the EMULATOR_BOARDS/--board grammar:
// "<scenario>[:<count>][@<device_id>]", comma-separated.
func TestBuildBoards_SpecParsing(t *testing.T) {
	t.Run("count expands to N distinct boards", func(t *testing.T) {
		scenarios := fakeScenarios("mux-light-temp")
		cfg := &Config{EmulatorBoards: "mux-light-temp:3"}

		boards, err := BuildBoards(cfg, scenarios)
		require.NoError(t, err)
		require.Len(t, boards, 3)

		ids := make(map[string]struct{}, 3)
		for _, b := range boards {
			assert.Equal(t, "mux-light-temp", b.Scenario)
			ids[b.DeviceID] = struct{}{}
		}
		assert.Len(t, ids, 3, "expected 3 distinct device ids")
	})

	t.Run("device_id override is honored", func(t *testing.T) {
		scenarios := fakeScenarios("single-light")
		cfg := &Config{EmulatorBoards: "single-light@leaflab-deadbeef0001"}

		boards, err := BuildBoards(cfg, scenarios)
		require.NoError(t, err)
		require.Len(t, boards, 1)
		assert.Equal(t, "leaflab-deadbeef0001", boards[0].DeviceID)
	})

	t.Run("unknown scenario errors", func(t *testing.T) {
		scenarios := fakeScenarios("single-light")
		cfg := &Config{EmulatorBoards: "does-not-exist"}

		_, err := BuildBoards(cfg, scenarios)
		require.Error(t, err)
	})

	t.Run("count of 0 errors", func(t *testing.T) {
		scenarios := fakeScenarios("single-light")
		cfg := &Config{EmulatorBoards: "single-light:0"}

		_, err := BuildBoards(cfg, scenarios)
		require.Error(t, err)
	})

	t.Run("duplicate explicit device_id errors", func(t *testing.T) {
		scenarios := fakeScenarios("single-light", "light-temp")
		cfg := &Config{EmulatorBoards: "single-light@leaflab-deadbeef0001,light-temp@leaflab-deadbeef0001"}

		_, err := BuildBoards(cfg, scenarios)
		require.Error(t, err)
	})

	t.Run("empty spec yields one board per scenario", func(t *testing.T) {
		scenarios := fakeScenarios("single-light", "light-temp", "mux-light-temp")
		cfg := &Config{EmulatorBoards: ""}

		boards, err := BuildBoards(cfg, scenarios)
		require.NoError(t, err)
		require.Len(t, boards, len(scenarios))

		gotScenarios := make(map[string]int, len(scenarios))
		for _, b := range boards {
			gotScenarios[b.Scenario]++
		}
		for name := range scenarios {
			assert.Equal(t, 1, gotScenarios[name], "scenario %q should produce exactly 1 board", name)
		}
	})
}

// TestBuildBoards_RealScenarioFiles is a lighter-weight integration check
// that BuildBoards works end-to-end against the real scenario files loaded
// via LoadScenarioDir, not just the synthetic fixtures above.
func TestBuildBoards_RealScenarioFiles(t *testing.T) {
	scenarios, err := LoadScenarioDir(scenariosDir(t))
	require.NoError(t, err)

	cfg := &Config{EmulatorBoards: ""}
	boards, err := BuildBoards(cfg, scenarios)
	require.NoError(t, err)
	assert.Len(t, boards, len(scenarios), "empty spec should produce one board per loaded scenario")

	for _, b := range boards {
		assert.Regexp(t, deviceIDPattern, b.DeviceID)
		assert.NotEmpty(t, b.Sensors)
	}
}
