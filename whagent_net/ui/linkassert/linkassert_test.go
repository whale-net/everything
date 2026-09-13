package linkassert

// Testing-phase suite (issue #2595) for linkassert: LoadKey's rejection
// modes, Mint's claim shape (kid/iss/exp-iat/jti, and the deliberate
// absence of whagent.Claim's Actor/session fields), and JWKSHandler's
// public-key-only, unauthenticated, method-gated behavior. Mirrors the
// shape of whagent_net/api/persona's keys_test.go/jwks_test.go, but all in
// one file since this package has a single active key and no rotation
// ledger to exercise.

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/whagent"
)

// generateSigningKeyPEM returns a fresh Ed25519 private key, PEM-encoded
// as PKCS8 -- the exact format LoadKey requires (mirrors
// persona/keys_test.go's identically-named helper).
func generateSigningKeyPEM(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// mustLoadTestKey builds a valid *Key with a fresh Ed25519 keypair and the
// given kid -- the happy-path fixture the Mint/JWKS tests below start
// from.
func mustLoadTestKey(t *testing.T, kid string) *Key {
	t.Helper()
	k, err := LoadKey(generateSigningKeyPEM(t), kid)
	require.NoError(t, err)
	require.NotNil(t, k)
	return k
}

// TestLoadKey_Rejects asserts LoadKey returns an error -- never a usable
// Key -- for an empty string, non-PEM garbage, a PEM block that isn't
// PKCS8, and a symmetric/HMAC secret.
func TestLoadKey_Rejects(t *testing.T) {
	validKid := "ui-key-1"

	cases := map[string]struct {
		pem string
		kid string
	}{
		"empty pem": {
			pem: "",
			kid: validKid,
		},
		"empty kid": {
			pem: generateSigningKeyPEM(t),
			kid: "",
		},
		"non-PEM garbage": {
			pem: "this is not a PEM block",
			kid: validKid,
		},
		"PEM block that isn't PKCS8": {
			// A PEM-armored block whose payload is not a PKCS8 DER
			// structure at all -- x509.ParsePKCS8PrivateKey must reject
			// this, not silently accept garbage bytes.
			pem: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("not asn.1 der")})),
			kid: validKid,
		},
		"symmetric/HMAC secret": {
			// An HMAC secret is raw bytes, not a PKCS8 DER structure --
			// still routed through the same PEM block above, since a
			// symmetric secret never parses as PKCS8 in the first place
			// (linkassert.go's LoadKey doc comment).
			pem: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("0123456789abcdef0123456789abcdef")})),
			kid: validKid,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			k, err := LoadKey(tc.pem, tc.kid)
			require.Error(t, err)
			assert.Nil(t, k)
		})
	}
}

// TestMint_ClaimShape asserts a minted token's header kid equals the
// loaded key's kid, its iss equals the issuer argument, its exp - iat
// equals whagent.DefaultTTL exactly, and its jti differs across two
// successive calls with identical arguments.
func TestMint_ClaimShape(t *testing.T) {
	k := mustLoadTestKey(t, "ui-key-1")
	now := time.Now()

	tokenA, err := k.Mint("https://ui.example", "operator-sub", "https://keycloak.example/realms/ops", "https://return.example/callback", now)
	require.NoError(t, err)
	tokenB, err := k.Mint("https://ui.example", "operator-sub", "https://keycloak.example/realms/ops", "https://return.example/callback", now)
	require.NoError(t, err)

	parsedA, err := jwt.ParseSigned(tokenA, []jose.SignatureAlgorithm{jose.EdDSA, jose.ES256, jose.ES384, jose.ES512, jose.RS256})
	require.NoError(t, err)
	require.Len(t, parsedA.Headers, 1)
	assert.Equal(t, "ui-key-1", parsedA.Headers[0].KeyID)

	var claimsA assertionClaims
	require.NoError(t, parsedA.Claims(k.signer.Public(), &claimsA))
	assert.Equal(t, "https://ui.example", claimsA.Issuer)
	assert.Equal(t, whagent.DefaultTTL, claimsA.Expiry.Time().Sub(claimsA.IssuedAt.Time()))

	parsedB, err := jwt.ParseSigned(tokenB, []jose.SignatureAlgorithm{jose.EdDSA})
	require.NoError(t, err)
	var claimsB assertionClaims
	require.NoError(t, parsedB.Claims(k.signer.Public(), &claimsB))

	assert.NotEmpty(t, claimsA.ID)
	assert.NotEmpty(t, claimsB.ID)
	assert.NotEqual(t, claimsA.ID, claimsB.ID, "jti must differ across two successive Mint calls with identical arguments")
}

