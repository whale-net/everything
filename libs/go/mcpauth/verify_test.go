package mcpauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
)

// fakeCredentialStore is an in-memory CredentialStore for pure-Go tests of
// TokenVerifier / RequireBearerToken — no Postgres involved. It reproduces
// just enough of pgxCredentialStore's Verify semantics (unknown, malformed,
// and revoked tokens all fail identically) to exercise the adapters in this
// file.
type fakeCredentialStore struct {
	// byToken maps a raw token to the identity that minted it.
	byToken map[string]string
	// revoked marks a raw token as revoked (present in byToken but no
	// longer live).
	revoked map[string]bool
}

func newFakeCredentialStore() *fakeCredentialStore {
	return &fakeCredentialStore{
		byToken: make(map[string]string),
		revoked: make(map[string]bool),
	}
}

var _ CredentialStore = (*fakeCredentialStore)(nil)

func (s *fakeCredentialStore) Mint(_ context.Context, identity string) (string, Credential, error) {
	token := "raw-token-" + uuid.NewString()
	s.byToken[token] = identity
	return token, Credential{ID: uuid.New(), Identity: identity, TokenHash: hashToken(token)}, nil
}

func (s *fakeCredentialStore) Verify(_ context.Context, rawToken string) (string, Credential, error) {
	identity, known := s.byToken[rawToken]
	if !known || s.revoked[rawToken] {
		return "", Credential{}, ErrInvalidCredential
	}
	return identity, Credential{Identity: identity, TokenHash: hashToken(rawToken)}, nil
}

func (s *fakeCredentialStore) Revoke(_ context.Context, _ uuid.UUID, _ string) error {
	return nil
}

func (s *fakeCredentialStore) revokeToken(rawToken string) {
	s.revoked[rawToken] = true
}

func (s *fakeCredentialStore) List(_ context.Context, _ string) ([]Credential, error) {
	return nil, nil
}

// ── TokenVerifier ────────────────────────────────────────────────────────

func TestTokenVerifier_ValidToken_ResolvesToMintingIdentity(t *testing.T) {
	store := newFakeCredentialStore()
	rawToken, _, err := store.Mint(context.Background(), "person-42")
	require.NoError(t, err)

	verifier := TokenVerifier(store)
	info, err := verifier(context.Background(), rawToken, nil)

	require.NoError(t, err)
	require.NotNil(t, info)
	assert.Equal(t, "person-42", info.UserID)
	assert.True(t, info.Expiration.IsZero(), "mcpauth credentials carry no expiration")
}

// TestTokenVerifier_FailureModesAreIndistinguishable proves FR6/NFR1: an
// unknown token, an empty token, a whitespace token, and a revoked token
// must all fail with errors that (a) satisfy errors.Is(err,
// sdkauth.ErrInvalidToken) and (b) have byte-identical Error() strings —
// asserted by direct string equality against each other, not by
// inspection.
func TestTokenVerifier_FailureModesAreIndistinguishable(t *testing.T) {
	store := newFakeCredentialStore()
	revokedToken, _, err := store.Mint(context.Background(), "person-99")
	require.NoError(t, err)
	store.revokeToken(revokedToken)

	verifier := TokenVerifier(store)

	cases := map[string]string{
		"unknown token":    "totally-unknown-token",
		"empty token":      "",
		"whitespace token": "   ",
		"revoked token":    revokedToken,
	}

	var firstMsg string
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			info, err := verifier(context.Background(), token, nil)
			require.Error(t, err)
			assert.Nil(t, info)
			assert.True(t, errors.Is(err, sdkauth.ErrInvalidToken), "%s: error must wrap sdkauth.ErrInvalidToken", name)

			if firstMsg == "" {
				firstMsg = err.Error()
			} else {
				assert.Equal(t, firstMsg, err.Error(), "%s: error message must be byte-identical across every failure mode", name)
			}
		})
	}
}

// TestTokenVerifier_ErrorMessageNeverContainsPresentedToken guards NFR1
// directly: the fixed error message must not embed the raw token that was
// presented, for either an unknown or a revoked token.
func TestTokenVerifier_ErrorMessageNeverContainsPresentedToken(t *testing.T) {
	store := newFakeCredentialStore()
	revokedToken, _, err := store.Mint(context.Background(), "person-1")
	require.NoError(t, err)
	store.revokeToken(revokedToken)

	verifier := TokenVerifier(store)

	for _, token := range []string{"some-unknown-token-value", revokedToken} {
		_, err := verifier(context.Background(), token, nil)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), token)
	}
}

// ── RequireBearerToken ──────────────────────────────────────────────────

func TestRequireBearerToken_NoAuthorizationHeader_Returns401WithChallenge(t *testing.T) {
	store := newFakeCredentialStore()
	mw := RequireBearerToken(store, &sdkauth.RequireBearerTokenOptions{ResourceMetadataURL: "https://example.test/.well-known/oauth-protected-resource"})

	handlerCalled := false
	h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { handlerCalled = true }))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, handlerCalled)
	assert.Contains(t, rec.Header().Get("WWW-Authenticate"), "resource_metadata=")
}

func TestRequireBearerToken_WrongScheme_Returns401(t *testing.T) {
	store := newFakeCredentialStore()
	mw := RequireBearerToken(store, nil)

	handlerCalled := false
	h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { handlerCalled = true }))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, handlerCalled)
}

// TestRequireBearerToken_ValidToken_InvokesHandlerWithTokenInfo also proves
// the AllowMissingExpiration default (NFR3): fakeCredentialStore-minted
// credentials never carry an expiration, exactly like every real mcpauth
// credential (audience_score_system/mcp/server/transport.go relies on this
// same behavior today) — without RequireBearerToken forcing
// AllowMissingExpiration true, this request would 401 on "token missing
// expiration" instead of reaching the handler.
func TestRequireBearerToken_ValidToken_InvokesHandlerWithTokenInfo(t *testing.T) {
	store := newFakeCredentialStore()
	rawToken, _, err := store.Mint(context.Background(), "person-7")
	require.NoError(t, err)

	mw := RequireBearerToken(store, nil)

	var gotUserID string
	handlerCalled := false
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		info := sdkauth.TokenInfoFromContext(r.Context())
		require.NotNil(t, info)
		gotUserID = info.UserID
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+rawToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, handlerCalled)
	assert.Equal(t, "person-7", gotUserID)
}

// TestRequireBearerToken_ForcesAllowMissingExpirationTrue proves that even
// a caller-supplied opts with AllowMissingExpiration explicitly false is
// overridden — since no mcpauth credential ever carries an expiration, a
// caller-set false here would break every credential outright.
func TestRequireBearerToken_ForcesAllowMissingExpirationTrue(t *testing.T) {
	store := newFakeCredentialStore()
	rawToken, _, err := store.Mint(context.Background(), "person-8")
	require.NoError(t, err)

	callerOpts := &sdkauth.RequireBearerTokenOptions{AllowMissingExpiration: false}
	mw := RequireBearerToken(store, callerOpts)

	// The caller's own opts value must not be mutated by RequireBearerToken.
	assert.False(t, callerOpts.AllowMissingExpiration, "RequireBearerToken must not mutate the caller's opts in place")

	handlerCalled := false
	h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { handlerCalled = true }))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+rawToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, handlerCalled)
}

func TestRequireBearerToken_InvalidToken_Returns401(t *testing.T) {
	store := newFakeCredentialStore()
	mw := RequireBearerToken(store, nil)

	h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler must not be invoked for an invalid token")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, strings.Contains(rec.Body.String(), "not-a-real-token"))
}
