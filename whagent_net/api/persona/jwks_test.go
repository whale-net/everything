package persona_test

// Testing-phase suite (issue #2115) for the JWKS HTTP handler: correct key
// publication and kid matching, exercised through a real
// httptest.Server + whagent.Verifier round trip (the same shape a domain
// server's own Verifier wiring uses in production). See keys_test.go for
// KeySet/LoadKeySet's own unit tests and issuer_test.go for Issuer.Issue.

import (
	"context"
	"crypto"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-jose/go-jose/v4"

	"github.com/whale-net/everything/libs/go/whagent"
	"github.com/whale-net/everything/whagent_net/api/persona"
)

// mustParsePrivateKeyPEMForTest decodes a PEM-encoded PKCS8 private key --
// the same format generateSigningKeyPEM (keys_test.go) produces -- back
// into a crypto.Signer, for constructing a *whagent.Signer directly from a
// "retired" key's material (simulating a key `worker`/`api` would have
// used to mint moments before it was rotated out), independently of
// persona's own unexported parsePrivateKeyPEM.
func mustParsePrivateKeyPEMForTest(t *testing.T, pemStr string) crypto.Signer {
	t.Helper()
	block, _ := pem.Decode([]byte(pemStr))
	require.NotNil(t, block)
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	require.NoError(t, err)
	signer, ok := key.(crypto.Signer)
	require.True(t, ok)
	return signer
}

func TestJWKSHandler_ServesPublicKeyMaterialAtTheStablePath(t *testing.T) {
	cfg := validKeySetEnvConfig(t)
	ks, err := persona.LoadKeySet(cfg)
	require.NoError(t, err)

	srv := httptest.NewServer(persona.NewMux(ks))
	defer srv.Close()

	resp, err := http.Get(srv.URL + persona.JWKSPath)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	var got jose.JSONWebKeySet
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	require.Len(t, got.Keys, 1)
	assert.Equal(t, cfg.ActiveKeyID, got.Keys[0].KeyID)
	assert.True(t, got.Keys[0].IsPublic(), "JWKS response must contain only public key material")
}

// TestJWKSHandler_RejectsNonGetHeadMethods proves the handler is a
// read-only publication surface -- another angle on "no externally
// reachable RPC/endpoint mints a credential": a POST here cannot even
// reach a mint operation, since JWKSHandler never does anything but read
// ks.JWKS().
func TestJWKSHandler_RejectsNonGetHeadMethods(t *testing.T) {
	cfg := validKeySetEnvConfig(t)
	ks, err := persona.LoadKeySet(cfg)
	require.NoError(t, err)

	srv := httptest.NewServer(persona.NewMux(ks))
	defer srv.Close()

	resp, err := http.Post(srv.URL+persona.JWKSPath, "application/json", nil)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}

func TestJWKSHandler_HEADReturnsHeadersWithNoBody(t *testing.T) {
	cfg := validKeySetEnvConfig(t)
	ks, err := persona.LoadKeySet(cfg)
	require.NoError(t, err)

	srv := httptest.NewServer(persona.NewMux(ks))
	defer srv.Close()

	resp, err := http.Head(srv.URL + persona.JWKSPath)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
}

// TestJWKSHandler_TokensFromEveryPublishedKidVerifyThroughRealHTTPRoundTrip
// is this task's rotation-window acceptance criterion exercised at the
// HTTP layer, in full: "serves multiple kids during rotation; a token
// signed with the newly added key verifies before the old key is removed".
// Unlike keys_test.go's KeySet.JWKS()-level checks, this drives a real
// whagent.NewVerifier against a live httptest.Server, so it also proves
// kid-based key *selection* -- not just publication -- actually works: a
// token signed by the retired key must select the retired key's JWKS
// entry by its own kid, not simply "whichever key happens to be first".
func TestJWKSHandler_TokensFromEveryPublishedKidVerifyThroughRealHTTPRoundTrip(t *testing.T) {
	cfg := validKeySetEnvConfig(t)
	retiredPEM := generateSigningKeyPEM(t)
	additional, err := json.Marshal([]map[string]string{
		{"kid": "retired-1", "private_key_pem": retiredPEM},
	})
	require.NoError(t, err)
	cfg.AdditionalKeysJSON = string(additional)

	ks, err := persona.LoadKeySet(cfg)
	require.NoError(t, err)

	srv := httptest.NewServer(persona.NewMux(ks))
	defer srv.Close()

	ctx := context.Background()
	verifier, err := whagent.NewVerifier(ctx, srv.URL+persona.JWKSPath, cfg.Issuer)
	require.NoError(t, err)

	issuer := persona.NewIssuer(ks.ActiveSigner())
	sess := testSession()

	// The active key's token verifies via the JWKS this handler serves.
	activeToken, err := issuer.Issue(ctx, sess, "research-agent-v3", "audience-score-system")
	require.NoError(t, err)
	claim, err := verifier.Verify(ctx, activeToken, "audience-score-system")
	require.NoError(t, err)
	require.NotNil(t, claim)

	// A token signed with the *retired* key -- simulating one minted moments
	// before a rotation -- must still verify against this same JWKS
	// endpoint, because JWKSHandler publishes KeySet.JWKS()'s full union,
	// not just the active signer's key.
	retiredKeyConfig, err := whagent.New(mustParsePrivateKeyPEMForTest(t, retiredPEM), cfg.Issuer, "retired-1")
	require.NoError(t, err)
	retiredToken, err := retiredKeyConfig.Mint(ctx, whagent.MintRequest{
		Subject:       sess.OnBehalfOf.Sub,
		SubjectIssuer: sess.OnBehalfOf.Iss,
		Actor:         whagent.Actor{Subject: sess.Subject.Sub, AgentID: "research-agent-v3"},
		SessionID:     sess.SessionID.String(),
		Audience:      "audience-score-system",
	})
	require.NoError(t, err)

	retiredClaim, err := verifier.Verify(ctx, retiredToken, "audience-score-system")
	require.NoError(t, err, "a token signed with a still-published retired kid must verify during the rotation window")
	require.NotNil(t, retiredClaim)
}
