package link

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/whale-net/everything/libs/go/whagent"
)

// Fixed field values every test's valid claim set uses unless a case
// deliberately overrides or deletes one -- see validClaims.
const (
	testIssuer  = "https://ui.example.test"
	testSubject = "person-1"
	testSubIss  = "https://keycloak.example.test/realms/humans"
	testJTI     = "test-jti-1"
	testReturn  = "https://web.example.test/return"
)

// testKey is a locally generated Ed25519 key pair -- this package's tests
// never depend on a running ui; they mint their own tokens and serve their
// own JWKS.
type testKey struct {
	priv ed25519.PrivateKey
	pub  ed25519.PublicKey
	kid  string
}

func newTestKey(t *testing.T, kid string) testKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}
	return testKey{priv: priv, pub: pub, kid: kid}
}

func (k testKey) jwk() jose.JSONWebKey {
	return jose.JSONWebKey{Key: k.pub, KeyID: k.kid, Algorithm: string(jose.EdDSA), Use: "sig"}
}

// signRawClaims signs an arbitrary claims map directly with k, bypassing
// any Mint-style validation -- this is how tests construct malformed or
// edge-case tokens (missing fields, wrong iss, already expired) that a
// well-formed mint could never produce.
func signRawClaims(t *testing.T, k testKey, claims map[string]interface{}) string {
	t.Helper()
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.EdDSA, Key: k.priv},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", k.kid),
	)
	if err != nil {
		t.Fatalf("constructing signer: %v", err)
	}
	token, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatalf("signing claims: %v", err)
	}
	return token
}

// validClaims returns a claims map with every field Verify requires,
// present and valid, for the given iss -- tests copy this and
// delete/mutate individual fields to exercise one rejection case at a
// time, rather than duplicating the full wire shape in every test.
func validClaims(iss string) map[string]interface{} {
	now := time.Now()
	return map[string]interface{}{
		"iss":        iss,
		"sub":        testSubject,
		"sub_iss":    testSubIss,
		"jti":        testJTI,
		"exp":        now.Add(5 * time.Minute).Unix(),
		"iat":        now.Unix(),
		"return_url": testReturn,
	}
}

// tamperPayload edits token's payload segment after signing (changing the
// subject) while leaving the header and signature segments untouched --
// this is what a genuine tampering attempt looks like: the signature no
// longer matches the (now different) payload it was computed over.
func tamperPayload(t *testing.T, token string) string {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d segments, want 3", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decoding payload: %v", err)
	}
	var claims map[string]interface{}
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("unmarshaling payload: %v", err)
	}
	claims["sub"] = "attacker"
	tampered, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshaling tampered payload: %v", err)
	}
	parts[1] = base64.RawURLEncoding.EncodeToString(tampered)
	return strings.Join(parts, ".")
}

// testJWKS serves a mutable JSON Web Key Set from an httptest.Server, so
// tests can add a key mid-test (kid rotation) without recreating the
// Verifier under test.
type testJWKS struct {
	mu   sync.Mutex
	keys []jose.JSONWebKey
	srv  *httptest.Server
}

func newTestJWKS(t *testing.T, keys ...jose.JSONWebKey) *testJWKS {
	t.Helper()
	tj := &testJWKS{keys: keys}
	tj.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tj.mu.Lock()
		defer tj.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: tj.keys})
	}))
	t.Cleanup(tj.srv.Close)
	return tj
}

func (tj *testJWKS) addKey(k jose.JSONWebKey) {
	tj.mu.Lock()
	defer tj.mu.Unlock()
	tj.keys = append(tj.keys, k)
}

func newTestVerifier(t *testing.T, jwksURL string) *Verifier {
	t.Helper()
	v, err := NewVerifier(context.Background(), jwksURL, testIssuer)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return v
}

