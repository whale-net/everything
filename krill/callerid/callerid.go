// Package callerid derives the verified caller identity (acting and
// on-behalf-of) from a credential an auth front door already verified. It is
// pure: it never reads a request, only the verified credential.
package callerid

import (
	"errors"
	"fmt"

	"github.com/whale-net/everything/krill/identity"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/whagent"
)

// Kind is the derivable identity kind.
type Kind string

const (
	KindHuman   Kind = "human"
	KindAgent   Kind = "agent"
	KindService Kind = "service"
)

// Subject is one verified (iss, sub) identity.
type Subject struct {
	Iss  string
	Sub  string
	Kind Kind
}

// Caller is the identity a front door verified. Acting made the call;
// OnBehalfOf is who its writes are attributed to.
type Caller struct {
	Acting           Subject
	OnBehalfOf       Subject
	WhagentSessionID string
}

// ErrUnauthenticated marks a credential carrying no usable identity.
var ErrUnauthenticated = errors.New("unauthenticated")

// FromMCPAuth derives the caller from the mcpauth (OAuth) door's
// TokenInfo.UserID ("iss|sub") and, when present, the whagent door's claim.
// A whagent claim takes precedence.
func FromMCPAuth(userID string, claim *whagent.Claim) (Caller, error) {
	if claim != nil {
		return FromWhagent(claim)
	}
	iss, sub, err := identity.Decode(userID)
	if err != nil {
		return Caller{}, fmt.Errorf("%w: caller credential carries no usable identity: %v", ErrUnauthenticated, err)
	}
	self := Subject{Iss: iss, Sub: sub, Kind: KindHuman}
	return Caller{Acting: self, OnBehalfOf: self}, nil
}

// FromWhagent derives the caller from a verified whagent claim: acting is the
// agent, on-behalf-of is the human. act.agent_id is not carried (known gap).
func FromWhagent(claim *whagent.Claim) (Caller, error) {
	if claim == nil || claim.Issuer == "" || claim.Actor.Subject == "" || claim.SubjectIssuer == "" || claim.Subject == "" {
		return Caller{}, fmt.Errorf("%w: whagent claim missing iss/act.sub/sub_iss/sub", ErrUnauthenticated)
	}
	return Caller{
		Acting:           Subject{Iss: claim.Issuer, Sub: claim.Actor.Subject, Kind: KindAgent},
		OnBehalfOf:       Subject{Iss: claim.SubjectIssuer, Sub: claim.Subject, Kind: KindHuman},
		WhagentSessionID: claim.WhagentSessionID,
	}, nil
}

// FromOIDC derives the caller from verified Keycloak claims.
func FromOIDC(c *grpcauth.Claims) (Caller, error) {
	if c == nil || c.Issuer == "" || c.Subject == "" {
		return Caller{}, fmt.Errorf("%w: OIDC claims missing iss/sub", ErrUnauthenticated)
	}
	kind := KindHuman
	if c.IsServiceAccount {
		kind = KindService
	}
	self := Subject{Iss: c.Issuer, Sub: c.Subject, Kind: kind}
	return Caller{Acting: self, OnBehalfOf: self}, nil
}
