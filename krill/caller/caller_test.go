package caller

import (
	"errors"
	"testing"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/identity"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/whagent"
)

func whagentClaim() *whagent.Claim {
	claim := &whagent.Claim{SubjectIssuer: "kc", WhagentSessionID: "ws1"}
	claim.Issuer, claim.Subject = "whagent", "human1"
	claim.Actor.Subject = "agent1"
	return claim
}

func TestFromTokenInfoMCPAuth(t *testing.T) {
	for _, bad := range []string{"", "nopipe", "|sub", "iss|", "a|b|c"} {
		_, err := FromTokenInfo(&sdkauth.TokenInfo{UserID: bad})
		require.True(t, errors.Is(err, ErrUnauthenticated), bad)
	}
	_, err := FromTokenInfo(nil)
	require.True(t, errors.Is(err, ErrUnauthenticated))

	id, err := FromTokenInfo(&sdkauth.TokenInfo{UserID: "https://kc/realms/x|u1"})
	require.NoError(t, err)
	want := Subject{Iss: "https://kc/realms/x", Sub: "u1", Kind: KindHuman}
	require.Equal(t, want, id.Acting)
	require.Equal(t, want, id.OnBehalfOf)
}

func TestFromTokenInfoWhagent(t *testing.T) {
	info := &sdkauth.TokenInfo{UserID: "ignored|x", Extra: map[string]any{WhagentClaimExtraKey: whagentClaim()}}
	id, err := FromTokenInfo(info)
	require.NoError(t, err)
	require.Equal(t, Subject{Iss: "whagent", Sub: "agent1", Kind: KindAgent}, id.Acting)
	require.Equal(t, Subject{Iss: "kc", Sub: "human1", Kind: KindHuman}, id.OnBehalfOf)
	require.Equal(t, "ws1", id.WhagentSessionID)

	bad := whagentClaim()
	bad.Actor.Subject = ""
	_, err = FromTokenInfo(&sdkauth.TokenInfo{Extra: map[string]any{WhagentClaimExtraKey: bad}})
	require.True(t, errors.Is(err, ErrUnauthenticated))

	bad = whagentClaim()
	bad.Subject = "a|b"
	_, err = FromTokenInfo(&sdkauth.TokenInfo{Extra: map[string]any{WhagentClaimExtraKey: bad}})
	require.True(t, errors.Is(err, ErrUnauthenticated))

	_, err = FromTokenInfo(&sdkauth.TokenInfo{Extra: map[string]any{WhagentClaimExtraKey: "wrong type"}})
	require.True(t, errors.Is(err, ErrUnauthenticated))
}

func TestFromOIDCClaims(t *testing.T) {
	id, err := FromOIDCClaims(&grpcauth.Claims{Issuer: "kc", Subject: "u1"})
	require.NoError(t, err)
	require.Equal(t, KindHuman, id.Acting.Kind)

	id, err = FromOIDCClaims(&grpcauth.Claims{Issuer: "kc", Subject: "sa1", IsServiceAccount: true})
	require.NoError(t, err)
	require.Equal(t, Subject{Iss: "kc", Sub: "sa1", Kind: KindService}, id.Acting)
	require.Equal(t, id.Acting, id.OnBehalfOf)

	for _, bad := range []*grpcauth.Claims{nil, {Subject: "u"}, {Issuer: "kc"}, {Issuer: "k|c", Subject: "u"}, {Issuer: "kc", Subject: "u|v"}} {
		_, err := FromOIDCClaims(bad)
		require.True(t, errors.Is(err, ErrUnauthenticated))
	}
}

// One Keycloak user resolves to the same principal through mcpauth and OIDC.
func TestCrossDoorPrincipalEquality(t *testing.T) {
	iss, sub := "https://kc/realms/x", "user-1"
	uid, err := identity.Encode(iss, sub)
	require.NoError(t, err)
	a, err := FromTokenInfo(&sdkauth.TokenInfo{UserID: uid})
	require.NoError(t, err)
	b, err := FromOIDCClaims(&grpcauth.Claims{Issuer: iss, Subject: sub})
	require.NoError(t, err)
	require.Equal(t, a, b)
}
