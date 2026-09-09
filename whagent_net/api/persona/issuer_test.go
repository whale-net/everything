package persona_test

// Testing-phase suite (issue #2115) for Issuer.Issue: claim shape, TTL,
// and signing correctness verified via //libs/go/whagent's own Verifier --
// "the real round trip across both halves of the contract" the issue's
// Testing section calls out first. See keys_test.go for KeySet/LoadKeySet
// and jwks_test.go for the JWKS HTTP handler.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/whagent"
	"github.com/whale-net/everything/whagent_net/api/persona"
	pb "github.com/whale-net/everything/whagent_net/protos"
	"github.com/whale-net/everything/whagent_net/session"
)

// newTestIssuerAndVerifier builds a persona.Issuer over a freshly generated
// signing key (via LoadKeySet, exactly as `worker` will construct one in
// #2118), plus a whagent.Verifier constructed against that same key's
// public half -- the round trip a domain server's own Verifier performs
// against `api`'s published JWKS.
func newTestIssuerAndVerifier(t *testing.T) (*persona.Issuer, *whagent.Verifier, *persona.KeySet) {
	t.Helper()
	cfg := validKeySetEnvConfig(t)
	ks, err := persona.LoadKeySet(cfg)
	require.NoError(t, err)

	jwks, err := ks.JWKS()
	require.NoError(t, err)
	require.Len(t, jwks.Keys, 1)

	verifier, err := whagent.NewVerifierFromKey(jwks.Keys[0].Key, cfg.Issuer)
	require.NoError(t, err)

	return persona.NewIssuer(ks.ActiveSigner()), verifier, ks
}

// testSession returns a session.Session with distinct OnBehalfOf/Subject
// values so a claim-shape test can tell the two apart (Issue.Issue must
// never conflate the on-behalf-of subject with the acting one).
func testSession() *session.Session {
	return &session.Session{
		SessionID: uuid.New(),
		Subject: session.Subject{
			Iss:  "https://whagent-net.internal.test",
			Sub:  "acting-subject-1",
			Kind: session.SubjectKindService,
		},
		OnBehalfOf: session.Subject{
			Iss:  "https://keycloak.example.test/realms/humans",
			Sub:  "person-1",
			Kind: session.SubjectKindHuman,
		},
	}
}

func TestIssuer_Issue_RejectsNilSession(t *testing.T) {
	issuer, _, _ := newTestIssuerAndVerifier(t)

	token, err := issuer.Issue(context.Background(), nil, "research-agent-v3", "audience-score-system")

	require.Error(t, err)
	assert.Empty(t, token)
}

// TestIssuer_Issue_RoundTripsThroughWhagentVerifier is the issue's Testing
// section's first bullet, verbatim: "A minted token verifies against the
// published JWKS using whagent.Verifier -- the real round trip across both
// halves of the contract".
func TestIssuer_Issue_RoundTripsThroughWhagentVerifier(t *testing.T) {
	issuer, verifier, _ := newTestIssuerAndVerifier(t)
	sess := testSession()

	token, err := issuer.Issue(context.Background(), sess, "research-agent-v3", "audience-score-system")
	require.NoError(t, err)
	require.NotEmpty(t, token)

	claim, err := verifier.Verify(context.Background(), token, "audience-score-system")
	require.NoError(t, err)
	require.NotNil(t, claim)
}

// TestIssuer_Issue_ClaimShapeMatchesSessionOnBehalfOfAndActor is the second
// bullet: "The minted claim's sub/sub_iss equal the session row's
// on_behalf_of_*, and act names the acting subject + agent_id".
func TestIssuer_Issue_ClaimShapeMatchesSessionOnBehalfOfAndActor(t *testing.T) {
	issuer, verifier, _ := newTestIssuerAndVerifier(t)
	sess := testSession()

	token, err := issuer.Issue(context.Background(), sess, "research-agent-v3", "audience-score-system")
	require.NoError(t, err)

	claim, err := verifier.Verify(context.Background(), token, "audience-score-system")
	require.NoError(t, err)

	assert.Equal(t, sess.OnBehalfOf.Sub, claim.Subject, "sub must equal the session's on-behalf-of subject, not the acting one")
	assert.Equal(t, sess.OnBehalfOf.Iss, claim.SubjectIssuer, "sub_iss must equal the session's on-behalf-of issuer")
	assert.Equal(t, sess.Subject.Sub, claim.Actor.Subject, "act.sub must name the acting subject")
	assert.Equal(t, "research-agent-v3", claim.Actor.AgentID, "act.agent_id must be the caller-supplied agentID, not sess.AgentID")
	assert.Equal(t, sess.SessionID.String(), claim.WhagentSessionID)
}

// TestIssuer_Issue_AudienceEqualsRequestedTargetServer is the third bullet:
// "aud equals the requested target server, and a token minted for server A
// fails verification against server B".
//
// This is also this task's designated Red/Green proof (issue's Testing
// section: "mint with a wildcard aud, observe the cross-audience rejection
// test go red, revert"). To confirm it actually guards the single-audience
// contract: temporarily change issuer.go's Issue to pass a second,
// wildcard-style audience alongside the requested one (e.g. build the
// MintRequest with `Audience: audience + ",*"` or otherwise widen it) and
// rerun
//
//	bazel test //whagent_net/api/persona:persona_test --test_filter=TestIssuer_Issue_AudienceEqualsRequestedTargetServer
//
// -- claimB below stops being nil / err stops being whagent.ErrInvalidAudience
// because the widened token now verifies against "server-b" too; then
// revert.
func TestIssuer_Issue_AudienceEqualsRequestedTargetServer(t *testing.T) {
	issuer, verifier, _ := newTestIssuerAndVerifier(t)
	sess := testSession()

	token, err := issuer.Issue(context.Background(), sess, "research-agent-v3", "server-a")
	require.NoError(t, err)

	claimA, err := verifier.Verify(context.Background(), token, "server-a")
	require.NoError(t, err)
	require.NotNil(t, claimA)
	assert.Equal(t, []string{"server-a"}, []string(claimA.Audience))

	claimB, err := verifier.Verify(context.Background(), token, "server-b")
	assert.Nil(t, claimB)
	assert.ErrorIs(t, err, whagent.ErrInvalidAudience)
}

