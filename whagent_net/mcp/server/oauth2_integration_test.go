package server_test

// Integration coverage for issue #2249's FR9 OAuth2 path -- modeled on
// audience_score_system/mcp/server/oauth_bootstrap_integration_test.go,
// scoped to what this task (the resource-server half) actually owns: a
// real streamable-HTTP mcp.Client against a real server.NewHTTPHandler,
// a real (fake-Postgres-backed) mcpauth.CredentialStore.Verify call, a
// real RFC 8693 HTTP request/response against a fake Keycloak token
// endpoint (server.KeycloakExchanger), and a real (bufconn) gRPC call
// into a fake `api`. It does not stand up #2245's authorization-server
// side (`ui`'s mcpauth.Provider/browser sign-in) -- that belongs to that
// task's own Testing phase -- so credentials here are minted directly
// into fakeOAuthCredentialStore rather than through a real /authorize ->
// /token exchange; everything downstream of "an operator already holds a
// live mcpauth credential" is exercised for real.
import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/mcpauth"
	"github.com/whale-net/everything/whagent_net/mcp/server"
	"github.com/whale-net/everything/whagent_net/mcp/tools"
	"github.com/whale-net/everything/whagent_net/mcpidentity"
)

// ── fake mcpauth.CredentialStore ─────────────────────────────────────────

// fakeOAuthCredentialStore implements mcpauth.CredentialStore in memory --
// this file's own copy, distinct from server_test's package boundary
// (server package's auth_test.go has its own, for its own unit tests).
type fakeOAuthCredentialStore struct {
	mu         sync.Mutex
	identities map[string]string // raw token -> encoded identity
	revoked    map[string]bool
}

func newFakeOAuthCredentialStore() *fakeOAuthCredentialStore {
	return &fakeOAuthCredentialStore{identities: map[string]string{}, revoked: map[string]bool{}}
}

// newCredentialShapedToken returns a 64-character lowercase hex string --
// the exact shape server.isCredentialShaped (auth.go) routes onto the
// OAuth2 path, mirroring libs/go/mcpauth/credential.go's own
// generateToken (32 crypto/rand bytes, hex-encoded).
func newCredentialShapedToken(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return hex.EncodeToString(b)
}

func (f *fakeOAuthCredentialStore) mint(t *testing.T, iss, sub string) string {
	t.Helper()
	encoded, err := mcpidentity.Encode(iss, sub)
	require.NoError(t, err)
	token := newCredentialShapedToken(t)
	f.mu.Lock()
	f.identities[token] = encoded
	f.mu.Unlock()
	return token
}

func (f *fakeOAuthCredentialStore) revoke(token string) {
	f.mu.Lock()
	f.revoked[token] = true
	f.mu.Unlock()
}

func (f *fakeOAuthCredentialStore) Mint(context.Context, string) (string, mcpauth.Credential, error) {
	return "", mcpauth.Credential{}, errors.New("fakeOAuthCredentialStore.Mint is not used by these tests")
}

func (f *fakeOAuthCredentialStore) Verify(_ context.Context, rawToken string) (string, mcpauth.Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.revoked[rawToken] {
		return "", mcpauth.Credential{}, mcpauth.ErrInvalidCredential
	}
	identity, ok := f.identities[rawToken]
	if !ok {
		return "", mcpauth.Credential{}, mcpauth.ErrInvalidCredential
	}
	return identity, mcpauth.Credential{Identity: identity}, nil
}

func (f *fakeOAuthCredentialStore) Revoke(context.Context, uuid.UUID, string) error {
	return errors.New("fakeOAuthCredentialStore.Revoke is not used by these tests")
}

func (f *fakeOAuthCredentialStore) List(context.Context, string) ([]mcpauth.Credential, error) {
	return nil, errors.New("fakeOAuthCredentialStore.List is not used by these tests")
}

var _ mcpauth.CredentialStore = (*fakeOAuthCredentialStore)(nil)

// ── fake Keycloak token endpoint ─────────────────────────────────────────

// newFakeKeycloakTokenEndpoint stands up an httptest.Server implementing
// just enough of Keycloak's RFC 8693 token endpoint for
// server.KeycloakExchanger to talk to: it mints a deterministic JWT-shaped
// string embedding requested_subject (so a test can assert exactly whose
// identity api ends up seeing) and counts requests, so cache-reuse can be
// asserted the same way tokenexchange_test.go does.
func newFakeKeycloakTokenEndpoint(t *testing.T) (url string, hits func() int) {
	t.Helper()
	var mu sync.Mutex
	var n int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		assert.Equal(t, "urn:ietf:params:oauth:grant-type:token-exchange", r.FormValue("grant_type"))
		sub := r.FormValue("requested_subject")
		require.NotEmpty(t, sub)

		mu.Lock()
		n++
		count := n
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": fmt.Sprintf("keycloak-issued.exchanged-for-%s.call-%d", sub, count),
			"expires_in":   300,
		})
	}))
	t.Cleanup(ts.Close)
	return ts.URL, func() int { mu.Lock(); defer mu.Unlock(); return n }
}

