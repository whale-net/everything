//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //leaflab/...` (which runs on Docker-less machines too) never compiles or
// runs it. See BUILD.bazel's ui_integration_test target and
// libs/go/dbtest/README.md for how to run it explicitly.
//
// Covers this issue's (#1329) Testing section bullets that need a real
// Postgres:
//   - "Session survives process restart: a session row written, the store
//     re-opened, the session still resolves" (pattern:
//     libs/go/htmxauth/db_session_integration_test.go)
//   - "Sign-out deletes the session row and a subsequent request is
//     unauthenticated"
//
// Schema here is a self-contained copy of the column set migration
// 014_htmxauth_sessions.up.sql creates (see
// leaflab/migrate/migrations/014_htmxauth_sessions.up.sql) -- dbtest's own
// README asks integration tests to keep schema self-contained rather than
// importing another package's migrations.
package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/htmxauth"
)

const uiSessionsSchema = `
	CREATE TABLE ui_sessions (
		session_id       TEXT        PRIMARY KEY,
		user_info        JSONB       NOT NULL DEFAULT '{}',
		access_token     TEXT        NOT NULL,
		refresh_token    TEXT        NOT NULL,
		token_expires_at TIMESTAMPTZ NOT NULL,
		created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		expires_at       TIMESTAMPTZ NOT NULL
	);
`

const sessionTestSecret = "test-secret-that-is-at-least-32-bytes-long"

