package main

import (
	"fmt"
	"os"
	"strconv"
)

// Config holds all configuration for the processor service
type Config struct {
	RabbitMQURL        string
	DBHost             string
	DBPort             string
	DBUser             string
	DBPassword         string
	DBName             string
	DBSSLMode          string
	QueueName          string
	LogLevel           string
	HealthCheckPort    string
	StaleHostThreshold    int
	StaleSessionThreshold int
	ExternalExchange      string
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
		StaleHostThreshold:    getEnvInt("STALE_HOST_THRESHOLD_SECONDS", 10),
		StaleSessionThreshold: getEnvInt("STALE_SESSION_THRESHOLD_SECONDS", 300), // Default 5 minutes
		ExternalExchange:      getEnv("EXTERNAL_EXCHANGE", "external"),
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
