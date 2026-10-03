//go:build integration

// Proves a delegated manmanv2-ops start (Slack-bot service account, or an
// allowlisted whagent agent client) mints persona claims whose sub/sub_iss are
// the originating user, with act naming the delegating caller.
package handlers_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/whagent"
	"github.com/whale-net/everything/whagent_net/api/persona"
	pb "github.com/whale-net/everything/whagent_net/protos"
)

const (
	manmanOpsRole     = "whagent-manmanv2-ops"
	manmanServerAudie = "https://manmanv2-mcp.test"
)

func TestDelegatedManmanV2OpsStart_MintsClaimAsOriginatingUser(t *testing.T) {
	cases := []struct {
		name   string
		caller *grpcauth.Claims
	}{
		{"slack_bot_service_account", &grpcauth.Claims{Subject: "fcm-service", ClientID: "fcm-client", IsServiceAccount: true, Roles: []string{manmanOpsRole}}},
		{"delegating_whagent_agent_client", &grpcauth.Claims{Subject: "parent-agent-svc", ClientID: "parent-agent-client", IsServiceAccount: true, Roles: []string{manmanOpsRole}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, store := newOnBehalfOfTestServer(t, tc.caller.ClientID)
			ctx := context.Background()
			role := manmanOpsRole
			seedServiceTestAgent(t, ctx, store, "manmanv2-ops", &role)

			resp, err := srv.StartSession(grpcauth.ContextWithClaims(ctx, tc.caller), &pb.StartSessionRequest{
				AgentId:    "manmanv2-ops",
				OnBehalfOf: subjectToTestProto(assertedUser),
			})
			require.NoError(t, err)
			sess, err := store.Sessions().GetByID(ctx, mustUUID(t, resp.Session.SessionId))
			require.NoError(t, err)

			pub, priv, err := ed25519.GenerateKey(rand.Reader)
			require.NoError(t, err)
			signer, err := whagent.New(priv, "whagent-net-test", "k1")
			require.NoError(t, err)
			verifier, err := whagent.NewVerifierFromKey(pub, "whagent-net-test")
			require.NoError(t, err)

			token, err := persona.NewIssuer(signer).Issue(ctx, sess, "manmanv2-ops", manmanServerAudie)
			require.NoError(t, err)
			claim, err := verifier.Verify(ctx, token, manmanServerAudie)
			require.NoError(t, err)

			assert.Equal(t, assertedUser.Sub, claim.Subject)
			assert.Equal(t, assertedUser.Iss, claim.SubjectIssuer)
			assert.Equal(t, tc.caller.Subject, claim.Actor.Subject)
			assert.NotEqual(t, tc.caller.Subject, claim.Subject, "claim subject must never be the delegating caller")
			assert.Equal(t, "manmanv2-ops", claim.Actor.AgentID)
		})
	}
}

// A caller lacking the agent's required role is refused even when delegating.
func TestDelegatedManmanV2OpsStart_CallerWithoutRoleDenied(t *testing.T) {
	srv, store := newOnBehalfOfTestServer(t, "fcm-client")
	ctx := context.Background()
	role := manmanOpsRole
	seedServiceTestAgent(t, ctx, store, "manmanv2-ops", &role)

	_, err := srv.StartSession(grpcauth.ContextWithClaims(ctx, &grpcauth.Claims{Subject: "fcm-service", ClientID: "fcm-client", IsServiceAccount: true}), &pb.StartSessionRequest{
		AgentId:    "manmanv2-ops",
		OnBehalfOf: subjectToTestProto(assertedUser),
	})
	require.Error(t, err)
}
