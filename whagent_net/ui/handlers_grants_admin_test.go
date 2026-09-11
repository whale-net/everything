package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/whagent_net/delegatedgrant"
)

// This file carries route/render-level sanity checks for GET /admin/grants
// and POST /admin/grants/revoke, wired through the real auth middleware
// (devModeAuthenticator, shared with handlers_session_test.go and
// handlers_grants_test.go -- AuthModeNone, so isGrantsAdmin's dev-mode
// bypass applies and every request here is treated as admin). The
// Testing phase (FR14/FR15/NFR3/NFR4/NFR7) adds the full admin-gate
// (adminRoleGranted, exercised directly against hand-built access
// tokens rather than through this dev-mode bypass), live-status,
// revoke-scoping, and audit-log coverage described in the issue.

// TestHandleGrantsAdmin_RendersPageStub is a scaffold-level sanity check:
// GET /admin/grants renders the page shell with today's (always-empty,
// scaffold-stub) row list. There is no admin-role gate yet -- that is
// the Implementation phase's NFR3 fresh-role check.
func TestHandleGrantsAdmin_RendersPageStub(t *testing.T) {
	app := &App{auth: devModeAuthenticator(t)}
	wrapped := app.auth.RequireAuthFunc(app.handleGrantsAdmin)

	req := httptest.NewRequest(http.MethodGet, "/admin/grants", nil)
	w := httptest.NewRecorder()
	wrapped(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "All operator grants")
}

// TestHandleGrantsAdminRevoke_RequiresDomain is a scaffold-level sanity
// check: a POST missing the domain field is rejected with 400 before any
// store call exists to make.
func TestHandleGrantsAdminRevoke_RequiresDomain(t *testing.T) {
	app := &App{auth: devModeAuthenticator(t)}
	wrapped := app.auth.RequireAuthFunc(app.handleGrantsAdminRevoke)

	req := httptest.NewRequest(http.MethodPost, "/admin/grants/revoke", strings.NewReader("subject_sub=operator-a"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	wrapped(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

// TestHandleGrantsAdminRevoke_RequiresSubjectSub is a scaffold-level
// sanity check: unlike the self-service handler (handlers_grants.go),
// this handler must be able to name a target operator at all -- a POST
// missing subject_sub is rejected with 400.
func TestHandleGrantsAdminRevoke_RequiresSubjectSub(t *testing.T) {
	app := &App{auth: devModeAuthenticator(t)}
	wrapped := app.auth.RequireAuthFunc(app.handleGrantsAdminRevoke)

	req := httptest.NewRequest(http.MethodPost, "/admin/grants/revoke", strings.NewReader("domain=audience_score_system"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	wrapped(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

// TestHandleGrantsAdminRevoke_UnconfiguredStoreIs503 proves a POST with
// both fields present, but no delegated-grant store configured on this
// deployment (initializeDelegatedGrant's degrade path), 503s rather than
// pretending to succeed.
func TestHandleGrantsAdminRevoke_UnconfiguredStoreIs503(t *testing.T) {
	app := &App{auth: devModeAuthenticator(t)}
	wrapped := app.auth.RequireAuthFunc(app.handleGrantsAdminRevoke)

	req := httptest.NewRequest(http.MethodPost, "/admin/grants/revoke", strings.NewReader("domain=audience_score_system&subject_sub=operator-a"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	wrapped(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
}

// TestHandleGrantsAdminRevoke_RedirectsOnValidFields proves a POST naming
// a real (subject, domain) pair the store holds is revoked and redirects
// back to /admin/grants.
func TestHandleGrantsAdminRevoke_RedirectsOnValidFields(t *testing.T) {
	store := grpcauth.NewFakeStore()
	subjectKey, err := grantSubjectKey(testIssuer, "operator-a")
	require.NoError(t, err)
	require.NoError(t, store.Persist(context.Background(), subjectKey, "audience_score_system", grpcauth.TokenMaterial{RefreshToken: "rt", ObtainedAt: time.Now()}))

	app := &App{
		auth:       devModeAuthenticator(t),
		oidcIssuer: testIssuer,
		grant:      delegatedgrant.Components{Store: store},
	}
	wrapped := app.auth.RequireAuthFunc(app.handleGrantsAdminRevoke)

	req := httptest.NewRequest(http.MethodPost, "/admin/grants/revoke", strings.NewReader("domain=audience_score_system&subject_sub=operator-a"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	wrapped(w, req)

	require.Equal(t, http.StatusSeeOther, w.Code)
	require.Equal(t, "/admin/grants", w.Header().Get("Location"))

	status, err := store.Status(context.Background(), subjectKey, "audience_score_system")
	require.NoError(t, err)
	require.Equal(t, grpcauth.GrantStatusRevoked, status)
}
