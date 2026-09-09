package whagent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMintVerify_RoundTrip_YieldsExactClaimFields is the Testing-phase's
// primary happy-path proof: every field MintRequest carried comes back
// unchanged off Verifier.Verify, mapped exactly per claim.go's doc.
func TestMintVerify_RoundTrip_YieldsExactClaimFields(t *testing.T) {
	signer, pub := newTestSigner(t, "whagent-net-test")
	verifier, err := NewVerifierFromKey(pub, "whagent-net-test")
	require.NoError(t, err)

	req := MintRequest{
		Subject:       "person-123",
		SubjectIssuer: "https://keycloak.example.test/realms/humans",
		Actor:         Actor{Subject: "agent-actor-1", AgentID: "research-agent-v3"},
		SessionID:     "session-abc",
		Audience:      "audience-score-system",
	}

	before := time.Now()
	token, err := signer.Mint(context.Background(), req)
	require.NoError(t, err)

	claim, err := verifier.Verify(context.Background(), token, "audience-score-system")
	require.NoError(t, err)
	require.NotNil(t, claim)

	assert.Equal(t, req.Subject, claim.Subject)
	assert.Equal(t, req.SubjectIssuer, claim.SubjectIssuer)
	assert.Equal(t, req.Actor, claim.Actor)
	assert.Equal(t, req.SessionID, claim.WhagentSessionID)
	require.Len(t, claim.Audience, 1)
	assert.Equal(t, req.Audience, claim.Audience[0])
	assert.Equal(t, "whagent-net-test", claim.Issuer)
	assert.NotEmpty(t, claim.ID)
	require.NotNil(t, claim.IssuedAt)
	require.NotNil(t, claim.Expiry)
	assert.WithinDuration(t, before.Add(DefaultTTL), claim.Expiry.Time(), 5*time.Second)
}

// TestMint_SerializedTokenCarriesNoProfileAttributes asserts directly on
// the serialized JWT payload (not just the Claim struct's field set) that
// FR10's no-profile-attribute rule holds -- a struct-only assertion
// wouldn't catch a stray field added via an embedded type or a custom
// MarshalJSON.
func TestMint_SerializedTokenCarriesNoProfileAttributes(t *testing.T) {
	signer, _ := newTestSigner(t, "whagent-net-test")
	token, err := signer.Mint(context.Background(), MintRequest{
		Subject:       "person-123",
		SubjectIssuer: "https://keycloak.example.test/realms/humans",
		Actor:         Actor{Subject: "agent-actor-1", AgentID: "research-agent-v3"},
		SessionID:     "session-abc",
		Audience:      "audience-score-system",
	})
	require.NoError(t, err)

	parts := strings.Split(token, ".")
	require.Len(t, parts, 3, "a JWS compact serialization has exactly 3 dot-separated parts")
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, json.Unmarshal(payload, &body))

	for _, forbidden := range []string{"email", "name", "preferred_username", "picture"} {
		_, present := body[forbidden]
		assert.False(t, present, "minted claim body must never carry %q (FR10)", forbidden)
	}
}
