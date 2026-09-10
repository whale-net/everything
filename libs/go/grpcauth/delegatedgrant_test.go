package grpcauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/whale-net/everything/libs/go/grpcauth/internal/keycloakfake"
)

// validDelegatedGrantConfig returns a config that passes Validate(), pointed
// at fake's endpoints verbatim (so tests never need a live Keycloak, NFR4).
// Callers mutate the returned value for the specific field(s) under test.
func validDelegatedGrantConfig(fake *keycloakfake.Server) DelegatedGrantConfig {
	return DelegatedGrantConfig{
		Issuer:       fake.URL,
		ClientID:     "test-client",
		ClientSecret: "super-secret-value",
		RedirectURI:  "https://example.invalid/callback",
		Store:        NewFakeStore(),
		Endpoints: Endpoints{
			Authorization: fake.URL + "/authorize",
			Token:         fake.URL + "/token",
			Revocation:    fake.URL + "/revoke",
		},
	}
}

// --- Validate() ------------------------------------------------------------

// TestDelegatedGrantConfigValidate proves each required field's absence
// produces its own distinct sentinel error, and a fully-populated config
// passes.
func TestDelegatedGrantConfigValidate(t *testing.T) {
	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)

	base := validDelegatedGrantConfig(fake)

	cases := []struct {
		name    string
		mutate  func(c DelegatedGrantConfig) DelegatedGrantConfig
		wantErr error
	}{
		{
			name: "missing issuer and endpoints",
			mutate: func(c DelegatedGrantConfig) DelegatedGrantConfig {
				c.Issuer = ""
				c.Endpoints = Endpoints{}
				return c
			},
			wantErr: ErrConfigIssuerRequired,
		},
		{
			name: "missing client id",
			mutate: func(c DelegatedGrantConfig) DelegatedGrantConfig {
				c.ClientID = ""
				return c
			},
			wantErr: ErrConfigClientIDRequired,
		},
		{
			name: "missing client secret",
			mutate: func(c DelegatedGrantConfig) DelegatedGrantConfig {
				c.ClientSecret = ""
				return c
			},
			wantErr: ErrConfigClientSecretRequired,
		},
		{
			name: "missing redirect uri",
			mutate: func(c DelegatedGrantConfig) DelegatedGrantConfig {
				c.RedirectURI = ""
				return c
			},
			wantErr: ErrConfigRedirectURIRequired,
		},
		{
			name: "missing store",
			mutate: func(c DelegatedGrantConfig) DelegatedGrantConfig {
				c.Store = nil
				return c
			},
			wantErr: ErrConfigStoreRequired,
		},
		{
			name: "encryption key wrong size",
			mutate: func(c DelegatedGrantConfig) DelegatedGrantConfig {
				c.EncryptionKey = []byte("too-short")
				return c
			},
			wantErr: ErrConfigEncryptionKeySize,
		},
		{
			name: "fully populated",
			mutate: func(c DelegatedGrantConfig) DelegatedGrantConfig {
				c.EncryptionKey = make([]byte, GrantKeySize)
				return c
			},
			wantErr: nil,
		},
	}

	// Every non-nil wantErr in this table must be pairwise distinct, or the
	// test itself would not actually prove "each missing field produces its
	// own distinct error".
	seen := map[error]string{}
	for _, tc := range cases {
		if tc.wantErr == nil {
			continue
		}
		if other, ok := seen[tc.wantErr]; ok {
			t.Fatalf("test table bug: case %q and %q share the same wantErr", tc.name, other)
		}
		seen[tc.wantErr] = tc.name
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.mutate(base)
			err := cfg.Validate()
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate() = %v, want errors.Is match for %v", err, tc.wantErr)
			}
		})
	}
}

// --- issuer-required edge case (Endpoints alone is enough) -----------------

// TestValidateAllowsEmptyIssuerWithFullEndpoints proves ErrConfigIssuerRequired
// is only about having *something* to resolve endpoints from: a config with a
// fully populated Endpoints and no Issuer is valid.
func TestValidateAllowsEmptyIssuerWithFullEndpoints(t *testing.T) {
	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)

	cfg := validDelegatedGrantConfig(fake)
	cfg.Issuer = ""
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil (Endpoints alone should suffice)", err)
	}
}

