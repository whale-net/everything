// Pure-Go coverage for MCPCallerResolver (issue #1646, FR12) -- no Docker,
// runs as part of `bazel test //...`. This file covers the one case that
// needs no database round trip: no session cookie at all, where
// SessionManager.PersonID fails before ever reaching Postgres (mirrors
// auth_test.go's TestRequireSignedIn_NoCookie_RedirectsToLogin). The
// valid-session, tampered-cookie, and expired-session cases all need a real
// web_session row (or the deliberate absence/expiry of one) -- see
// mcpauth_integration_test.go.
package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMCPCallerResolver_NoCookie_ReturnsFalse(t *testing.T) {
	sessions := NewSessionManager(nil, testCookieName, "session-secret", testEncKey())
	// NewForTests wires no oauth2Config/verifier at all (both left nil) --
	// this is deliberate the same way auth_test.go's other pure-Go tests
	// rely on it: if MCPCallerResolver ever touched either field (an IdP
	// call it must never make, per resolver.go's CallerResolver doc), this
	// test would nil-pointer-panic instead of merely returning ok=false,
	// proving the "no network call of any kind" requirement by
	// construction rather than by assertion.
	a := NewForTests(&fakePersonStore{}, sessions)

	resolver := a.MCPCallerResolver()
	req := httptest.NewRequest(http.MethodGet, "/authorize", nil)

	identity, ok := resolver(req)
	assert.False(t, ok)
	assert.Empty(t, identity)
}
