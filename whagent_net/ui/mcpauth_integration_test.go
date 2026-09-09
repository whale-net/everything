//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. See the go_test target's gotags in BUILD.bazel and
// //libs/go/dbtest's README for how to run it.
//
// It covers the three mcpCallerResolver (issue #2245, FR9/NFR7) cases
// that need a real ui_sessions row -- a live session resolves to the
// encoded (iss, sub) pair, a tampered (unrecognized) session id fails,
// and an expired session fails -- mirroring
// audience_score_system/web/auth/mcpauth_integration_test.go's
// TestMCPCallerResolver_ValidSession_ReturnsPersonIDString /
// TestMCPCallerResolver_TamperedCookie_ReturnsFalse /
// TestMCPCallerResolver_ExpiredSession_ReturnsFalse -- and the full
// authorization-code + PKCE bootstrap round trip against the actually-
// mounted mcpauth.Provider (discovery -> registration -> /authorize ->
// /token -> a credential whose stored identity is the encoded pair),
// mirroring audience_score_system/mcp/server/
// oauth_bootstrap_integration_test.go reduced to the single-binary
// (authorization-server-only) case this task builds -- issue #2245's
// mcp-side verification half is a separate, dependent task, so this file
// stops at "the minted credential's stored identity is correct", never
// exercising an MCP tool call.
//
// ui_sessions here is a self-contained schema literal, not whagent-net's
// own migration -- because whagent-net doesn't have one yet (see
// Scope note: whagent_net has no ui_sessions migration...). This mirrors
// libs/go/htmxauth's own db_session_integration_test.go, whose doc
// comment explains why dbtest's README asks integration tests to keep
// schema self-contained: no dependency on migrations from other packages.
// mcp_credential/mcp_oauth_client/mcp_auth_code, by contrast, ARE
// whagent-net's own migration (004_mcpauth_credential, this task's own
// Scaffold work) and are applied from the real embedded schema below, not
// a hand-copied literal.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/libs/go/mcpauth"
	"github.com/whale-net/everything/libs/go/migrate"
	"github.com/whale-net/everything/whagent_net/mcpidentity"
	"github.com/whale-net/everything/whagent_net/migrate/schema"
)

// uiSessionsSchema is byte-for-byte the same shape
// libs/go/htmxauth/db_session_integration_test.go's own literal uses --
// the exact column set db_session.go's SetUserInfo INSERT / GetUserInfo
// SELECT rely on. See this file's package doc for why it's self-contained
// rather than sourced from a whagent-net migration.
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

// mcpAuthTestStack bundles a fully wired *App (real DB-backed
// htmxauth.Authenticator + a real mcpauth.Provider constructed exactly
// like setupMCPAuth/NewApp do in production) mounted on a real
// httptest.Server via app.setupRoutes -- so tests drive actual HTTP
// requests against the actual route table, not a hand-built subset of it.
type mcpAuthTestStack struct {
	app        *App
	ts         *httptest.Server
	pool       *pgxpool.Pool
	cookieName string
	oidcIssuer string
}