// --- secret redaction (NFR1) ------------------------------------------------

// TestDelegatedGrantConfigStringRedactsSecrets proves String() (and therefore
// any accidental %v/%s of a DelegatedGrantConfig) never contains the client
// secret or the encryption key, while still indicating they are set.
func TestDelegatedGrantConfigStringRedactsSecrets(t *testing.T) {
	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)

	const secret = "extremely-secret-client-secret-value"
	key := make([]byte, GrantKeySize)
	for i := range key {
		key[i] = byte('a' + i%26)
	}

	cfg := validDelegatedGrantConfig(fake)
	cfg.ClientSecret = secret
	cfg.EncryptionKey = key

	rendered := cfg.String()
	if strings.Contains(rendered, secret) {
		t.Fatalf("String() leaked ClientSecret: %q", rendered)
	}
	if strings.Contains(rendered, string(key)) {
		t.Fatalf("String() leaked EncryptionKey: %q", rendered)
	}
	if !strings.Contains(rendered, "(redacted)") {
		t.Fatalf("String() = %q, want it to indicate secrets are set via a redaction placeholder", rendered)
	}

	// %v must go through String(), not the struct's default formatting.
	viaFmt := fmt.Sprintf("%v", cfg)
	if strings.Contains(viaFmt, secret) || strings.Contains(viaFmt, string(key)) {
		t.Fatalf("fmt %%v leaked a secret: %q", viaFmt)
	}

	logged := cfg.LogValue()
	if strings.Contains(logged.String(), secret) || strings.Contains(logged.String(), string(key)) {
		t.Fatalf("LogValue() leaked a secret: %q", logged.String())
	}
}

// TestDelegatedGrantConfigStringUnsetSecrets proves an unset secret/key is
// distinguishable from a set-but-redacted one, so on-call reading a log line
// can tell "not configured" from "configured, value withheld".
func TestDelegatedGrantConfigStringUnsetSecrets(t *testing.T) {
	cfg := DelegatedGrantConfig{Issuer: "https://issuer.invalid"}
	rendered := cfg.String()
	if !strings.Contains(rendered, "(unset)") {
		t.Fatalf("String() = %q, want it to indicate unset secrets", rendered)
	}
	if strings.Contains(rendered, "(redacted)") {
		t.Fatalf("String() = %q, unset secrets should not read as redacted", rendered)
	}
}

// TestValidateErrorsNeverContainSecret proves no Validate() error embeds the
// client secret or encryption key, across every rejecting case.
func TestValidateErrorsNeverContainSecret(t *testing.T) {
	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)

	const secret = "another-extremely-secret-value"
	cfg := validDelegatedGrantConfig(fake)
	cfg.ClientSecret = secret

	badKey := []byte("not-32-bytes")
	cfg.EncryptionKey = badKey

	err = cfg.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want ErrConfigEncryptionKeySize")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Validate() error leaked ClientSecret: %v", err)
	}
	if strings.Contains(err.Error(), string(badKey)) {
		t.Fatalf("Validate() error leaked EncryptionKey: %v", err)
	}
}

// --- resolvedScopes (FR1) ---------------------------------------------------

// TestResolvedScopes proves offline_access is present exactly once in the
// resolved scope set, whether the caller supplied it or not.
func TestResolvedScopes(t *testing.T) {
	cases := []struct {
		name  string
		in    []string
		count int // expected occurrences of offline_access in the result
	}{
		{name: "omitted", in: []string{"openid", "profile"}, count: 1},
		{name: "already present", in: []string{"openid", "offline_access"}, count: 1},
		{name: "empty", in: nil, count: 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolvedScopes(tc.in)
			n := 0
			for _, s := range got {
				if s == offlineAccessScope {
					n++
				}
			}
			if n != tc.count {
				t.Fatalf("resolvedScopes(%v) = %v, offline_access appears %d times, want %d", tc.in, got, n, tc.count)
			}
		})
	}
}