func TestVerify_HappyPath(t *testing.T) {
	key := newTestKey(t, "kid-1")
	jwks := newTestJWKS(t, key.jwk())
	v := newTestVerifier(t, jwks.srv.URL)

	claims := validClaims(testIssuer)
	token := signRawClaims(t, key, claims)

	assertion, err := v.Verify(context.Background(), token)
	if err != nil {
		t.Fatalf("Verify: unexpected error: %v", err)
	}
	if assertion.Issuer != testIssuer {
		t.Errorf("Issuer = %q, want %q", assertion.Issuer, testIssuer)
	}
	if assertion.Subject != testSubject {
		t.Errorf("Subject = %q, want %q", assertion.Subject, testSubject)
	}
	if assertion.SubjectIssuer != testSubIss {
		t.Errorf("SubjectIssuer = %q, want %q", assertion.SubjectIssuer, testSubIss)
	}
	if assertion.ID != testJTI {
		t.Errorf("ID = %q, want %q", assertion.ID, testJTI)
	}
	if assertion.ReturnURL != testReturn {
		t.Errorf("ReturnURL = %q, want %q", assertion.ReturnURL, testReturn)
	}
	wantExpiry := claims["exp"].(int64)
	if assertion.Expiry.Unix() != wantExpiry {
		t.Errorf("Expiry.Unix() = %d, want %d", assertion.Expiry.Unix(), wantExpiry)
	}
}

func TestVerify_RejectsTamperedSignature(t *testing.T) {
	key := newTestKey(t, "kid-1")
	jwks := newTestJWKS(t, key.jwk())
	v := newTestVerifier(t, jwks.srv.URL)

	token := signRawClaims(t, key, validClaims(testIssuer))
	tampered := tamperPayload(t, token)

	assertion, err := v.Verify(context.Background(), tampered)
	if assertion != nil {
		t.Errorf("Verify: got non-nil assertion, want nil")
	}
	if err != ErrInvalidSignature {
		t.Errorf("Verify: err = %v, want %v", err, ErrInvalidSignature)
	}
}

func TestVerify_RejectsUnknownKey(t *testing.T) {
	published := newTestKey(t, "kid-1")
	unknown := newTestKey(t, "kid-absent")
	jwks := newTestJWKS(t, published.jwk())
	v := newTestVerifier(t, jwks.srv.URL)

	token := signRawClaims(t, unknown, validClaims(testIssuer))

	assertion, err := v.Verify(context.Background(), token)
	if assertion != nil {
		t.Errorf("Verify: got non-nil assertion, want nil")
	}
	if err != ErrInvalidSignature {
		t.Errorf("Verify: err = %v, want %v", err, ErrInvalidSignature)
	}
}

func TestVerify_RejectsExpiredToken(t *testing.T) {
	key := newTestKey(t, "kid-1")
	jwks := newTestJWKS(t, key.jwk())
	v := newTestVerifier(t, jwks.srv.URL)

	claims := validClaims(testIssuer)
	claims["exp"] = time.Now().Add(-1 * time.Minute).Unix()
	token := signRawClaims(t, key, claims)

	assertion, err := v.Verify(context.Background(), token)
	if assertion != nil {
		t.Errorf("Verify: got non-nil assertion, want nil")
	}
	if err != ErrExpired {
		t.Errorf("Verify: err = %v, want %v", err, ErrExpired)
	}
}

func TestVerify_RejectsUnknownIssuer(t *testing.T) {
	key := newTestKey(t, "kid-1")
	jwks := newTestJWKS(t, key.jwk())
	v := newTestVerifier(t, jwks.srv.URL)

	token := signRawClaims(t, key, validClaims("https://not-ui.example.test"))

	assertion, err := v.Verify(context.Background(), token)
	if assertion != nil {
		t.Errorf("Verify: got non-nil assertion, want nil")
	}
	if err != ErrUnknownIssuer {
		t.Errorf("Verify: err = %v, want %v", err, ErrUnknownIssuer)
	}
}

func TestVerify_RejectsMissingFields(t *testing.T) {
	cases := []string{"iss", "sub", "sub_iss", "jti", "return_url"}

	for _, missing := range cases {
		t.Run(missing, func(t *testing.T) {
			key := newTestKey(t, "kid-1")
			jwks := newTestJWKS(t, key.jwk())
			v := newTestVerifier(t, jwks.srv.URL)

			claims := validClaims(testIssuer)
			delete(claims, missing)
			token := signRawClaims(t, key, claims)

			assertion, err := v.Verify(context.Background(), token)
			if assertion != nil {
				t.Errorf("Verify: got non-nil assertion, want nil")
			}
			if err != ErrMissingField {
				t.Errorf("Verify: err = %v, want %v", err, ErrMissingField)
			}
		})
	}
}

