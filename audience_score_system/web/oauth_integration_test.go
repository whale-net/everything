//go:build integration

// `web` handler tests via httptest against the real mcpauth.Provider
// mounted exactly like run() mounts it in main.go -- MCPCallerResolver
// backed by a real Postgres web_session (web/auth), and Postgres-backed
// ClientRegistry/AuthCodeStore/CredentialStore (mcpauth), sharing one
// dbtest-provisioned database and the domain's real embedded migrations
// (including 007_mcpauth_oauth). This is the this task's Testing section
// "`web` handler tests via httptest against the mounted provider" bullet:
// what it proves that libs/go/mcpauth's own extensive authorize_test.go/
// token_test.go suite (stubResolver, in-memory stores) cannot -- that
// ASS's real MCPCallerResolver and real Postgres-backed stores actually
// integrate through Provider correctly. See
// audience_score_system/mcp/server/oauth_bootstrap_integration_test.go for
// the full cross-binary NFR4 bootstrap sequence this feeds into.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //audience_score_system/web:web_oauth_integration_test --test_output=all
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/migrate/schema"
	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/web/auth"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/mcpauth"
	"github.com/whale-net/everything/libs/go/migrate"
)

// oauthTestCookieName mirrors sessionName's role in main.go, kept distinct
// from that production constant so a future rename of one doesn't silently
// desync the other.
const oauthTestCookieName = "test_ass_web_session"

// oauthTestStack bundles everything a Provider-mounted-on-web test needs:
// a real Person/session/mcpauth-table-backed Postgres, the Authenticator
// whose MCPCallerResolver Provider.Resolver is wired to, and an
// httptest.Server with the Provider actually Mount-ed on it -- exactly
// run()'s wiring in main.go, minus HTTP binding to ASS_HTTP_ADDR and minus
// every other route this task doesn't touch.
type oauthTestStack struct {
	st            *store.Store
	sessions      *auth.SessionManager
	authenticator *auth.Authenticator
	provider      *mcpauth.Provider
	creds         mcpauth.CredentialStore
	srv           *httptest.Server
	client        *http.Client // never follows redirects, so tests can inspect Location
}

func newOAuthTestStack(t *testing.T) *oauthTestStack {
	t.Helper()
	ctx := context.Background()

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB, err := sql.Open("pgx", pg.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "apply every migration from the real embedded schema, including 007_mcpauth_oauth")

	encKey := sha256.Sum256([]byte("oauth-integration-test-token-key"))
	sessions := auth.NewSessionManager(pg.Pool, oauthTestCookieName, "session-secret", encKey)
	st := store.New(pg.Pool)
	// NewForTests: this suite never drives HandleLogin/HandleCallback
	// (Google) itself -- sessions are established directly via
	// sessions.Establish, exactly like auth_integration_test.go's
	// RequireSignedIn tests -- so no oauth2Config/verifier is needed.
	authenticator := auth.NewForTests(st.Persons(), sessions)

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	clients, err := mcpauth.NewPostgresClientRegistry(ctx, mcpauth.ClientRegistryConfig{Pool: pg.Pool})
	require.NoError(t, err)
	authCodes, err := mcpauth.NewPostgresAuthCodeStore(ctx, mcpauth.AuthCodeStoreConfig{Pool: pg.Pool})
	require.NoError(t, err)
	creds, err := mcpauth.NewCredentialStore(ctx, mcpauth.StoreConfig{
		Pool:           pg.Pool,
		TableName:      "mcp_credential",
		IdentityColumn: "person_id",
		IdentityCast:   "uuid",
	})
	require.NoError(t, err)

	provider, err := mcpauth.NewProvider(mcpauth.ProviderConfig{
		Issuer:       srv.URL,
		Resource:     "https://mcp.example.com",
		ResourceName: "Test MCP",
		Resolver:     authenticator.MCPCallerResolver(),
		Credentials:  creds,
		Clients:      clients,
		AuthCodes:    authCodes,
		SignInURL:    "/login",
	})
	require.NoError(t, err)
	provider.Mount(mux)

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	return &oauthTestStack{
		st: st, sessions: sessions, authenticator: authenticator,
		provider: provider, creds: creds, srv: srv, client: client,
	}
}

// establishSession creates a real Person + web_session row and returns the
// resulting session cookie, exactly like a completed Google sign-in would.
func (ts *oauthTestStack) establishSession(t *testing.T, googleSub string) (personID string, cookie *http.Cookie) {
	t.Helper()
	ctx := context.Background()

	person, _, err := ts.st.Persons().UpsertByGoogleSubject(ctx, googleSub, googleSub+"@example.com", "Test Person")
	require.NoError(t, err)

	w := httptest.NewRecorder()
	require.NoError(t, ts.sessions.Establish(ctx, w, person.ID.String(), ""))
	for _, c := range w.Result().Cookies() {
		if c.Name == oauthTestCookieName {
			return person.ID.String(), c
		}
	}
	t.Fatalf("session cookie %q not set", oauthTestCookieName)
	return "", nil
}

