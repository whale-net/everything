package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func clearEmulatorEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"MQTT_BROKER_URL",
		"MQTT_USERNAME",
		"MQTT_PASSWORD",
		"SCENARIO_DIR",
		"EMULATOR_BOARDS",
		"PUBLISH_INTERVAL",
		"RANDOM_SEED",
	} {
		t.Setenv(key, "")
	}
}

func TestLoadConfig_Defaults(t *testing.T) {
	clearEmulatorEnv(t)

	cfg, err := LoadConfig()
	require.NoError(t, err)

	assert.Equal(t, "tcp://localhost:1883", cfg.MQTTBrokerURL)
	assert.Equal(t, "rabbit", cfg.MQTTUsername)
	assert.Equal(t, "password", cfg.MQTTPassword)
	assert.Equal(t, "leaflab/scripts/scenarios", cfg.ScenarioDir)
	assert.Equal(t, "", cfg.EmulatorBoards)
	assert.Equal(t, 5*time.Second, cfg.PublishInterval)
	assert.Equal(t, int64(0), cfg.RandomSeed)
}

func TestLoadConfig_Overrides(t *testing.T) {
	clearEmulatorEnv(t)

	t.Setenv("MQTT_BROKER_URL", "tcp://broker.example.com:1883")
	t.Setenv("MQTT_USERNAME", "custom-user")
	t.Setenv("MQTT_PASSWORD", "custom-pass")
	t.Setenv("SCENARIO_DIR", "/tmp/scenarios")
	t.Setenv("EMULATOR_BOARDS", "board1,board2")
	t.Setenv("PUBLISH_INTERVAL", "10s")
	t.Setenv("RANDOM_SEED", "42")

	cfg, err := LoadConfig()
	require.NoError(t, err)

	assert.Equal(t, "tcp://broker.example.com:1883", cfg.MQTTBrokerURL)
	assert.Equal(t, "custom-user", cfg.MQTTUsername)
	assert.Equal(t, "custom-pass", cfg.MQTTPassword)
	assert.Equal(t, "/tmp/scenarios", cfg.ScenarioDir)
	assert.Equal(t, "board1,board2", cfg.EmulatorBoards)
	assert.Equal(t, 10*time.Second, cfg.PublishInterval)
	assert.Equal(t, int64(42), cfg.RandomSeed)
}

func TestLoadConfig_InvalidPublishInterval(t *testing.T) {
	clearEmulatorEnv(t)
	t.Setenv("PUBLISH_INTERVAL", "nope")

	_, err := LoadConfig()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PUBLISH_INTERVAL")
}

func TestLoadConfig_InvalidRandomSeed(t *testing.T) {
	clearEmulatorEnv(t)
	t.Setenv("RANDOM_SEED", "not-a-number")

	_, err := LoadConfig()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RANDOM_SEED")
}
