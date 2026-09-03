package mcpauth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── ClientRegistry (memory) ─────────────────────────────────────────────

func TestMemoryClientRegistry_RegisterThenGet_RoundTrips(t *testing.T) {
	ctx := context.Background()
	reg := NewMemoryClientRegistry()

	meta := oauthex.ClientRegistrationMetadata{
		RedirectURIs: []string{"https://client.example.com/callback"},
		ClientName:   "Test Client",
	}
	client, err := reg.Register(ctx, meta)
	require.NoError(t, err)
	assert.NotEmpty(t, client.ClientID)
	assert.Equal(t, meta.RedirectURIs, client.RedirectURIs)
	assert.False(t, client.CreatedAt.IsZero())

	got, err := reg.Get(ctx, client.ClientID)
	require.NoError(t, err)
	assert.Equal(t, client.ClientID, got.ClientID)
	assert.Equal(t, meta.RedirectURIs, got.RedirectURIs)
}

func TestMemoryClientRegistry_Get_UnknownClientID_ReturnsErrClientNotFound(t *testing.T) {
	ctx := context.Background()
	reg := NewMemoryClientRegistry()

	_, err := reg.Get(ctx, "never-registered")
	assert.ErrorIs(t, err, ErrClientNotFound)
}

func TestMemoryClientRegistry_TwoRegistrations_GetDistinctClientIDs(t *testing.T) {
	ctx := context.Background()
	reg := NewMemoryClientRegistry()
	meta := oauthex.ClientRegistrationMetadata{RedirectURIs: []string{"https://client.example.com/callback"}}

	c1, err := reg.Register(ctx, meta)
	require.NoError(t, err)
	c2, err := reg.Register(ctx, meta)
	require.NoError(t, err)

	assert.NotEqual(t, c1.ClientID, c2.ClientID)
}

func TestMemoryClientRegistry_IsolatedAcrossInstances(t *testing.T) {
	// Documents the trade-off README.md states: a client registered
	// against one memoryClientRegistry instance is unknown to another
	// (mirrors "replica A" vs "replica B").
	ctx := context.Background()
	regA := NewMemoryClientRegistry()
	regB := NewMemoryClientRegistry()

	client, err := regA.Register(ctx, oauthex.ClientRegistrationMetadata{RedirectURIs: []string{"https://client.example.com/callback"}})
	require.NoError(t, err)

	_, err = regB.Get(ctx, client.ClientID)
	assert.ErrorIs(t, err, ErrClientNotFound)
}

// ── generateClientID ─────────────────────────────────────────────────────

func TestGenerateClientID_ProducesDistinctNonEmptyValues(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 200; i++ {
		id, err := generateClientID()
		require.NoError(t, err)
		require.NotEmpty(t, id)
		require.False(t, seen[id], "generateClientID must not repeat across calls")
		seen[id] = true
	}
}

// ── validateRedirectURI ──────────────────────────────────────────────────

func TestValidateRedirectURI_AcceptsAllowedShapes(t *testing.T) {
	valid := []string{
		"https://client.example.com/callback",
		"http://127.0.0.1:51000/callback",
		"http://localhost:51000/callback",
		"com.example.app:/callback", // private-use custom scheme (native app)
		"myapp://callback",
	}
	for _, uri := range valid {
		t.Run(uri, func(t *testing.T) {
			assert.NoError(t, validateRedirectURI(uri))
		})
	}
}

func TestValidateRedirectURI_RejectsDisallowedShapes(t *testing.T) {
	invalid := []string{
		"",
		"not-a-url",
		"javascript:alert(document.cookie)",
		"data:text/html,<script>alert(1)</script>",
		"vbscript:msgbox(1)",
		"http://evil.example.com/callback", // non-loopback http
		"https://",                         // no host
	}
	for _, uri := range invalid {
		t.Run(uri, func(t *testing.T) {
			assert.Error(t, validateRedirectURI(uri))
		})
	}
}

// ── POST /register (handleRegister, via a mounted Provider) ─────────────