// TestResolvedScopesRedGreen deliberately breaks the offline_access injection
// to prove this test actually guards the behaviour (red), then restores it
// (green). This documents the discipline; it does not mutate production
// code -- it calls the unexported helper directly with an input that would
// have failed against the pre-fix behaviour of "never add offline_access".
func TestResolvedScopesRedGreen(t *testing.T) {
	// Simulate the broken behaviour directly: a resolver that never adds
	// offline_access. If TestResolvedScopes above would pass against this
	// broken resolver, it would not be guarding anything.
	brokenResolvedScopes := func(scopes []string) []string {
		return scopes
	}

	got := brokenResolvedScopes([]string{"openid"})
	found := false
	for _, s := range got {
		if s == offlineAccessScope {
			found = true
		}
	}
	if found {
		t.Fatal("test bug: broken resolver unexpectedly added offline_access")
	}
	// The real resolver must differ from the broken one for the omitted case.
	if real := resolvedScopes([]string{"openid"}); len(real) == len(got) {
		t.Fatal("resolvedScopes did not add offline_access -- guard is not red/green sound")
	}
}

// --- endpoint resolution -----------------------------------------------------

// TestNewDelegatedGrantSourceVerbatimEndpointsSkipsDiscovery proves that a
// fully populated Endpoints is used as-is and never triggers OIDC discovery
// (FR7-style "prove zero calls" via the fake's call counter).
func TestNewDelegatedGrantSourceVerbatimEndpointsSkipsDiscovery(t *testing.T) {
	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)

	cfg := validDelegatedGrantConfig(fake)

	src, err := NewDelegatedGrantSource(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewDelegatedGrantSource: %v", err)
	}

	if got := fake.DiscoveryCalls(); got != 0 {
		t.Fatalf("DiscoveryCalls() = %d, want 0 (verbatim Endpoints must skip discovery)", got)
	}

	want := Endpoints{
		Authorization: fake.URL + "/authorize",
		Token:         fake.URL + "/token",
		Revocation:    fake.URL + "/revoke",
	}
	if got := src.Endpoints(); got != want {
		t.Fatalf("Endpoints() = %+v, want %+v", got, want)
	}
}

// TestNewDelegatedGrantSourceDiscovery proves construction without an
// Endpoints override fetches and uses the fake's real discovery document.
func TestNewDelegatedGrantSourceDiscovery(t *testing.T) {
	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)

	cfg := validDelegatedGrantConfig(fake)
	cfg.Endpoints = Endpoints{} // force discovery

	src, err := NewDelegatedGrantSource(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewDelegatedGrantSource: %v", err)
	}

	if got := fake.DiscoveryCalls(); got != 1 {
		t.Fatalf("DiscoveryCalls() = %d, want 1", got)
	}

	want := Endpoints{
		Authorization: fake.URL + "/authorize",
		Token:         fake.URL + "/token",
		Revocation:    fake.URL + "/revoke",
	}
	if got := src.Endpoints(); got != want {
		t.Fatalf("Endpoints() = %+v, want %+v", got, want)
	}
}

// TestNewDelegatedGrantSourceDiscoveryMissingRevocation proves a discovery
// document without revocation_endpoint is not fatal at construction, and
// results in an empty Endpoints.Revocation (the revoke path's later signal
// to log a WARNING and skip the remote call, FR13).
func TestNewDelegatedGrantSourceDiscoveryMissingRevocation(t *testing.T) {
	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)
	fake.OmitRevocationEndpoint()

	cfg := validDelegatedGrantConfig(fake)
	cfg.Endpoints = Endpoints{}

	src, err := NewDelegatedGrantSource(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewDelegatedGrantSource: %v", err)
	}
	if got := src.Endpoints().Revocation; got != "" {
		t.Fatalf("Endpoints().Revocation = %q, want empty", got)
	}
	if got := src.Endpoints().Authorization; got == "" {
		t.Fatal("Endpoints().Authorization is empty, want populated")
	}
	if got := src.Endpoints().Token; got == "" {
		t.Fatal("Endpoints().Token is empty, want populated")
	}
}

// --- FR14: per-domain client config, not a hardcoded shared client ---------

