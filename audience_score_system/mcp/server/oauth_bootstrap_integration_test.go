//go:build integration

// This file is the NFR4 bar issue #1646 names explicitly: one test that
// drives the EXACT MCP-client bootstrap sequence end to end, across the
// real two-binary split -- `web` as the OAuth2 authorization server
// (mcpauth.Provider mounted exactly like web/main.go's run(), with the
// real MCPCallerResolver backed by a real Postgres web_session) and `mcp`
// as the OAuth2 protected resource (server.NewHTTPHandler exactly like
// mcp/main.go's run()) -- sharing one Postgres and nothing else: two
// independent *pgxpool.Pool connections, two independent httptest.Server
// instances, no in-process state in common. See ../../ARCHITECTURE.md
// "MCP server: caller authentication" for the design this proves, and
// server_integration_test.go (this package) for the auth/Channel-scoping/
// idempotency/statelessness coverage this file's Testing-section siblings
// already own -- this file's only job is the discovery-to-tool-call
// bootstrap chain itself.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //audience_score_system/mcp/server:server_integration_test --test_output=all
package server_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/mcp/server"
	"github.com/whale-net/everything/audience_score_system/mcp/tools"
	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/web/auth"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/mcpauth"
)

// bootstrapPKCEPair returns a random code_verifier and its S256
// code_challenge -- this package cannot reach libs/go/mcpauth's own
// unexported genPKCEPair, so this is a local, functionally identical copy.
func bootstrapPKCEPair(t *testing.T) (verifier, challenge string) {
	t.Helper()
	b := make([]byte, 32)
	_, err := rand.Read(b)
	require.NoError(t, err)
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge
}

// extractResourceMetadataURL pulls the resource_metadata="..." value out of
// a `WWW-Authenticate: Bearer resource_metadata="...", ...` challenge
// header, per RFC 9728 §5.1.
func extractResourceMetadataURL(t *testing.T, challenge string) string {
	t.Helper()
	const marker = `resource_metadata="`
	i := strings.Index(challenge, marker)
	require.NotEqual(t, -1, i, "WWW-Authenticate must carry resource_metadata: %q", challenge)
	rest := challenge[i+len(marker):]
	end := strings.Index(rest, `"`)
	require.NotEqual(t, -1, end)
	return rest[:end]
}

// oauthBootstrapStack bundles both independently-constructed sides of the
// split plus everything a test needs to drive the full sequence: the real
// Person this run signs in as, its session cookie, and the underlying
// dbtest.Postgres (so a test can open yet another independent pool/store,
// e.g. to simulate an `mcp` process restart against the same database).
type oauthBootstrapStack struct {
	pg *dbtest.Postgres

	webURL string
	mcpURL string

	person store.Person
	cookie *http.Cookie
}

