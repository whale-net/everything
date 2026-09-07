package main

import (
	"os"
	"path/filepath"
	"testing"

	firmwarepb "github.com/whale-net/everything/firmware/proto"
	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scenariosDir locates leaflab/scripts/scenarios. Bazel stages it as a data
// dependency (//leaflab/scripts:scenarios) relative to this test's own
// runfiles package directory; a plain `go test` run (which also chdirs into
// leaflab/emulator) finds it the same way. Extra fallback candidates cover
// invocation from other working directories. See
// tools/app_registry/citest's githubDir for the same walk-up convention.
func scenariosDir(t *testing.T) string {
	t.Helper()
	for _, c := range []string{
		"../scripts/scenarios",
		"leaflab/scripts/scenarios",
		"../../leaflab/scripts/scenarios",
	} {
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			return c
		}
	}
	t.Fatal("could not locate leaflab/scripts/scenarios -- check the data dependency in BUILD.bazel")
	return ""
}

// writeTempScenario writes a one-off scenario JSON body to a fresh temp dir
// and returns its path, for exercising load-time rejections that the 7 real
// scenario files (correctly) never hit.
func writeTempScenario(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name+".json")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return path
}

// TestLoadScenario_AllRealScenarioFiles is the regression guard that a
// future scenario file addition does not silently break the emulator: every
// file under leaflab/scripts/scenarios/ must parse and every sensor it
// declares must resolve to a non-UNKNOWN SensorType. push-config.sh owns
// these files' format; the emulator only reads them.
func TestLoadScenario_AllRealScenarioFiles(t *testing.T) {
	dir := scenariosDir(t)
	scenarios, err := LoadScenarioDir(dir)
	require.NoError(t, err)

	wantNames := []string{
		"single-light",
		"light-temp",
		"light-mux",
		"light-mux-ch1",
		"light-mux-ch6",
		"mux-light-temp",
		"light-temp-humi-mux",
	}

	for _, name := range wantNames {
		name := name
		t.Run(name, func(t *testing.T) {
			scenario, ok := scenarios[name]
			require.Truef(t, ok, "scenario %q not loaded from %s", name, dir)
			require.NotEmptyf(t, scenario.Config.GetSensors(), "scenario %q has no sensors", name)

			for _, s := range scenario.Config.GetSensors() {
				resolved, err := resolveSensorType(s.GetChipType(), s.GetSensorType())
				require.NoErrorf(t, err, "scenario %q sensor %q", name, s.GetName())
				assert.NotEqualf(t, firmwarepb.SensorType_SENSOR_TYPE_UNKNOWN, resolved,
					"scenario %q sensor %q resolved to UNKNOWN", name, s.GetName())
			}
		})
	}
}

// TestLoadScenario_MuxLightTemp asserts the specific 3-sensor/mux-address
// shape of mux-light-temp.json: BH1750 light on mux 112/ch7, SHT3x
// temperature and humidity sharing addr 68 + mux ch5 but resolving to
// distinct SensorTypes via the declared sensorType discriminator.
func TestLoadScenario_MuxLightTemp(t *testing.T) {
	path := filepath.Join(scenariosDir(t), "mux-light-temp.json")
	scenario, err := LoadScenario(path)
	require.NoError(t, err)

	sensors := scenario.Config.GetSensors()
	require.Len(t, sensors, 3)

	byName := make(map[string]*configpb.SensorConfig, len(sensors))
	for _, s := range sensors {
		byName[s.GetName()] = s
	}

	light, ok := byName["light"]
	require.True(t, ok, "expected a %q sensor", "light")
	lightType, err := resolveSensorType(light.GetChipType(), light.GetSensorType())
	require.NoError(t, err)
	assert.Equal(t, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE, lightType)
	assert.Equal(t, uint32(35), light.GetI2CAddress())
	require.Len(t, light.GetMuxPath(), 1)
	assert.Equal(t, uint32(112), light.GetMuxPath()[0].GetMuxAddress())
	assert.Equal(t, uint32(7), light.GetMuxPath()[0].GetMuxChannel())

	temperature, ok := byName["temperature"]
	require.True(t, ok, "expected a %q sensor", "temperature")
	temperatureType, err := resolveSensorType(temperature.GetChipType(), temperature.GetSensorType())
	require.NoError(t, err)
	assert.Equal(t, firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE, temperatureType)
	assert.Equal(t, uint32(68), temperature.GetI2CAddress())
	require.Len(t, temperature.GetMuxPath(), 1)
	assert.Equal(t, uint32(112), temperature.GetMuxPath()[0].GetMuxAddress())
	assert.Equal(t, uint32(5), temperature.GetMuxPath()[0].GetMuxChannel())

	humidity, ok := byName["humidity"]
	require.True(t, ok, "expected a %q sensor", "humidity")
	humidityType, err := resolveSensorType(humidity.GetChipType(), humidity.GetSensorType())
	require.NoError(t, err)
	assert.Equal(t, firmwarepb.SensorType_SENSOR_TYPE_HUMIDITY, humidityType)
	assert.Equal(t, uint32(68), humidity.GetI2CAddress())
	require.Len(t, humidity.GetMuxPath(), 1)
	assert.Equal(t, uint32(112), humidity.GetMuxPath()[0].GetMuxAddress())
	assert.Equal(t, uint32(5), humidity.GetMuxPath()[0].GetMuxChannel())
}

// TestLoadScenario_SingleLightHasNoMux asserts single-light.json's one
// sensor (no muxPath key at all) resolves to MuxAddress == 0 && MuxChannel
// == 0 in the Board model, not some other "unset" sentinel.
func TestLoadScenario_SingleLightHasNoMux(t *testing.T) {
	path := filepath.Join(scenariosDir(t), "single-light.json")
	scenario, err := LoadScenario(path)
	require.NoError(t, err)

	sensors, err := resolveBoardSensors(scenario)
	require.NoError(t, err)
	require.Len(t, sensors, 1)
	assert.Equal(t, uint32(0), sensors[0].MuxAddress)
	assert.Equal(t, uint32(0), sensors[0].MuxChannel)
}

// TestLoadScenario_Rejections covers the load-time invariants
// validateScenarioSensors enforces beyond protojson decoding: duplicate
// sensor names, CHIP_TYPE_UNKNOWN, and multi-hop muxPath (the real
// firmware -- LB7 -- is single-hop-only).
func TestLoadScenario_Rejections(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantError string
	}{
		{
			name: "duplicate sensor name",
			body: `{
				"sensors": [
					{"chipType": "CHIP_TYPE_BH1750", "i2cAddress": 35, "name": "light", "enabled": true},
					{"chipType": "CHIP_TYPE_BH1750", "i2cAddress": 36, "name": "light", "enabled": true}
				]
			}`,
			wantError: "duplicate sensor name",
		},
		{
			name: "CHIP_TYPE_UNKNOWN",
			body: `{
				"sensors": [
					{"chipType": "CHIP_TYPE_UNKNOWN", "i2cAddress": 35, "name": "mystery", "enabled": true}
				]
			}`,
			wantError: "CHIP_TYPE_UNKNOWN",
		},
		{
			name: "two-hop muxPath",
			body: `{
				"sensors": [
					{"chipType": "CHIP_TYPE_BH1750", "muxPath": [
						{"muxAddress": 112, "muxChannel": 7},
						{"muxAddress": 113, "muxChannel": 2}
					], "i2cAddress": 35, "name": "light", "enabled": true}
				]
			}`,
			wantError: "muxPath",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTempScenario(t, "rejected", tc.body)
			_, err := LoadScenario(path)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantError)
		})
	}
}
