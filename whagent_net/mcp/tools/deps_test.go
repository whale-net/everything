package tools

// TestBUILD_NoStoreOrTemporalDependency pins issue #2120's Implementation
// section: "mcp never talks to Postgres or Temporal directly; the gRPC API
// is the service boundary." This package's own BUILD.bazel deps list is
// the ground truth for what actually links into the mcp binary -- embedding
// it here means a future PR that (accidentally or otherwise) adds a direct
// Postgres/Temporal/session-store dependency to this package fails this
// test, rather than silently drifting from the facade design ../../
// ARCHITECTURE.md's "Service boundary vs. package boundary" describes.
import (
	_ "embed"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

//go:embed BUILD.bazel
var toolsBUILD string

func TestBUILD_NoStoreOrTemporalDependency(t *testing.T) {
	forbidden := []string{
		"whagent_net/session",
		"temporal",
		"pgx",
		"dbtest",
		"jackc",
	}
	for _, dep := range forbidden {
		assert.NotContains(t, strings.ToLower(toolsBUILD), strings.ToLower(dep),
			"whagent_net/mcp/tools/BUILD.bazel must not depend on %q -- mcp is a pure facade over api's gRPC SessionService, never Postgres or Temporal directly", dep)
	}
}
