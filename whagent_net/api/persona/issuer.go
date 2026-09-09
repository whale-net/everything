package persona

import (
	"context"
	"errors"

	"github.com/whale-net/everything/libs/go/whagent"
	"github.com/whale-net/everything/whagent_net/session"
)

// Issuer mints whagent-net-signed persona Claims (LB3, FR10) -- the
// per-tool-call minting surface this task adds. It wraps a *whagent.Signer
// (a KeySet's ActiveSigner) rather than reimplementing minting:
// //libs/go/whagent (#2110) is the one place the Claim's field-by-field
// JWT mapping and short-TTL enforcement live.
//
// Exposure surface (issue #2115's Implementation section, decided): Issue
// is never registered on any gRPC or MCP service, public or otherwise --
// there is no RPC path to it at all. `worker` (#2118) constructs its own
// Issuer directly, in-process, from the same signing-key configuration
// `api` reads (see whagent_net/ENV.md), and mints immediately before
// dispatching each tool call -- mirroring the "no RPC hop" package-
// boundary `api` and `worker` already share for `whagent_net/session`.
// See whagent_net/ARCHITECTURE.md "Identity and auth chaining" §
// "Issuance mechanism" for the full writeup. Concretely, this means the
// only way to ever call Issue is to already be a process holding both the
// signing-key secret and direct `session` store access -- there is no
// path by which an external caller can mint a credential for a subject
// other than an existing session's own.
type Issuer struct {
	signer *whagent.Signer
}

// NewIssuer constructs an Issuer around signer -- see KeySet.ActiveSigner
// for how `api`'s configured signing key becomes one.
func NewIssuer(signer *whagent.Signer) *Issuer {
	return &Issuer{signer: signer}
}

// Issue mints a persona Claim for one tool call: sess's on-behalf-of
// subject (session.Session.OnBehalfOf) becomes the Claim's sub/sub_iss
// (LB2's shape verbatim, FR10), agentID plus sess's acting subject
// (session.Session.Subject) become the Claim's act, and audience names
// the one target domain server this Claim is minted for (never a
// wildcard or multi-audience token -- see whagent.MintRequest.Audience).
// agentID is passed explicitly rather than read from sess.AgentID because
// a session's assigned agent definition can drift over its lifetime
// (SCD2, ARCHITECTURE.md "Domain-owned MCP servers and the tool
// contract") -- the caller supplies whichever agent definition is current
// for the turn being minted, not necessarily sess's original one.
func (i *Issuer) Issue(ctx context.Context, sess *session.Session, agentID, audience string) (string, error) {
	if sess == nil {
		return "", errors.New("persona: Issue requires a non-nil session")
	}
	return i.signer.Mint(ctx, whagent.MintRequest{
		Subject:       sess.OnBehalfOf.Sub,
		SubjectIssuer: sess.OnBehalfOf.Iss,
		Actor: whagent.Actor{
			Subject: sess.Subject.Sub,
			AgentID: agentID,
		},
		SessionID: sess.SessionID.String(),
		Audience:  audience,
	})
}
