package main

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds all configuration for the leaflab processor.
type Config struct {
	RabbitMQURL string
	QueueName   string
	DatabaseURL string // PG_DATABASE_URL — postgres://user:pass@host:5432/dbname
	// CacheBackstopInterval is how often RunCacheBackstop fully reloads
	// SensorCache from the database, self-healing a dropped FR73
	// invalidation event within this bound. See backstop.go and
	// leaflab/ARCHITECTURE.md — this is not what satisfies FR73's 5s
	// bound, the invalidation signal is.
	CacheBackstopInterval time.Duration
}

func LoadConfig() (*Config, error) {
	cfg := &Config{
		RabbitMQURL:           getEnv("RABBITMQ_URL", ""),
		QueueName:             getEnv("QUEUE_NAME", "leaflab-processor"),
		DatabaseURL:           getEnv("PG_DATABASE_URL", ""),
		CacheBackstopInterval: DefaultCacheBackstopInterval,
	}

	if v := os.Getenv("CACHE_BACKSTOP_INTERVAL_SECONDS"); v != "" {
		secs, err := strconv.Atoi(v)
		if err != nil || secs <= 0 {
			return nil, fmt.Errorf("CACHE_BACKSTOP_INTERVAL_SECONDS must be a positive integer, got %q", v)
		}
		cfg.CacheBackstopInterval = time.Duration(secs) * time.Second
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
