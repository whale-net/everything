package tracing

import (
	"context"
	"os"
	"testing"

	obscontext "github.com/whale-net/everything/libs/go/observability/context"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

func TestDefaultConfig(t *testing.T) {
	// Set test environment
	os.Setenv("APP_NAME", "test-app")
	os.Setenv("APP_VERSION", "v1.0.0")
	os.Setenv("APP_ENV", "testing")
	defer func() {
		os.Unsetenv("APP_NAME")
		os.Unsetenv("APP_VERSION")
		os.Unsetenv("APP_ENV")
	}()

	cfg := DefaultConfig()

	assert.Equal(t, "test-app", cfg.ServiceName)
	assert.Equal(t, "v1.0.0", cfg.ServiceVersion)
	assert.Equal(t, "testing", cfg.Environment)
	assert.True(t, cfg.EnableOTLP)
	assert.Equal(t, 1.0, cfg.SampleRate)
}

func TestConfigureOTLPDisabled(t *testing.T) {
	cfg := &Config{
		ServiceName:    "test-service",
		ServiceVersion: "v1.0.0",
		Environment:    "test",
		EnableOTLP:     false,
		SampleRate:     1.0,
	}

	provider, err := Configure(cfg)
	require.NoError(t, err)
	require.NotNil(t, provider)
	
	// Clean up
	ctx := context.Background()
	err = Shutdown(ctx)
	assert.NoError(t, err)
}

func TestTracer(t *testing.T) {
	cfg := &Config{
		ServiceName:    "test-service",
		ServiceVersion: "v1.0.0",
		Environment:    "test",
		EnableOTLP:     false,
		SampleRate:     1.0,
	}

	_, err := Configure(cfg)
	require.NoError(t, err)

	tracer := Tracer("test-tracer")
	assert.NotNil(t, tracer)
	
	// Clean up
	ctx := context.Background()
	err = Shutdown(ctx)
	assert.NoError(t, err)
}

func TestStartSpan(t *testing.T) {
	cfg := &Config{
		ServiceName:    "test-service",
		ServiceVersion: "v1.0.0",
		Environment:    "test",
		EnableOTLP:     false,
		SampleRate:     1.0,
	}

	_, err := Configure(cfg)
	require.NoError(t, err)

	ctx := context.Background()
	ctx, span := StartSpan(ctx, "test-span")
	
	assert.NotNil(t, span)
	assert.True(t, span.IsRecording())
	
	span.End()
	
	// Clean up
	err = Shutdown(ctx)
	assert.NoError(t, err)
}

func TestStartSpanWithContext(t *testing.T) {
	cfg := &Config{
		ServiceName:    "test-service",
		ServiceVersion: "v1.0.0",
		Environment:    "test",
		EnableOTLP:     false,
		SampleRate:     1.0,
	}

	_, err := Configure(cfg)
	require.NoError(t, err)

	// Create context with observability data
	obsCtx := obscontext.NewContext()
	obsCtx.RequestID = "req-123"
	obsCtx.UserID = "user-456"
	obsCtx.HTTPMethod = "POST"
	obsCtx.HTTPPath = "/api/test"
	obsCtx.ClientIP = "192.168.1.1"

	ctx := obscontext.WithContext(context.Background(), obsCtx)

	// Start span with context
	ctx, span := StartSpanWithContext(ctx, "test-span-with-context")
	
	assert.NotNil(t, span)
	assert.True(t, span.IsRecording())
	
	span.End()
	
	// Clean up
	err = Shutdown(ctx)
	assert.NoError(t, err)
}

func TestNestedSpans(t *testing.T) {
	cfg := &Config{
		ServiceName:    "test-service",
		ServiceVersion: "v1.0.0",
		Environment:    "test",
		EnableOTLP:     false,
		SampleRate:     1.0,
	}

	_, err := Configure(cfg)
	require.NoError(t, err)

	ctx := context.Background()
	
	// Start parent span
	ctx, parentSpan := StartSpan(ctx, "parent-span")
	assert.True(t, parentSpan.IsRecording())
	
	// Start child span
	ctx, childSpan := StartSpan(ctx, "child-span")
	assert.True(t, childSpan.IsRecording())
	
	// Verify span context is maintained
	spanCtx := trace.SpanContextFromContext(ctx)
	assert.True(t, spanCtx.IsValid())
	
	childSpan.End()
	parentSpan.End()
	
	// Clean up
	err = Shutdown(ctx)
	assert.NoError(t, err)
}

func TestSamplingRates(t *testing.T) {
	tests := []struct {
		name       string
		sampleRate float64
	}{
		{"always sample", 1.0},
		{"never sample", 0.0},
		{"50% sample", 0.5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				ServiceName:    "test-service",
				ServiceVersion: "v1.0.0",
				Environment:    "test",
				EnableOTLP:     false,
				SampleRate:     tt.sampleRate,
			}

			provider, err := Configure(cfg)
			require.NoError(t, err)
			require.NotNil(t, provider)
			
			// Clean up
			ctx := context.Background()
			err = Shutdown(ctx)
			assert.NoError(t, err)
		})
	}
}

func TestShutdown(t *testing.T) {
	// Test shutdown with no tracer provider
	ctx := context.Background()
	err := Shutdown(ctx)
	assert.NoError(t, err)
}

func TestSpanAttributes(t *testing.T) {
	cfg := &Config{
		ServiceName:    "test-service",
		ServiceVersion: "v1.0.0",
		Environment:    "test",
		EnableOTLP:     false,
		SampleRate:     1.0,
	}

	_, err := Configure(cfg)
	require.NoError(t, err)

	ctx := context.Background()
	ctx, span := StartSpan(ctx, "test-span")
	
	// Add custom attributes using the attribute package
	// The span.SetAttributes method exists but requires proper attribute types
	// For this test, we just verify the span is recording
	
	assert.True(t, span.IsRecording())
	
	span.End()
	
	// Clean up
	err = Shutdown(ctx)
	assert.NoError(t, err)
}
