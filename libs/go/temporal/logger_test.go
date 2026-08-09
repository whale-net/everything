package temporal

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/logging"
)

// TestNewLogger_RoutesThroughLibsGoLogging verifies the Temporal log.Logger
// returned by NewLogger emits through libs/go/logging rather than a
// second, independent logging stack: the bridge should carry the same
// service-identity fields (app_name, etc.) that logging.Configure sets up,
// and should encode the keyvals Temporal passes into Info/Error/etc.
func TestNewLogger_RoutesThroughLibsGoLogging(t *testing.T) {
	var buf bytes.Buffer
	logging.Configure(logging.Config{
		ServiceName: "temporal-lib-test",
		JSONFormat:  true,
		Writer:      &buf,
	})
	buf.Reset() // drop the "logging configured" startup line

	logger := NewLogger("worker-bootstrap")
	logger.Info("worker started", "task_queue", "writeback")

	var out map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &out))

	assert.Equal(t, "worker started", out["message"])
	assert.Equal(t, "temporal-lib-test", out["app_name"])
	assert.Equal(t, "worker-bootstrap", out["logger"])
	assert.Equal(t, "writeback", out["task_queue"])
}

func TestNewLogger_Levels(t *testing.T) {
	var buf bytes.Buffer
	logging.Configure(logging.Config{
		ServiceName: "temporal-lib-test",
		JSONFormat:  true,
		Writer:      &buf,
	})
	buf.Reset()

	logger := NewLogger("levels")
	logger.Warn("careful", "n", 1)

	var out map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &out))
	assert.Equal(t, "WARN", out["severity"])
}