// newMCPAuthTestStack provisions a throwaway Postgres (dbtest), applies
// whagent-net's real embedded migrations (schema.Migrations -- including
// 004_mcpauth_credential, this task's own mcp_credential/
// mcp_oauth_client/mcp_auth_code tables) plus the self-contained
// ui_sessions literal above, then builds an *App exactly the way NewApp
// does: a real *htmxauth.Authenticator in OIDC mode (against a throwaway
// discovery server -- see newTestOIDCAuthenticator's doc comment for why
// AuthModeNone cannot stand in here) and a real *mcpauth.Provider via
// setupMCPAuth, mounted via app.setupRoutes on an httptest.Server.
//
// The server's own address must be known before ProviderConfig.Issuer can
// be set (mcpauth.NewProvider validates it as an absolute URL up front),
// so this uses httptest.NewUnstartedServer to learn the listening address
// first -- mirroring oauth_bootstrap_integration_test.go's mcpTS trick in
// audience_score_system.
func newMCPAuthTestStack(t *testing.T) *mcpAuthTestStack {
	t.Helper()
	ctx := context.Background()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "apply every migration from whagent-net's real embedded schema")

	_, err = db.Pool.Exec(ctx, uiSessionsSchema)
	require.NoError(t, err, "create the self-contained ui_sessions table")

	var discoveryServer *httptest.Server
	discoveryServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 discoveryServer.URL,
			"authorization_endpoint": discoveryServer.URL + "/auth",
			"token_endpoint":         discoveryServer.URL + "/token",
			"jwks_uri":               discoveryServer.URL + "/keys",
		})
	}))
	t.Cleanup(discoveryServer.Close)

	const cookieName = "test_mcpauth_ui_session"
	const sessionSecret = "test-secret-that-is-at-least-32-bytes-long"

	sessionStore, err := htmxauth.NewDBSessionManager(ctx, db.Pool, sessionSecret, cookieName)
	require.NoError(t, err)

	auth, err := htmxauth.NewAuthenticatorWithDB(ctx, htmxauth.Config{
		Mode:             htmxauth.AuthModeOIDC,
		SessionSecret:    sessionSecret,
		SessionName:      cookieName,
		OIDCIssuer:       discoveryServer.URL,
		OIDCClientID:     "test-client",
		OIDCClientSecret: "test-client-secret",
		OIDCRedirectURL:  "http://localhost/auth/callback",
	}, sessionStore)
	require.NoError(t, err)

	app := &App{auth: auth, oidcIssuer: discoveryServer.URL}

	ts := httptest.NewUnstartedServer(nil)
	uiPublicURL := "http://" + ts.Listener.Addr().String()
	t.Cleanup(ts.Close)

	cfg := config{
		UIPublicURL: uiPublicURL,
		// A loopback URL is enough here -- validateAbsoluteURL requires
		// https or loopback http, and this task's Testing scope stops at
		// "the minted credential's stored identity is correct" (see
		// package doc), never dialing this address for a real MCP call.
		MCPPublicURL: "http://127.0.0.1:19999",
	}
	mcpProvider, err := setupMCPAuth(ctx, db.Pool, cfg, app.mcpCallerResolver())
	require.NoError(t, err)
	app.mcpProvider = mcpProvider

	mux := http.NewServeMux()
	app.setupRoutes(mux)
	ts.Config = &http.Server{Handler: mux}
	ts.Start()

	return &mcpAuthTestStack{
		app:        app,
		ts:         ts,
		pool:       db.Pool,
		cookieName: cookieName,
		oidcIssuer: discoveryServer.URL,
	}
}