// newOAuthBootstrapStack provisions one throwaway Postgres (dbtest, real
// embedded migrations including 007_mcpauth_oauth), then builds `web`
// (mcpauth.Provider, Postgres-backed Clients/AuthCodes/Credentials, the
// real MCPCallerResolver) and `mcp` (server.NewHTTPHandler +
// tools.RegisterWhoami) as two INDEPENDENTLY CONSTRUCTED server instances
// -- separate *pgxpool.Pool connections (via newIndependentStore, this
// package's existing statelessness-test helper), separate *mcp.Server /
// httptest.Server -- sharing only the underlying database, mirroring
// TestMCP_Statelessness_ReplayAcrossTwoIndependentServerInstances's own
// "no shared in-process state" proof.
func newOAuthBootstrapStack(t *testing.T) *oauthBootstrapStack {
	t.Helper()
	ctx := context.Background()

	pg := newTestDB(t)

	webStore, webPool := newIndependentStore(t, pg)
	mcpStore, mcpPool := newIndependentStore(t, pg)

	encKey := sha256.Sum256([]byte("oauth-bootstrap-integration-test-key"))
	sessions := auth.NewSessionManager(webPool, "test_bootstrap_session", "session-secret", encKey)
	authenticator := auth.NewForTests(webStore.Persons(), sessions)

	// `mcp` needs to know its own externally-reachable URL
	// (ResourceMetadataConfig.Resource) before its handler is constructed,
	// but httptest.NewServer only hands back a URL once already listening
	// -- so use NewUnstartedServer (which already holds an open Listener)
	// to learn the address first, exactly the ordering problem `mcp` and
	// `web` solve in production via the operator-supplied
	// ASS_MCP_PUBLIC_URL env var (a value known in advance, not derived
	// from an ephemeral test port).
	mcpTS := httptest.NewUnstartedServer(nil)
	mcpURL := "http://" + mcpTS.Listener.Addr().String()
	t.Cleanup(mcpTS.Close)

	mux := http.NewServeMux()
	webTS := httptest.NewServer(mux)
	t.Cleanup(webTS.Close)

	clients, err := mcpauth.NewPostgresClientRegistry(ctx, mcpauth.ClientRegistryConfig{Pool: webPool})
	require.NoError(t, err)
	authCodes, err := mcpauth.NewPostgresAuthCodeStore(ctx, mcpauth.AuthCodeStoreConfig{Pool: webPool})
	require.NoError(t, err)
	webCreds := newTestCredentialStore(t, webPool)

	provider, err := mcpauth.NewProvider(mcpauth.ProviderConfig{
		Issuer:       webTS.URL,
		Resource:     mcpURL,
		ResourceName: "Test MCP",
		Resolver:     authenticator.MCPCallerResolver(),
		Credentials:  webCreds,
		Clients:      clients,
		AuthCodes:    authCodes,
		SignInURL:    "/login",
	})
	require.NoError(t, err)
	provider.Mount(mux)

	mcpCreds := newTestCredentialStore(t, mcpPool)
	srv := server.New(mcpStore)
	reg := server.NewRegistry(srv, mcpStore)
	tools.RegisterWhoami(reg)
	handler := server.NewHTTPHandler(srv, mcpCreds, server.ResourceMetadataConfig{
		Resource:            mcpURL,
		AuthorizationServer: webTS.URL,
		ResourceName:        "Test MCP",
	})
	mcpTS.Config = &http.Server{Handler: handler}
	mcpTS.Start()

	person, _, err := webStore.Persons().UpsertByGoogleSubject(ctx, "sub-oauth-bootstrap", "bootstrap@example.com", "Bootstrap Person")
	require.NoError(t, err)

	w := httptest.NewRecorder()
	require.NoError(t, sessions.Establish(ctx, w, person.ID.String(), ""))
	var cookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == "test_bootstrap_session" {
			cookie = c
		}
	}
	require.NotNil(t, cookie, "session cookie must be set")

	return &oauthBootstrapStack{pg: pg, webURL: webTS.URL, mcpURL: mcpTS.URL, person: person, cookie: cookie}
}

