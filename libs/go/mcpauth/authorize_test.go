package mcpauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── shared authorize/token test fixtures ────────────────────────────────

// stubResolver is a mutable CallerResolver for tests: set identity/ok
// directly between requests within the same test, unlike
// provider_test.go's fixed always-false CallerResolverFunc.
type stubResolver struct {
	identity string
	ok       bool
}

func (s *stubResolver) ResolveCaller(r *http.Request) (string, bool) {
	return s.identity, s.ok
}

var _ CallerResolver = (*stubResolver)(nil)

// authTestServer bundles everything an authorize_test.go/token_test.go case
// needs: the mounted httptest server, the Provider, and direct handles to
// its (mutable, in-memory) Resolver/Clients/Credentials/AuthCodes so tests
// can drive and inspect state without a second HTTP round trip.
type authTestServer struct {
	srv      *httptest.Server
	provider *Provider
	resolver *stubResolver
	clients  ClientRegistry
	creds    *fakeCredentialStore
	client   *http.Client // never follows redirects — see newAuthTestServer
}

// newAuthTestServer builds a Provider mounted on a fresh httptest.Server,
// exactly like provider_test.go's newTestServer, but with a mutable
// stubResolver (rather than a resolver fixed to always-false) and an
// http.Client configured to stop at the first redirect so tests can inspect
// a 302's Location header directly instead of the browser silently
// following it.
func newAuthTestServer(t *testing.T, mutate func(cfg *ProviderConfig)) *authTestServer {
	t.Helper()

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resolver := &stubResolver{}
	clients := NewMemoryClientRegistry()
	creds := newFakeCredentialStore()

	cfg := ProviderConfig{
		Issuer:       srv.URL,
		Resource:     srv.URL + testResourcePath,
		ResourceName: "Test MCP Resource",
		Resolver:     resolver,
		Credentials:  creds,
		Clients:      clients,
	}
	if mutate != nil {
		mutate(&cfg)
	}

	p, err := NewProvider(cfg)
	require.NoError(t, err)
	p.Mount(mux)

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	return &authTestServer{srv: srv, provider: p, resolver: resolver, clients: clients, creds: creds, client: client}
}

// registerClient registers a test OAuthClient with a single redirect_uri
// and returns both.
func (ts *authTestServer) registerClient(t *testing.T, redirectURI string) OAuthClient {
	t.Helper()
	client, err := ts.clients.Register(context.Background(), oauthex.ClientRegistrationMetadata{
		RedirectURIs: []string{redirectURI},
		ClientName:   "Test Client",
	})
	require.NoError(t, err)
	return client
}

// genPKCEPair returns a random code_verifier and its S256 code_challenge —
// a well-formed PKCE pair for building a valid /authorize request.
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

// authorizeURL builds a GET /authorize URL against ts.srv from the given
// query parameters (any zero-value/omitted parameter is simply left out of
// the query string).
func (ts *authTestServer) authorizeURL(params url.Values) string {
	return ts.srv.URL + "/authorize?" + params.Encode()
}

// doAuthorize issues the GET /authorize request and returns the raw
// *http.Response (redirects are never followed — see newAuthTestServer).
func (ts *authTestServer) doAuthorize(t *testing.T, params url.Values) *http.Response {
	t.Helper()
	resp, err := ts.client.Get(ts.authorizeURL(params))
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// completeAuthorize drives a full, successful /authorize round trip
// (resolved caller, registered client, valid PKCE params) and returns the
// raw authorization code plus everything needed to redeem it at /token.
func (ts *authTestServer) completeAuthorize(t *testing.T, identity, state string) (code, verifier, clientID, redirectURI string) {
	t.Helper()

	redirectURI = "https://client.example.com/callback"
	client := ts.registerClient(t, redirectURI)
	verifier, challenge := genPKCEPair(t)

	ts.resolver.identity = identity
	ts.resolver.ok = true

	params := url.Values{
		"response_type":         {"code"},
		"client_id":             {client.ClientID},
		"redirect_uri":          {redirectURI},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	if state != "" {
		params.Set("state", state)
	}

	resp := ts.doAuthorize(t, params)
	require.Equal(t, http.StatusFound, resp.StatusCode)

	loc, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	code = loc.Query().Get("code")
	require.NotEmpty(t, code, "successful /authorize must issue a code")

	return code, verifier, client.ClientID, redirectURI
}

// ── GET /authorize ──────────────────────────────────────────────────────

func TestAuthorize_HappyPath_IssuesCodeAndEchoesState(t *testing.T) {
	ts := newAuthTestServer(t, nil)

	redirectURI := "https://client.example.com/callback"
	client := ts.registerClient(t, redirectURI)
	_, challenge := genPKCEPair(t)

	ts.resolver.identity = "person-1"
	ts.resolver.ok = true

	resp := ts.doAuthorize(t, url.Values{
		"response_type":         {"code"},
		"client_id":             {client.ClientID},
		"redirect_uri":          {redirectURI},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"state":                 {"xyz123"},
	})

	require.Equal(t, http.StatusFound, resp.StatusCode)
	loc, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	assert.True(t, loc.String() != "" && loc.Scheme == "https" && loc.Host == "client.example.com" && loc.Path == "/callback")
	assert.NotEmpty(t, loc.Query().Get("code"))
	assert.Equal(t, "xyz123", loc.Query().Get("state"), "state must be echoed back byte-for-byte")
}

func TestAuthorize_UnresolvedCaller_SignInURLSet_RedirectsWithReturnTo(t *testing.T) {
	ts := newAuthTestServer(t, func(cfg *ProviderConfig) {
		cfg.SignInURL = "https://consuming-domain.example.com/sign-in"
	})
	ts.resolver.ok = false

	redirectURI := "https://client.example.com/callback"
	client := ts.registerClient(t, redirectURI)
	_, challenge := genPKCEPair(t)

	params := url.Values{
		"response_type":         {"code"},
		"client_id":             {client.ClientID},
		"redirect_uri":          {redirectURI},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"state":                 {"preserve-me"},
	}
	resp := ts.doAuthorize(t, params)

	require.Equal(t, http.StatusFound, resp.StatusCode)
	loc, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "consuming-domain.example.com", loc.Host)
	assert.Equal(t, "/sign-in", loc.Path)

	returnTo := loc.Query().Get("return_to")
	require.NotEmpty(t, returnTo)
	returnToURL, err := url.Parse(returnTo)
	require.NoError(t, err)
	assert.Equal(t, ts.srv.URL+"/authorize", returnToURL.Scheme+"://"+returnToURL.Host+returnToURL.Path)
	assert.Equal(t, params.Encode(), returnToURL.RawQuery, "return_to must carry the exact original authorize query string")

	// No code was issued.
	mem, ok := ts.provider.cfg.AuthCodes.(*memoryAuthCodeStore)
	require.True(t, ok)
	assert.Empty(t, mem.codes, "an unresolved caller must never result in an issued authorization code")
}

func TestAuthorize_UnresolvedCaller_NoSignInURL_Returns401(t *testing.T) {
	ts := newAuthTestServer(t, nil)
	ts.resolver.ok = false

	redirectURI := "https://client.example.com/callback"
	client := ts.registerClient(t, redirectURI)
	_, challenge := genPKCEPair(t)

	resp := ts.doAuthorize(t, url.Values{
		"response_type":         {"code"},
		"client_id":             {client.ClientID},
		"redirect_uri":          {redirectURI},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	})

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("Location"))

	mem, ok := ts.provider.cfg.AuthCodes.(*memoryAuthCodeStore)
	require.True(t, ok)
	assert.Empty(t, mem.codes)
}

