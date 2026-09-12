package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/sessions"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/grpcauth/grantindex"
	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/whagent_net/delegatedgrant"
)

// This file carries route/render-level sanity checks for GET /admin/grants
// and POST /admin/grants/revoke, wired through the real auth middleware
// (devModeAuthenticator, shared with handlers_session_test.go and
// handlers_grants_test.go -- AuthModeNone, so isGrantsAdmin's dev-mode
// bypass applies and every request here is treated as admin), plus the
// Testing phase (FR14/FR15/NFR3/NFR4/NFR7/FR17) coverage the issue calls
// for:
//
//   - adminRoleGranted is exercised directly against hand-built access
//     tokens (buildFakeAccessToken below), including the NFR3 stale-vs-
//     fresh comparison -- this is the one decision point both handlers
//     call, so testing it once covers both routes' gate logic.
//   - the 403/not-403 *wiring* (does handleGrantsAdmin/
//     handleGrantsAdminRevoke actually consult that decision and react to
//     it) is exercised through the real htmxauth.Authenticator in
//     AuthModeOIDC, with a session forged directly via gorilla/sessions
//     using the same auth key and session name the Authenticator's own
//     SessionManager derives (requestWithForgedSession below). This is
//     necessary, not a shortcut: devModeAuthenticator's AuthModeNone
//     unconditionally treats every request as admin (adminRoleGranted's
//     devModeAccessToken bypass), so a non-admin 403 cannot be produced
//     through it at all, and oidc.IDToken has no public constructor for
//     injecting test claims outside a full discovery+verify round trip
//     (see libs/go/htmxauth/db_session_integration_test.go's own doc
//     comment) -- but SessionManager's session Values (sub,
//     preferred_username, access_token) are plain gorilla session data
//     with no ID-token dependency, so writing them directly with a
//     matching key reproduces exactly what a real login would have
//     written, without needing a live Keycloak or a signed token.
//   - listing (buildAdminGrantRows) and revoke (revokeGrantAsAdmin) are
//     exercised directly, mirroring handlers_grants_test.go's own
//     buildGrantRows/revokeGrant tests, since app.grant.Index is a
//     concrete *grantindex.Index (no interface substitution possible at
//     the App/HTTP level without a real Postgres pool).

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

