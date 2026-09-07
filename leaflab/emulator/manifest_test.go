package main

import (
	"path/filepath"
	"testing"

	firmwarepb "github.com/whale-net/everything/firmware/proto"
	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildManifest_MuxLightTemp asserts the manifest shape for
// mux-light-temp.json against the real scenario file: exactly 3
// descriptors with the right name/type/unit/i2c_address/mux_address/
// mux_channel/chip_model.
func TestBuildManifest_MuxLightTemp(t *testing.T) {
	path := filepath.Join(scenariosDir(t), "mux-light-temp.json")
	scenario, err := LoadScenario(path)
	require.NoError(t, err)

	sensors, err := resolveBoardSensors(scenario)
	require.NoError(t, err)

	b := Board{DeviceID: "leaflab-deadbeef0001", Scenario: scenario.Name, Sensors: sensors}
	manifest := BuildManifest(b)

	assert.Equal(t, "leaflab-deadbeef0001", manifest.GetDeviceId())
	require.Len(t, manifest.GetSensors(), 3)

	byName := make(map[string]*firmwarepb.SensorDescriptor, 3)
	for _, d := range manifest.GetSensors() {
		byName[d.GetName()] = d
	}

	light, ok := byName["light"]
	require.True(t, ok)
	assert.Equal(t, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE, light.GetType())
	assert.Equal(t, "lx", light.GetUnit())
	assert.Equal(t, uint32(35), light.GetI2CAddress())
	assert.Equal(t, uint32(112), light.GetMuxAddress())
	assert.Equal(t, uint32(7), light.GetMuxChannel())
	assert.Equal(t, "BH1750", light.GetChipModel())

	temperature, ok := byName["temperature"]
	require.True(t, ok)
	assert.Equal(t, firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE, temperature.GetType())
	assert.Equal(t, "°C", temperature.GetUnit())
	assert.Equal(t, uint32(68), temperature.GetI2CAddress())
	assert.Equal(t, uint32(112), temperature.GetMuxAddress())
	assert.Equal(t, uint32(5), temperature.GetMuxChannel())
	assert.Equal(t, "SHT3x", temperature.GetChipModel())

	humidity, ok := byName["humidity"]
	require.True(t, ok)
	assert.Equal(t, firmwarepb.SensorType_SENSOR_TYPE_HUMIDITY, humidity.GetType())
	assert.Equal(t, "%RH", humidity.GetUnit())
	assert.Equal(t, uint32(68), humidity.GetI2CAddress())
	assert.Equal(t, uint32(112), humidity.GetMuxAddress())
	assert.Equal(t, uint32(5), humidity.GetMuxChannel())
	assert.Equal(t, "SHT3x", humidity.GetChipModel())
}

// TestBuildManifest_SingleLightHasNoMux asserts single-light.json's one
// sensor (no muxPath at all) produces mux_address == 0 && mux_channel == 0
// in the manifest, not some other "unset" sentinel.
func TestBuildManifest_SingleLightHasNoMux(t *testing.T) {
	path := filepath.Join(scenariosDir(t), "single-light.json")
	scenario, err := LoadScenario(path)
	require.NoError(t, err)

	sensors, err := resolveBoardSensors(scenario)
	require.NoError(t, err)

	b := Board{DeviceID: "leaflab-deadbeef0002", Scenario: scenario.Name, Sensors: sensors}
	manifest := BuildManifest(b)

	require.Len(t, manifest.GetSensors(), 1)
	d := manifest.GetSensors()[0]
	assert.Equal(t, "light", d.GetName())
	assert.Equal(t, uint32(0), d.GetMuxAddress())
	assert.Equal(t, uint32(0), d.GetMuxChannel())
}

// TestBuildManifest_DisabledSensorsOmitted asserts a disabled sensor is
// absent from the manifest entirely, not merely marked disabled.
func TestBuildManifest_DisabledSensorsOmitted(t *testing.T) {
	b := Board{
		DeviceID: "leaflab-deadbeef0003",
		Scenario: "synthetic",
		Sensors: []Sensor{
			{Name: "enabled-sensor", ChipType: configpb.ChipType_CHIP_TYPE_BH1750, SensorType: firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE, Enabled: true},
			{Name: "disabled-sensor", ChipType: configpb.ChipType_CHIP_TYPE_BH1750, SensorType: firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE, Enabled: false},
		},
	}

	manifest := BuildManifest(b)
	require.Len(t, manifest.GetSensors(), 1)
	assert.Equal(t, "enabled-sensor", manifest.GetSensors()[0].GetName())
}

// TestChipModelNames pins the exact casing BuildManifest emits for
// chip_model against firmware/sensor/catalog/chips.yaml's `name:` values --
// the processor joins sensor rows to the catalog on this string, so a
// casing drift here is a silent join failure downstream.
func TestChipModelNames(t *testing.T) {
	cases := []struct {
		chip configpb.ChipType
		want string
	}{
		{configpb.ChipType_CHIP_TYPE_BH1750, "BH1750"},
		{configpb.ChipType_CHIP_TYPE_SHT3X, "SHT3x"},
		{configpb.ChipType_CHIP_TYPE_CCS811, "CCS811"},
	}

	for _, tc := range cases {
		b := Board{
			DeviceID: "leaflab-deadbeef0004",
			Sensors: []Sensor{
				{Name: "s", ChipType: tc.chip, SensorType: firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE, Enabled: true},
			},
		}
		manifest := BuildManifest(b)
		require.Len(t, manifest.GetSensors(), 1)
		assert.Equal(t, tc.want, manifest.GetSensors()[0].GetChipModel())
	}
}
