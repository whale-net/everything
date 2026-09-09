package server

// TestBUILD_NoStoreOrTemporalDependency mirrors ../tools/deps_test.go for
// this package: server.go/auth.go/transport.go are the HTTP + MCP-protocol
// wiring, and must depend on nothing but the mcp SDK and grpcauth's
// context-forwarding helper -- never Postgres or Temporal directly (issue
// #2120's Implementation section).
import (
	_ "embed"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

//go:embed BUILD.bazel
var serverBUILD string

func TestBUILD_NoStoreOrTemporalDependency(t *testing.T) {
	forbidden := []string{
		"whagent_net/session",
		"temporal",
		"pgx",
		"dbtest",
		"jackc",
	}
	for _, dep := range forbidden {
		assert.NotContains(t, strings.ToLower(serverBUILD), strings.ToLower(dep),
			"whagent_net/mcp/server/BUILD.bazel must not depend on %q -- mcp is a pure facade over api's gRPC SessionService, never Postgres or Temporal directly", dep)
	}
}
