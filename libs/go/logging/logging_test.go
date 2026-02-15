package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// configureFresh resets global state and configures with a fresh buffer.
// Returns the buffer for inspection. The initial "logging configured"
// message is drained so tests only see their own output.
func configureFresh(t *testing.T, cfg Config) *bytes.Buffer {
	t.Helper()
	mu.Lock()
	configured = false
	mu.Unlock()

	var buf bytes.Buffer
	cfg.Writer = &buf
	Configure(cfg)

	// Drain the startup message emitted by Configure()
	buf.Reset()
	return &buf
}

func TestJSONHandler_BasicOutput(t *testing.T) {
	buf := configureFresh(t, Config{
		ServiceName: "test-app",
		Domain:      "test",
		Environment: "testing",
		Version:     "v0.1.0",
		JSONFormat:  true,
	})

	slog.Info("hello world", "key", "value")

	var m map[string]any
	err := json.Unmarshal(buf.Bytes(), &m)
	require.NoError(t, err, "output should be valid JSON")

	assert.Equal(t, "hello world", m["message"])
	assert.Equal(t, "INFO", m["severity"])
	assert.Equal(t, "test-app", m["app_name"])
	assert.Equal(t, "test", m["domain"])
	assert.Equal(t, "testing", m["environment"])
	assert.Equal(t, "v0.1.0", m["version"])
	assert.Equal(t, "value", m["key"])
	assert.Contains(t, m, "timestamp")
	assert.Contains(t, m, "source")
}

func TestJSONHandler_OmitsEmptyOptionalFields(t *testing.T) {
	buf := configureFresh(t, Config{
		ServiceName: "test-app",
		Environment: "testing",
		JSONFormat:  true,
	})

	slog.Info("msg")

	var m map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &m))

	assert.NotContains(t, m, "commit_sha")
	assert.NotContains(t, m, "pod_name")
	assert.NotContains(t, m, "namespace")
	assert.NotContains(t, m, "node_name")
}

func TestConsoleHandler_BasicOutput(t *testing.T) {
	buf := configureFresh(t, Config{
		ServiceName: "console-app",
		Environment: "dev",
		JSONFormat:  false,
	})

	slog.Info("starting up", "port", "8080")

	line := buf.String()
	assert.Contains(t, line, "[console-app]")
	assert.Contains(t, line, "INFO")
	assert.Contains(t, line, "starting up")
	assert.Contains(t, line, "port=8080")
}

func TestGet_AddsLoggerAttr(t *testing.T) {
	buf := configureFresh(t, Config{
		ServiceName: "test-app",
		JSONFormat:  true,
	})

	logger := Get("mypackage")
	logger.Info("test message")

	var m map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &m))
	assert.Equal(t, "mypackage", m["logger"])
}

func TestJSONHandler_LevelFiltering(t *testing.T) {
	buf := configureFresh(t, Config{
		ServiceName: "test-app",
		Level:       slog.LevelWarn,
		JSONFormat:  true,
	})

	slog.Info("should be filtered")
	assert.Empty(t, buf.String(), "INFO should be filtered at WARN level")

	slog.Warn("should appear")
	assert.NotEmpty(t, buf.String())
}

func TestConsoleHandler_LevelFiltering(t *testing.T) {
	buf := configureFresh(t, Config{
		ServiceName: "test-app",
		Level:       slog.LevelError,
		JSONFormat:  false,
	})

	slog.Warn("filtered")
	assert.Empty(t, buf.String())

	slog.Error("visible")
	assert.Contains(t, buf.String(), "visible")
}

func TestJSONHandler_WithAttrs(t *testing.T) {
	buf := configureFresh(t, Config{
		ServiceName: "test-app",
		JSONFormat:  true,
	})

	logger := slog.Default().With("request_id", "req-123")
	logger.Info("handling")

	var m map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &m))
	assert.Equal(t, "req-123", m["request_id"])
}

func TestApplyDefaults_FromEnv(t *testing.T) {
	t.Setenv("APP_NAME", "env-app")
	t.Setenv("APP_DOMAIN", "api")
	t.Setenv("APP_ENV", "staging")
	t.Setenv("APP_VERSION", "v2.0.0")

	cfg := Config{}
	applyDefaults(&cfg)

	assert.Equal(t, "env-app", cfg.ServiceName)
	assert.Equal(t, "api", cfg.Domain)
	assert.Equal(t, "staging", cfg.Environment)
	assert.Equal(t, "v2.0.0", cfg.Version)
}

func TestApplyDefaults_ExplicitOverridesEnv(t *testing.T) {
	t.Setenv("APP_NAME", "env-app")

	cfg := Config{ServiceName: "explicit-app"}
	applyDefaults(&cfg)

	assert.Equal(t, "explicit-app", cfg.ServiceName)
}

func TestJSONHandler_NumericAttrs(t *testing.T) {
	buf := configureFresh(t, Config{
		ServiceName: "test-app",
		JSONFormat:  true,
	})

	slog.Info("metrics", "count", 42, "ratio", 3.14, "enabled", true)

	var m map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &m))

	assert.Equal(t, float64(42), m["count"])
	assert.Equal(t, 3.14, m["ratio"])
	assert.Equal(t, true, m["enabled"])
}

func TestJSONHandler_MultipleRecords(t *testing.T) {
	buf := configureFresh(t, Config{
		ServiceName: "test-app",
		JSONFormat:  true,
	})

	slog.Info("first")
	slog.Info("second")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	assert.Len(t, lines, 2)

	var m1, m2 map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &m1))
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &m2))
	assert.Equal(t, "first", m1["message"])
	assert.Equal(t, "second", m2["message"])
}

func TestSlogToOTELSeverity(t *testing.T) {
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
		sev := slogToOTELSeverity(tt.level)
		assert.Contains(t, sev.String(), tt.expected)
	}
}
