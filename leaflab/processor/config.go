package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/whale-net/everything/leaflab/api/claim"
)

// Config holds all configuration for the leaflab processor.
type Config struct {
	RabbitMQURL string
	QueueName   string
	DatabaseURL string // PG_DATABASE_URL — postgres://user:pass@host:5432/dbname
	// RestartUptimeThresholdSeconds is FR76's restart-detection threshold
	// (#1342): an uptime_s regression below this value is treated as a
	// genuine restart (leaflab/processor/handler.go's
	// CheckAndUpdateUptimeWatermark call). Reads the same
	// LEAFLAB_API_CLAIM_RESTART_UPTIME_THRESHOLD_SECONDS variable
	// leaflab-api's claim.Config uses -- one env var as the single source of
	// truth shared by both processes, rather than duplicating the value
	// under a processor-specific name. Deliberately does not pull in the
	// rest of claim.Config (RoundsRequired etc.) via claim.LoadConfigFromEnv:
	// this is the one field the processor actually needs, and reusing the
	// full loader would fail this process's boot on a misconfigured field
	// (e.g. ROUNDS_REQUIRED < 2) that has nothing to do with restart
	// detection.
	RestartUptimeThresholdSeconds uint32
}

func LoadConfig() (*Config, error) {
	cfg := &Config{
		RabbitMQURL: getEnv("RABBITMQ_URL", ""),
		QueueName:   getEnv("QUEUE_NAME", "leaflab-processor"),
		DatabaseURL: getEnv("PG_DATABASE_URL", ""),
	}

	if cfg.RabbitMQURL == "" {
		return nil, fmt.Errorf("RABBITMQ_URL is required")
	}
	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("PG_DATABASE_URL is required")
	}

	thresholdSeconds := int(claim.DefaultConfig.RestartUptimeThreshold.Seconds())
	if raw := os.Getenv(claim.EnvRestartThresholdSecs); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("%s=%q: %w", claim.EnvRestartThresholdSecs, raw, err)
		}
		thresholdSeconds = v
	}
	cfg.RestartUptimeThresholdSeconds = uint32(thresholdSeconds)

	return cfg, nil
}

func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}
