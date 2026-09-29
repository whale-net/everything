// Package caller derives the verified caller identity from an auth front
// door's credential -- a pure function of the credential, never of
// client-supplied request fields. Shared by krill/mcp and krill/api.
package caller

import (
	"errors"
	"fmt"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/whale-net/everything/krill/identity"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/whagent"
)

// WhagentClaimExtraKey is the TokenInfo.Extra key the whagent door stashes
// its verified *whagent.Claim under.
const WhagentClaimExtraKey = "krill/mcp/server.whagent_claim"

// Kinds a caller identity can derive to.
const (
	KindHuman   = "human"
	KindAgent   = "agent"
	KindService = "service"
)

// ErrUnauthenticated marks a credential that yields no usable identity.
var ErrUnauthenticated = errors.New("unauthenticated")

// Subject is one verified (iss, sub) identity and its kind.
type Subject struct {
	Iss  string
	Sub  string
	Kind string
}

// Identity is the verified caller. Acting made the call; OnBehalfOf is who
// its writes are attributed to.
type Identity struct {
	Acting           Subject
	OnBehalfOf       Subject
	WhagentSessionID string
}

func unauth(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrUnauthenticated, fmt.Sprintf(format, a...))
}

// FromTokenInfo derives the caller from an MCP door's verified TokenInfo:
// the whagent claim when present, else the mcpauth "iss|sub" UserID.
func FromTokenInfo(info *sdkauth.TokenInfo) (Identity, error) {
	if info == nil {
		return Identity{}, unauth("no caller credential resolved")
	}
	if v, present := info.Extra[WhagentClaimExtraKey]; present {
		claim, ok := v.(*whagent.Claim)
		if !ok || claim == nil {
			return Identity{}, unauth("malformed whagent claim")
		}
		return fromWhagent(claim)
	}
	iss, sub, err := identity.Decode(info.UserID)
	if err != nil {
		return Identity{}, unauth("credential carries no usable identity: %v", err)
	}
	self := Subject{Iss: iss, Sub: sub, Kind: KindHuman}
	return Identity{Acting: self, OnBehalfOf: self}, nil
}

func fromWhagent(c *whagent.Claim) (Identity, error) {
	if _, err := identity.Encode(c.Issuer, c.Actor.Subject); err != nil {
		return Identity{}, unauth("whagent actor: %v", err)
	}
	if _, err := identity.Encode(c.SubjectIssuer, c.Subject); err != nil {
		return Identity{}, unauth("whagent subject: %v", err)
	}
	return Identity{
		Acting:           Subject{Iss: c.Issuer, Sub: c.Actor.Subject, Kind: KindAgent},
		OnBehalfOf:       Subject{Iss: c.SubjectIssuer, Sub: c.Subject, Kind: KindHuman},
		WhagentSessionID: c.WhagentSessionID,
	}, nil
}

// FromOIDCClaims derives the caller from a verified Keycloak token's
// grpcauth Claims; a service account is kind service, otherwise human.
func FromOIDCClaims(c *grpcauth.Claims) (Identity, error) {
	if c == nil {
		return Identity{}, unauth("no caller credential resolved")
	}
	if _, err := identity.Encode(c.Issuer, c.Subject); err != nil {
		return Identity{}, unauth("credential carries no usable identity: %v", err)
	}
	kind := KindHuman
	if c.IsServiceAccount {
		kind = KindService
	}
	self := Subject{Iss: c.Issuer, Sub: c.Subject, Kind: kind}
	return Identity{Acting: self, OnBehalfOf: self}, nil
}