// TestIssuer_Issue_ClaimExpiryIsShortLived is the fourth bullet's "short"
// half: Issue never overrides whagent.MintRequest.TTL, so every claim it
// mints gets exactly whagent.DefaultTTL, always well under MaxTTL (LB3 --
// minutes, not hours).
func TestIssuer_Issue_ClaimExpiryIsShortLived(t *testing.T) {
	issuer, verifier, _ := newTestIssuerAndVerifier(t)
	sess := testSession()

	before := time.Now()
	token, err := issuer.Issue(context.Background(), sess, "research-agent-v3", "audience-score-system")
	require.NoError(t, err)

	claim, err := verifier.Verify(context.Background(), token, "audience-score-system")
	require.NoError(t, err)

	assert.WithinDuration(t, before.Add(whagent.DefaultTTL), claim.Expiry.Time(), 5*time.Second)
	assert.LessOrEqual(t, claim.Expiry.Time().Sub(before), whagent.MaxTTL, "Issue must never mint a token longer-lived than whagent.MaxTTL")
}

// TestIssuer_Issue_UnderlyingClaimFailsVerificationAfterItLapses is the
// fourth bullet's "fails verification after it lapses" half. Issue itself
// exposes no TTL override (it always mints DefaultTTL, ~5 minutes -- too
// long to wait out in a unit test), so this mints directly against the
// same *whagent.Signer Issuer.Issue wraps (KeySet.ActiveSigner -- the
// identical signer, not a separate one) with a deliberately tiny TTL, to
// prove the persona package's own KeySet-to-Verifier round trip enforces
// expiry end to end.
func TestIssuer_Issue_UnderlyingClaimFailsVerificationAfterItLapses(t *testing.T) {
	_, verifier, ks := newTestIssuerAndVerifier(t)

	token, err := ks.ActiveSigner().Mint(context.Background(), whagent.MintRequest{
		Subject:       "person-1",
		SubjectIssuer: "https://keycloak.example.test/realms/humans",
		Actor:         whagent.Actor{Subject: "acting-subject-1", AgentID: "research-agent-v3"},
		SessionID:     "session-abc",
		Audience:      "audience-score-system",
		TTL:           time.Millisecond,
	})
	require.NoError(t, err)

	time.Sleep(10 * time.Millisecond)

	claim, err := verifier.Verify(context.Background(), token, "audience-score-system")
	assert.Nil(t, claim)
	assert.ErrorIs(t, err, whagent.ErrExpired)
}

// TestIssuer_Issue_ClaimBodyContainsNoProfileAttributes is the fifth
// bullet, FR10's forbidden-fields proof: "Serialized token body contains
// none of email, name, preferred_username, picture". Decodes the raw JWT
// payload segment directly (rather than only checking whagent.Claim's Go
// struct, which by construction has no such field) so a future field added
// carelessly to the wire payload -- even one not surfaced on the Claim
// struct -- would still be caught.
func TestIssuer_Issue_ClaimBodyContainsNoProfileAttributes(t *testing.T) {
	issuer, _, _ := newTestIssuerAndVerifier(t)
	sess := testSession()

	token, err := issuer.Issue(context.Background(), sess, "research-agent-v3", "audience-score-system")
	require.NoError(t, err)

	parts := strings.Split(token, ".")
	require.Len(t, parts, 3, "a JWT must have exactly 3 dot-separated parts")

	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(payloadJSON, &payload))

	for _, forbidden := range []string{"email", "name", "preferred_username", "picture"} {
		_, present := payload[forbidden]
		assert.False(t, present, "FR10 forbids the %q profile attribute from ever appearing on a minted claim", forbidden)
	}
}

// TestSessionServiceServer_ExposesNoIssueOrMintRPC is the issue's Testing
// section's last bullet: "No externally reachable RPC/endpoint mints a
// credential for an arbitrary subject (assert the mint path is not
// registered on the public gRPC service)". SessionServiceServer is the
// complete, generated interface every whagent_net/api gRPC method
// satisfies (whagent_net/protos/session.proto) -- reflecting over it
// directly (rather than over the concrete *handlers.SessionServer, which
// could in principle grow an unexported helper method that reflection
// would also catch, but which is not what "registered on the public gRPC
// service" means) proves no method mints, because nothing outside this
// generated interface is ever registered with grpc.Server.
func TestSessionServiceServer_ExposesNoIssueOrMintRPC(t *testing.T) {
	iface := reflect.TypeOf((*pb.SessionServiceServer)(nil)).Elem()
	for i := 0; i < iface.NumMethod(); i++ {
		name := strings.ToLower(iface.Method(i).Name)
		assert.NotContains(t, name, "issue", "SessionService must never expose an Issue RPC -- persona.Issuer.Issue is an internal-only mint path (issue #2115)")
		assert.NotContains(t, name, "mint", "SessionService must never expose a Mint RPC -- persona.Issuer.Issue is an internal-only mint path (issue #2115)")
	}
}