// TestHandleGrantsAdminRevoke_RequiresScope is a scaffold-level sanity
// check: a POST missing the scope field is rejected with 400 before any
// store call exists to make.
func TestHandleGrantsAdminRevoke_RequiresScope(t *testing.T) {
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

	req := httptest.NewRequest(http.MethodPost, "/admin/grants/revoke", strings.NewReader("scope=audience_score_system"))
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

	req := httptest.NewRequest(http.MethodPost, "/admin/grants/revoke", strings.NewReader("scope=audience_score_system&subject_sub=operator-a"))
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

	req := httptest.NewRequest(http.MethodPost, "/admin/grants/revoke", strings.NewReader("scope=audience_score_system&subject_sub=operator-a"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	wrapped(w, req)

	require.Equal(t, http.StatusSeeOther, w.Code)
	require.Equal(t, "/admin/grants", w.Header().Get("Location"))

	status, err := store.Status(context.Background(), subjectKey, "audience_score_system")
	require.NoError(t, err)
	require.Equal(t, grpcauth.GrantStatusRevoked, status)
}

// ── adminRoleGranted: the fresh-role gate itself (FR15/NFR3) ──────────────

// buildFakeAccessToken builds a JWT-shaped (never verified -- see
// rolesFromAccessToken's own doc comment) access token string carrying the
// given realm_access.roles, for driving adminRoleGranted/rolesFromAccessToken
// without a real Keycloak-issued token.
func buildFakeAccessToken(t *testing.T, roles []string) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, err := json.Marshal(map[string]any{
		"realm_access": map[string]any{"roles": roles},
	})
	require.NoError(t, err)
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

const grantsAdminTestRole = "grants-admin"

// fakeAccessTokenReader is an accessTokenReader test double that can also
// carry a "cached" role snapshot (cachedRoles) never read by
// adminRoleGranted -- accessTokenReader's interface has no method that
// could reach it. TestAdminRoleGranted_NFR3FreshTokenOverridesStaleCache
// uses this to model "GetUserInfo's 24h-cached Roles still say admin, but
// the freshly-refreshed access token no longer does" (NFR3) and prove the
// gate is decided by fresh.
type fakeAccessTokenReader struct {
	freshToken  string
	freshErr    error
	cachedRoles []string // never consulted by adminRoleGranted; present only to model NFR3's stale snapshot
}

func (f *fakeAccessTokenReader) GetAccessToken(_ *http.Request) (string, error) {
	return f.freshToken, f.freshErr
}

func TestAdminRoleGranted_RoleAbsent_False(t *testing.T) {
	tokens := &fakeAccessTokenReader{freshToken: buildFakeAccessToken(t, []string{"some-other-role"})}
	granted, err := adminRoleGranted(tokens, grantsAdminTestRole, httptest.NewRequest(http.MethodGet, "/", nil))
	require.NoError(t, err)
	assert.False(t, granted)
}

func TestAdminRoleGranted_RolePresent_True(t *testing.T) {
	tokens := &fakeAccessTokenReader{freshToken: buildFakeAccessToken(t, []string{"some-other-role", grantsAdminTestRole})}
	granted, err := adminRoleGranted(tokens, grantsAdminTestRole, httptest.NewRequest(http.MethodGet, "/", nil))
	require.NoError(t, err)
	assert.True(t, granted)
}

// TestAdminRoleGranted_NFR3FreshTokenOverridesStaleCache is the issue's
// NFR3 stale-role test: a "session" whose cached role snapshot still
// contains the admin role, but whose freshly-fetched access token no
// longer does, is rejected. adminRoleGranted's tokens parameter is typed
// as accessTokenReader, which exposes only GetAccessToken -- there is no
// method it could call to reach fakeAccessTokenReader.cachedRoles at all,
// which is exactly what guarantees "the handler calls the refreshing
// accessor, not GetUserInfo, for the gate."
func TestAdminRoleGranted_NFR3FreshTokenOverridesStaleCache(t *testing.T) {
	tokens := &fakeAccessTokenReader{
		freshToken:  buildFakeAccessToken(t, []string{"some-other-role"}), // admin role revoked, refreshed token reflects it
		cachedRoles: []string{grantsAdminTestRole},                        // stale 24h-cached snapshot still says admin
	}
	granted, err := adminRoleGranted(tokens, grantsAdminTestRole, httptest.NewRequest(http.MethodGet, "/", nil))
	require.NoError(t, err)
	assert.False(t, granted, "a revoked admin role must be rejected on the very next request, even if some cached snapshot still carries it")
}

// TestAdminRoleGranted_UnconfiguredAdminRole_AlwaysFalse proves there is no
// "everyone is admin" default: an empty adminRole never matches any token,
// however many roles it carries.
func TestAdminRoleGranted_UnconfiguredAdminRole_AlwaysFalse(t *testing.T) {
	tokens := &fakeAccessTokenReader{freshToken: buildFakeAccessToken(t, []string{"admin", "superuser", grantsAdminTestRole})}
	granted, err := adminRoleGranted(tokens, "", httptest.NewRequest(http.MethodGet, "/", nil))
	require.NoError(t, err)
	assert.False(t, granted)
}

// TestAdminRoleGranted_DevModeBypass_True documents/pins isGrantsAdmin's
// AuthModeNone bypass at the adminRoleGranted level: the devModeAccessToken
// sentinel is treated as admin regardless of adminRole, mirroring
// RequireAuth's own AuthModeNone dev user.
func TestAdminRoleGranted_DevModeBypass_True(t *testing.T) {
	tokens := &fakeAccessTokenReader{freshToken: devModeAccessToken}
	granted, err := adminRoleGranted(tokens, grantsAdminTestRole, httptest.NewRequest(http.MethodGet, "/", nil))
	require.NoError(t, err)
	assert.True(t, granted)
}

// ── HTTP-level gate wiring: real Authenticator, forged session (FR15) ────

const (
	grantsAdminTestSessionSecret = "grants-admin-gate-test-secret-at-least-32-bytes"
	grantsAdminTestSessionName   = "whagent_net_ui_grants_admin_gate_test_session"
)

// newGrantsAdminGateTestAuthenticator builds a real OIDC-mode
// *htmxauth.Authenticator against a throwaway discovery server (mirrors
// main_test.go's newTestOIDCAuthenticator). AuthModeNone cannot be used
// for these tests: isGrantsAdmin's dev-mode bypass unconditionally treats
// every request as admin (see this file's devModeAccessToken-based tests
// above), so a non-admin 403 can only be constructed in OIDC mode.
func newGrantsAdminGateTestAuthenticator(t *testing.T) *htmxauth.Authenticator {
	t.Helper()

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 srv.URL,
			"authorization_endpoint": srv.URL + "/auth",
			"token_endpoint":         srv.URL + "/token",
			"jwks_uri":               srv.URL + "/keys",
		})
	}))
	t.Cleanup(srv.Close)

	auth, err := htmxauth.NewAuthenticator(context.Background(), htmxauth.Config{
		Mode:             htmxauth.AuthModeOIDC,
		SessionSecret:    grantsAdminTestSessionSecret,
		SessionName:      grantsAdminTestSessionName,
		OIDCIssuer:       srv.URL,
		OIDCClientID:     "test-client",
		OIDCClientSecret: "test-client-secret",
		OIDCRedirectURL:  "http://localhost/auth/callback",
	})
	require.NoError(t, err)
	return auth
}

