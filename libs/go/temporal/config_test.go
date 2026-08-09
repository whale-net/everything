package temporal

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestConfigFromEnv_Defaults(t *testing.T) {
	t.Setenv("TEMPORAL_HOST", "")
	t.Setenv("TEMPORAL_NAMESPACE", "")
	t.Setenv("TEMPORAL_TASK_QUEUE", "")

	cfg := ConfigFromEnv()

	assert.Equal(t, DefaultHostPort, cfg.HostPort)
	assert.Equal(t, DefaultNamespace, cfg.Namespace)
	assert.Equal(t, "", cfg.TaskQueue)
}

func TestConfigFromEnv_Overrides(t *testing.T) {
	t.Setenv("TEMPORAL_HOST", "temporal-dev.app-registry-local-dev.svc.cluster.local:7233")
	t.Setenv("TEMPORAL_NAMESPACE", "app-registry")
	t.Setenv("TEMPORAL_TASK_QUEUE", "writeback")

	cfg := ConfigFromEnv()

	assert.Equal(t, "temporal-dev.app-registry-local-dev.svc.cluster.local:7233", cfg.HostPort)
	assert.Equal(t, "app-registry", cfg.Namespace)
	assert.Equal(t, "writeback", cfg.TaskQueue)
}

func TestConfigFromEnv_PartialOverride(t *testing.T) {
	// Only namespace set — host port and task queue keep their env-derived
	// defaults (empty for TaskQueue, since it has none).
	t.Setenv("TEMPORAL_HOST", "")
	t.Setenv("TEMPORAL_NAMESPACE", "custom-ns")
	t.Setenv("TEMPORAL_TASK_QUEUE", "")

	cfg := ConfigFromEnv()

	assert.Equal(t, DefaultHostPort, cfg.HostPort)
	assert.Equal(t, "custom-ns", cfg.Namespace)
	assert.Equal(t, "", cfg.TaskQueue)
}