// TestOAuthBootstrap_FullMCPClientSequence drives the exact seven-step
// sequence issue #1646's Testing section names: unauthenticated 401 ->
// protected-resource metadata -> authorization-server metadata ->
// dynamic registration -> /authorize (real session) -> /token -> an
// authenticated tools/call resolving to the right Person -- with no
// client-specific branching anywhere in this sequence (NFR4).
func TestOAuthBootstrap_FullMCPClientSequence(t *testing.T) {
	stack := newOAuthBootstrapStack(t)
	ctx := context.Background()

	// Step 1: unauthenticated request to `mcp`'s MCP endpoint -> 401 with a
	// WWW-Authenticate challenge naming resource_metadata.
	resp1, err := http.Get(stack.mcpURL + "/")
	require.NoError(t, err)
	resp1.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp1.StatusCode)
	challenge := resp1.Header.Get("WWW-Authenticate")
	require.NotEmpty(t, challenge)
	resourceMetadataURL := extractResourceMetadataURL(t, challenge)
	require.True(t, strings.HasPrefix(resourceMetadataURL, stack.mcpURL),
		"the 401 challenge must point at mcp's OWN protected-resource metadata endpoint (%q), not web's: got %q", stack.mcpURL, resourceMetadataURL)

	// Step 2: GET that resource-metadata URL -> authorization_servers[0].
	resp2, err := http.Get(resourceMetadataURL)
	require.NoError(t, err)
	var resourceMeta struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
	}
	require.NoError(t, json.NewDecoder(resp2.Body).Decode(&resourceMeta))
	resp2.Body.Close()
	require.Equal(t, http.StatusOK, resp2.StatusCode)
	assert.Equal(t, stack.mcpURL, resourceMeta.Resource)
	require.NotEmpty(t, resourceMeta.AuthorizationServers)
	issuer := resourceMeta.AuthorizationServers[0]
	assert.Equal(t, stack.webURL, issuer, "the protected-resource metadata mcp serves must name web's issuer")

	// Step 3: GET <issuer>/.well-known/oauth-authorization-server ->
	// endpoint URLs.
	resp3, err := http.Get(issuer + "/.well-known/oauth-authorization-server")
	require.NoError(t, err)
	var asMeta struct {
		Issuer                string `json:"issuer"`
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
		RegistrationEndpoint  string `json:"registration_endpoint"`
	}
	require.NoError(t, json.NewDecoder(resp3.Body).Decode(&asMeta))
	resp3.Body.Close()
	require.Equal(t, http.StatusOK, resp3.StatusCode)
	require.NotEmpty(t, asMeta.RegistrationEndpoint)
	require.NotEmpty(t, asMeta.AuthorizationEndpoint)
	require.NotEmpty(t, asMeta.TokenEndpoint)

	// Step 4: POST /register with a loopback redirect URI -> client_id.
	const redirectURI = "http://127.0.0.1:54321/callback"
	regBody := `{"redirect_uris": ["` + redirectURI + `"]}`
	resp4, err := http.Post(asMeta.RegistrationEndpoint, "application/json", strings.NewReader(regBody))
	require.NoError(t, err)
	var clientReg struct {
		ClientID string `json:"client_id"`
	}
	require.NoError(t, json.NewDecoder(resp4.Body).Decode(&clientReg))
	resp4.Body.Close()
	require.Equal(t, http.StatusCreated, resp4.StatusCode)
	require.NotEmpty(t, clientReg.ClientID)

	// Step 5: GET /authorize with a real session cookie + S256 challenge ->
	// code.
	verifier, pkceChallenge := bootstrapPKCEPair(t)
	authClient := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	authReq, err := http.NewRequest(http.MethodGet, asMeta.AuthorizationEndpoint+"?"+url.Values{
		"response_type":         {"code"},
		"client_id":             {clientReg.ClientID},
		"redirect_uri":          {redirectURI},
		"code_challenge":        {pkceChallenge},
		"code_challenge_method": {"S256"},
		"state":                 {"bootstrap-state"},
	}.Encode(), nil)
	require.NoError(t, err)
	authReq.AddCookie(stack.cookie)
	resp5, err := authClient.Do(authReq)
	require.NoError(t, err)
	resp5.Body.Close()
	require.Equal(t, http.StatusFound, resp5.StatusCode)
	loc, err := url.Parse(resp5.Header.Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "bootstrap-state", loc.Query().Get("state"))
	code := loc.Query().Get("code")
	require.NotEmpty(t, code)

	// Step 6: POST /token with the verifier -> access_token. FR4: no
	// expires_in/refresh_token anywhere in the response.
	resp6, err := http.PostForm(asMeta.TokenEndpoint, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"client_id":     {clientReg.ClientID},
		"redirect_uri":  {redirectURI},
	})
	require.NoError(t, err)
	rawTokenBody, err := io.ReadAll(resp6.Body)
	require.NoError(t, err)
	resp6.Body.Close()
	require.Equal(t, http.StatusOK, resp6.StatusCode, "body: %s", rawTokenBody)
	assert.NotContains(t, string(rawTokenBody), "expires_in", "FR4: mcpauth credentials never carry an expiration")
	assert.NotContains(t, string(rawTokenBody), "refresh_token", "FR4: mcpauth issues no refresh lifecycle")

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	require.NoError(t, json.Unmarshal(rawTokenBody, &tokenResp))
	assert.Equal(t, "Bearer", tokenResp.TokenType)
	require.NotEmpty(t, tokenResp.AccessToken)

	// Step 7: use that token against `mcp`'s MCP endpoint -> a tools/call
	// succeeds and resolves to the right Person.
	callWhoami := func(mcpURL string) *mcp.CallToolResult {
		t.Helper()
		transport := &mcp.StreamableClientTransport{
			Endpoint:   mcpURL,
			HTTPClient: &http.Client{Transport: bearerRoundTripper{token: tokenResp.AccessToken}},
		}
		client := mcp.NewClient(&mcp.Implementation{Name: "bootstrap-test-client", Version: "0.0.1"}, nil)
		cs, err := client.Connect(ctx, transport, nil)
		require.NoError(t, err)
		t.Cleanup(func() { _ = cs.Close() })

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "whoami"})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected error: %s", textOf(res))
		return res
	}

	res := callWhoami(stack.mcpURL)
	m, ok := res.StructuredContent.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, stack.person.ID.String(), m["person_id"], "the minted credential must resolve to the Person who held the /authorize session")

	// "the credential still works after a simulated process restart":
	// construct a FRESH mcp.Server + server.NewHTTPHandler + httptest.Server
	// against the same database, and prove the same token still resolves.
	restartStore, restartPool := newIndependentStore(t, stack.pg)
	restartCreds := newTestCredentialStore(t, restartPool)
	restartSrv := server.New(restartStore)
	restartReg := server.NewRegistry(restartSrv, restartStore)
	tools.RegisterWhoami(restartReg)
	restartHandler := server.NewHTTPHandler(restartSrv, restartCreds, server.ResourceMetadataConfig{
		Resource:            stack.mcpURL,
		AuthorizationServer: stack.webURL,
		ResourceName:        "Test MCP",
	})
	restartTS := httptest.NewServer(restartHandler)
	t.Cleanup(restartTS.Close)

	res2 := callWhoami(restartTS.URL)
	m2, ok := res2.StructuredContent.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, stack.person.ID.String(), m2["person_id"], "the credential must still resolve after a simulated mcp process restart")
}
