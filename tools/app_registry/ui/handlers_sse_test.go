package main

import (
	"context"
	"testing"
	"time"

	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/libs/go/htmxsse"
)

// TestHandlePromoStatusSSE_CookieAbsent_Returns401 tests that a connection
// without an authentication cookie receives 401 with no Location header,
// no Content-Type, zero-length body, and no /auth/login in the response.
// (FR28b property 1)
// 
// Note: This is a test scaffold. Full implementation requires mocking the
// SSE handler's message broker connection, which is deferred to Implementation.
func TestHandlePromoStatusSSE_CookieAbsent_Returns401(t *testing.T) {
	// TODO: Scaffold test. Requires:
	// - Mocking htmxsse.Handler to avoid blocking on transport attachment
	// - Calling handlePromoStatusSSE without a valid session cookie
	// - Asserting 401, no Location, no Content-Type, empty body
	t.Skip("scaffold: requires SSE handler message broker mocking in Implementation")
}

// TestHandlePromoStatusSSE_AuthModeNone_ServesSyntheticUser tests that in
// AUTH_MODE=none, the route serves the synthetic dev user's stream and
// htmxauth.GetUser resolves inside the fragment function.
// (FR28a, FR28c property 2)
func TestHandlePromoStatusSSE_AuthModeNone_ServesSyntheticUser(t *testing.T) {
	// TODO: Scaffold test. Requires:
	// - Setting up handlePromoStatusSSE with AUTH_MODE=none
	// - Verifying the synthetic dev user context is available in the fragment
	t.Skip("scaffold: requires SSE handler fragment context testing in Implementation")
}

// TestHandlePromoStatusSSE_FR27_TransientClassDoesNotEndStream tests that
// on a transient token failure (credential failed, session intact), the
// stream stays open and resumes output on the next heartbeat.
// (FR27 property 3)
func TestHandlePromoStatusSSE_FR27_TransientClassDoesNotEndStream(t *testing.T) {
	// TODO: Scaffold test. Requires:
	// - Minted session cookie with access_token JWT
	// - Token expiry just past the 2-minute refresh threshold
	// - Authenticator.GetAccessToken to fail on first call (transient)
	// - DBSessionManager.GetUserInfo to succeed (session intact)
	// - Assert handler still running
	// - Assert nothing written for that interval
	// - Assert later successful invocation resumes on same connection
	t.Skip("scaffold: test stub for FR27 transient class")
}

// TestHandlePromoStatusSSE_FR27_TerminalClassEndsStream tests that on a
// terminal failure (session gone), the handler returns and the reconnect
// that follows is refused with 401.
// (FR27 property 4)
func TestHandlePromoStatusSSE_FR27_TerminalClassEndsStream(t *testing.T) {
	// TODO: Scaffold test. Requires:
	// - Seam at app-registry level for session manager
	// - Cookie validation to fail (session absent/expired)
	// - DBSessionManager.GetUserInfo to fail (session gone)
	// - Assert handler returns once session re-check fails
	// - Assert reconnect is refused with 401
	// - Assert exactly one reconnect (not a loop)
	t.Skip("scaffold: test stub for FR27 terminal class")
}

// setupTestApp creates a minimal App for testing.
func setupTestApp(t *testing.T) (*App, func()) {
	ctx := context.Background()

	// Note: This is minimal and does NOT include a real database.
	// A full test would need PG_DATABASE_URL or a mock session manager.
	// For now, this is scaffold for the phase.
	config := &Config{
		AuthMode:       "none",
		SessionSecret:  "dev-secret-at-least-32-bytes-long",
		RegistryAPIURL: "localhost:50051",
	}

	// Create just the auth part for no-auth mode
	auth, err := htmxauth.NewAuthenticator(ctx, htmxauth.Config{
		Mode:          htmxauth.AuthModeNone,
		SessionSecret: config.SessionSecret,
		SessionName:   "app_registry_ui_session",
	})
	if err != nil {
		t.Fatalf("failed to create authenticator: %v", err)
	}

	stubAttachFunc := func(ctx context.Context) (htmxsse.Transport, error) {
		return nil, nil // no-op for testing
	}
	app := &App{
		config: config,
		auth:   auth,
		sseHub: htmxsse.NewHub(stubAttachFunc, htmxsse.Config{
			HeartbeatInterval:       time.Second,
			MaxStreamLifetime:       time.Hour,
			AdvertisedRetryInterval: 3 * time.Second,
		}),
	}

	return app, func() {
		// Cleanup
	}
}

// TestPromoStatusSSE_ShimProperties tests that the noRedirectWriter shim
// blocks redirects and preserves Flush/Unwrap.
func TestPromoStatusSSE_ShimProperties(t *testing.T) {
	// These are tested in noredirect_writer_test.go
	// This test file focuses on integration with the SSE handler.
	t.Skip("see noredirect_writer_test.go for shim unit tests")
}