func TestRegister_HappyPath_Returns201WithClientIDNoSecretEchoedRedirects(t *testing.T) {
	srv, _ := newTestServer(t)

	redirects := []string{"https://client.example.com/callback"}
	body, err := json.Marshal(oauthex.ClientRegistrationMetadata{RedirectURIs: redirects, ClientName: "My Client"})
	require.NoError(t, err)

	resp, err := http.Post(srv.URL+registerPath, "application/json", strings.NewReader(string(body)))
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusCreated, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	var reg oauthex.ClientRegistrationResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&reg))
	assert.NotEmpty(t, reg.ClientID)
	assert.Empty(t, reg.ClientSecret, "mcpauth issues no client secret — public PKCE clients only")
	assert.Equal(t, redirects, reg.RedirectURIs)
	assert.Equal(t, "none", reg.TokenEndpointAuthMethod, "token_endpoint_auth_method must be forced to none")
}

func TestRegister_RegisteredClient_IsRetrievableViaGet(t *testing.T) {
	srv, p := newTestServer(t)

	body := `{"redirect_uris": ["https://client.example.com/callback"]}`
	resp, err := http.Post(srv.URL+registerPath, "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var reg oauthex.ClientRegistrationResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&reg))

	got, err := p.cfg.Clients.Get(context.Background(), reg.ClientID)
	require.NoError(t, err)
	assert.Equal(t, reg.ClientID, got.ClientID)
	assert.Equal(t, []string{"https://client.example.com/callback"}, got.RedirectURIs)
}

func TestRegister_TwoRegistrations_GetDistinctClientIDs(t *testing.T) {
	srv, _ := newTestServer(t)
	body := `{"redirect_uris": ["https://client.example.com/callback"]}`

	resp1, err := http.Post(srv.URL+registerPath, "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp1.Body.Close()
	var reg1 oauthex.ClientRegistrationResponse
	require.NoError(t, json.NewDecoder(resp1.Body).Decode(&reg1))

	resp2, err := http.Post(srv.URL+registerPath, "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp2.Body.Close()
	var reg2 oauthex.ClientRegistrationResponse
	require.NoError(t, json.NewDecoder(resp2.Body).Decode(&reg2))

	assert.NotEqual(t, reg1.ClientID, reg2.ClientID)
}

// registerRejectionCase names each documented RFC 7591 rejection case and
// its expected error code, so the loop below both exercises the request
// and asserts the exact body shape (no Go error text, correct code).
type registerRejectionCase struct {
	name        string
	body        string
	wantCode    string
	contentType string
}

func TestRegister_RejectsBadInput_WithRFC7591ErrorBodyNoGoErrorText(t *testing.T) {
	srv, _ := newTestServer(t)

	cases := []registerRejectionCase{
		{
			name:        "empty redirect_uris",
			body:        `{"redirect_uris": []}`,
			wantCode:    "invalid_client_metadata",
			contentType: "application/json",
		},
		{
			name:        "javascript redirect_uri",
			body:        `{"redirect_uris": ["javascript:alert(1)"]}`,
			wantCode:    "invalid_redirect_uri",
			contentType: "application/json",
		},
		{
			name:        "non-loopback http redirect_uri",
			body:        `{"redirect_uris": ["http://evil.example.com/callback"]}`,
			wantCode:    "invalid_redirect_uri",
			contentType: "application/json",
		},
		{
			name:        "malformed JSON",
			body:        `{not-json`,
			wantCode:    "invalid_client_metadata",
			contentType: "application/json",
		},
	}

	// Go stdlib error substrings that must never leak into a client-facing
	// error body — proof that handleRegister never does `err.Error()` on a
	// raw parse error.
	goErrorSubstrings := []string{"json:", "net/url:", "invalid character", "unexpected end of JSON"}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Post(srv.URL+registerPath, "application/json", strings.NewReader(tc.body))
			require.NoError(t, err)
			defer resp.Body.Close()

			require.Equal(t, http.StatusBadRequest, resp.StatusCode)
			assert.Equal(t, tc.contentType, resp.Header.Get("Content-Type"))

			raw := mustReadAll(t, resp)
			var regErr oauthex.ClientRegistrationError
			require.NoError(t, json.Unmarshal([]byte(raw), &regErr))
			assert.Equal(t, tc.wantCode, regErr.ErrorCode)

			for _, substr := range goErrorSubstrings {
				assert.NotContains(t, raw, substr, "error body must not leak a raw Go stdlib error string")
			}
		})
	}
}

func TestRegister_MethodNotAllowed_ForGET(t *testing.T) {
	srv, _ := newTestServer(t)

	resp, err := http.Get(srv.URL + registerPath)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.NotEqual(t, http.StatusNotFound, resp.StatusCode, "route must be registered (drift guard)")
}
