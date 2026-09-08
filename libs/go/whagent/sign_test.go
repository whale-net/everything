package whagent

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMint_RejectsTTLLongerThanMaxTTL(t *testing.T) {
	signer, _ := newTestSigner(t, "whagent-net-test")

	_, err := signer.Mint(context.Background(), MintRequest{
		Subject:       "person-1",
		SubjectIssuer: "https://keycloak.example.test/realms/humans",
		Actor:         Actor{Subject: "agent-actor-1", AgentID: "research-agent-v3"},
		SessionID:     "session-abc",
		Audience:      "audience-score-system",
		TTL:           MaxTTL + time.Minute,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "MaxTTL")
}

func TestMint_DefaultsToDefaultTTLWhenUnset(t *testing.T) {
	signer, pub := newTestSigner(t, "whagent-net-test")
	verifier, err := NewVerifierFromKey(pub, "whagent-net-test")
	require.NoError(t, err)

	before := time.Now()
	token, err := signer.Mint(context.Background(), MintRequest{
		Subject:       "person-1",
		SubjectIssuer: "https://keycloak.example.test/realms/humans",
		Actor:         Actor{Subject: "agent-actor-1", AgentID: "research-agent-v3"},
		SessionID:     "session-abc",
		Audience:      "audience-score-system",
	})
	require.NoError(t, err)

	claim, err := verifier.Verify(context.Background(), token, "audience-score-system")
	require.NoError(t, err)
	assert.WithinDuration(t, before.Add(DefaultTTL), claim.Expiry.Time(), 5*time.Second)
}

func TestSigner_JWKS_PublishesPublicKeyTaggedWithKeyID(t *testing.T) {
	signer, pub := newTestSigner(t, "whagent-net-test")

	jwks, err := signer.JWKS()
	require.NoError(t, err)
	require.Len(t, jwks.Keys, 1)
	assert.Equal(t, testKeyID, jwks.Keys[0].KeyID)
	assert.Equal(t, pub, jwks.Keys[0].Key)
	assert.Equal(t, "sig", jwks.Keys[0].Use)
}
