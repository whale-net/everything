// Coverage for authorizeConsentGate's needs_reauth handling (issue #2431's
// Testing section, "ui" bullet): "a needs_reauth grant for domain D routes
// the operator to consent for D on next access; a healthy grant for D' is
// untouched by that re-consent." authorizeConsentGate (handlers_consent.go)
// had no direct test at all before this file -- the comment pointing at
// "mcpauth_test.go's own /authorize concern" (handlers_consent_test.go's
// newConsentTestApp doc comment) describes an unrelated bootstrap flow, not
// this gate -- so this file is new coverage, not a duplicate of anything
// already exercised.
//
// Reuses newConsentTestApp/newFakeGrantIdP (handlers_consent_test.go, same
// package, no build tag) rather than hand-rolling a second *App
// constructor: authorizeConsentGate only reads app.defaultDomain,
// app.grant.Source/Store, and app.auth, all of which that helper already
// wires with AuthModeNone's fixed dev user (devUserSub).
package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/grpcauth"
)

// gateProbe wraps a handler with app.authorizeConsentGate and drives one
// GET /authorize request through it, reporting whether the wrapped
// "downstream" handler (standing in for mcpauth.Provider's own /authorize
// handler) was ever reached, and where the response redirected to when it
// was not.
func gateProbe(t *testing.T, app *App) (reached bool, redirectLocation string, status int) {
	t.Helper()

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})
	handler := app.authorizeConsentGate(next)

	req := httptest.NewRequest(http.MethodGet, "/authorize?client_id=mcp&response_type=code", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	return reached, rec.Header().Get("Location"), rec.Code
}

// TestAuthorizeConsentGate_NoGrantForDefaultDomain_RedirectsToConsent is the
// gate's base case (issue #2428, FR2/FR9): no delegated grant at all for
// app.defaultDomain sends the operator to the standalone consent route
// instead of ever reaching mcpauth's /authorize handler.
func TestAuthorizeConsentGate_NoGrantForDefaultDomain_RedirectsToConsent(t *testing.T) {
	fake := newFakeGrantIdP(t)
	store := grpcauth.NewFakeStore()
	app := newConsentTestApp(t, fake, store)
	app.defaultDomain = "manmanv2"

	reached, location, status := gateProbe(t, app)

	assert.False(t, reached, "the downstream /authorize handler must never be reached without an active grant for the default domain")
	assert.Equal(t, http.StatusFound, status)
	assert.Contains(t, location, "/mcp/consent")
	assert.Contains(t, location, "domain=manmanv2")
}

// TestAuthorizeConsentGate_ActiveGrantForDefaultDomain_FallsThrough proves
// the healthy-grant case never intercepts -- the control this task's
// needs_reauth case (below) is contrasted against.
func TestAuthorizeConsentGate_ActiveGrantForDefaultDomain_FallsThrough(t *testing.T) {
	fake := newFakeGrantIdP(t)
	store := grpcauth.NewFakeStore()
	app := newConsentTestApp(t, fake, store)
	app.defaultDomain = "manmanv2"

	require.NoError(t, store.Persist(context.Background(), devUserSub, "manmanv2", grpcauth.TokenMaterial{
		RefreshToken: "rt", ObtainedAt: time.Now(),
	}))

	reached, _, status := gateProbe(t, app)

	assert.True(t, reached, "an active grant for the default domain must let the request through unchanged")
	assert.Equal(t, http.StatusOK, status)
}

// TestAuthorizeConsentGate_NeedsReauthForDefaultDomain_RoutesBackToConsent
// is this task's (issue #2431) headline ui scenario: a grant that was
// previously active but is now needs_reauth (grpcauth.Store.MarkNeedsReauth
// -- e.g. because mcp's dispatch-time acquireGrantToken just observed
// ErrGrantNeedsReauth from it) is treated identically to "not consented" by
// this same gate, on the operator's very next /authorize attempt -- no
// second, needs_reauth-specific code path (FR18's "the existing GET
// /mcp/consent?domain=<d> route ... handles it without a second code
// path").
func TestAuthorizeConsentGate_NeedsReauthForDefaultDomain_RoutesBackToConsent(t *testing.T) {
	fake := newFakeGrantIdP(t)
	store := grpcauth.NewFakeStore()
	app := newConsentTestApp(t, fake, store)
	app.defaultDomain = "manmanv2"

	ctx := context.Background()
	require.NoError(t, store.Persist(ctx, devUserSub, "manmanv2", grpcauth.TokenMaterial{
		RefreshToken: "rt", ObtainedAt: time.Now(),
	}))
	require.NoError(t, store.MarkNeedsReauth(ctx, devUserSub, "manmanv2"))

	status, err := store.Status(ctx, devUserSub, "manmanv2")
	require.NoError(t, err)
	require.Equal(t, grpcauth.GrantStatusNeedsReauth, status, "test precondition: the grant must actually be needs_reauth before probing the gate")

	reached, location, code := gateProbe(t, app)

	assert.False(t, reached, "a needs_reauth grant must not be treated as sufficient to reach /authorize")
	assert.Equal(t, http.StatusFound, code)
	assert.Contains(t, location, "/mcp/consent")
	assert.Contains(t, location, "domain=manmanv2")
}

// TestAuthorizeConsentGate_NeedsReauthOnDefaultDomain_OtherDomainGrantUntouched
// proves the "for that domain specifically -- not a global re-consent"
// half of FR18: an operator with a healthy grant for a *different* domain
// is still routed to consent for the (needs_reauth) default domain, never
// substituted with or redirected toward the other domain's own grant --
// and that other domain's grant is left exactly as it was, never
// invalidated just because the default domain's grant needed re-consent.
func TestAuthorizeConsentGate_NeedsReauthOnDefaultDomain_OtherDomainGrantUntouched(t *testing.T) {
	fake := newFakeGrantIdP(t)
	store := grpcauth.NewFakeStore()
	app := newConsentTestApp(t, fake, store)
	app.defaultDomain = "manmanv2"

	ctx := context.Background()
	require.NoError(t, store.Persist(ctx, devUserSub, "manmanv2", grpcauth.TokenMaterial{
		RefreshToken: "rt-manmanv2", ObtainedAt: time.Now(),
	}))
	require.NoError(t, store.MarkNeedsReauth(ctx, devUserSub, "manmanv2"))

	require.NoError(t, store.Persist(ctx, devUserSub, "audience_score_system", grpcauth.TokenMaterial{
		RefreshToken: "rt-audience-score-system", ObtainedAt: time.Now(),
	}))

	reached, location, code := gateProbe(t, app)

	assert.False(t, reached)
	assert.Equal(t, http.StatusFound, code)
	assert.Contains(t, location, "domain=manmanv2", "the gate must route to consent for the default domain, never a domain that already has an active grant")
	assert.NotContains(t, location, "audience_score_system")

	otherStatus, err := store.Status(ctx, devUserSub, "audience_score_system")
	require.NoError(t, err)
	assert.Equal(t, grpcauth.GrantStatusActive, otherStatus, "the other domain's own active grant must be completely untouched by the default domain needing re-consent")
}