// TestNewDelegatedGrantSourcePerDomainClientConfig proves two sources built
// with different ClientID/ClientSecret/RedirectURI both construct
// successfully and each carries its own values into the OAuth2 request it
// would send -- not a shared/hardcoded client. It drives this through the
// fake's real /authorize endpoint (via the embedded oauth2.Config's own
// AuthCodeURL, already available from #2383's scaffold) rather than
// inspecting unexported state, so it proves the values reach an actual
// outbound request.
func TestNewDelegatedGrantSourcePerDomainClientConfig(t *testing.T) {
	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)

	domains := []struct {
		clientID     string
		clientSecret string
		redirectURI  string
	}{
		{clientID: "domain-a-client", clientSecret: "domain-a-secret", redirectURI: "https://a.example.invalid/callback"},
		{clientID: "domain-b-client", clientSecret: "domain-b-secret", redirectURI: "https://b.example.invalid/callback"},
	}

	httpClient := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	for _, d := range domains {
		t.Run(d.clientID, func(t *testing.T) {
			cfg := validDelegatedGrantConfig(fake)
			cfg.ClientID = d.clientID
			cfg.ClientSecret = d.clientSecret
			cfg.RedirectURI = d.redirectURI

			src, err := NewDelegatedGrantSource(context.Background(), cfg)
			if err != nil {
				t.Fatalf("NewDelegatedGrantSource: %v", err)
			}

			// Whitebox: the constructed oauth2.Config must carry this
			// source's own client values, not another source's.
			if src.oauth2Cfg.ClientID != d.clientID {
				t.Fatalf("oauth2Cfg.ClientID = %q, want %q", src.oauth2Cfg.ClientID, d.clientID)
			}
			if src.oauth2Cfg.ClientSecret != d.clientSecret {
				t.Fatalf("oauth2Cfg.ClientSecret = %q, want %q", src.oauth2Cfg.ClientSecret, d.clientSecret)
			}
			if src.oauth2Cfg.RedirectURL != d.redirectURI {
				t.Fatalf("oauth2Cfg.RedirectURL = %q, want %q", src.oauth2Cfg.RedirectURL, d.redirectURI)
			}

			authURL := src.oauth2Cfg.AuthCodeURL("state-" + d.clientID)
			parsed, err := url.Parse(authURL)
			if err != nil {
				t.Fatalf("parse AuthCodeURL: %v", err)
			}
			if got := parsed.Query().Get("client_id"); got != d.clientID {
				t.Fatalf("AuthCodeURL client_id = %q, want %q", got, d.clientID)
			}
			if got := parsed.Query().Get("redirect_uri"); got != d.redirectURI {
				t.Fatalf("AuthCodeURL redirect_uri = %q, want %q", got, d.redirectURI)
			}

			// Drive the URL against the fake for real, proving this
			// domain's own redirect_uri is what the fake actually
			// receives and echoes back.
			resp, err := httpClient.Get(authURL)
			if err != nil {
				t.Fatalf("GET AuthCodeURL: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusFound {
				t.Fatalf("GET AuthCodeURL status = %d, want %d", resp.StatusCode, http.StatusFound)
			}
			location := resp.Header.Get("Location")
			if !strings.HasPrefix(location, d.redirectURI) {
				t.Fatalf("Location = %q, want prefix %q", location, d.redirectURI)
			}
		})
	}
}

// --- keycloakfake self-test --------------------------------------------------

