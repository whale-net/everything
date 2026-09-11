package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// This file guards issue #2433's Scaffold section: GET /admin/grants and
// POST /admin/grants/revoke are reachable, wired through the real auth
// middleware (devModeAuthenticator, shared with handlers_session_test.go
// and handlers_grants_test.go), and compile against the real
// pages.GrantsAdmin/GrantsAdminData shapes. The Implementation phase's
// Testing section (FR14/FR15/NFR3/NFR4/NFR7) replaces these with the full
// admin-gate/live-status/revoke-scoping/audit-log coverage described in
// the issue.

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

// TestHandleGrantsAdminRevoke_RedirectsOnValidFields is a scaffold-level
// sanity check: a POST with both fields present redirects back to
// /admin/grants (the scaffold's whole revoke behavior today -- no store
// call is made yet, see handleGrantsAdminRevoke's doc comment).
func TestHandleGrantsAdminRevoke_RedirectsOnValidFields(t *testing.T) {
	app := &App{auth: devModeAuthenticator(t)}
	wrapped := app.auth.RequireAuthFunc(app.handleGrantsAdminRevoke)

	req := httptest.NewRequest(http.MethodPost, "/admin/grants/revoke", strings.NewReader("domain=audience_score_system&subject_sub=operator-a"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	wrapped(w, req)

	require.Equal(t, http.StatusSeeOther, w.Code)
	require.Equal(t, "/admin/grants", w.Header().Get("Location"))
}
