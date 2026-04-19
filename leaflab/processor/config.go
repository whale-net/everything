package main

import (
	"fmt"
	"os"
)

// Config holds all configuration for the leaflab processor.
type Config struct {
	RabbitMQURL string
	QueueName   string
	DBHost      string
	DBPort      string
	DBUser      string
	DBPassword  string
	DBName      string
	DBSSLMode   string
}

func LoadConfig() (*Config, error) {
	cfg := &Config{
		RabbitMQURL: getEnv("RABBITMQ_URL", ""),
		QueueName:   getEnv("QUEUE_NAME", "leaflab-processor"),
		DBHost:      getEnv("DB_HOST", "localhost"),
		DBPort:      getEnv("DB_PORT", "5432"),
		DBUser:      getEnv("DB_USER", "postgres"),
		DBPassword:  getEnv("DB_PASSWORD", ""),
		DBName:      getEnv("DB_NAME", "leaflab"),
		DBSSLMode:   getEnv("DB_SSL_MODE", "disable"),
	}

	if cfg.RabbitMQURL == "" {
		return nil, fmt.Errorf("RABBITMQ_URL is required")
	}

	return cfg, nil
}

func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}
