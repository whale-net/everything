package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// This file guards issue #2432's Scaffold section: GET /grants and POST
// /grants/revoke are reachable, wired through the real auth middleware
// (devModeAuthenticator, shared with handlers_session_test.go), and
// compile against the real pages.Grants/GrantsData shapes. The
// Implementation phase's Testing section (FR16/FR17/NFR4/NFR7) replaces
// these with the full scoped-listing/live-status/revoke-scoping/audit-log
// coverage described in the issue.

// TestHandleGrants_RendersPageStub is a scaffold-level sanity check: the
// signed-in operator's own GET /grants renders the page shell with
// today's (always-empty, scaffold-stub) row list.
func TestHandleGrants_RendersPageStub(t *testing.T) {
	app := &App{auth: devModeAuthenticator(t)}
	wrapped := app.auth.RequireAuthFunc(app.handleGrants)

	req := httptest.NewRequest(http.MethodGet, "/grants", nil)
	w := httptest.NewRecorder()
	wrapped(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "My grants")
}

// TestHandleGrantsRevoke_RequiresDomain is a scaffold-level sanity check:
// a POST with no domain field is rejected with 400 before any store call
// exists to make (the Implementation phase wires the real
// app.grant.Store.Revoke call and its FR17 scoping/NFR4 audit-log
// coverage).
func TestHandleGrantsRevoke_RequiresDomain(t *testing.T) {
	app := &App{auth: devModeAuthenticator(t)}
	wrapped := app.auth.RequireAuthFunc(app.handleGrantsRevoke)

	req := httptest.NewRequest(http.MethodPost, "/grants/revoke", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	wrapped(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

// TestHandleGrantsRevoke_RedirectsOnValidDomain is a scaffold-level sanity
// check: a POST with a domain field redirects back to /grants (the
// scaffold's whole revoke behavior today -- no store call is made yet,
// see handleGrantsRevoke's doc comment).
func TestHandleGrantsRevoke_RedirectsOnValidDomain(t *testing.T) {
	app := &App{auth: devModeAuthenticator(t)}
	wrapped := app.auth.RequireAuthFunc(app.handleGrantsRevoke)

	req := httptest.NewRequest(http.MethodPost, "/grants/revoke", strings.NewReader("domain=audience_score_system"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	wrapped(w, req)

	require.Equal(t, http.StatusSeeOther, w.Code)
	require.Equal(t, "/grants", w.Header().Get("Location"))
}
