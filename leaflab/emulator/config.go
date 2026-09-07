package main

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds all configuration for the leaflab board emulator.
type Config struct {
	MQTTBrokerURL   string        // MQTT_BROKER_URL — broker address
	MQTTUsername    string        // MQTT_USERNAME — broker user
	MQTTPassword    string        // MQTT_PASSWORD — broker password
	ScenarioDir     string        // SCENARIO_DIR — directory holding scenario JSON
	EmulatorBoards  string        // EMULATOR_BOARDS — raw board spec list, parsed by the board-spawning task
	PublishInterval time.Duration // PUBLISH_INTERVAL — default reading-publish interval (NFR6)
	RandomSeed      int64         // RANDOM_SEED — 0 means seed from time
}

func LoadConfig() (*Config, error) {
	cfg := &Config{
		MQTTBrokerURL:  getEnv("MQTT_BROKER_URL", "tcp://localhost:1883"),
		MQTTUsername:   getEnv("MQTT_USERNAME", "rabbit"),
		MQTTPassword:   getEnv("MQTT_PASSWORD", "password"),
		ScenarioDir:    getEnv("SCENARIO_DIR", "leaflab/scripts/scenarios"),
		EmulatorBoards: getEnv("EMULATOR_BOARDS", ""),
	}

	publishIntervalStr := getEnv("PUBLISH_INTERVAL", "5s")
	publishInterval, err := time.ParseDuration(publishIntervalStr)
	if err != nil {
		return nil, fmt.Errorf("invalid PUBLISH_INTERVAL %q: %w", publishIntervalStr, err)
	}
	cfg.PublishInterval = publishInterval

	randomSeedStr := getEnv("RANDOM_SEED", "0")
	randomSeed, err := strconv.ParseInt(randomSeedStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid RANDOM_SEED %q: %w", randomSeedStr, err)
	}
	cfg.RandomSeed = randomSeed

	return cfg, nil
}

func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}
