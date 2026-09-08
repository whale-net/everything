package whagent

// Shared test fixtures for the Testing-phase suite (mint/verify round trip,
// rejection cases, JWKS key rotation, middleware) -- see claim_test.go,
// sign_test.go, verify_test.go, middleware_test.go, idempotency_test.go.

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/require"
)

// testKeyID is the JWKS `kid` newTestSigner tags its generated key with.
const testKeyID = "test-key-1"

// newTestSigner builds a Signer backed by a freshly generated Ed25519 key
// pair for issuer, plus that key's public half for constructing a matching
// Verifier (NewVerifierFromKey) in the same test.
func newTestSigner(t *testing.T, issuer string) (*Signer, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := New(priv, issuer, testKeyID)
	require.NoError(t, err)
	return signer, pub
}

// signRawClaims signs an arbitrary claims map directly with s's own private
// key and key ID, bypassing Signer.Mint's field/TTL validation entirely --
// this is how tests construct malformed or edge-case tokens (missing
// fields, already-expired, wrong iss) that a well-formed Mint call could
// never produce.
func signRawClaims(t *testing.T, s *Signer, claims map[string]interface{}) string {
	t.Helper()
	alg, err := signatureAlgorithmFor(s.privateKey.Public())
	require.NoError(t, err)
	joseSigner, err := jose.NewSigner(
		jose.SigningKey{Algorithm: alg, Key: s.privateKey},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", s.keyID),
	)
	require.NoError(t, err)
	token, err := jwt.Signed(joseSigner).Claims(claims).Serialize()
	require.NoError(t, err)
	return token
}

// validRawClaims returns a claims map with every field Verifier.Verify
// requires present and valid, for the given iss/aud -- tests copy this and
// delete/mutate individual fields to exercise a single rejection case at a
// time, rather than duplicating the full claim shape in every test.
func validRawClaims(iss, aud string) map[string]interface{} {
	now := time.Now()
	return map[string]interface{}{
		"iss":                iss,
		"sub":                "person-1",
		"aud":                []string{aud},
		"exp":                now.Add(5 * time.Minute).Unix(),
		"iat":                now.Unix(),
		"jti":                "test-jti-1",
		"sub_iss":            "https://keycloak.example.test/realms/humans",
		"act":                map[string]string{"sub": "agent-actor-1", "agent_id": "research-agent-v3"},
		"whagent_session_id": "session-abc",
	}
}

// newKeycloakStyleToken builds a well-formed-looking RS256 token signed by
// a freshly generated, unrelated RSA key, carrying typical Keycloak-shaped
// claims (including profile attributes a whagent-net Claim would never
// carry) -- NFR4's "even a valid Keycloak token must be rejected" case.
// Its iss deliberately never matches any whagent-net Verifier's configured
// issuer, but the actual point of the test this feeds is that it is never
// signed by any key a whagent-net Verifier knows about, so Verify must
// reject it on signature verification alone -- before iss is ever
// inspected.
func newKeycloakStyleToken(t *testing.T) string {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	joseSigner, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: priv},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "keycloak-kid-1"),
	)
	require.NoError(t, err)
	now := time.Now()
	claims := map[string]interface{}{
		"iss":                "https://keycloak.example.test/realms/humans",
		"sub":                "person-1",
		"aud":                []string{"account"},
		"exp":                now.Add(5 * time.Minute).Unix(),
		"iat":                now.Unix(),
		"jti":                "keycloak-jti-1",
		"preferred_username": "alice",
		"email":              "alice@example.test",
		"azp":                "whagent-net",
		"realm_access":       map[string]any{"roles": []string{"user"}},
	}
	token, err := jwt.Signed(joseSigner).Claims(claims).Serialize()
	require.NoError(t, err)
	return token
}
