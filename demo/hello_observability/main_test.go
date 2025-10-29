package main

import (
	"context"
	"testing"
	"time"

	"github.com/whale-net/everything/libs/go/observability"
)

func TestMain(t *testing.T) {
	// Configure observability for testing
	logConfig := &observability.LogConfig{
		ServiceName:   "test-demo",
		EnableConsole: true,
		EnableOTLP:    false, // Disable OTLP for testing
		JSONFormat:    false,
	}

	_, err := observability.ConfigureLogging(logConfig)
	if err != nil {
		t.Fatalf("Failed to configure logging: %v", err)
	}

	traceConfig := &observability.TraceConfig{
		ServiceName: "test-demo",
		EnableOTLP:  false, // Disable OTLP for testing
	}

	_, err = observability.ConfigureTracing(traceConfig)
	if err != nil {
		t.Fatalf("Failed to configure tracing: %v", err)
	}

	// Test processing a request
	processRequest(context.Background(), 1)

	// Cleanup
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := observability.ShutdownAll(ctx); err != nil {
		t.Errorf("Failed to shutdown: %v", err)
	}
}

func TestValidateOrder(t *testing.T) {
	// Setup
	logConfig := &observability.LogConfig{
		ServiceName:   "test-demo",
		EnableConsole: false,
		EnableOTLP:    false,
	}
	observability.ConfigureLogging(logConfig)

	traceConfig := &observability.TraceConfig{
		ServiceName: "test-demo",
		EnableOTLP:  false,
	}
	observability.ConfigureTracing(traceConfig)

	// Test
	ctx := context.Background()
	validateOrder(ctx)

	// Cleanup
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	observability.ShutdownAll(ctx)
}

func TestSaveOrder(t *testing.T) {
	// Setup
	logConfig := &observability.LogConfig{
		ServiceName:   "test-demo",
		EnableConsole: false,
		EnableOTLP:    false,
	}
	observability.ConfigureLogging(logConfig)

	traceConfig := &observability.TraceConfig{
		ServiceName: "test-demo",
		EnableOTLP:  false,
	}
	observability.ConfigureTracing(traceConfig)

	// Test
	ctx := context.Background()
	saveOrder(ctx)

	// Cleanup
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	observability.ShutdownAll(ctx)
}
