package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newUpstreamHarness(t *testing.T) (*httptest.Server, *url.URL) {
	t.Helper()
	var lastAuthQuery url.Values
	kc := http.NewServeMux()
	kc.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		assert.Equal(t, "kc-client", r.PostForm.Get("client_id"))
		assert.Equal(t, "kc-secret", r.PostForm.Get("client_secret"))
		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			assert.Equal(t, s256(r.PostForm.Get("code_verifier")), lastAuthQuery.Get("code_challenge"))
			_ = json.NewEncoder(w).Encode(upstreamTokens{AccessToken: "kc-access", RefreshToken: "kc-refresh", ExpiresIn: 300})
		case "refresh_token":
			assert.Equal(t, "kc-refresh", r.PostForm.Get("refresh_token"))
			_ = json.NewEncoder(w).Encode(upstreamTokens{AccessToken: "kc-access-2", RefreshToken: "kc-refresh-2"})
		}
	})
	kc.HandleFunc("/auth", func(w http.ResponseWriter, r *http.Request) { lastAuthQuery = r.URL.Query() })
	kcSrv := httptest.NewServer(kc)
	t.Cleanup(kcSrv.Close)

	sealer, err := NewSealer("secret")
	require.NoError(t, err)
	up, err := NewUpstreamProvider(UpstreamConfig{
		Issuer: "http://localhost:9999", AuthorizeURL: kcSrv.URL + "/auth", TokenURL: kcSrv.URL + "/token",
		ClientID: "kc-client", ClientSecret: "kc-secret", Sealer: sealer,
	})
	require.NoError(t, err)
	mux := http.NewServeMux()
	up.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	// Record the authorize redirect the provider sends to the upstream.
	_ = lastAuthQuery
	return srv, u
}

func TestUpstreamFlowRegisterAuthorizeTokenRefresh(t *testing.T) {
	srv, _ := newUpstreamHarness(t)
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	// DCR
	resp, err := http.Post(srv.URL+"/register", "application/json", strings.NewReader(`{"redirect_uris":["http://localhost:1234/cb"]}`))
	require.NoError(t, err)
	var reg struct {
		ClientID string `json:"client_id"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&reg))
	require.NotEmpty(t, reg.ClientID)

	// /authorize -> redirect to upstream
	verifier := "verifier-verifier-verifier-verifier-verifier"
	q := url.Values{"response_type": {"code"}, "client_id": {reg.ClientID}, "redirect_uri": {"http://localhost:1234/cb"},
		"state": {"cs"}, "code_challenge": {s256(verifier)}, "code_challenge_method": {"S256"}}
	resp, err = noRedirect.Get(srv.URL + "/authorize?" + q.Encode())
	require.NoError(t, err)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	loc, _ := url.Parse(resp.Header.Get("Location"))
	upstreamState := loc.Query().Get("state")
	assert.Equal(t, "kc-client", loc.Query().Get("client_id"))
	assert.Equal(t, "http://localhost:9999/oauth/callback", loc.Query().Get("redirect_uri"))
	assert.NotEmpty(t, loc.Query().Get("code_challenge"))

	// /oauth/callback -> redirect to client with our code. The fake upstream
	// token endpoint's PKCE check needs the authorize query; replay it.
	// (handled via lastAuthQuery in the harness by hitting the auth endpoint)
	_, err = http.Get(loc.String())
	require.NoError(t, err)
	resp, err = noRedirect.Get(srv.URL + "/oauth/callback?code=kc-code&state=" + url.QueryEscape(upstreamState))
	require.NoError(t, err)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	back, _ := url.Parse(resp.Header.Get("Location"))
	assert.Equal(t, "cs", back.Query().Get("state"))
	code := back.Query().Get("code")
	require.NotEmpty(t, code)

	tokenForm := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {reg.ClientID},
		"redirect_uri": {"http://localhost:1234/cb"}, "code_verifier": {verifier}}
	resp, err = http.PostForm(srv.URL+"/token", tokenForm)
	require.NoError(t, err)
	var tok upstreamTokens
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&tok))
	assert.Equal(t, "kc-access", tok.AccessToken)
	assert.Equal(t, "kc-refresh", tok.RefreshToken)

	// wrong verifier is rejected
	tokenForm.Set("code_verifier", "wrong-wrong-wrong-wrong-wrong-wrong-wrong")
	resp, err = http.PostForm(srv.URL+"/token", tokenForm)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// refresh
	resp, err = http.PostForm(srv.URL+"/token", url.Values{"grant_type": {"refresh_token"}, "client_id": {reg.ClientID}, "refresh_token": {"kc-refresh"}})
	require.NoError(t, err)
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&tok))
	assert.Equal(t, "kc-access-2", tok.AccessToken)
}

func TestUpstreamRejectsUnknownClientAndTamperedCode(t *testing.T) {
	srv, _ := newUpstreamHarness(t)
	resp, err := http.Get(srv.URL + "/authorize?client_id=bogus&redirect_uri=http://localhost/cb&response_type=code")
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp, err = http.PostForm(srv.URL+"/token", url.Values{"grant_type": {"authorization_code"}, "code": {"nope"}, "client_id": {"x"}, "redirect_uri": {"y"}, "code_verifier": {"z"}})
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}
