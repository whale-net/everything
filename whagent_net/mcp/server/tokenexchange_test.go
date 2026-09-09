package server

// Pure-Go coverage for tokenexchange.go's KeycloakExchanger (issue #2249's
// Testing section, "Token exchange against a fake Keycloak token
// endpoint"): the exact RFC 8693 grant type/parameters sent,
// cache-within-window / refresh-after-expiry behavior (via e.now, this
// package's own test seam -- see KeycloakExchanger's doc comment), and
// NFR8's "the client secret never appears in a log line or an error
// message returned to the caller" -- captureLogger below temporarily
// swaps this package's own `logger` var so a test can inspect exactly
// what was logged, in the same package rather than through slog's global
// default (which the package-level `logger` var captured a Handler
// reference from at init time, before any test could redirect it).
import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureLogger redirects this package's shared `logger` for the duration
// of t, returning the buffer everything logged through it lands in, and
// restores the original logger on cleanup.
func captureLogger(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := logger
	logger = slog.New(slog.NewTextHandler(&buf, nil))
	t.Cleanup(func() { logger = prev })
	return &buf
}

func TestTokenExchangeConfig_Enabled(t *testing.T) {
	cases := []struct {
		name string
		cfg  TokenExchangeConfig
		want bool
	}{
		{"all three set", TokenExchangeConfig{ClientID: "a", ClientSecret: "b", TokenEndpoint: "c"}, true},
		{"missing client id", TokenExchangeConfig{ClientSecret: "b", TokenEndpoint: "c"}, false},
		{"missing client secret", TokenExchangeConfig{ClientID: "a", TokenEndpoint: "c"}, false},
		{"missing token endpoint", TokenExchangeConfig{ClientID: "a", ClientSecret: "b"}, false},
		{"zero value", TokenExchangeConfig{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.cfg.Enabled())
		})
	}
}

func TestKeycloakExchanger_Exchange_Disabled_ReturnsFixedErrorWithoutAnyRequest(t *testing.T) {
	e := NewKeycloakExchanger(TokenExchangeConfig{})
	_, err := e.Exchange(context.Background(), "iss", "sub")
	assert.ErrorIs(t, err, errExchangeDisabled)
}

// TestKeycloakExchanger_Exchange_SendsCorrectRFC8693Request proves the
// exact grant_type/parameters this task's Testing section requires: mcp
// authenticates as its own confidential client and asks for a token
// exchange naming requested_subject, never a subject_token of its own.
func TestKeycloakExchanger_Exchange_SendsCorrectRFC8693Request(t *testing.T) {
	var gotForm url.Values
	var gotMethod, gotContentType string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		require.NoError(t, r.ParseForm())
		gotForm = r.Form
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "the-exchanged-jwt", "expires_in": 300})
	}))
	defer ts.Close()

	e := NewKeycloakExchanger(TokenExchangeConfig{ClientID: "mcp-confidential-client", ClientSecret: "s3cr3t", TokenEndpoint: ts.URL})
	jwt, err := e.Exchange(context.Background(), "https://keycloak.example.test/realms/whagent", "operator-sub-1")
	require.NoError(t, err)
	assert.Equal(t, "the-exchanged-jwt", jwt)

	assert.Equal(t, http.MethodPost, gotMethod)
	assert.Equal(t, "application/x-www-form-urlencoded", gotContentType)
	require.NotNil(t, gotForm)
	assert.Equal(t, grantType, gotForm.Get("grant_type"))
	assert.Equal(t, "urn:ietf:params:oauth:grant-type:token-exchange", gotForm.Get("grant_type"))
	assert.Equal(t, requestedTokenType, gotForm.Get("requested_token_type"))
	assert.Equal(t, "mcp-confidential-client", gotForm.Get("client_id"))
	assert.Equal(t, "s3cr3t", gotForm.Get("client_secret"))
	assert.Equal(t, "operator-sub-1", gotForm.Get("requested_subject"))
	assert.Empty(t, gotForm.Get("subject_token"), "mcp must never hold or forward a token belonging to the resolved identity itself")
}

// TestKeycloakExchanger_Exchange_CachesWithinWindowThenRefreshesAfterExpiry
// covers both halves of this task's caching requirement in one sequence:
// a second call for the same identity, still comfortably inside the
// cacheExpiryMargin-adjusted validity window, must reuse the cached
// exchange (no second HTTP call); a call after crossing that boundary must
// re-exchange.
func TestKeycloakExchanger_Exchange_CachesWithinWindowThenRefreshesAfterExpiry(t *testing.T) {
	var mu sync.Mutex
	var hits int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		n := hits
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": fmt.Sprintf("jwt-%d", n), "expires_in": 100})
	}))
	defer ts.Close()

	e := NewKeycloakExchanger(TokenExchangeConfig{ClientID: "id", ClientSecret: "secret", TokenEndpoint: ts.URL})
	fixedNow := time.Now()
	e.now = func() time.Time { return fixedNow }

	jwt1, err := e.Exchange(context.Background(), "iss", "sub")
	require.NoError(t, err)
	assert.Equal(t, "jwt-1", jwt1)

	// 50s later: still well inside (100s expiry - 10s margin) = 90s of
	// validity -- must reuse the cache.
	fixedNow = fixedNow.Add(50 * time.Second)
	jwt2, err := e.Exchange(context.Background(), "iss", "sub")
	require.NoError(t, err)
	assert.Equal(t, jwt1, jwt2, "a call within the cached validity window must reuse the cached exchange")
	mu.Lock()
	assert.Equal(t, 1, hits, "the token endpoint must not be hit again while the cache is still valid")
	mu.Unlock()

	// Another 45s later (95s total since mint) crosses the 90s
	// cached-until boundary -- must re-exchange rather than hand back a
	// nearly-expired token.
	fixedNow = fixedNow.Add(45 * time.Second)
	jwt3, err := e.Exchange(context.Background(), "iss", "sub")
	require.NoError(t, err)
	assert.Equal(t, "jwt-2", jwt3)
	mu.Lock()
	assert.Equal(t, 2, hits, "a call past cacheExpiryMargin-before-expiry must re-exchange")
	mu.Unlock()
}