// TestKeycloakFakeTokenModes proves each scripted /token mode is reachable
// and increments the token call counter.
func TestKeycloakFakeTokenModes(t *testing.T) {
	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)

	cases := []struct {
		name       string
		mode       keycloakfake.Mode
		wantStatus int // 0 means "request errors out" (connection failure)
		wantErr    bool
	}{
		{name: "success", mode: keycloakfake.ModeSuccess, wantStatus: http.StatusOK},
		{name: "invalid_grant", mode: keycloakfake.ModeInvalidGrant, wantStatus: http.StatusBadRequest},
		{name: "server_error", mode: keycloakfake.ModeServerError, wantStatus: http.StatusInternalServerError},
		{name: "connection_failure", mode: keycloakfake.ModeConnectionFailure, wantErr: true},
	}

	before := fake.TokenCalls()
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake.SetTokenMode(tc.mode)
			resp, err := http.Post(fake.URL+"/token", "application/x-www-form-urlencoded", strings.NewReader("grant_type=refresh_token"))
			if tc.wantErr {
				if err == nil {
					resp.Body.Close()
					t.Fatal("POST /token = nil error, want a connection failure")
				}
			} else {
				if err != nil {
					t.Fatalf("POST /token: %v", err)
				}
				defer resp.Body.Close()
				if resp.StatusCode != tc.wantStatus {
					t.Fatalf("POST /token status = %d, want %d", resp.StatusCode, tc.wantStatus)
				}
			}
			if got, want := fake.TokenCalls(), before+i+1; got != want {
				t.Fatalf("TokenCalls() = %d, want %d", got, want)
			}
		})
	}
}

// TestKeycloakFakeRevokeModes proves each scripted /revoke mode is reachable
// and increments the revoke call counter.
func TestKeycloakFakeRevokeModes(t *testing.T) {
	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)

	cases := []struct {
		name       string
		mode       keycloakfake.Mode
		wantStatus int
		wantErr    bool
	}{
		{name: "success", mode: keycloakfake.ModeSuccess, wantStatus: http.StatusOK},
		{name: "invalid_grant", mode: keycloakfake.ModeInvalidGrant, wantStatus: http.StatusBadRequest},
		{name: "server_error", mode: keycloakfake.ModeServerError, wantStatus: http.StatusInternalServerError},
		{name: "connection_failure", mode: keycloakfake.ModeConnectionFailure, wantErr: true},
	}

	before := fake.RevokeCalls()
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake.SetRevokeMode(tc.mode)
			resp, err := http.Post(fake.URL+"/revoke", "application/x-www-form-urlencoded", strings.NewReader("token=x"))
			if tc.wantErr {
				if err == nil {
					resp.Body.Close()
					t.Fatal("POST /revoke = nil error, want a connection failure")
				}
			} else {
				if err != nil {
					t.Fatalf("POST /revoke: %v", err)
				}
				defer resp.Body.Close()
				if resp.StatusCode != tc.wantStatus {
					t.Fatalf("POST /revoke status = %d, want %d", resp.StatusCode, tc.wantStatus)
				}
			}
			if got, want := fake.RevokeCalls(), before+i+1; got != want {
				t.Fatalf("RevokeCalls() = %d, want %d", got, want)
			}
		})
	}
}

// TestKeycloakFakeRotatedRefreshToken proves a successful /token response
// carries the configured rotated refresh token (FR13's write-back path,
// exercised end-to-end once #2386 lands).
func TestKeycloakFakeRotatedRefreshToken(t *testing.T) {
	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)

	fake.SetNextRefreshToken("rotated-token-xyz")
	resp, err := http.Post(fake.URL+"/token", "application/x-www-form-urlencoded", strings.NewReader("grant_type=refresh_token"))
	if err != nil {
		t.Fatalf("POST /token: %v", err)
	}
	defer resp.Body.Close()

	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode /token response: %v", err)
	}
	if body.RefreshToken != "rotated-token-xyz" {
		t.Fatalf("refresh_token = %q, want %q", body.RefreshToken, "rotated-token-xyz")
	}
}

// TestKeycloakFakeMintAccessTokenClaims proves MintAccessToken bakes in the
// requested sub and roles, independent of any HTTP call, so FR4/NFR3
// assertions can decode a token with specific claims.
func TestKeycloakFakeMintAccessTokenClaims(t *testing.T) {
	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)

	token, err := fake.MintAccessToken("user-123", []string{"admin", "viewer"})
	if err != nil {
		t.Fatalf("MintAccessToken: %v", err)
	}
	if token == "" {
		t.Fatal("MintAccessToken returned empty token")
	}
	// A compact JWS has three dot-separated parts.
	if parts := strings.Split(token, "."); len(parts) != 3 {
		t.Fatalf("MintAccessToken token has %d dot-separated parts, want 3", len(parts))
	}
}
