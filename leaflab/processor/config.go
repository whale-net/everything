package main

import (
	"fmt"
	"os"
)

// Config holds all configuration for the leaflab processor.
type Config struct {
	RabbitMQURL string
	QueueName   string
	DatabaseURL string // PG_DATABASE_URL — postgres://user:pass@host:5432/dbname
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

	return cfg, nil
}

func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}
