package whagent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerify_RejectsTokenSignedByADifferentKey(t *testing.T) {
	_, pub := newTestSigner(t, "whagent-net-test")
	otherSigner, _ := newTestSigner(t, "whagent-net-test")

	verifier, err := NewVerifierFromKey(pub, "whagent-net-test")
	require.NoError(t, err)

	token, err := otherSigner.Mint(context.Background(), MintRequest{
		Subject:       "person-1",
		SubjectIssuer: "https://keycloak.example.test/realms/humans",
		Actor:         Actor{Subject: "agent-actor-1", AgentID: "research-agent-v3"},
		SessionID:     "session-abc",
		Audience:      "audience-score-system",
	})
	require.NoError(t, err)

	claim, err := verifier.Verify(context.Background(), token, "audience-score-system")
	assert.Nil(t, claim)
	assert.ErrorIs(t, err, ErrInvalidSignature)
}

// TestVerify_RejectsWrongAudience is this package's designated Red/Green
// proof (see the Testing-phase issue body). To confirm it actually guards
// the audience check: comment out the `if !claim.Audience.Contains(...)`
// block in verify.go's Verify, rerun `bazel test
// //libs/go/whagent:whagent_test --test_filter=TestVerify_RejectsWrongAudience`
// and observe it fail (ErrInvalidAudience never returned, claim comes back
// non-nil); then revert -- it passes again once the check is restored.
func TestVerify_RejectsWrongAudience(t *testing.T) {
	signer, pub := newTestSigner(t, "whagent-net-test")
	verifier, err := NewVerifierFromKey(pub, "whagent-net-test")
	require.NoError(t, err)

	token, err := signer.Mint(context.Background(), MintRequest{
		Subject:       "person-1",
		SubjectIssuer: "https://keycloak.example.test/realms/humans",
		Actor:         Actor{Subject: "agent-actor-1", AgentID: "research-agent-v3"},
		SessionID:     "session-abc",
		Audience:      "audience-score-system",
	})
	require.NoError(t, err)

	claim, err := verifier.Verify(context.Background(), token, "a-completely-different-domain")
	assert.Nil(t, claim)
	assert.ErrorIs(t, err, ErrInvalidAudience)
}

func TestVerify_RejectsExpiredToken(t *testing.T) {
	signer, pub := newTestSigner(t, "whagent-net-test")
	verifier, err := NewVerifierFromKey(pub, "whagent-net-test")
	require.NoError(t, err)

	claims := validRawClaims("whagent-net-test", "audience-score-system")
	claims["exp"] = time.Now().Add(-1 * time.Minute).Unix()
	token := signRawClaims(t, signer, claims)

	claim, err := verifier.Verify(context.Background(), token, "audience-score-system")
	assert.Nil(t, claim)
	assert.ErrorIs(t, err, ErrExpired)
}

func TestVerify_RejectsWrongIssuer(t *testing.T) {
	signer, pub := newTestSigner(t, "whagent-net-test")
	verifier, err := NewVerifierFromKey(pub, "whagent-net-test")
	require.NoError(t, err)

	claims := validRawClaims("some-other-issuer", "audience-score-system")
	token := signRawClaims(t, signer, claims)

	claim, err := verifier.Verify(context.Background(), token, "audience-score-system")
	assert.Nil(t, claim)
	assert.ErrorIs(t, err, ErrUnknownIssuer)
}

// TestVerify_RejectsMissingRequiredClaims covers every field claim.go
// documents as required -- sub_iss, act (both its sub and agent_id
// subfields), and whagent_session_id are the issue's explicitly named
// cases; the standard registered fields (sub, aud, iss, jti, iat, exp) are
// included too since Verify's missing-field check treats them identically.
func TestVerify_RejectsMissingRequiredClaims(t *testing.T) {
	signer, pub := newTestSigner(t, "whagent-net-test")
	verifier, err := NewVerifierFromKey(pub, "whagent-net-test")
	require.NoError(t, err)

	cases := []string{
		"sub", "aud", "iss", "jti", "iat", "exp",
		"sub_iss", "act", "whagent_session_id",
	}

	for _, missing := range cases {
		t.Run(missing, func(t *testing.T) {
			claims := validRawClaims("whagent-net-test", "audience-score-system")
			delete(claims, missing)
			token := signRawClaims(t, signer, claims)

			claim, err := verifier.Verify(context.Background(), token, "audience-score-system")
			assert.Nil(t, claim)
			assert.ErrorIs(t, err, ErrMissingClaim, "missing %q", missing)
		})
	}
}

