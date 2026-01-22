package rmq_test

import (
	"testing"

	"github.com/whale-net/everything/libs/go/rmq"
)

func TestNewConnection_Config(t *testing.T) {
	// This test would require a real RabbitMQ instance
	// For now, we test the config structure
	config := rmq.Config{
		Host:     "localhost",
		Port:     5672,
		Username: "guest",
		Password: "guest",
		VHost:    "/",
	}

	// Verify config fields
	if config.Host != "localhost" {
		t.Errorf("Expected host localhost, got %s", config.Host)
	}
	if config.Port != 5672 {
		t.Errorf("Expected port 5672, got %d", config.Port)
	}
}

func TestNewConnectionFromURL_InvalidURL(t *testing.T) {
	// Test with invalid URL format
	_, err := rmq.NewConnectionFromURL("invalid-url")
	if err == nil {
		t.Error("Expected error for invalid URL, got nil")
	}
}

func TestConnection_Close(t *testing.T) {
	// Test that Close doesn't panic on nil connection
	// This is a safety test
	conn := &rmq.Connection{}
	err := conn.Close()
	if err != nil {
		// Close on uninitialized connection might return error, that's OK
		_ = err
	}
}
