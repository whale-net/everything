package logging

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	obscontext "github.com/whale-net/everything/libs/go/observability/context"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	assert.True(t, cfg.EnableConsole)
	assert.True(t, cfg.EnableOTLP)
}

func TestConfigureConsoleOnly(t *testing.T) {
	var buf bytes.Buffer
	cfg := &Config{
		ServiceName:   "test-service",
		ServiceVersion: "v1.0.0",
		Environment:   "test",
		Level:         slog.LevelInfo,
		EnableConsole: true,
		ConsoleWriter: &buf,
		JSONFormat:    false,
		EnableOTLP:    false,
	}

	logger, err := Configure(cfg)
	require.NoError(t, err)
	require.NotNil(t, logger)

	// Test logging
	logger.Info("test message", "key", "value")

	output := buf.String()
	assert.Contains(t, output, "test message")
	assert.Contains(t, output, "key=value")
}

func TestConfigureJSON(t *testing.T) {
	var buf bytes.Buffer
	cfg := &Config{
		ServiceName:   "test-service",
		ServiceVersion: "v1.0.0",
		Environment:   "test",
		Level:         slog.LevelInfo,
		EnableConsole: true,
		ConsoleWriter: &buf,
		JSONFormat:    true,
		EnableOTLP:    false,
	}

	logger, err := Configure(cfg)
	require.NoError(t, err)
	require.NotNil(t, logger)

	// Test logging
	logger.Info("test message", "key", "value")

	output := buf.String()
	assert.Contains(t, output, "test message")
	assert.Contains(t, output, "\"key\":\"value\"")
}

func TestLoggerWithContext(t *testing.T) {
	var buf bytes.Buffer
	cfg := &Config{
		ServiceName:   "test-service",
		Level:         slog.LevelInfo,
		EnableConsole: true,
		ConsoleWriter: &buf,
		JSONFormat:    false,
		EnableOTLP:    false,
	}

	logger, err := Configure(cfg)
	require.NoError(t, err)

	// Create context with observability data
	obsCtx := obscontext.NewContext()
	obsCtx.RequestID = "req-123"
	obsCtx.UserID = "user-456"
	obsCtx.HTTPMethod = "POST"
	obsCtx.HTTPPath = "/api/test"

	ctx := obscontext.WithContext(context.Background(), obsCtx)

	// Log with context
	logger.InfoContext(ctx, "processing request")

	output := buf.String()
	assert.Contains(t, output, "processing request")
	assert.Contains(t, output, "request_id=req-123")
	assert.Contains(t, output, "user_id=user-456")
	assert.Contains(t, output, "http_method=POST")
	assert.Contains(t, output, "http_path=/api/test")
}

func TestLoggerLevels(t *testing.T) {
	var buf bytes.Buffer
	cfg := &Config{
		ServiceName:   "test-service",
		Level:         slog.LevelDebug,
		EnableConsole: true,
		ConsoleWriter: &buf,
		JSONFormat:    false,
		EnableOTLP:    false,
	}

	logger, err := Configure(cfg)
	require.NoError(t, err)

	ctx := context.Background()

	logger.DebugContext(ctx, "debug message")
	logger.InfoContext(ctx, "info message")
	logger.WarnContext(ctx, "warn message")
	logger.ErrorContext(ctx, "error message")

	output := buf.String()
	assert.Contains(t, output, "debug message")
	assert.Contains(t, output, "info message")
	assert.Contains(t, output, "warn message")
	assert.Contains(t, output, "error message")
}

func TestLoggerWithAttrs(t *testing.T) {
	var buf bytes.Buffer
	cfg := &Config{
		ServiceName:   "test-service",
		Level:         slog.LevelInfo,
		EnableConsole: true,
		ConsoleWriter: &buf,
		JSONFormat:    false,
		EnableOTLP:    false,
	}

	logger, err := Configure(cfg)
	require.NoError(t, err)

	// Add attributes
	logger = logger.WithAttrs(
		slog.String("component", "api"),
		slog.String("module", "users"),
	)

	logger.Info("test message")

	output := buf.String()
	assert.Contains(t, output, "test message")
	assert.Contains(t, output, "component=api")
	assert.Contains(t, output, "module=users")
}

func TestDefaultLogger(t *testing.T) {
	// Reset default logger
	defaultLogger = nil

	logger := Default()
	assert.NotNil(t, logger)
}

func TestEnrichArgsWithCustomAttributes(t *testing.T) {
	var buf bytes.Buffer
	cfg := &Config{
		ServiceName:   "test-service",
		Level:         slog.LevelInfo,
		EnableConsole: true,
		ConsoleWriter: &buf,
		JSONFormat:    false,
		EnableOTLP:    false,
	}

	logger, err := Configure(cfg)
	require.NoError(t, err)

	// Create context with custom attributes
	obsCtx := obscontext.NewContext()
	obsCtx.Custom["tenant_id"] = "tenant-123"
	obsCtx.Custom["feature_flag"] = "new_ui"

	ctx := obscontext.WithContext(context.Background(), obsCtx)

	// Log with context
	logger.InfoContext(ctx, "custom attributes test")

	output := buf.String()
	assert.Contains(t, output, "custom attributes test")
	assert.Contains(t, output, "tenant_id")
	assert.Contains(t, output, "feature_flag")
}

func TestMultiHandler(t *testing.T) {
	var buf1 bytes.Buffer
	var buf2 bytes.Buffer

	handler1 := slog.NewTextHandler(&buf1, &slog.HandlerOptions{Level: slog.LevelInfo})
	handler2 := slog.NewTextHandler(&buf2, &slog.HandlerOptions{Level: slog.LevelWarn})

	multi := &multiHandler{
		handlers: []slog.Handler{handler1, handler2},
	}

	logger := slog.New(multi)

	// Info should go to handler1 only
	logger.Info("info message")
	assert.Contains(t, buf1.String(), "info message")
	assert.NotContains(t, buf2.String(), "info message")

	// Warn should go to both
	logger.Warn("warn message")
	assert.Contains(t, buf1.String(), "warn message")
	assert.Contains(t, buf2.String(), "warn message")
}

func TestSlogLevelToOTLP(t *testing.T) {
	tests := []struct {
		level    slog.Level
		expected string
	}{
		{slog.LevelDebug, "DEBUG"},
		{slog.LevelInfo, "INFO"},
		{slog.LevelWarn, "WARN"},
		{slog.LevelError, "ERROR"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			severity := slogLevelToOTLP(tt.level)
			assert.Contains(t, strings.ToUpper(severity.String()), tt.expected)
		})
	}
}

func TestShutdown(t *testing.T) {
	// Test shutdown with no logger provider
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := Shutdown(ctx)
	assert.NoError(t, err)
}