// requestWithForgedSession attaches a session cookie written directly via
// gorilla/sessions, using the exact same auth key (see
// libs/go/htmxauth/auth.go's NewSessionManager: []byte(secret)[:32]) and
// session name newGrantsAdminGateTestAuthenticator's Authenticator reads.
// See this file's package doc comment for why this -- not a real OIDC
// round trip -- is how these tests drive RequireAuthFunc/GetAccessToken's
// OIDC-mode session-read path.
func requestWithForgedSession(t *testing.T, req *http.Request, sub, accessToken string) *http.Request {
	t.Helper()

	store := sessions.NewCookieStore([]byte(grantsAdminTestSessionSecret)[:32])
	sess, err := store.New(req, grantsAdminTestSessionName)
	require.NoError(t, err)
	sess.Values["authenticated"] = true
	sess.Values["sub"] = sub
	sess.Values["preferred_username"] = sub
	sess.Values["access_token"] = accessToken

	w := httptest.NewRecorder()
	require.NoError(t, store.Save(req, w, sess))

	cookies := w.Result().Cookies()
	require.NotEmpty(t, cookies, "forged session must produce a Set-Cookie header")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	return req
}

// TestHandleGrantsAdmin_NonAdminForbidden_NotEmptyList is the issue's
// red/green gate test for GET /admin/grants: an operator signed in but
// without the admin role gets 403, never a silently-empty list (FR15).
func TestHandleGrantsAdmin_NonAdminForbidden_NotEmptyList(t *testing.T) {
	app := &App{auth: newGrantsAdminGateTestAuthenticator(t), adminRole: grantsAdminTestRole}
	wrapped := app.auth.RequireAuthFunc(app.handleGrantsAdmin)

	req := httptest.NewRequest(http.MethodGet, "/admin/grants", nil)
	req = requestWithForgedSession(t, req, "non-admin-operator", buildFakeAccessToken(t, []string{"some-other-role"}))
	w := httptest.NewRecorder()
	wrapped(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	body := w.Body.String()
	assert.NotContains(t, body, "All operator grants", "a 403 must never render the (even empty) admin grants page shell")
	assert.NotContains(t, body, "<table")
}

// TestHandleGrantsAdmin_AdminPassesGate proves the admin role, read fresh
// off the real Authenticator's access token, lets the request past the
// gate (200, not 403). app.grant is left zero-valued (no Postgres in this
// test), so a passing gate renders the "not configured" degrade branch --
// what matters here is that the gate did not reject the request.
func TestHandleGrantsAdmin_AdminPassesGate(t *testing.T) {
	app := &App{auth: newGrantsAdminGateTestAuthenticator(t), adminRole: grantsAdminTestRole}
	wrapped := app.auth.RequireAuthFunc(app.handleGrantsAdmin)

	req := httptest.NewRequest(http.MethodGet, "/admin/grants", nil)
	req = requestWithForgedSession(t, req, "admin-operator", buildFakeAccessToken(t, []string{grantsAdminTestRole}))
	w := httptest.NewRecorder()
	wrapped(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "All operator grants")
}

// TestHandleGrantsAdminRevoke_NonAdminForbidden is the issue's red/green
// gate test for POST /admin/grants/revoke: a non-admin gets 403 before any
// store call is even attempted.
func TestHandleGrantsAdminRevoke_NonAdminForbidden(t *testing.T) {
	store := grpcauth.NewFakeStore()
	app := &App{
		auth:       newGrantsAdminGateTestAuthenticator(t),
		adminRole:  grantsAdminTestRole,
		oidcIssuer: testIssuer,
		grant:      delegatedgrant.Components{Store: store},
	}
	wrapped := app.auth.RequireAuthFunc(app.handleGrantsAdminRevoke)

	req := httptest.NewRequest(http.MethodPost, "/admin/grants/revoke", strings.NewReader("scope=audience_score_system&subject_sub=operator-a"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = requestWithForgedSession(t, req, "non-admin-operator", buildFakeAccessToken(t, []string{"some-other-role"}))
	w := httptest.NewRecorder()
	wrapped(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	assert.Zero(t, store.Calls().Revoke, "a rejected admin request must never reach the store")
}

// TestHandleGrantsAdminRevoke_AdminPassesGate proves the admin role lets a
// revoke request past the gate (past 403, into the handler's own
// unconfigured-store 503 branch -- app.grant.Store is left nil here so a
// passing gate is unambiguous: any status other than 403 proves it).
func TestHandleGrantsAdminRevoke_AdminPassesGate(t *testing.T) {
	app := &App{auth: newGrantsAdminGateTestAuthenticator(t), adminRole: grantsAdminTestRole}
	wrapped := app.auth.RequireAuthFunc(app.handleGrantsAdminRevoke)

	req := httptest.NewRequest(http.MethodPost, "/admin/grants/revoke", strings.NewReader("scope=audience_score_system&subject_sub=operator-a"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = requestWithForgedSession(t, req, "admin-operator", buildFakeAccessToken(t, []string{grantsAdminTestRole}))
	w := httptest.NewRecorder()
	wrapped(w, req)

	assert.NotEqual(t, http.StatusForbidden, w.Code)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

// ── buildAdminGrantRows: multi-operator listing, live status (FR14) ──────

// fakeGrantIndexAll is a grantIndexAllLister backed by an in-memory slice,
// for driving buildAdminGrantRows without a real Postgres pool.
type fakeGrantIndexAll struct {
	entries []grantindex.Entry
}

func (f *fakeGrantIndexAll) ListAll(_ context.Context) ([]grantindex.Entry, error) {
	return f.entries, nil
}

// TestBuildAdminGrantRows_MultipleOperators proves an admin's listing spans
// every operator's entries (unlike buildGrantRows' own-subject scoping),
// each carrying its own captured preferred_username.
func TestBuildAdminGrantRows_MultipleOperators(t *testing.T) {
	store := grpcauth.NewFakeStore()
	ctx := context.Background()

	aKey, err := grantSubjectKey(testIssuer, "operator-a")
	require.NoError(t, err)
	bKey, err := grantSubjectKey(testIssuer, "operator-b")
	require.NoError(t, err)
	require.NoError(t, store.Persist(ctx, aKey, "audience_score_system", grpcauth.TokenMaterial{RefreshToken: "rt", ObtainedAt: time.Now()}))
	require.NoError(t, store.Persist(ctx, bKey, "manmanv2", grpcauth.TokenMaterial{RefreshToken: "rt", ObtainedAt: time.Now()}))

	index := &fakeGrantIndexAll{entries: []grantindex.Entry{
		{SubjectIss: testIssuer, SubjectSub: "operator-a", Domain: "audience_score_system", PreferredUsername: "alice", GrantedAt: time.Now()},
		{SubjectIss: testIssuer, SubjectSub: "operator-b", Domain: "manmanv2", PreferredUsername: "bob", GrantedAt: time.Now()},
	}}

	rows, err := buildAdminGrantRows(ctx, index, store, testIssuer, discardLogger())
	require.NoError(t, err)
	require.Len(t, rows, 2)

	byUsername := map[string]string{}
	for _, row := range rows {
		byUsername[row.OperatorLabel] = row.Scope
	}
	assert.Equal(t, "audience_score_system", byUsername["alice"])
	assert.Equal(t, "manmanv2", byUsername["bob"])
}

// TestBuildAdminGrantRows_StatusIsLiveNeverFromIndex mirrors
// handlers_grants_test.go's TestBuildGrantRows_StatusIsLiveNeverFromIndex
// for the admin listing: a status change in the store with no index write
// of any kind is reflected on the very next call.
func TestBuildAdminGrantRows_StatusIsLiveNeverFromIndex(t *testing.T) {
	store := grpcauth.NewFakeStore()
	ctx := context.Background()

	subjectKey, err := grantSubjectKey(testIssuer, "operator-a")
	require.NoError(t, err)
	require.NoError(t, store.Persist(ctx, subjectKey, "manmanv2", grpcauth.TokenMaterial{RefreshToken: "rt", ObtainedAt: time.Now()}))

	index := &fakeGrantIndexAll{entries: []grantindex.Entry{
		{SubjectIss: testIssuer, SubjectSub: "operator-a", Domain: "manmanv2", PreferredUsername: "alice", GrantedAt: time.Now()},
	}}

	rows, err := buildAdminGrantRows(ctx, index, store, testIssuer, discardLogger())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "active", rows[0].Status)

	require.NoError(t, store.MarkNeedsReauth(ctx, subjectKey, "manmanv2"))

	rows, err = buildAdminGrantRows(ctx, index, store, testIssuer, discardLogger())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "needs_reauth", rows[0].Status)
}

// ── revokeGrantAsAdmin: FR17 scoping, NFR7, NFR4 audit log ────────────────

// TestRevokeGrantAsAdmin_FR17ScopedToExactlyOnePair is the plan's own FR17
// example, for the admin path: an admin revokes operator A's
// audience_score_system grant; A's manmanv2 grant and operator B's
// audience_score_system grant must both remain active.
func TestRevokeGrantAsAdmin_FR17ScopedToExactlyOnePair(t *testing.T) {
	store := grpcauth.NewFakeStore()
	ctx := context.Background()

	aKey, err := grantSubjectKey(testIssuer, "operator-a")
	require.NoError(t, err)
	bKey, err := grantSubjectKey(testIssuer, "operator-b")
	require.NoError(t, err)
	require.NoError(t, store.Persist(ctx, aKey, "audience_score_system", grpcauth.TokenMaterial{RefreshToken: "rt", ObtainedAt: time.Now()}))
	require.NoError(t, store.Persist(ctx, aKey, "manmanv2", grpcauth.TokenMaterial{RefreshToken: "rt", ObtainedAt: time.Now()}))
	require.NoError(t, store.Persist(ctx, bKey, "audience_score_system", grpcauth.TokenMaterial{RefreshToken: "rt", ObtainedAt: time.Now()}))

	require.NoError(t, revokeGrantAsAdmin(ctx, store, testIssuer, "admin-operator", "operator-a", "audience_score_system", discardLogger()))

	aASSStatus, err := store.Status(ctx, aKey, "audience_score_system")
	require.NoError(t, err)
	assert.Equal(t, grpcauth.GrantStatusRevoked, aASSStatus)

	aManmanStatus, err := store.Status(ctx, aKey, "manmanv2")
	require.NoError(t, err)
	assert.Equal(t, grpcauth.GrantStatusActive, aManmanStatus, "revoking A's audience_score_system grant must not affect A's manmanv2 grant")

	bASSStatus, err := store.Status(ctx, bKey, "audience_score_system")
	require.NoError(t, err)
	assert.Equal(t, grpcauth.GrantStatusActive, bASSStatus, "revoking A's grant must not affect B's grant for the same domain")
}

// TestRevokeGrantAsAdmin_NFR7EffectiveWithoutRestart proves an admin revoke
// is effective on the very next credential read in the same process: no
// cache exists (FR8), so grpcauth.Store.TokenMaterial fails immediately
// after revokeGrantAsAdmin returns.
func TestRevokeGrantAsAdmin_NFR7EffectiveWithoutRestart(t *testing.T) {
	store := grpcauth.NewFakeStore()
	ctx := context.Background()

	subjectKey, err := grantSubjectKey(testIssuer, "operator-a")
	require.NoError(t, err)
	require.NoError(t, store.Persist(ctx, subjectKey, "audience_score_system", grpcauth.TokenMaterial{RefreshToken: "rt", ObtainedAt: time.Now()}))

	_, err = store.TokenMaterial(ctx, subjectKey, "audience_score_system")
	require.NoError(t, err)

	require.NoError(t, revokeGrantAsAdmin(ctx, store, testIssuer, "admin-operator", "operator-a", "audience_score_system", discardLogger()))

	_, err = store.TokenMaterial(ctx, subjectKey, "audience_score_system")
	require.ErrorIs(t, err, grpcauth.ErrGrantRevoked)
}

// TestRevokeGrantAsAdmin_LogsExactlyOneINFORecordWithAdminAndTarget is
// NFR4's audit-log guard for the admin path: exactly one INFO record,
// naming both the admin identity (revoker) and the target operator's
// subject, distinctly -- this is the case handlers_grants.go's own
// self-service revokeGrant can never exercise (revoker == subject there
// always).
func TestRevokeGrantAsAdmin_LogsExactlyOneINFORecordWithAdminAndTarget(t *testing.T) {
	store := grpcauth.NewFakeStore()
	ctx := context.Background()

	subjectKey, err := grantSubjectKey(testIssuer, "operator-a")
	require.NoError(t, err)
	require.NoError(t, store.Persist(ctx, subjectKey, "audience_score_system", grpcauth.TokenMaterial{RefreshToken: "rt", ObtainedAt: time.Now()}))

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	require.NoError(t, revokeGrantAsAdmin(ctx, store, testIssuer, "admin-operator", "operator-a", "audience_score_system", logger))

	output := buf.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	require.Len(t, lines, 1, "expected exactly one log record, got: %q", output)

	assert.Contains(t, output, "level=INFO")
	assert.Contains(t, output, "revoked_by_sub=admin-operator", "audit log must name the admin who revoked")
	assert.Contains(t, output, "subject_sub=operator-a", "audit log must name the target operator")
	assert.Contains(t, output, "audience_score_system", "audit log must name the domain")
	assert.NotContains(t, output, "level=WARN")
	assert.NotContains(t, output, "level=ERROR")
}
