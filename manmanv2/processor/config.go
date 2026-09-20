package main

import (
	"fmt"
	"os"
	"strconv"

	temporallib "github.com/whale-net/everything/libs/go/temporal"
	"github.com/whale-net/everything/manmanv2/events"
	"github.com/whale-net/everything/manmanv2/processor/backupsched"
)

// Config holds all configuration for the processor service
type Config struct {
	RabbitMQURL           string
	DBHost                string
	DBPort                string
	DBUser                string
	DBPassword            string
	DBName                string
	DBSSLMode             string
	QueueName             string
	LogLevel              string
	HealthCheckPort       string
	StaleHostThreshold    int
	StaleSessionThreshold int
	ExternalExchange      string
	LiveExchange          string

	// Temporal — the backupsched package's worker registration (FR16, FR17
	// M7). TemporalTaskQueue defaults to backupsched.DefaultTaskQueue
	// ("manmanv2-processor", named after this worker binary) rather than
	// libs/go/temporal.ConfigFromEnv's own empty default, per
	// manmanv2/ARCHITECTURE.md's one-task-queue-per-worker-binary
	// convention.
	TemporalHost      string
	TemporalNamespace string
	TemporalTaskQueue string
}

// LoadConfig loads configuration from environment variables
func LoadConfig() (*Config, error) {
	cfg := &Config{
		RabbitMQURL:           getEnv("RABBITMQ_URL", ""),
		DBHost:                getEnv("DB_HOST", "localhost"),
		DBPort:                getEnv("DB_PORT", "5432"),
		DBUser:                getEnv("DB_USER", "postgres"),
		DBPassword:            getEnv("DB_PASSWORD", ""),
		DBName:                getEnv("DB_NAME", "manman"),
		DBSSLMode:             getEnv("DB_SSL_MODE", "disable"),
		QueueName:             getEnv("QUEUE_NAME", "processor-events"),
		LogLevel:              getEnv("LOG_LEVEL", "info"),
		HealthCheckPort:       getEnv("HEALTH_CHECK_PORT", "8080"),
		StaleHostThreshold:    getEnvInt("STALE_HOST_THRESHOLD_SECONDS", 90),
		StaleSessionThreshold: getEnvInt("STALE_SESSION_THRESHOLD_SECONDS", 30), // Default 30 seconds
		ExternalExchange:      getEnv("EXTERNAL_EXCHANGE", "external"),
		LiveExchange:          getEnv("LIVE_EXCHANGE", events.ExchangeName),
		TemporalHost:          getEnv("TEMPORAL_HOST", temporallib.DefaultHostPort),
		TemporalNamespace:     getEnv("TEMPORAL_NAMESPACE", temporallib.DefaultNamespace),
		TemporalTaskQueue:     getEnv("TEMPORAL_TASK_QUEUE", backupsched.DefaultTaskQueue),
	}

	// Validate required fields
	if cfg.RabbitMQURL == "" {
		return nil, fmt.Errorf("RABBITMQ_URL is required")
	}
	if cfg.DBPassword == "" {
		return nil, fmt.Errorf("DB_PASSWORD is required")
	}

	return cfg, nil
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intVal, err := strconv.Atoi(value); err == nil {
			return intVal
		}
	}
	return defaultValue
}
