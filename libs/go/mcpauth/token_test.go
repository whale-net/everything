package mcpauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// doToken POSTs application/x-www-form-urlencoded form to ts.srv's /token
// endpoint.
func (ts *authTestServer) doToken(t *testing.T, form url.Values) *http.Response {
	t.Helper()
	resp, err := ts.client.PostForm(ts.srv.URL+"/token", form)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func decodeJSONBody(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	return body
}

// readRawBody reads resp.Body fully as a string, for byte-identical
// comparison across failure modes.
func readRawBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return sb.String()
}

// ── POST /token happy path ──────────────────────────────────────────────

func TestToken_HappyPath_ExchangesCodeForCredential_NoRefreshLifecycleLeaked(t *testing.T) {
	ts := newAuthTestServer(t, nil)
	code, verifier, clientID, redirectURI := ts.completeAuthorize(t, "person-1", "")

	resp := ts.doToken(t, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"client_id":     {clientID},
		"redirect_uri":  {redirectURI},
	})

	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))

	body := decodeJSONBody(t, resp)
	assert.Equal(t, "Bearer", body["token_type"])
	accessToken, _ := body["access_token"].(string)
	assert.NotEmpty(t, accessToken)

	// FR4: exactly access_token + token_type — no expires_in, no
	// refresh_token, no fabricated scope.
	_, hasExpiresIn := body["expires_in"]
	_, hasRefreshToken := body["refresh_token"]
	_, hasScope := body["scope"]
	assert.False(t, hasExpiresIn, "response must not contain expires_in")
	assert.False(t, hasRefreshToken, "response must not contain refresh_token")
	assert.False(t, hasScope, "response must not fabricate a scope")
	assert.Len(t, body, 2, "response must contain exactly access_token and token_type")

	// End-to-end FR1→FR4 chain: the minted token verifies back to the same
	// identity the resolver returned at /authorize.
	identity, _, err := ts.creds.Verify(context.Background(), accessToken)
	require.NoError(t, err)
	assert.Equal(t, "person-1", identity)
}

// ── POST /token failure modes: byte-identical invalid_grant ────────────

func TestToken_FailureModes_AreByteIdenticalInvalidGrant(t *testing.T) {
	var firstBody string

	assertInvalidGrant := func(t *testing.T, resp *http.Response) {
		t.Helper()
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		raw := readRawBody(t, resp)
		if firstBody == "" {
			firstBody = raw
		} else {
			assert.Equal(t, firstBody, raw, "every invalid_grant failure mode must render a byte-identical body")
		}
		assert.Contains(t, raw, `"invalid_grant"`)
	}

	t.Run("unknown code", func(t *testing.T) {
		ts := newAuthTestServer(t, nil)
		redirectURI := "https://client.example.com/callback"
		client := ts.registerClient(t, redirectURI)

		resp := ts.doToken(t, url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {"never-issued-code"},
			"code_verifier": {"whatever-verifier"},
			"client_id":     {client.ClientID},
			"redirect_uri":  {redirectURI},
		})
		assertInvalidGrant(t, resp)
	})

	t.Run("wrong code_verifier", func(t *testing.T) {
		ts := newAuthTestServer(t, nil)
		code, _, clientID, redirectURI := ts.completeAuthorize(t, "person-1", "")

		resp := ts.doToken(t, url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {code},
			"code_verifier": {"totally-wrong-verifier"},
			"client_id":     {clientID},
			"redirect_uri":  {redirectURI},
		})
		assertInvalidGrant(t, resp)
	})

	t.Run("mismatched redirect_uri", func(t *testing.T) {
		ts := newAuthTestServer(t, nil)
		code, verifier, clientID, _ := ts.completeAuthorize(t, "person-1", "")

		resp := ts.doToken(t, url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {code},
			"code_verifier": {verifier},
			"client_id":     {clientID},
			"redirect_uri":  {"https://different.example.com/callback"},
		})
		assertInvalidGrant(t, resp)
	})

	t.Run("mismatched client_id", func(t *testing.T) {
		ts := newAuthTestServer(t, nil)
		code, verifier, _, redirectURI := ts.completeAuthorize(t, "person-1", "")
		otherClient := ts.registerClient(t, "https://other.example.com/callback")

		resp := ts.doToken(t, url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {code},
			"code_verifier": {verifier},
			"client_id":     {otherClient.ClientID},
			"redirect_uri":  {redirectURI},
		})
		assertInvalidGrant(t, resp)
	})

	t.Run("expired code", func(t *testing.T) {
		ts := newAuthTestServer(t, func(cfg *ProviderConfig) {
			cfg.AuthCodeTTL = 1 * time.Millisecond
		})
		code, verifier, clientID, redirectURI := ts.completeAuthorize(t, "person-1", "")
		time.Sleep(10 * time.Millisecond)

		resp := ts.doToken(t, url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {code},
			"code_verifier": {verifier},
			"client_id":     {clientID},
			"redirect_uri":  {redirectURI},
		})
		assertInvalidGrant(t, resp)
	})
}

// ── replay ───────────────────────────────────────────────────────────────

func TestToken_Replay_SecondExchangeFailsAndMintsNoSecondCredential(t *testing.T) {
	ts := newAuthTestServer(t, nil)
	code, verifier, clientID, redirectURI := ts.completeAuthorize(t, "person-1", "")

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"client_id":     {clientID},
		"redirect_uri":  {redirectURI},
	}

	first := ts.doToken(t, form)
	require.Equal(t, http.StatusOK, first.StatusCode)
	body := decodeJSONBody(t, first)
	firstToken, _ := body["access_token"].(string)
	require.NotEmpty(t, firstToken)

	second := ts.doToken(t, form)
	assert.Equal(t, http.StatusBadRequest, second.StatusCode)
	raw := readRawBody(t, second)
	assert.Contains(t, raw, `"invalid_grant"`)

	// Exactly one credential was minted for this code — the replay must not
	// mint a second one.
	assert.Len(t, ts.creds.byToken, 1)
}

// ── unsupported grant types ──────────────────────────────────────────────

func TestToken_UnsupportedGrantType_Rejected(t *testing.T) {
	cases := []string{"refresh_token", "client_credentials", "password", "implicit", ""}

	for _, grantType := range cases {
		t.Run("grant_type="+grantType, func(t *testing.T) {
			ts := newAuthTestServer(t, nil)
			redirectURI := "https://client.example.com/callback"
			client := ts.registerClient(t, redirectURI)

			resp := ts.doToken(t, url.Values{
				"grant_type":    {grantType},
				"code":          {"whatever"},
				"code_verifier": {"whatever"},
				"client_id":     {client.ClientID},
				"redirect_uri":  {redirectURI},
			})

			require.Equal(t, http.StatusBadRequest, resp.StatusCode)
			raw := readRawBody(t, resp)
			assert.Contains(t, raw, `"unsupported_grant_type"`)
		})
	}
}