func TestAuthorize_UnknownClientID_RendersDirectErrorNoRedirect(t *testing.T) {
	ts := newAuthTestServer(t, nil)
	ts.resolver.identity = "person-1"
	ts.resolver.ok = true

	_, challenge := genPKCEPair(t)
	resp := ts.doAuthorize(t, url.Values{
		"response_type":         {"code"},
		"client_id":             {"never-registered-client-id"},
		"redirect_uri":          {"https://attacker.example.com/callback"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	})

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("Location"), "an unknown client_id must never produce a redirect (open-redirect guard)")
}

func TestAuthorize_UnregisteredRedirectURI_RendersDirectErrorNoRedirect(t *testing.T) {
	ts := newAuthTestServer(t, nil)
	ts.resolver.identity = "person-1"
	ts.resolver.ok = true

	registeredRedirect := "https://client.example.com/callback"
	client := ts.registerClient(t, registeredRedirect)
	_, challenge := genPKCEPair(t)

	resp := ts.doAuthorize(t, url.Values{
		"response_type":         {"code"},
		"client_id":             {client.ClientID},
		"redirect_uri":          {"https://attacker.example.com/callback"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	})

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("Location"), "an unregistered redirect_uri must never produce a redirect (open-redirect guard)")
}

func TestAuthorize_RedirectURIPrefixMatch_IsRejected(t *testing.T) {
	// Guards the "exact string match only" requirement: a redirect_uri that
	// is merely a prefix/substring of a registered one must be rejected the
	// same way an entirely unregistered one is.
	ts := newAuthTestServer(t, nil)
	ts.resolver.identity = "person-1"
	ts.resolver.ok = true

	registeredRedirect := "https://client.example.com/callback"
	client := ts.registerClient(t, registeredRedirect)
	_, challenge := genPKCEPair(t)

	resp := ts.doAuthorize(t, url.Values{
		"response_type":         {"code"},
		"client_id":             {client.ClientID},
		"redirect_uri":          {registeredRedirect + "/extra"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	})

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("Location"))
}

func TestAuthorize_InvalidParams_RedirectWithError(t *testing.T) {
	cases := map[string]func(v url.Values){
		"response_type=token":         func(v url.Values) { v.Set("response_type", "token") },
		"code_challenge_method=plain": func(v url.Values) { v.Set("code_challenge_method", "plain") },
		"missing code_challenge":      func(v url.Values) { v.Del("code_challenge") },
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			ts := newAuthTestServer(t, nil)
			ts.resolver.identity = "person-1"
			ts.resolver.ok = true

			redirectURI := "https://client.example.com/callback"
			client := ts.registerClient(t, redirectURI)
			_, challenge := genPKCEPair(t)

			params := url.Values{
				"response_type":         {"code"},
				"client_id":             {client.ClientID},
				"redirect_uri":          {redirectURI},
				"code_challenge":        {challenge},
				"code_challenge_method": {"S256"},
				"state":                 {"keep-me"},
			}
			mutate(params)

			resp := ts.doAuthorize(t, params)

			require.Equal(t, http.StatusFound, resp.StatusCode, "%s must redirect (client_id/redirect_uri already validated)", name)
			loc, err := url.Parse(resp.Header.Get("Location"))
			require.NoError(t, err)
			assert.NotEmpty(t, loc.Query().Get("error"), "%s must carry an error= param", name)
			assert.Equal(t, "keep-me", loc.Query().Get("state"), "%s must still echo state on error", name)
			assert.Empty(t, loc.Query().Get("code"), "%s must not issue a code", name)
		})
	}
}
