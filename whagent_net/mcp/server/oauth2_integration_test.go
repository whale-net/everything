package server_test

// Integration coverage for issue #2249's FR9 OAuth2 identity-resolution
// path, updated by issue #2430's FR7/FR8/FR19 (RFC 8693 impersonation
// exchange and its tokenexchange.go cache are gone -- token acquisition
// now goes through GrantSource at tool-dispatch time): a real
// streamable-HTTP mcp.Client against a real server.NewHTTPHandler, a real
// (fake-Postgres-backed) mcpauth.CredentialStore.Verify call, a fake
// DomainResolver/GrantSource standing in for whagent_net/mcpdomain.Resolver
// and //whagent_net/delegatedgrant's real, Postgres/Keycloak-backed
// implementations, and a real (bufconn) gRPC call into a fake `api`. It
// does not stand up #2245's authorization-server side (`ui`'s
// mcpauth.Provider/browser sign-in) -- that belongs to that task's own
// Testing phase -- so credentials here are minted directly into
// fakeOAuthCredentialStore rather than through a real /authorize -> /token
// exchange; everything downstream of "an operator already holds a live
// mcpauth credential" is exercised for real.
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
	"golang.org/x/oauth2"

	"github.com/whale-net/everything/libs/go/grpcauth"
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

// ── fake tools.DomainResolver / tools.GrantSource ─────────────────────────

// fakeIntegrationDomainResolver implements tools.DomainResolver against a
// single fixed domain -- this file's own copy, distinct from the tools
// package's own unexported fakeDomainResolver (fake_domain_resolver_test.go),
// which this package cannot import.
type fakeIntegrationDomainResolver struct {
	domain string
}

func (f *fakeIntegrationDomainResolver) DomainForAgent(context.Context, string) (string, error) {
	return f.domain, nil
}

func (f *fakeIntegrationDomainResolver) DomainForSession(context.Context, string) (string, error) {
	return f.domain, nil
}

var _ tools.DomainResolver = (*fakeIntegrationDomainResolver)(nil)

// fakeIntegrationGrantCall records one TokenSource(subject, grant) call.
type fakeIntegrationGrantCall struct {
	subject string
	grant   string
}

// fakeIntegrationGrantSource implements tools.GrantSource, recording every
// (subject, grant) pair it was asked for, in order, and backing every
// resulting GrantTokenSource.Token call with either a fixed access token
// or a fixed error. Deliberately caches nothing (NFR7): each TokenSource
// call is independent, exactly like grpcauth.DelegatedGrantSource's own
// contract.
type fakeIntegrationGrantSource struct {
	mu       sync.Mutex
	token    string
	tokenErr error
	calls    []fakeIntegrationGrantCall
}

func (f *fakeIntegrationGrantSource) TokenSource(subject, grant string) grpcauth.GrantTokenSource {
	f.mu.Lock()
	f.calls = append(f.calls, fakeIntegrationGrantCall{subject: subject, grant: grant})
	f.mu.Unlock()
	return fakeIntegrationTokenSource{source: f}
}

func (f *fakeIntegrationGrantSource) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

var _ tools.GrantSource = (*fakeIntegrationGrantSource)(nil)

type fakeIntegrationTokenSource struct {
	source *fakeIntegrationGrantSource
}

