// Split out of handlers_consent_test.go: this is the one /mcp/consent test
// that needs newTestOIDCAuthenticator/requestWasAuthBlocked (main_test.go)
// -- handlers_consent_test.go itself is shared with the "integration"
// build (handlers_consent_integration_test.go), which does not compile
// main_test.go (see that file's doc comment), so this one case lives on
// its own here instead.
package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMCPConsent_UnauthenticatedRequest_RedirectsToSignIn(t *testing.T) {
	app := &App{auth: newTestOIDCAuthenticator(t)}
	mux := consentMux(app)

	req := httptest.NewRequest(http.MethodGet, "/mcp/consent?scope=audience_score_system", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	assert.True(t, requestWasAuthBlocked(w), "unauthenticated GET /mcp/consent must redirect to sign-in, got status %d, Location %q", w.Code, w.Header().Get("Location"))
	assert.NotContains(t, w.Body.String(), "Grant access", "an unauthenticated request must never render the consent page itself")
}