// insertSessionRow inserts a session row using exactly the columns and
// shape DBSessionManager.SetUserInfo's own INSERT statement writes (see
// libs/go/htmxauth/db_session.go) -- oidc.IDToken has no public
// constructor for injecting test claims outside a full discovery+verify
// round trip, so this stands in for a real OIDC login.
func insertSessionRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sub, preferredUsername string) string {
	t.Helper()

	userInfo := map[string]any{
		"sub":                sub,
		"preferred_username": preferredUsername,
		"name":               preferredUsername,
		"email":              sub + "@example.com",
		"roles":              []string{"viewer"},
	}
	userInfoJSON, err := json.Marshal(userInfo)
	require.NoError(t, err)

	sessionID := sub + "-session-id"
	_, err = pool.Exec(ctx, `
		INSERT INTO ui_sessions
			(session_id, user_info, access_token, refresh_token, token_expires_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, sessionID, userInfoJSON, "access-tok", "enc-refresh-tok", time.Now().Add(time.Hour), time.Now().Add(24*time.Hour))
	require.NoError(t, err)

	return sessionID
}

// TestSessionSurvivesStoreReopen_ProcessRestart proves a session written
// through one DBSessionManager still resolves through a second
// DBSessionManager opened against a fresh connection pool to the same
// database -- standing in for a BFF pod restart (FR13): only the database
// row persists across a real restart, not any in-process state, so
// re-deriving the store from a new pool is the faithful simulation.
func TestSessionSurvivesStoreReopen_ProcessRestart(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: uiSessionsSchema})

	store1, err := htmxauth.NewDBSessionManager(ctx, db.Pool, sessionTestSecret, "leaflab_ui_session")
	require.NoError(t, err)

	sessionID := insertSessionRow(t, ctx, db.Pool, "u-restart", "restart-user")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "leaflab_ui_session", Value: sessionID})

	got1, err := store1.GetUserInfo(req)
	require.NoError(t, err, "the session must resolve immediately after being written")
	assert.Equal(t, "u-restart", got1.Sub)

	// "process restart": a brand new pool to the same database, and a
	// brand new DBSessionManager on top of it -- nothing from store1 is
	// reused.
	pool2, err := pgxpool.New(ctx, db.ConnString)
	require.NoError(t, err)
	defer pool2.Close()

	store2, err := htmxauth.NewDBSessionManager(ctx, pool2, sessionTestSecret, "leaflab_ui_session")
	require.NoError(t, err)

	got2, err := store2.GetUserInfo(req)
	require.NoError(t, err, "the session must still resolve through a freshly reopened store (BFF pod restart)")
	assert.Equal(t, "u-restart", got2.Sub)
	assert.Equal(t, "restart-user", got2.PreferredUsername)

	// End-to-end: route an actual request through the app's real handler
	// chain (setupRoutes -> RequireAuthFunc -> handleHome) using the
	// post-restart store, proving "authenticated request reaches the home
	// page" survives the restart, not just the raw session lookup.
	oidcSrv := newFakeOIDCServer(t)
	auth2, err := htmxauth.NewAuthenticatorWithDB(ctx, oidcTestConfig(oidcSrv), store2)
	require.NoError(t, err)

	fake := &fakeLeafLabClient{healthResp: &pb.GetHealthResponse{Status: pb.HealthStatus_HEALTH_UP}}
	app := &App{auth: auth2, api: &APIClient{LeafLab: fake}}
	mux := http.NewServeMux()
	app.setupRoutes(mux)

	homeReq := httptest.NewRequest(http.MethodGet, "/", nil)
	homeReq.AddCookie(&http.Cookie{Name: "leaflab_ui_session", Value: sessionID})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, homeReq)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	assert.Contains(t, w.Body.String(), "Welcome to LeafLab")
	assert.Contains(t, w.Body.String(), "restart-user")
}

// TestExpiredSession_FullRedirectChainReachesLoginFormHTML proves a
// genuinely expired ui_sessions row (expires_at in the past, matching what
// DBSessionManager's own hourly cleanup would eventually delete anyway --
// see db_session.go's cleanupLoop) is treated as unauthenticated: the
// session cookie itself is well-formed, but GetUserInfo's query
// (`expires_at > NOW()`) simply finds no row, and the request follows the
// same real redirect chain as a first-time visitor -- leaflab-ui's own
// /auth/login hop, then on to the IdP's hosted login page (faked by
// newFakeOIDCServer) -- landing on real HTML, never a blank screen, a raw
// error, or a JSON status body (FR13).
func TestExpiredSession_FullRedirectChainReachesLoginFormHTML(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: uiSessionsSchema})

	store, err := htmxauth.NewDBSessionManager(ctx, db.Pool, sessionTestSecret, "leaflab_ui_session")
	require.NoError(t, err)

	sessionID := "u-expired-session-id"
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO ui_sessions
			(session_id, user_info, access_token, refresh_token, token_expires_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, sessionID, `{"sub":"u-expired"}`, "access-tok", "enc-refresh-tok",
		time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour)) // expires_at is in the past
	require.NoError(t, err)

	oidcSrv := newFakeOIDCServer(t)
	auth, err := htmxauth.NewAuthenticatorWithDB(ctx, oidcTestConfig(oidcSrv), store)
	require.NoError(t, err)

	app := &App{auth: auth}
	mux := http.NewServeMux()
	app.setupRoutes(mux)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	client := &http.Client{Jar: jar}

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/", nil)
	require.NoError(t, err)
	req.AddCookie(&http.Cookie{Name: "leaflab_ui_session", Value: sessionID})

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)
	assert.NotEmpty(t, body, "expected a non-empty body -- never a blank screen (FR13)")
	assert.Contains(t, resp.Header.Get("Content-Type"), "text/html", "never a JSON status body (FR13)")
	assert.Contains(t, string(body), "<form")
}

// TestSignOut_DeletesSessionRow_SubsequentRequestUnauthenticated proves
// HandleLogout (routed through leaflab-ui's own /auth/logout, exactly as a
// browser reaches it) deletes the ui_sessions row and clears the cookie,
// so a subsequent request from the same browser (same cookie jar) is
// unauthenticated.
func TestSignOut_DeletesSessionRow_SubsequentRequestUnauthenticated(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: uiSessionsSchema})

	store, err := htmxauth.NewDBSessionManager(ctx, db.Pool, sessionTestSecret, "leaflab_ui_session")
	require.NoError(t, err)

	sessionID := insertSessionRow(t, ctx, db.Pool, "u-signout", "signout-user")

	oidcSrv := newFakeOIDCServer(t)
	auth, err := htmxauth.NewAuthenticatorWithDB(ctx, oidcTestConfig(oidcSrv), store)
	require.NoError(t, err)

	fake := &fakeLeafLabClient{healthResp: &pb.GetHealthResponse{Status: pb.HealthStatus_HEALTH_UP}}
	app := &App{auth: auth, api: &APIClient{LeafLab: fake}}
	mux := http.NewServeMux()
	app.setupRoutes(mux)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	tsURL, err := url.Parse(ts.URL)
	require.NoError(t, err)
	jar.SetCookies(tsURL, []*http.Cookie{{Name: "leaflab_ui_session", Value: sessionID}})

	// Sanity: authenticated before sign-out.
	preResp, err := client.Get(ts.URL + "/")
	require.NoError(t, err)
	preBody, err := io.ReadAll(preResp.Body)
	require.NoError(t, err)
	preResp.Body.Close()
	require.Equal(t, http.StatusOK, preResp.StatusCode, "body: %s", preBody)
	assert.Contains(t, string(preBody), "Welcome to LeafLab")

	// Sign out.
	logoutResp, err := client.Get(ts.URL + "/auth/logout")
	require.NoError(t, err)
	logoutResp.Body.Close()
	assert.Equal(t, http.StatusSeeOther, logoutResp.StatusCode)
	assert.Equal(t, "/", logoutResp.Header.Get("Location"), "no end_session_endpoint advertised -- local-only logout")

	// The DB row is gone.
	var count int
	err = db.Pool.QueryRow(ctx, `SELECT count(*) FROM ui_sessions WHERE session_id = $1`, sessionID).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "sign-out must delete the ui_sessions row, not just clear the cookie")

	// A subsequent request from the same browser (cookie jar) -- the
	// cleared cookie means no valid session cookie is sent -- is
	// unauthenticated.
	subsequentResp, err := client.Get(ts.URL + "/")
	require.NoError(t, err)
	subsequentResp.Body.Close()
	assert.Equal(t, http.StatusSeeOther, subsequentResp.StatusCode)
	assert.Equal(t, "/auth/login?next=/", subsequentResp.Header.Get("Location"))
}
