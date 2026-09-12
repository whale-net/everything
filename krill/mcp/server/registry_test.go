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

// ── RegisterWrite (issue #2547) ──────────────────────────────────────────────
//
// Pure-Go coverage of RegisterWrite's own gating mechanism (registry.go):
// the persona-resolved gate it shares with RegisterRead, plus the
// allow-list RegisterRead has no equivalent of. This deliberately does not
// exercise any real design-session tool (../tools/design.go) or a krill
// session id -- that behavior-level coverage, including propose_entities'
// real Agent-only restriction, is design_test.go's job (needs a real
// store.SessionStore, which has no in-memory fake -- see that file's own
// doc comment). This file proves the mechanism generic RegisterWrite
// callers rely on, with a bare counting handler.

func countingWriteHandler(counter *int32) mcp.ToolHandlerFor[countInput, countOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, _ countInput) (*mcp.CallToolResult, countOutput, error) {
		n := atomic.AddInt32(counter, 1)
		return nil, countOutput{Calls: int(n)}, nil
	}
}

func TestRegisterWrite_NoPersonaResolved_HandlerNotInvoked(t *testing.T) {
	var calls int32
	srv, reg := newTestServer("")
	RegisterWrite(reg, &mcp.Tool{Name: "design_write"}, nil, countingWriteHandler(&calls))
	cs := connectClient(t, srv)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "design_write", Arguments: countInput{}})
	require.NoError(t, err, "a rejected write is a tool error, not a protocol error")
	assert.True(t, res.IsError, "a call with no resolved persona must be reported as a tool error")
	assert.Contains(t, textOf(res), "unauthenticated")
	assert.Equal(t, int32(0), atomic.LoadInt32(&calls), "the handler must never run when no persona has been resolved")
}

func TestRegisterWrite_PersonaResolved_NoAllowList_EveryPersonaLetThrough(t *testing.T) {
	for _, persona := range []Persona{PersonaSwarmOperator, PersonaAgent} {
		t.Run(string(persona), func(t *testing.T) {
			var calls int32
			srv, reg := newTestServer(persona)
			RegisterWrite(reg, &mcp.Tool{Name: "design_write"}, nil, countingWriteHandler(&calls))
			cs := connectClient(t, srv)

			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "design_write", Arguments: countInput{}})
			require.NoError(t, err)
			assert.False(t, res.IsError, "unexpected error: %s", textOf(res))
			assert.Equal(t, int32(1), atomic.LoadInt32(&calls), "a nil/empty allow-list must let any resolved persona through, exactly like open_design_session and append_revision_event")
		})
	}
}

func TestRegisterWrite_AllowList_RejectsDisallowedPersona_AllowsListedPersona(t *testing.T) {
	t.Run("a persona not on the allow-list is rejected before the handler runs", func(t *testing.T) {
		var calls int32
		srv, reg := newTestServer(PersonaSwarmOperator)
		RegisterWrite(reg, &mcp.Tool{Name: "agent_only_write"}, []Persona{PersonaAgent}, countingWriteHandler(&calls))
		cs := connectClient(t, srv)

		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "agent_only_write", Arguments: countInput{}})
		require.NoError(t, err)
		assert.True(t, res.IsError, "PersonaSwarmOperator must be rejected by an allow-list naming only PersonaAgent -- mirrors propose_entities' FR9/FR10 restriction")
		assert.Contains(t, textOf(res), "forbidden")
		assert.Equal(t, int32(0), atomic.LoadInt32(&calls), "the handler must never run for a disallowed persona")
	})

	t.Run("a persona on the allow-list reaches the handler", func(t *testing.T) {
		var calls int32
		srv, reg := newTestServer(PersonaAgent)
		RegisterWrite(reg, &mcp.Tool{Name: "agent_only_write"}, []Persona{PersonaAgent}, countingWriteHandler(&calls))
		cs := connectClient(t, srv)

		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "agent_only_write", Arguments: countInput{}})
		require.NoError(t, err)
		assert.False(t, res.IsError, "unexpected error: %s", textOf(res))
		assert.Equal(t, int32(1), atomic.LoadInt32(&calls))
	})
}