// insertSession inserts a ui_sessions row using exactly the columns
// db_session.go's own SetUserInfo INSERT writes (see
// libs/go/htmxauth/db_session_integration_test.go's insertSessionRow,
// which this mirrors), standing in for a real OIDC login since
// oidc.IDToken cannot be constructed with injected test claims outside a
// full verify flow.
func (s *mcpAuthTestStack) insertSession(t *testing.T, sessionID, sub string, expiresAt time.Time) {
	t.Helper()
	userInfo, err := json.Marshal(map[string]string{"sub": sub})
	require.NoError(t, err)

	_, err = s.pool.Exec(context.Background(), `
		INSERT INTO ui_sessions
			(session_id, user_info, access_token, refresh_token, token_expires_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, sessionID, userInfo, "access-tok", "enc-refresh-tok", time.Now().Add(time.Hour), expiresAt)
	require.NoError(t, err)
}

// signIn inserts a live (non-expired) session for sub and returns the
// cookie a caller presenting that session would carry.
func (s *mcpAuthTestStack) signIn(t *testing.T, sub string) *http.Cookie {
	t.Helper()
	sessionID := randomHex(t)
	s.insertSession(t, sessionID, sub, time.Now().Add(24*time.Hour))
	return &http.Cookie{Name: s.cookieName, Value: sessionID}
}

func randomHex(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return hex.EncodeToString(b)
}

// ── mcpCallerResolver: tampered / expired / valid session ──────────────

func TestMCPCallerResolver_ValidSession_ReturnsEncodedIssSub(t *testing.T) {
	stack := newMCPAuthTestStack(t)
	cookie := stack.signIn(t, "operator-sub-123")

	req := httptest.NewRequest(http.MethodGet, "/authorize", nil)
	req.AddCookie(cookie)

	identity, ok := stack.app.mcpCallerResolver()(req)
	require.True(t, ok)

	wantIdentity, err := mcpidentity.Encode(stack.oidcIssuer, "operator-sub-123")
	require.NoError(t, err)
	assert.Equal(t, wantIdentity, identity, "the resolved identity must be the operator's own encoded (iss, sub)")
}

func TestMCPCallerResolver_TamperedCookie_ReturnsFalse(t *testing.T) {
	stack := newMCPAuthTestStack(t)

	req := httptest.NewRequest(http.MethodGet, "/authorize", nil)
	req.AddCookie(&http.Cookie{Name: stack.cookieName, Value: "this-session-id-does-not-exist-in-ui_sessions"})

	identity, ok := stack.app.mcpCallerResolver()(req)
	assert.False(t, ok)
	assert.Empty(t, identity)
}

func TestMCPCallerResolver_ExpiredSession_ReturnsFalse(t *testing.T) {
	stack := newMCPAuthTestStack(t)

	sessionID := randomHex(t)
	stack.insertSession(t, sessionID, "operator-sub-expired", time.Now().Add(-time.Hour))

	req := httptest.NewRequest(http.MethodGet, "/authorize", nil)
	req.AddCookie(&http.Cookie{Name: stack.cookieName, Value: sessionID})

	identity, ok := stack.app.mcpCallerResolver()(req)
	assert.False(t, ok)
	assert.Empty(t, identity)
}

// ── Red/green discipline (verified by hand, then reverted) ─────────────
//
// Temporarily changed mcpCallerResolver (mcpauth.go) to treat
// app.auth.CurrentUser's error case as success (returning a bogus
// identity/true instead of ""/false) and ran both this target and
// ui_test. Went red exactly where expected --
// TestMCPCallerResolver_NoCookie_ReturnsFalse (mcpauth_test.go),
// TestMCPCallerResolver_TamperedCookie_ReturnsFalse,
// TestMCPCallerResolver_ExpiredSession_ReturnsFalse (both here), and
// TestAuthorize_SignedOut_RedirectsToLogin (a 400 from mcpauth's own
// unknown-client_id check instead of the expected 302 to /login, since
// /authorize no longer treated the caller as unresolved) -- while
// TestMCPCallerResolver_ValidSession_ReturnsEncodedIssSub and the full
// bootstrap test stayed green (the happy path never touches the broken
// branch). Reverting the change restored every test to green. This
// proves these tests actually guard the resolver's session lookup rather
// than passing vacuously.

// ── Full OAuth2 authorization-code + PKCE bootstrap ─────────────────────

// pkcePair returns a random code_verifier and its S256 code_challenge --
// this package cannot reach libs/go/mcpauth's own unexported genPKCEPair,
// so this is a local, functionally identical copy (mirrors
// oauth_bootstrap_integration_test.go's bootstrapPKCEPair).
func pkcePair(t *testing.T) (verifier, challenge string) {
	t.Helper()
	b := make([]byte, 32)
	_, err := rand.Read(b)
	require.NoError(t, err)
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge
}

// TestOAuthBootstrap_DiscoveryToToken_MintsCredentialWithEncodedIdentity
// drives discovery -> registration -> /authorize (signed in) -> /token
// against the actually-mounted Provider, then asserts the minted
// credential's stored identity is the signed-in operator's encoded
// (iss, sub) pair -- issue #2245's Testing section, reduced to the
// authorization-server-only scope this task builds (see package doc).
func TestOAuthBootstrap_DiscoveryToToken_MintsCredentialWithEncodedIdentity(t *testing.T) {
	stack := newMCPAuthTestStack(t)
	ctx := context.Background()
	cookie := stack.signIn(t, "bootstrap-operator-sub")

	// Step 1: discovery.
	respMeta, err := http.Get(stack.ts.URL + "/.well-known/oauth-authorization-server")
	require.NoError(t, err)
	var asMeta struct {
		Issuer                string `json:"issuer"`
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
		RegistrationEndpoint  string `json:"registration_endpoint"`
	}
	require.NoError(t, json.NewDecoder(respMeta.Body).Decode(&asMeta))
	respMeta.Body.Close()
	require.Equal(t, http.StatusOK, respMeta.StatusCode)
	assert.Equal(t, stack.ts.URL, asMeta.Issuer)
	require.NotEmpty(t, asMeta.RegistrationEndpoint)
	require.NotEmpty(t, asMeta.AuthorizationEndpoint)
	require.NotEmpty(t, asMeta.TokenEndpoint)

	// Step 2: dynamic client registration.
	const redirectURI = "http://127.0.0.1:54321/callback"
	respReg, err := http.Post(asMeta.RegistrationEndpoint, "application/json",
		strings.NewReader(`{"redirect_uris": ["`+redirectURI+`"]}`))
	require.NoError(t, err)
	var clientReg struct {
		ClientID string `json:"client_id"`
	}
	require.NoError(t, json.NewDecoder(respReg.Body).Decode(&clientReg))
	respReg.Body.Close()
	require.Equal(t, http.StatusCreated, respReg.StatusCode)
	require.NotEmpty(t, clientReg.ClientID)

	// Step 3: /authorize with the real signed-in session cookie + S256
	// challenge -> code.
	verifier, challenge := pkcePair(t)
	authClient := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	authReq, err := http.NewRequest(http.MethodGet, asMeta.AuthorizationEndpoint+"?"+url.Values{
		"response_type":         {"code"},
		"client_id":             {clientReg.ClientID},
		"redirect_uri":          {redirectURI},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"state":                 {"bootstrap-state"},
	}.Encode(), nil)
	require.NoError(t, err)
	authReq.AddCookie(cookie)
	respAuth, err := authClient.Do(authReq)
	require.NoError(t, err)
	respAuth.Body.Close()
	require.Equal(t, http.StatusFound, respAuth.StatusCode)
	loc, err := url.Parse(respAuth.Header.Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "bootstrap-state", loc.Query().Get("state"))
	code := loc.Query().Get("code")
	require.NotEmpty(t, code)

	// Step 4: /token with the verifier -> access_token.
	respTok, err := http.PostForm(asMeta.TokenEndpoint, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"client_id":     {clientReg.ClientID},
		"redirect_uri":  {redirectURI},
	})
	require.NoError(t, err)
	rawTokenBody, err := io.ReadAll(respTok.Body)
	require.NoError(t, err)
	respTok.Body.Close()
	require.Equal(t, http.StatusOK, respTok.StatusCode, "body: %s", rawTokenBody)

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	require.NoError(t, json.Unmarshal(rawTokenBody, &tokenResp))
	assert.Equal(t, "Bearer", tokenResp.TokenType)
	require.NotEmpty(t, tokenResp.AccessToken)

	// Step 5: the minted credential's stored identity is the signed-in
	// operator's encoded (iss, sub) pair (NFR7) -- verified via a second,
	// independently constructed CredentialStore against the same table,
	// exactly as `mcp`'s own verification middleware (a dependent task)
	// eventually will.
	credentials, err := mcpauth.NewCredentialStore(ctx, mcpauth.StoreConfig{Pool: stack.pool})
	require.NoError(t, err)
	identity, cred, err := credentials.Verify(ctx, tokenResp.AccessToken)
	require.NoError(t, err)

	wantIdentity, err := mcpidentity.Encode(stack.oidcIssuer, "bootstrap-operator-sub")
	require.NoError(t, err)
	assert.Equal(t, wantIdentity, identity, "the minted credential's stored identity must be the signed-in operator's encoded (iss, sub) pair, not any local id")
	assert.NotEmpty(t, cred.ID)
}

// TestAuthorize_SignedOut_RedirectsToLogin covers issue #2245's Testing
// section: "/authorize while signed out redirects to /login". The
// resolver runs before any client_id/redirect_uri validation
// (authorize.go's own doc comment), so a garbage client_id here still
// exercises exactly the code path this test is after.
func TestAuthorize_SignedOut_RedirectsToLogin(t *testing.T) {
	stack := newMCPAuthTestStack(t)

	target := stack.ts.URL + "/authorize?" + url.Values{
		"response_type":         {"code"},
		"client_id":             {"not-a-registered-client"},
		"redirect_uri":          {"http://127.0.0.1:1/callback"},
		"code_challenge":        {"challenge"},
		"code_challenge_method": {"S256"},
	}.Encode()

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(target)
	require.NoError(t, err)
	resp.Body.Close()

	require.Equal(t, http.StatusFound, resp.StatusCode)
	loc, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "/login", loc.Path, "an unresolved caller must be sent to SignInURL, not shown an error or a login form of mcpauth's own")

	returnTo := loc.Query().Get("next")
	require.NotEmpty(t, returnTo, "the return-to target must be preserved so sign-in can redirect back")
	assert.True(t, strings.HasPrefix(returnTo, stack.ts.URL+"/authorize"),
		"return-to target must point back at this provider's own /authorize, got %q", returnTo)
}