// ── shared MCP-client plumbing ────────────────────────────────────────────

func callGetSession(t *testing.T, mcpURL, bearer string) (*mcp.CallToolResult, error) {
	t.Helper()
	transport := &mcp.StreamableClientTransport{
		Endpoint:   mcpURL,
		HTTPClient: &http.Client{Transport: bearerRoundTripper{token: bearer}},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "oauth2-integration-test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(context.Background(), transport, nil)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "get_session",
		Arguments: tools.GetSessionInput{SessionID: "sess-1"},
	})
}

// oauth2Stack bundles one fully wired `mcp` instance (real
// server.NewHTTPHandler/server.New/server.NewKeycloakExchanger, a fake
// api backend, a fake Keycloak token endpoint, and an in-memory
// mcpauth.CredentialStore) for this file's tests to drive.
type oauth2Stack struct {
	url         string
	fake        *fakeSessionServer
	credentials *fakeOAuthCredentialStore
	tokenHits   func() int
}

func newOAuth2Stack(t *testing.T) *oauth2Stack {
	t.Helper()
	fake, client := newFakeBackend(t) // auth_pass_through_test.go's helper, same package

	tokenURL, hits := newFakeKeycloakTokenEndpoint(t)
	exchanger := server.NewKeycloakExchanger(server.TokenExchangeConfig{
		ClientID:      "mcp-confidential-client",
		ClientSecret:  "mcp-token-exchange-secret",
		TokenEndpoint: tokenURL,
	})

	credentials := newFakeOAuthCredentialStore()

	srv := server.New(exchanger)
	// nil domainResolver/grant: this suite exercises FR9's OAuth2
	// identity-resolution/exchange path only, never FR7/FR8's
	// dispatch-time resolution -- get_session's call method does not use
	// either yet (issue #2430's Scaffold phase is injection only).
	tools.RegisterGetSession(srv, client, nil, nil)

	// httptest.NewUnstartedServer to learn the listen address before
	// building the handler, exactly like the audience_score_system model
	// this file mirrors -- ResourceMetadataConfig.Resource must be this
	// server's OWN externally reachable URL.
	ts := httptest.NewUnstartedServer(nil)
	mcpURL := "http://" + ts.Listener.Addr().String()
	t.Cleanup(ts.Close)

	handler := server.NewHTTPHandler(srv, credentials, server.ResourceMetadataConfig{
		Resource:            mcpURL,
		AuthorizationServer: "https://ui.example.test",
		ResourceName:        "whagent-net MCP (test)",
	})
	ts.Config = &http.Server{Handler: handler}
	ts.Start()

	return &oauth2Stack{url: mcpURL, fake: fake, credentials: credentials, tokenHits: hits}
}

// ── discovery ─────────────────────────────────────────────────────────────

// TestOAuth2_Discovery_UnauthenticatedRequestPointsAtOwnProtectedResourceMetadata
// is this file's "discovery" step (the Testing section's "discovery ->
// credential -> tool call" chain): an unauthenticated request must 401
// with a challenge naming mcp's OWN resource-metadata endpoint (never
// ui's), and that endpoint must actually serve RFC 9728 metadata naming
// ui as the authorization server.
func TestOAuth2_Discovery_UnauthenticatedRequestPointsAtOwnProtectedResourceMetadata(t *testing.T) {
	stack := newOAuth2Stack(t)

	resp, err := http.Get(stack.url + "/")
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	challenge := resp.Header.Get("WWW-Authenticate")
	require.NotEmpty(t, challenge)
	const marker = `resource_metadata="`
	i := strings.Index(challenge, marker)
	require.NotEqual(t, -1, i, "challenge must carry resource_metadata: %q", challenge)
	rest := challenge[i+len(marker):]
	end := strings.Index(rest, `"`)
	require.NotEqual(t, -1, end)
	resourceMetadataURL := rest[:end]
	require.True(t, strings.HasPrefix(resourceMetadataURL, stack.url), "the challenge must point at mcp's OWN metadata endpoint: %q", resourceMetadataURL)

	metaResp, err := http.Get(resourceMetadataURL)
	require.NoError(t, err)
	defer metaResp.Body.Close()
	var meta struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
	}
	require.NoError(t, json.NewDecoder(metaResp.Body).Decode(&meta))
	assert.Equal(t, stack.url, meta.Resource)
	assert.Equal(t, []string{"https://ui.example.test"}, meta.AuthorizationServers)

	assert.Empty(t, stack.fake.recordedAuthHeaders(), "the gRPC backend must never be reached for an unauthenticated discovery-only sequence")
}

// ── credential -> tool call ────────────────────────────────────────────────

