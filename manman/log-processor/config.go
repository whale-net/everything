package main

import (
	"os"
	"strconv"
)

// Config holds the log-processor configuration
type Config struct {
	RabbitMQURL       string
	GRPCPort          string
	LogBufferTTL      int // seconds
	LogBufferMaxMsgs  int
	DebugLogOutput    bool
}

// LoadConfig loads configuration from environment variables
func LoadConfig() *Config {
	return &Config{
		RabbitMQURL:      getEnv("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/"),
		GRPCPort:         getEnv("GRPC_PORT", "50053"),
		LogBufferTTL:     getEnvInt("LOG_BUFFER_TTL", 180),         // 3 minutes
		LogBufferMaxMsgs: getEnvInt("LOG_BUFFER_MAX_MESSAGES", 500),
		DebugLogOutput:   getEnvBool("DEBUG_LOG_OUTPUT", false),
	}
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

func getEnvBool(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		if boolVal, err := strconv.ParseBool(value); err == nil {
			return boolVal
		}
	}
	return defaultValue
}