func TestKeycloakExchanger_Exchange_NonPositiveExpiresIn_NeverCached(t *testing.T) {
	var mu sync.Mutex
	var hits int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "jwt", "expires_in": 0})
	}))
	defer ts.Close()

	e := NewKeycloakExchanger(TokenExchangeConfig{ClientID: "a", ClientSecret: "b", TokenEndpoint: ts.URL})
	_, err := e.Exchange(context.Background(), "iss", "sub")
	require.NoError(t, err)
	_, err = e.Exchange(context.Background(), "iss", "sub")
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 2, hits, "a response with no positive expires_in must never be cached, so every call re-exchanges")
}

func TestKeycloakExchanger_Exchange_MissingAccessToken_Fails(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"expires_in": 300})
	}))
	defer ts.Close()

	e := NewKeycloakExchanger(TokenExchangeConfig{ClientID: "a", ClientSecret: "b", TokenEndpoint: ts.URL})
	_, err := e.Exchange(context.Background(), "iss", "sub")
	assert.Equal(t, errExchangeFailed, err)
}

// TestKeycloakExchanger_Exchange_FailureNeverLeaksClientSecret is this
// task's NFR8 Testing requirement: a failed exchange's returned error
// (errExchangeFailed, a fixed sentinel) and everything logged about the
// failure must never contain the client secret.
func TestKeycloakExchanger_Exchange_FailureNeverLeaksClientSecret(t *testing.T) {
	const secret = "super-secret-value-must-never-leak"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"client secret mismatch"}`))
	}))
	defer ts.Close()

	buf := captureLogger(t)
	e := NewKeycloakExchanger(TokenExchangeConfig{ClientID: "mcp-client", ClientSecret: secret, TokenEndpoint: ts.URL})
	_, err := e.Exchange(context.Background(), "iss", "sub")

	require.Error(t, err)
	assert.Equal(t, errExchangeFailed, err)
	assert.NotContains(t, err.Error(), secret, "the caller-visible error must never contain the client secret")
	assert.NotContains(t, buf.String(), secret, "the ERROR log line must never contain the client secret")
	assert.Contains(t, buf.String(), "token exchange failed", "the failure must still be logged at ERROR for an operator to diagnose")
}

// TestKeycloakExchanger_Exchange_HTTPTransportFailure_ReturnsFixedError
// covers the transport-level failure case (unreachable endpoint) alongside
// the non-2xx case above -- both must collapse to the same fixed
// errExchangeFailed sentinel.
func TestKeycloakExchanger_Exchange_HTTPTransportFailure_ReturnsFixedError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	unreachableURL := ts.URL
	ts.Close() // closed before any request is made -- guarantees a transport-level failure

	e := NewKeycloakExchanger(TokenExchangeConfig{ClientID: "a", ClientSecret: "b", TokenEndpoint: unreachableURL})
	_, err := e.Exchange(context.Background(), "iss", "sub")
	assert.Equal(t, errExchangeFailed, err)
}

func TestKeycloakExchanger_Exchange_DistinctIdentities_CachedIndependently(t *testing.T) {
	var mu sync.Mutex
	var hits int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		n := hits
		mu.Unlock()
		require.NoError(t, r.ParseForm())
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": fmt.Sprintf("jwt-for-%s-%d", r.FormValue("requested_subject"), n),
			"expires_in":   300,
		})
	}))
	defer ts.Close()

	e := NewKeycloakExchanger(TokenExchangeConfig{ClientID: "a", ClientSecret: "b", TokenEndpoint: ts.URL})

	jwtA1, err := e.Exchange(context.Background(), "iss", "sub-a")
	require.NoError(t, err)
	jwtB1, err := e.Exchange(context.Background(), "iss", "sub-b")
	require.NoError(t, err)
	assert.NotEqual(t, jwtA1, jwtB1)

	jwtA2, err := e.Exchange(context.Background(), "iss", "sub-a")
	require.NoError(t, err)
	jwtB2, err := e.Exchange(context.Background(), "iss", "sub-b")
	require.NoError(t, err)
	assert.Equal(t, jwtA1, jwtA2, "identity A's cached exchange must be untouched by identity B's calls")
	assert.Equal(t, jwtB1, jwtB2, "identity B's cached exchange must be untouched by identity A's calls")

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 2, hits, "two distinct identities must each be exchanged exactly once, independently cached")
}