// genPKCEPair returns a random code_verifier and its S256 code_challenge.
func genPKCEPair(t *testing.T) (verifier, challenge string) {
	t.Helper()
	b := make([]byte, 32)
	_, err := rand.Read(b)
	require.NoError(t, err)
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge
}

// registerClient dynamically registers a test OAuth2 client via the
// Provider's real POST /register endpoint (not a direct ClientRegistry
// call), so these tests also exercise the exact HTTP path an MCP client
// drives.
func (ts *oauthTestStack) registerClient(t *testing.T, redirectURI string) string {
	t.Helper()
	body := `{"redirect_uris": ["` + redirectURI + `"]}`
	resp, err := http.Post(ts.srv.URL+"/register", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var reg struct {
		ClientID string `json:"client_id"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&reg))
	require.NotEmpty(t, reg.ClientID)
	return reg.ClientID
}

func TestOAuthAuthorize_NoSession_RedirectsToLoginWithNext(t *testing.T) {
	ts := newOAuthTestStack(t)

	redirectURI := "https://mcp-client.example.com/callback"
	clientID := ts.registerClient(t, redirectURI)
	_, challenge := genPKCEPair(t)

	authorizeURL := ts.srv.URL + "/authorize?" + url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"state":                 {"xyz"},
	}.Encode()

	resp, err := ts.client.Get(authorizeURL)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusFound, resp.StatusCode)
	loc, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "/login", loc.Path, "an unresolved caller must be sent to /login, never a form Provider renders itself")

	next := loc.Query().Get("next")
	require.NotEmpty(t, next, "the return-to target must use ASS's own ?next= convention (HandleLogin), not mcpauth's return_to default")
	nextURL, err := url.Parse(next)
	require.NoError(t, err)
	assert.Equal(t, "/authorize", nextURL.Path)
	assert.Equal(t, clientID, nextURL.Query().Get("client_id"), "next must round-trip the exact original /authorize query string")
}

func TestOAuthAuthorize_ValidSession_IssuesCodeAndRedirectsToClient(t *testing.T) {
	ts := newOAuthTestStack(t)

	redirectURI := "https://mcp-client.example.com/callback"
	clientID := ts.registerClient(t, redirectURI)
	_, cookie := ts.establishSession(t, "sub-authorize-valid")
	_, challenge := genPKCEPair(t)

	req, err := http.NewRequest(http.MethodGet, ts.srv.URL+"/authorize?"+url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"state":                 {"preserve-me"},
	}.Encode(), nil)
	require.NoError(t, err)
	req.AddCookie(cookie)

	resp, err := ts.client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusFound, resp.StatusCode)
	loc, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "mcp-client.example.com", loc.Host)
	assert.Equal(t, "/callback", loc.Path)
	assert.NotEmpty(t, loc.Query().Get("code"), "a resolved caller must receive an authorization code")
	assert.Equal(t, "preserve-me", loc.Query().Get("state"), "state must be echoed back byte-for-byte")
}

func TestOAuthToken_Exchange_ReturnsCredentialThatVerifiesToSamePerson(t *testing.T) {
	ts := newOAuthTestStack(t)
	ctx := context.Background()

	redirectURI := "https://mcp-client.example.com/callback"
	clientID := ts.registerClient(t, redirectURI)
	personID, cookie := ts.establishSession(t, "sub-token-exchange")
	verifier, challenge := genPKCEPair(t)

	authReq, err := http.NewRequest(http.MethodGet, ts.srv.URL+"/authorize?"+url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}.Encode(), nil)
	require.NoError(t, err)
	authReq.AddCookie(cookie)

	authResp, err := ts.client.Do(authReq)
	require.NoError(t, err)
	defer authResp.Body.Close()
	require.Equal(t, http.StatusFound, authResp.StatusCode)
	loc, err := url.Parse(authResp.Header.Get("Location"))
	require.NoError(t, err)
	code := loc.Query().Get("code")
	require.NotEmpty(t, code)

	tokenResp, err := http.PostForm(ts.srv.URL+"/token", url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"client_id":     {clientID},
		"redirect_uri":  {redirectURI},
	})
	require.NoError(t, err)
	defer tokenResp.Body.Close()
	require.Equal(t, http.StatusOK, tokenResp.StatusCode)

	var body struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	require.NoError(t, json.NewDecoder(tokenResp.Body).Decode(&body))
	assert.Equal(t, "Bearer", body.TokenType)
	require.NotEmpty(t, body.AccessToken)

	resolvedIdentity, _, err := ts.creds.Verify(ctx, body.AccessToken)
	require.NoError(t, err, "the minted credential must verify against the same mcpauth.CredentialStore `mcp` uses")
	assert.Equal(t, personID, resolvedIdentity, "the credential must resolve to the same Person who held the session")
}
