package mcpauth

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── protected-resource metadata (RFC 9728) ──────────────────────────────

func TestProtectedResourceMetadata_ExactShape(t *testing.T) {
	srv, _ := newTestServer(t)

	resp, err := http.Get(srv.URL + protectedResourceMetadataPath)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "*", resp.Header.Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	var meta oauthex.ProtectedResourceMetadata
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&meta))

	assert.Equal(t, srv.URL+testResourcePath, meta.Resource)
	assert.Equal(t, []string{srv.URL}, meta.AuthorizationServers, "authorization_servers must contain exactly the issuer")
	assert.Equal(t, []string{"mcp"}, meta.ScopesSupported)
	assert.Equal(t, []string{"header"}, meta.BearerMethodsSupported)
	assert.Equal(t, "Test MCP Resource", meta.ResourceName)
}

func TestProtectedResourceMetadata_OPTIONS_Returns204(t *testing.T) {
	srv, _ := newTestServer(t)

	req, err := http.NewRequest(http.MethodOptions, srv.URL+protectedResourceMetadataPath, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.Equal(t, "*", resp.Header.Get("Access-Control-Allow-Origin"))
}

// ── authorization-server metadata (RFC 8414) ────────────────────────────

func TestAuthServerMetadata_ExactShape(t *testing.T) {
	srv, _ := newTestServer(t)

	resp, err := http.Get(srv.URL + authServerMetadataPath)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "*", resp.Header.Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	var meta authServerMetadata
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&meta))

	assert.Equal(t, srv.URL, meta.Issuer)
	assert.Equal(t, srv.URL+authorizePath, meta.AuthorizationEndpoint)
	assert.Equal(t, srv.URL+tokenPath, meta.TokenEndpoint)
	assert.Equal(t, srv.URL+registerPath, meta.RegistrationEndpoint)
	assert.Equal(t, []string{"code"}, meta.ResponseTypesSupported)
	assert.Equal(t, []string{"authorization_code"}, meta.GrantTypesSupported)
	assert.Equal(t, []string{"none"}, meta.TokenEndpointAuthMethodsSupported)
	assert.Equal(t, []string{"S256"}, meta.CodeChallengeMethodsSupported, "S256 only — plain must never be advertised")
}

func TestAuthServerMetadata_JWKSURI_NeverPresent(t *testing.T) {
	// mcpauth issues opaque credentials and has no JWKS document (see the
	// "Resolved: JWKSURI" doc comment in metadata.go) — the field must be
	// entirely absent from the wire body, not present-but-empty.
	srv, _ := newTestServer(t)

	resp, err := http.Get(srv.URL + authServerMetadataPath)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.NotContains(t, mustReadAll(t, resp), "jwks_uri")
}

func TestAuthServerMetadata_NoPlainPKCEMethodAdvertisedAnywhere(t *testing.T) {
	srv, _ := newTestServer(t)

	resp, err := http.Get(srv.URL + authServerMetadataPath)
	require.NoError(t, err)
	defer resp.Body.Close()

	raw := mustReadAll(t, resp)
	assert.NotContains(t, strings.ToLower(raw), `"plain"`, "authorization-server metadata must never advertise the plain PKCE method")
}

func TestAuthServerMetadata_OPTIONS_Returns204(t *testing.T) {
	srv, _ := newTestServer(t)

	req, err := http.NewRequest(http.MethodOptions, srv.URL+authServerMetadataPath, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.Equal(t, "*", resp.Header.Get("Access-Control-Allow-Origin"))
}

func TestAuthServerMetadata_MethodNotAllowed(t *testing.T) {
	srv, _ := newTestServer(t)

	resp, err := http.Post(srv.URL+authServerMetadataPath, "application/json", nil)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}

// TestAuthServerMetadata_AdvertisedEndpointsResolve is the metadata/route
// drift guard: every URL authServerMetadataDoc() advertises must be
// absolute and resolve to a path Mount actually registered (probed by
// issuing a request to each path and asserting it is not 404).
func TestAuthServerMetadata_AdvertisedEndpointsResolve(t *testing.T) {
	srv, _ := newTestServer(t)

	resp, err := http.Get(srv.URL + authServerMetadataPath)
	require.NoError(t, err)
	defer resp.Body.Close()

	var meta authServerMetadata
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&meta))

	endpoints := map[string]string{
		"authorization_endpoint": meta.AuthorizationEndpoint,
		"token_endpoint":         meta.TokenEndpoint,
		"registration_endpoint":  meta.RegistrationEndpoint,
	}
	for name, endpointURL := range endpoints {
		t.Run(name, func(t *testing.T) {
			require.NotEmpty(t, endpointURL)
			require.True(t, strings.HasPrefix(endpointURL, srv.URL), "%s must be absolute and under the issuer: %q", name, endpointURL)

			// GET is enough to prove the path is routed (not 404) — the
			// registration endpoint's real 405/400 behavior for GET is
			// covered by clients_test.go, this test only cares that Mount
			// actually wired the path metadata advertises.
			r, err := http.Get(endpointURL)
			require.NoError(t, err)
			defer r.Body.Close()
			assert.NotEqual(t, http.StatusNotFound, r.StatusCode, "%s (%q) must be routed by Mount", name, endpointURL)
		})
	}
}

// ── full discovery-to-registration bootstrap chain (NFR4) ──────────────

// TestBootstrapChain_ProtectedResourceToAuthServerToRegister exercises the
// exact sequence NFR4 names: an MCP client fetches protected-resource
// metadata, follows authorization_servers[0] to authorization-server
// metadata, then POSTs to the advertised registration_endpoint — all in
// one test, so a break anywhere in that chain (not just an individual
// endpoint) is caught.
func TestBootstrapChain_ProtectedResourceToAuthServerToRegister(t *testing.T) {
	srv, _ := newTestServer(t)

	// Step 1: protected-resource metadata.
	resp1, err := http.Get(srv.URL + protectedResourceMetadataPath)
	require.NoError(t, err)
	var resourceMeta oauthex.ProtectedResourceMetadata
	require.NoError(t, json.NewDecoder(resp1.Body).Decode(&resourceMeta))
	resp1.Body.Close()
	require.NotEmpty(t, resourceMeta.AuthorizationServers)

	// Step 2: follow authorization_servers[0] to authorization-server
	// metadata.
	issuer := resourceMeta.AuthorizationServers[0]
	resp2, err := http.Get(issuer + authServerMetadataPath)
	require.NoError(t, err)
	var authMeta authServerMetadata
	require.NoError(t, json.NewDecoder(resp2.Body).Decode(&authMeta))
	resp2.Body.Close()
	require.NotEmpty(t, authMeta.RegistrationEndpoint)

	// Step 3: dynamically register against the advertised
	// registration_endpoint.
	body := strings.NewReader(`{"redirect_uris": ["http://127.0.0.1:12345/callback"]}`)
	resp3, err := http.Post(authMeta.RegistrationEndpoint, "application/json", body)
	require.NoError(t, err)
	defer resp3.Body.Close()

	require.Equal(t, http.StatusCreated, resp3.StatusCode)
	var reg oauthex.ClientRegistrationResponse
	require.NoError(t, json.NewDecoder(resp3.Body).Decode(&reg))
	assert.NotEmpty(t, reg.ClientID)
	assert.Empty(t, reg.ClientSecret)
}

func mustReadAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(b)
}
