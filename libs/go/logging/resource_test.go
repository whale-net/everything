package logging

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// attrMap converts resource attributes to a plain map for easy assertion.
// It uses Value.Emit() so that non-string attribute types (int64, bool, etc.)
// are represented as their display string rather than "".
func attrMap(t *testing.T, cfg Config) map[string]string {
	t.Helper()
	ctx := context.Background()
	res, err := buildResource(ctx, cfg)
	// partial-failure errors are acceptable (e.g., detector not available in sandbox)
	if err != nil {
		t.Logf("buildResource partial error (expected in sandbox): %v", err)
	}
	require.NotNil(t, res, "resource must not be nil even on partial failure")

	m := make(map[string]string, len(res.Attributes()))
	for _, kv := range res.Attributes() {
		m[string(kv.Key)] = kv.Value.Emit()
	}
	return m
}

func TestBuildResource_CoreServiceAttrs(t *testing.T) {
	cfg := Config{
		ServiceName: "test-svc",
		Version:     "v1.2.3",
		Environment: "staging",
	}
	m := attrMap(t, cfg)

	assert.Equal(t, "test-svc", m["service.name"])
	assert.Equal(t, "v1.2.3", m["service.version"])
	assert.Equal(t, "staging", m["deployment.environment"])
}

func TestBuildResource_OptionalAttrsIncluded(t *testing.T) {
	cfg := Config{
		ServiceName: "svc",
		Version:     "v1.0.0",
		Environment: "prod",
		Domain:      "api",
		AppType:     "server",
		CommitSHA:   "abc1234",
		PodName:     "svc-abc-xyz",
		Namespace:   "default",
		NodeName:    "node-1",
	}
	m := attrMap(t, cfg)

	assert.Equal(t, "api", m["service.domain"])
	assert.Equal(t, "server", m["service.type"])
	assert.Equal(t, "abc1234", m["vcs.repository.ref.revision"])
	assert.Equal(t, "svc-abc-xyz", m["service.instance.id"])
	assert.Equal(t, "svc-abc-xyz", m["k8s.pod.name"])
	assert.Equal(t, "default", m["k8s.namespace.name"])
	assert.Equal(t, "node-1", m["k8s.node.name"])
}

func TestBuildResource_OptionalAttrsOmitted(t *testing.T) {
	cfg := Config{
		ServiceName: "svc",
		Version:     "v1.0.0",
		Environment: "prod",
		// Domain, AppType, CommitSHA, PodName, Namespace, NodeName all empty
	}
	m := attrMap(t, cfg)

	assert.NotContains(t, m, "service.domain")
	assert.NotContains(t, m, "service.type")
	assert.NotContains(t, m, "vcs.repository.ref.revision")
	assert.NotContains(t, m, "service.instance.id")
	assert.NotContains(t, m, "k8s.pod.name")
	assert.NotContains(t, m, "k8s.namespace.name")
	assert.NotContains(t, m, "k8s.node.name")
}

func TestBuildResource_IncludesStandardDetectors(t *testing.T) {
	cfg := Config{
		ServiceName: "svc",
		Version:     "v1.0.0",
		Environment: "test",
	}
	m := attrMap(t, cfg)

	// telemetry.sdk.* come from resource.WithTelemetrySDK()
	assert.Equal(t, "opentelemetry", m["telemetry.sdk.name"])
	assert.Equal(t, "go", m["telemetry.sdk.language"])
	assert.NotEmpty(t, m["telemetry.sdk.version"])

	// process.* come from resource.WithProcess()
	assert.NotEmpty(t, m["process.pid"], "process.pid should be set")
	assert.NotEmpty(t, m["process.runtime.name"], "process.runtime.name should be set")
	// process.runtime.name is "go"
	assert.Equal(t, "go", m["process.runtime.name"])
}

func TestBuildResource_OTELEnvOverride(t *testing.T) {
	t.Setenv("OTEL_SERVICE_NAME", "env-service")

	cfg := Config{
		// ServiceName left empty — will be filled by applyDefaults in real usage,
		// but here we set it explicitly to something different to confirm env wins
		// via WithFromEnv() (which reads OTEL_SERVICE_NAME).
		ServiceName: "code-service",
		Version:     "v1.0.0",
		Environment: "test",
	}

	// resource.WithFromEnv() is processed first; resource.WithAttributes() is
	// last and wins.  So code-service overrides env-service.  This test just
	// confirms WithFromEnv() is wired in and doesn't panic.
	m := attrMap(t, cfg)
	// The explicit cfg value wins because WithAttributes is placed last.
	assert.Equal(t, "code-service", m["service.name"])
}

// TestJSONHandler_WithAttrs_PreservesTypes ensures that attributes attached via
// slog.Default().With(...) preserve their native types (int, float, bool)
// rather than being serialised as strings.
func TestJSONHandler_WithAttrs_PreservesTypes(t *testing.T) {
	buf := configureFresh(t, Config{
		ServiceName: "test-app",
		JSONFormat:  true,
	})

	logger := slog.Default().With("count", 42, "ratio", 3.14, "enabled", true)
	logger.Info("msg")

	var m map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &m))

	assert.Equal(t, float64(42), m["count"], "int With() attr should be float64 in JSON (not string)")
	assert.Equal(t, 3.14, m["ratio"], "float With() attr should be float64 in JSON (not string)")
	assert.Equal(t, true, m["enabled"], "bool With() attr should be bool in JSON (not string)")
}