// TestOAuth2_MintedCredential_ToolCallExchangesForKeycloakJWT drives
// "credential -> MCP tool call -> api sees a Keycloak-verified caller
// whose (iss, sub) is the operator's": the credential minted for a real
// (iss, sub) pair, presented as the bearer token, must reach api carrying
// a jwt embedding that exact sub -- never the opaque credential itself.
func TestOAuth2_MintedCredential_ToolCallExchangesForKeycloakJWT(t *testing.T) {
	stack := newOAuth2Stack(t)

	const iss = "https://keycloak.example.test/realms/whagent"
	const sub = "operator-sub-42"
	token := stack.credentials.mint(t, iss, sub)

	res, err := callGetSession(t, stack.url, token)
	require.NoError(t, err)
	require.False(t, res.IsError, "unexpected tool error")

	headers := stack.fake.recordedAuthHeaders()
	require.Len(t, headers, 1)
	assert.Contains(t, headers[0], "exchanged-for-"+sub, "api must see a JWT the fake Keycloak endpoint minted for the operator's own sub")
	assert.NotContains(t, headers[0], token, "the opaque mcpauth credential itself must never reach api")

	// Red/green (verified by hand, then reverted): temporarily changing
	// AuthMiddleware's OAuth2 branch to forward the raw presented
	// credential (identity.sub, or the opaque token itself) instead of
	// exchanger.Exchange's result made the "Contains(...,
	// exchanged-for-...)" assertion above fail -- api recorded the raw
	// value instead of a Keycloak-minted jwt. Reverting restored it to
	// green.
}

// TestOAuth2_MintedCredential_ExchangeCachedAcrossCalls proves the cache
// (already unit-tested against KeycloakExchanger directly in
// tokenexchange_test.go) is actually wired through the full stack: two
// tool calls for the same identity must hit the fake Keycloak endpoint
// exactly once.
func TestOAuth2_MintedCredential_ExchangeCachedAcrossCalls(t *testing.T) {
	stack := newOAuth2Stack(t)
	token := stack.credentials.mint(t, "https://keycloak.example.test/realms/whagent", "operator-sub-cache")

	_, err := callGetSession(t, stack.url, token)
	require.NoError(t, err)
	_, err = callGetSession(t, stack.url, token)
	require.NoError(t, err)

	assert.Equal(t, 1, stack.tokenHits(), "two tool calls for the same identity must exchange exactly once, reusing the cache")
	headers := stack.fake.recordedAuthHeaders()
	require.Len(t, headers, 2)
	assert.Equal(t, headers[0], headers[1], "both calls must forward the identical cached exchange result")
}

// TestOAuth2_RevokedCredential_Rejected covers the revoked-credential case
// end to end.
func TestOAuth2_RevokedCredential_Rejected(t *testing.T) {
	stack := newOAuth2Stack(t)
	token := stack.credentials.mint(t, "https://keycloak.example.test/realms/whagent", "operator-sub-revoked")
	stack.credentials.revoke(token)

	_, err := callGetSession(t, stack.url, token)
	require.Error(t, err, "a revoked mcpauth credential must be rejected before any tool handler runs")
	assert.Empty(t, stack.fake.recordedAuthHeaders(), "api must never be reached for a revoked credential")
}

// TestOAuth2_GarbageCredential_Rejected covers a credential-shaped token
// (64 lowercase hex chars -- so it IS routed onto the credential-store
// branch, never silently treated as a manual token) that was never
// minted.
func TestOAuth2_GarbageCredential_Rejected(t *testing.T) {
	stack := newOAuth2Stack(t)

	_, err := callGetSession(t, stack.url, newCredentialShapedToken(t))
	require.Error(t, err)
	assert.Empty(t, stack.fake.recordedAuthHeaders())
}

// TestOAuth2_ManualTokenPath_StillWorksWithOAuth2Configured is this
// task's regression requirement, the OAuth2-configured mirror of
// auth_pass_through_test.go's own coverage (which proves the same thing
// with NO OAuth2 configuration present at all): with a real
// mcpauth.CredentialStore and a real Exchanger both wired in, a manual
// (non-credential-shaped) bearer token must still be forwarded byte for
// byte, completely bypassing the credential store and the exchanger.
func TestOAuth2_ManualTokenPath_StillWorksWithOAuth2Configured(t *testing.T) {
	stack := newOAuth2Stack(t)

	const manualToken = "operators-own-raw-keycloak-access-token"
	res, err := callGetSession(t, stack.url, manualToken)
	require.NoError(t, err)
	require.False(t, res.IsError)

	headers := stack.fake.recordedAuthHeaders()
	require.Len(t, headers, 1)
	assert.Equal(t, "Bearer "+manualToken, headers[0], "the manual-token path must forward the operator's own token byte for byte, never touching the exchanger")
	assert.Equal(t, 0, stack.tokenHits(), "a manual-token call must never hit the Keycloak token-exchange endpoint")
}
