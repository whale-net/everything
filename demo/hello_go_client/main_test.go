package main

import (
	"testing"

	client "github.com/whale-net/everything/generated/demo/hello_fastapi_go"
)

func TestClientConfiguration(t *testing.T) {
	// Test creating a new configuration
	cfg := client.NewConfiguration()
	if cfg == nil {
		t.Fatal("NewConfiguration() returned nil")
	}

	// Test setting host and scheme
	cfg.Host = "localhost:8000"
	cfg.Scheme = "http"

	if cfg.Host != "localhost:8000" {
		t.Errorf("Expected host to be 'localhost:8000', got '%s'", cfg.Host)
	}

	if cfg.Scheme != "http" {
		t.Errorf("Expected scheme to be 'http', got '%s'", cfg.Scheme)
	}
}

func TestAPIClientCreation(t *testing.T) {
	// Test creating a new API client
	cfg := client.NewConfiguration()
	cfg.Host = "localhost:8000"
	cfg.Scheme = "http"

	apiClient := client.NewAPIClient(cfg)
	if apiClient == nil {
		t.Fatal("NewAPIClient() returned nil")
	}

	// Verify the client has the expected DefaultAPI
	if apiClient.DefaultAPI == nil {
		t.Error("APIClient.DefaultAPI is nil")
	}
}

func TestConfigurationDefaults(t *testing.T) {
	// Test that configuration has reasonable defaults
	cfg := client.NewConfiguration()

	// Check that basic fields are initialized
	if cfg.UserAgent == "" {
		t.Error("UserAgent should have a default value")
	}

	// DefaultAPI should be accessible after client creation
	apiClient := client.NewAPIClient(cfg)
	if apiClient.GetConfig() == nil {
		t.Error("GetConfig() should return the configuration")
	}
}
