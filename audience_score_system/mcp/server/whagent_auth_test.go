package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scaffold-phase coverage only: DualAuthHTTPHandler and
// WhagentPersonMiddleware are stubs (see whagent_auth.go's package doc
// comment) until Implementation lands the routing/resolution bodies
// described there. These two tests pin today's pass-through behavior --
// wiring either stub into transport.go/main.go must change no existing
// runtime behavior -- so a regression is caught here first, not by a
// downstream integration test. Issue #2116's Testing phase replaces both
// with the full behavioral suite the issue's Testing section lists (valid
// whagent credential resolves to a Person, auto-provisioning on first
// (iss, sub) sight, every NFR4 rejection case, etc.).

func TestDualAuthHTTPHandler_ScaffoldPassesThroughUnchanged(t *testing.T) {
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusTeapot)
	})

	handler := DualAuthHTTPHandler(inner, nil, WhagentAuthConfig{}, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	handler.ServeHTTP(rec, req)

	assert.True(t, called, "scaffold stub must still invoke the wrapped handler")
	assert.Equal(t, http.StatusTeapot, rec.Code)
}

func TestWhagentPersonMiddleware_ScaffoldCallsNextUnconditionally(t *testing.T) {
	var gotMethod string
	next := func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		gotMethod = method
		return nil, nil
	}

	mw := WhagentPersonMiddleware(nil)
	_, err := mw(next)(context.Background(), "tools/call", nil)

	require.NoError(t, err)
	assert.Equal(t, "tools/call", gotMethod)
}
