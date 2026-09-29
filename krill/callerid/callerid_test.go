package callerid

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/identity"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/whagent"
)

func TestFromMCPAuth(t *testing.T) {
	for _, bad := range []string{"", "nopipe", "|sub", "iss|", "a|b|c"} {
		_, err := FromMCPAuth(bad, nil)
		require.Error(t, err, bad)
		require.True(t, errors.Is(err, ErrUnauthenticated), bad)
	}
	c, err := FromMCPAuth("https://kc/realms/x|u1", nil)
	require.NoError(t, err)
	want := Subject{Iss: "https://kc/realms/x", Sub: "u1", Kind: KindHuman}
	require.Equal(t, want, c.Acting)
	require.Equal(t, want, c.OnBehalfOf)
}

func TestFromWhagent(t *testing.T) {
	claim := &whagent.Claim{SubjectIssuer: "kc", WhagentSessionID: "ws1"}
	claim.Issuer, claim.Subject = "whagent", "human1"
	claim.Actor.Subject = "agent1"

	// The claim wins over any UserID.
	c, err := FromMCPAuth("ignored|x", claim)
	require.NoError(t, err)
	require.Equal(t, Subject{Iss: "whagent", Sub: "agent1", Kind: KindAgent}, c.Acting)
	require.Equal(t, Subject{Iss: "kc", Sub: "human1", Kind: KindHuman}, c.OnBehalfOf)
	require.Equal(t, "ws1", c.WhagentSessionID)

	bad := *claim
	bad.Actor.Subject = ""
	_, err = FromWhagent(&bad)
	require.True(t, errors.Is(err, ErrUnauthenticated))
	_, err = FromWhagent(nil)
	require.True(t, errors.Is(err, ErrUnauthenticated))
}

func TestFromOIDC(t *testing.T) {
	c, err := FromOIDC(&grpcauth.Claims{Issuer: "kc", Subject: "u1"})
	require.NoError(t, err)
	require.Equal(t, KindHuman, c.Acting.Kind)

	c, err = FromOIDC(&grpcauth.Claims{Issuer: "kc", Subject: "sa1", IsServiceAccount: true})
	require.NoError(t, err)
	require.Equal(t, Subject{Iss: "kc", Sub: "sa1", Kind: KindService}, c.Acting)
	require.Equal(t, c.Acting, c.OnBehalfOf)

	for _, bad := range []*grpcauth.Claims{nil, {Subject: "u"}, {Issuer: "kc"}} {
		_, err := FromOIDC(bad)
		require.True(t, errors.Is(err, ErrUnauthenticated))
	}
}

// One Keycloak user resolves to the same principal through mcpauth and OIDC.
func TestCrossDoorPrincipalEquality(t *testing.T) {
	iss, sub := "https://kc/realms/x", "user-1"
	uid, err := identity.Encode(iss, sub)
	require.NoError(t, err)
	a, err := FromMCPAuth(uid, nil)
	require.NoError(t, err)
	b, err := FromOIDC(&grpcauth.Claims{Issuer: iss, Subject: sub})
	require.NoError(t, err)
	require.Equal(t, a, b)
}
