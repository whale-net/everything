package server

// Pure-Go coverage for PersonaMiddleware (auth.go): every rejection path
// (no Extra, nil TokenInfo, empty UserID) plus the success path (a fixed
// PersonaSwarmOperator placed on ctx, next invoked exactly once) and the
// coexistence contract with WhagentPersonaMiddleware (a persona already on
// ctx is never re-resolved) -- all without a real HTTP request or
// database. whagent_auth_test.go covers WhagentPersonaMiddleware itself;
// registry_test.go covers RegisterRead's own persona gate built on top of
// both.

import (
	"context"
	"testing"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPersonaMiddleware_RejectsWhenUnauthenticated(t *testing.T) {
	cases := []struct {
		name string
		req  mcp.Request
	}{
		{"no Extra at all", requestWithExtra(nil)},
		{"Extra with nil TokenInfo", requestWithExtra(&mcp.RequestExtra{})},
		{"TokenInfo with empty UserID", requestWithExtra(&mcp.RequestExtra{TokenInfo: &sdkauth.TokenInfo{UserID: ""}})},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var nextCalled bool
			next := mcp.MethodHandler(func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
				nextCalled = true
				return nil, nil
			})

			_, err := PersonaMiddleware()(next)(context.Background(), "tools/call", tc.req)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "unauthenticated")
			assert.False(t, nextCalled, "next must never run when caller identity does not resolve")
		})
	}
}

func TestPersonaMiddleware_ResolvesSwarmOperatorAndCallsNext(t *testing.T) {
	var nextCalled bool
	var gotPersona Persona
	next := mcp.MethodHandler(func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		nextCalled = true
		gotPersona = PersonaFromContext(ctx)
		return nil, nil
	})

	req := requestWithExtra(&mcp.RequestExtra{TokenInfo: &sdkauth.TokenInfo{UserID: "any-resolved-caller"}})
	_, err := PersonaMiddleware()(next)(context.Background(), "tools/call", req)
	require.NoError(t, err)
	assert.True(t, nextCalled, "next must run once caller identity resolves")
	assert.Equal(t, PersonaSwarmOperator, gotPersona, "the mcpauth door resolves every authenticated caller to PersonaSwarmOperator in M1 -- see auth.go's doc comment")
}

func TestPersonaMiddleware_DoesNotReResolveWhenPersonaAlreadySet(t *testing.T) {
	// Simulates WhagentPersonaMiddleware (mounted outside/before this one)
	// having already resolved the call to PersonaAgent: PersonaMiddleware
	// must fall through unchanged, even though this request carries no
	// TokenInfo at all (which would otherwise be a rejection).
	ctx := withPersona(context.Background(), PersonaAgent)

	var nextCalled bool
	var gotPersona Persona
	next := mcp.MethodHandler(func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		nextCalled = true
		gotPersona = PersonaFromContext(ctx)
		return nil, nil
	})

	_, err := PersonaMiddleware()(next)(ctx, "tools/call", requestWithExtra(nil))
	require.NoError(t, err)
	assert.True(t, nextCalled)
	assert.Equal(t, PersonaAgent, gotPersona, "an already-resolved persona must never be overwritten by PersonaMiddleware")
}

func TestPersonaFromContext_EmptyWhenNothingResolved(t *testing.T) {
	assert.Equal(t, Persona(""), PersonaFromContext(context.Background()))
}