// TestMint_NoActorOrSessionFields asserts Mint's output carries none of
// whagent.Claim's Actor/session fields. Checked by decoding the token's
// second segment (the claims) as a raw map and confirming none of
// whagent.Claim's Actor-shape JSON keys are present, so a future field
// added to assertionClaims by copy-paste from whagent.Claim would be
// caught even if it were never read back out via the assertionClaims
// struct itself.
func TestMint_NoActorOrSessionFields(t *testing.T) {
	k := mustLoadTestKey(t, "ui-key-1")

	token, err := k.Mint("https://ui.example", "operator-sub", "https://keycloak.example/realms/ops", "https://return.example/callback", time.Now())
	require.NoError(t, err)

	parsed, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{jose.EdDSA})
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, parsed.Claims(k.signer.Public(), &raw))

	for _, forbidden := range []string{"actor", "whagent_session_id", "session_id", "aud"} {
		_, present := raw[forbidden]
		assert.False(t, present, "Mint's claims must not carry whagent.Claim's %q field", forbidden)
	}

	// The claim shape is exactly this set -- nothing more.
	want := map[string]bool{"iss": true, "sub": true, "sub_iss": true, "jti": true, "exp": true, "iat": true, "return_url": true}
	for key := range raw {
		assert.True(t, want[key], "unexpected claim field %q in Mint's output", key)
	}
}

// TestJWKSHandler_PublicKeyOnly asserts the JWKS document contains the
// active key, kid matches, and no private JWK parameter (d, p, q, ...)
// appears anywhere in the serialized output -- mirrors persona's own jwks
// test assertion.
func TestJWKSHandler_PublicKeyOnly(t *testing.T) {
	k := mustLoadTestKey(t, "ui-key-1")

	req := httptest.NewRequest(http.MethodGet, JWKSPath, nil)
	w := httptest.NewRecorder()
	JWKSHandler(k).ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var got jose.JSONWebKeySet
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got.Keys, 1)
	assert.Equal(t, "ui-key-1", got.Keys[0].KeyID)
	assert.True(t, got.Keys[0].IsPublic(), "JWKS response must contain only public key material")

	assert.NotContains(t, w.Body.String(), `"d":`, "serialized JWKS must never carry a private `d` parameter")
}

// TestJWKSHandler_UnauthenticatedAndMethodGated asserts the JWKS route is
// reachable without an authenticated session (no redirect to /login) and
// rejects non-GET/HEAD with 405. JWKSHandler itself never consults any
// session/auth state -- exercising it directly (with no
// app.auth.RequireAuthFunc wrapper in front) is the proof that it is
// unconditionally public; ui/main.go's setupRoutes registration (never
// wrapped in RequireAuthFunc) is what carries that guarantee into
// production.
func TestJWKSHandler_UnauthenticatedAndMethodGated(t *testing.T) {
	k := mustLoadTestKey(t, "ui-key-1")
	handler := JWKSHandler(k)

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, JWKSPath, nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			assert.Equal(t, http.StatusOK, w.Code)
			assert.NotEqual(t, http.StatusFound, w.Code)
			assert.Empty(t, w.Header().Get("Location"), "must never redirect to a sign-in page")
		})
	}

	req := httptest.NewRequest(http.MethodPost, JWKSPath, nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}