func (t fakeIntegrationTokenSource) Token(context.Context) (*oauth2.Token, error) {
	if t.source.tokenErr != nil {
		return nil, t.source.tokenErr
	}
	return &oauth2.Token{AccessToken: t.source.token}, nil
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

// integrationDomain is the fixed domain fakeIntegrationDomainResolver
// resolves every agent id/session id to in this file's tests -- a
// grantkey.ForDomain-valid value (lowercase, digits, underscore, hyphen).
const integrationDomain = "audience_score_system"

// oauth2Stack bundles one fully wired `mcp` instance (real
// server.NewHTTPHandler/server.New, a fake api backend, an in-memory
// mcpauth.CredentialStore, and fake DomainResolver/GrantSource doubles)
// for this file's tests to drive.
type oauth2Stack struct {
	url         string
	fake        *fakeSessionServer
	credentials *fakeOAuthCredentialStore
	grant       *fakeIntegrationGrantSource
}

func newOAuth2Stack(t *testing.T) *oauth2Stack {
	t.Helper()
	fake, client := newFakeBackend(t) // auth_pass_through_test.go's helper, same package

	credentials := newFakeOAuthCredentialStore()
	domainResolver := &fakeIntegrationDomainResolver{domain: integrationDomain}
	grant := &fakeIntegrationGrantSource{token: "delegated-grant-token-xyz"}

	srv := server.New()
	tools.RegisterGetSession(srv, client, domainResolver, grant)

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

	return &oauth2Stack{url: mcpURL, fake: fake, credentials: credentials, grant: grant}
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

// TestOAuth2_MintedCredential_ToolCallAcquiresDelegatedGrantToken drives
// "credential -> MCP tool call -> api sees a token acquired via
// GrantSource": the credential minted for a real (iss, sub) pair,
// presented as the bearer token, must resolve to that operator's raw
// Keycloak sub (never the mcpidentity-encoded iss|sub composite --
// whagent_net/ui/handlers_consent.go's authorizeConsentGate documents why)
// as GrantSource.TokenSource's subject, and the resulting access token
// -- never the opaque credential itself -- is what reaches api.
func TestOAuth2_MintedCredential_ToolCallAcquiresDelegatedGrantToken(t *testing.T) {
	stack := newOAuth2Stack(t)

	const iss = "https://keycloak.example.test/realms/whagent"
	const sub = "operator-sub-42"
	token := stack.credentials.mint(t, iss, sub)

	res, err := callGetSession(t, stack.url, token)
	require.NoError(t, err)
	require.False(t, res.IsError, "unexpected tool error")

	require.Len(t, stack.grant.calls, 1)
	assert.Equal(t, fakeIntegrationGrantCall{subject: sub, grant: integrationDomain}, stack.grant.calls[0],
		"GrantSource must be keyed on the operator's raw sub and the resolved domain, never the mcpidentity-encoded composite")

	headers := stack.fake.recordedAuthHeaders()
	require.Len(t, headers, 1)
	assert.Equal(t, "Bearer delegated-grant-token-xyz", headers[0], "api must see the token GrantSource acquired, never the opaque credential itself")
	assert.NotContains(t, headers[0], token, "the opaque mcpauth credential itself must never reach api")

	// Red/green (verified by hand, then reverted): temporarily changing
	// AuthMiddleware/dispatch.go to forward the raw presented credential
	// (or identity.Sub itself) instead of grant.TokenSource(...).Token's
	// result made the "Bearer delegated-grant-token-xyz" assertion above
	// fail -- api recorded the raw value instead. Reverting restored it to
	// green.
}

// TestOAuth2_MintedCredential_EachToolCallAcquiresTokenFresh_NoLocalCache
// is NFR7's own requirement, proved through the full stack: two tool
// calls for the identical identity must call GrantSource.TokenSource
// twice -- mcp introduces no cache of its own on top of
// grpcauth.DelegatedGrantSource, which already re-reads its Store on
// every call (libs/go/grpcauth's GrantTokenSource doc comment).
func TestOAuth2_MintedCredential_EachToolCallAcquiresTokenFresh_NoLocalCache(t *testing.T) {
	stack := newOAuth2Stack(t)
	token := stack.credentials.mint(t, "https://keycloak.example.test/realms/whagent", "operator-sub-cache")

	_, err := callGetSession(t, stack.url, token)
	require.NoError(t, err)
	_, err = callGetSession(t, stack.url, token)
	require.NoError(t, err)

	assert.Equal(t, 2, stack.grant.callCount(), "two tool calls for the same identity must acquire a token twice -- mcp must never cache one of its own (NFR7)")
}

// TestOAuth2_MintedCredential_NoGrantForDomain_FailsNamingDomain covers
// FR8's "no grant at all" case: GrantSource returning ErrGrantNotFound
// must fail the tool call, naming the domain, and never fall back to any
// other credential path.
func TestOAuth2_MintedCredential_NoGrantForDomain_FailsNamingDomain(t *testing.T) {
	stack := newOAuth2Stack(t)
	stack.grant.tokenErr = fmt.Errorf("grpcauth: token material for grant %q: %w", integrationDomain, grpcauth.ErrGrantNotFound)
	token := stack.credentials.mint(t, "https://keycloak.example.test/realms/whagent", "operator-sub-no-grant")

	res, err := callGetSession(t, stack.url, token)
	require.NoError(t, err, "a dispatch-time acquisition failure is a tool error, not a transport error")
	require.True(t, res.IsError)
	require.NotEmpty(t, res.Content)
	text, ok := res.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.Contains(t, text.Text, integrationDomain, "the failure must name the domain the operator needs to consent for")

	assert.Empty(t, stack.fake.recordedAuthHeaders(), "api must never be reached when no working credential was acquired")
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
	assert.Empty(t, stack.grant.calls, "GrantSource must never be consulted for a rejected credential")
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
// mcpauth.CredentialStore and real DomainResolver/GrantSource doubles all
// wired in, a manual (non-credential-shaped) bearer token must still be
// forwarded byte for byte, completely bypassing the credential store and
// dispatch-time resolution.
func TestOAuth2_ManualTokenPath_StillWorksWithOAuth2Configured(t *testing.T) {
	stack := newOAuth2Stack(t)

	const manualToken = "operators-own-raw-keycloak-access-token"
	res, err := callGetSession(t, stack.url, manualToken)
	require.NoError(t, err)
	require.False(t, res.IsError)

	headers := stack.fake.recordedAuthHeaders()
	require.Len(t, headers, 1)
	assert.Equal(t, "Bearer "+manualToken, headers[0], "the manual-token path must forward the operator's own token byte for byte, never touching dispatch-time resolution")
	assert.Empty(t, stack.grant.calls, "a manual-token call must never consult GrantSource")
}
