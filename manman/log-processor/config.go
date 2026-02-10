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
	S3Bucket          string
	S3Region          string
	S3Endpoint        string
	S3AccessKey       string
	S3SecretKey       string
	DatabaseURL       string
	APIAddress        string
}

// LoadConfig loads configuration from environment variables
func LoadConfig() *Config {
	return &Config{
		RabbitMQURL:      getEnv("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/"),
		GRPCPort:         getEnv("GRPC_PORT", "50053"),
		LogBufferTTL:     getEnvInt("LOG_BUFFER_TTL", 180),         // 3 minutes
		LogBufferMaxMsgs: getEnvInt("LOG_BUFFER_MAX_MESSAGES", 500),
		DebugLogOutput:   getEnvBool("DEBUG_LOG_OUTPUT", false),
		S3Bucket:         getEnv("S3_BUCKET", "manman-logs"),
		S3Region:         getEnv("S3_REGION", "us-east-1"),
		S3Endpoint:       getEnv("S3_ENDPOINT", ""),
		S3AccessKey:      getEnv("S3_ACCESS_KEY", ""),
		S3SecretKey:      getEnv("S3_SECRET_KEY", ""),
		DatabaseURL:      getEnv("DATABASE_URL", ""),
		APIAddress:       getEnv("API_ADDRESS", "localhost:50051"),
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
