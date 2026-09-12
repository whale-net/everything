package server

// Pure-Go coverage for RegisterRead's persona gate (registry.go): a call
// with no resolved Persona is rejected before the handler ever runs; a
// call with a resolved Persona (set via newTestServer's fixed middleware,
// standing in for PersonaMiddleware/WhagentPersonaMiddleware -- covered
// directly by auth_test.go/whagent_auth_test.go) reaches the handler
// exactly once, and every persona this milestone produces
// (PersonaSwarmOperator, PersonaAgent) is let through -- there is no
// per-tool persona allow-list in M1 (see registry.go's doc comment). No
// Docker required, runs as part of `bazel test //...`.

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type countInput struct{}

type countOutput struct {
	Calls int `json:"calls"`
}

func countingReadHandler(counter *int32) mcp.ToolHandlerFor[countInput, countOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, _ countInput) (*mcp.CallToolResult, countOutput, error) {
		n := atomic.AddInt32(counter, 1)
		return nil, countOutput{Calls: int(n)}, nil
	}
}

func TestRegisterRead_NoPersonaResolved_HandlerNotInvoked(t *testing.T) {
	var calls int32
	srv, reg := newTestServer("")
	RegisterRead(reg, &mcp.Tool{Name: "spec_read"}, countingReadHandler(&calls))
	cs := connectClient(t, srv)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "spec_read", Arguments: countInput{}})
	require.NoError(t, err, "an unauthenticated call is a tool error, not a protocol error")
	assert.True(t, res.IsError, "a call with no resolved persona must be reported as a tool error")
	assert.Contains(t, textOf(res), "unauthenticated")
	assert.Equal(t, int32(0), atomic.LoadInt32(&calls), "the handler must never run when no persona has been resolved")
}

func TestRegisterRead_PersonaResolved_HandlerInvokedExactlyOnce(t *testing.T) {
	for _, persona := range []Persona{PersonaSwarmOperator, PersonaAgent} {
		t.Run(string(persona), func(t *testing.T) {
			var calls int32
			srv, reg := newTestServer(persona)
			RegisterRead(reg, &mcp.Tool{Name: "spec_read"}, countingReadHandler(&calls))
			cs := connectClient(t, srv)

			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "spec_read", Arguments: countInput{}})
			require.NoError(t, err)
			assert.False(t, res.IsError, "unexpected error: %s", textOf(res))
			assert.Equal(t, int32(1), atomic.LoadInt32(&calls), "the handler must run exactly once for a caller with a resolved persona -- M1 has no per-tool persona allow-list")
		})
	}
}