// TestVerify_SignatureCheckedBeforeExpiry is this package's designated
// Red/Green proof: a token that is both wrongly signed AND expired must
// come back ErrInvalidSignature, never ErrExpired -- proving the
// signature check runs first and an unsigned/wrongly-signed token never
// reaches expiry validation. To confirm it actually guards the ordering:
// in verifier.go's Verify, swap the exp check to run before
// v.keySet.VerifySignature, rerun
// `bazel test //audience_score_system/web/link:link_test
// --test_filter=TestVerify_SignatureCheckedBeforeExpiry` and observe it
// fail (err comes back ErrExpired instead); then revert.
func TestVerify_SignatureCheckedBeforeExpiry(t *testing.T) {
	published := newTestKey(t, "kid-1")
	unknown := newTestKey(t, "kid-absent")
	jwks := newTestJWKS(t, published.jwk())
	v := newTestVerifier(t, jwks.srv.URL)

	claims := validClaims(testIssuer)
	claims["exp"] = time.Now().Add(-1 * time.Minute).Unix()
	token := signRawClaims(t, unknown, claims)

	assertion, err := v.Verify(context.Background(), token)
	if assertion != nil {
		t.Errorf("Verify: got non-nil assertion, want nil")
	}
	if err != ErrInvalidSignature {
		t.Errorf("Verify: err = %v, want %v (signature must be checked before expiry)", err, ErrInvalidSignature)
	}
}

// TestVerify_RejectsWhagentClaimShapedToken proves a genuine
// whagent.Claim persona credential -- signed by an entirely different key,
// as a real ui-minted assertion never would be -- is rejected. This
// verifier is not a second path into whagent-net's persona-credential
// trust root: even a validly-signed whagent.Claim fails here on signature
// alone, because it was never signed by any key this Verifier's
// configured (ui) JWKS knows about.
func TestVerify_RejectsWhagentClaimShapedToken(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating whagent-net key: %v", err)
	}
	_ = pub // never published to this test's JWKS -- see doc comment above

	uiKey := newTestKey(t, "kid-1")
	jwks := newTestJWKS(t, uiKey.jwk())
	v := newTestVerifier(t, jwks.srv.URL)

	signer, err := whagent.New(priv, "https://api.example.test", "whagent-net-kid")
	if err != nil {
		t.Fatalf("whagent.New: %v", err)
	}
	token, err := signer.Mint(context.Background(), whagent.MintRequest{
		Subject:       testSubject,
		SubjectIssuer: testSubIss,
		Actor:         whagent.Actor{Subject: "agent-actor-1", AgentID: "research-agent-v3"},
		SessionID:     "session-abc",
		Audience:      "audience-score-system",
	})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	assertion, err := v.Verify(context.Background(), token)
	if assertion != nil {
		t.Errorf("Verify: got non-nil assertion, want nil")
	}
	if err != ErrInvalidSignature {
		t.Errorf("Verify: err = %v, want %v", err, ErrInvalidSignature)
	}
}

// TestVerify_KidRotation proves a second key added to the served JWKS
// mid-test verifies without recreating the Verifier -- oidc.RemoteKeySet's
// documented behavior of refetching on an unrecognized kid.
func TestVerify_KidRotation(t *testing.T) {
	key1 := newTestKey(t, "kid-1")
	jwks := newTestJWKS(t, key1.jwk())
	v := newTestVerifier(t, jwks.srv.URL)

	token1 := signRawClaims(t, key1, validClaims(testIssuer))
	assertion1, err := v.Verify(context.Background(), token1)
	if err != nil {
		t.Fatalf("Verify (kid-1): unexpected error: %v", err)
	}
	if assertion1.Subject != testSubject {
		t.Errorf("Subject = %q, want %q", assertion1.Subject, testSubject)
	}

	key2 := newTestKey(t, "kid-2")
	jwks.addKey(key2.jwk())

	claims2 := validClaims(testIssuer)
	claims2["jti"] = "test-jti-2"
	token2 := signRawClaims(t, key2, claims2)

	assertion2, err := v.Verify(context.Background(), token2)
	if err != nil {
		t.Fatalf("Verify (kid-2, after rotation): unexpected error: %v", err)
	}
	if assertion2.ID != "test-jti-2" {
		t.Errorf("ID = %q, want %q", assertion2.ID, "test-jti-2")
	}
}
