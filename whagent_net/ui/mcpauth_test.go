// Pure-Go coverage for mcpCallerResolver (issue #2245, FR9/NFR7) -- no
// Docker, runs as part of `bazel test //...`. This file covers the one
// case that needs no database round trip at all: no session cookie,
// where htmxauth.Authenticator.CurrentUser fails before ever reaching
// Postgres -- mirrors
// audience_score_system/web/auth/mcpauth_test.go's identically-scoped
// TestMCPCallerResolver_NoCookie_ReturnsFalse. The tampered-cookie,
// expired-session, and valid-session cases all need a real ui_sessions
// row (or the deliberate absence/expiry of one) -- see
// mcpauth_integration_test.go.
package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMCPCallerResolver_NoCookie_ReturnsFalse(t *testing.T) {
	// newTestOIDCAuthenticator (main_test.go) builds a real
	// *htmxauth.Authenticator in OIDC mode against a throwaway discovery
	// server, backed by a cookie session store (no DB, no oauth2Config
	// call ever made) -- CurrentUser delegates straight to
	// sessions.GetUserInfo(r), which fails on a request with no session
	// cookie before touching anything else. If mcpCallerResolver ever
	// made an IdP call of its own (forbidden by resolver.go's
	// CallerResolver contract), this test's throwaway discovery server
	// only serves the one path NewAuthenticator itself already hit
	// during construction, so any further hit would 404 -- this test
	// doesn't need to prove that separately.
	app := &App{auth: newTestOIDCAuthenticator(t), oidcIssuer: "https://keycloak.example.com/realms/whagent"}

	resolver := app.mcpCallerResolver()
	req := httptest.NewRequest(http.MethodGet, "/authorize", nil)

	identity, ok := resolver(req)
	assert.False(t, ok)
	assert.Empty(t, identity)
}