// TestVerify_RejectsActMissingSubfields covers the two Actor subfields
// individually -- a claim with an `act` object present but incomplete
// (only `sub`, or only `agent_id`) is just as invalid as `act` being
// entirely absent (covered above).
func TestVerify_RejectsActMissingSubfields(t *testing.T) {
	signer, pub := newTestSigner(t, "whagent-net-test")
	verifier, err := NewVerifierFromKey(pub, "whagent-net-test")
	require.NoError(t, err)

	cases := map[string]map[string]string{
		"act missing sub":      {"agent_id": "research-agent-v3"},
		"act missing agent_id": {"sub": "agent-actor-1"},
	}

	for name, act := range cases {
		t.Run(name, func(t *testing.T) {
			claims := validRawClaims("whagent-net-test", "audience-score-system")
			claims["act"] = act
			token := signRawClaims(t, signer, claims)

			claim, err := verifier.Verify(context.Background(), token, "audience-score-system")
			assert.Nil(t, claim)
			assert.ErrorIs(t, err, ErrMissingClaim)
		})
	}
}

// TestVerify_RejectsForeignKeycloakStyleToken is NFR4's dedicated proof:
// a well-formed, validly-signed RS256 token shaped like a genuine Keycloak
// ID token -- carrying profile attributes this package's own Claim would
// never emit -- must still be rejected, because it was never signed by any
// key the Verifier's configured key set actually knows about. This is
// distinct from TestVerify_RejectsWrongIssuer: that case uses a
// whagent-net-signed token with a wrong `iss` string; this case proves
// signature verification -- not the iss check -- is what actually stops a
// foreign, differently-signed token, since a Keycloak token would never
// even carry whagent-net's claim shape for the iss check to run against.
func TestVerify_RejectsForeignKeycloakStyleToken(t *testing.T) {
	_, pub := newTestSigner(t, "whagent-net-test")
	verifier, err := NewVerifierFromKey(pub, "whagent-net-test")
	require.NoError(t, err)

	token := newKeycloakStyleToken(t)

	claim, err := verifier.Verify(context.Background(), token, "audience-score-system")
	assert.Nil(t, claim)
	assert.ErrorIs(t, err, ErrInvalidSignature)
}

// TestVerifier_JWKS_PicksKeyByKidAndRefreshesOnUnknownKid proves NewVerifier
// against a real JWKS-serving httptest.Server: (a) with two keys already
// published, a token verifies against whichever key its own `kid` names --
// not just "the first key in the set" -- and (b) a token signed with a
// third key the Verifier has never seen still verifies once that key
// appears at the JWKS endpoint, because an unrecognized `kid` triggers a
// remote refetch rather than an outright rejection (oidc.RemoteKeySet's
// documented rotation behavior).
func TestVerifier_JWKS_PicksKeyByKidAndRefreshesOnUnknownKid(t *testing.T) {
	pub1, priv1, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pub2, priv2, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pub3, priv3, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	signer1, err := New(priv1, "whagent-net-test", "kid-1")
	require.NoError(t, err)
	signer2, err := New(priv2, "whagent-net-test", "kid-2")
	require.NoError(t, err)
	signer3, err := New(priv3, "whagent-net-test", "kid-3")
	require.NoError(t, err)

	var mu sync.Mutex
	current := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
		{Key: pub1, KeyID: "kid-1", Algorithm: string(jose.EdDSA), Use: "sig"},
		{Key: pub2, KeyID: "kid-2", Algorithm: string(jose.EdDSA), Use: "sig"},
	}}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(current))
	}))
	defer srv.Close()

	verifier, err := NewVerifier(context.Background(), srv.URL, "whagent-net-test")
	require.NoError(t, err)

	mint := func(s *Signer, subject string) string {
		token, err := s.Mint(context.Background(), MintRequest{
			Subject:       subject,
			SubjectIssuer: "https://keycloak.example.test/realms/humans",
			Actor:         Actor{Subject: "agent-actor-1", AgentID: "research-agent-v3"},
			SessionID:     "session-abc",
			Audience:      "audience-score-system",
		})
		require.NoError(t, err)
		return token
	}

	// Both keys are already published -- each token must verify against its
	// own kid, proving key selection isn't just "the only key present".
	claim1, err := verifier.Verify(context.Background(), mint(signer1, "person-1"), "audience-score-system")
	require.NoError(t, err)
	assert.Equal(t, "person-1", claim1.Subject)

	claim2, err := verifier.Verify(context.Background(), mint(signer2, "person-2"), "audience-score-system")
	require.NoError(t, err)
	assert.Equal(t, "person-2", claim2.Subject)

	// kid-3 has never been seen -- a token signed with it must fail against
	// the cached kid-1/kid-2 keys and force a refetch once kid-3 actually
	// appears at the JWKS endpoint.
	mu.Lock()
	current = jose.JSONWebKeySet{Keys: append(current.Keys, jose.JSONWebKey{
		Key: pub3, KeyID: "kid-3", Algorithm: string(jose.EdDSA), Use: "sig",
	})}
	mu.Unlock()

	claim3, err := verifier.Verify(context.Background(), mint(signer3, "person-3"), "audience-score-system")
	require.NoError(t, err)
	assert.Equal(t, "person-3", claim3.Subject)
}
