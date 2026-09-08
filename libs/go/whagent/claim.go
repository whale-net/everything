// Package whagent is the one published contract a domain-owned MCP server
// satisfies to be usable by a whagent-net session (FR12), and the
// credential whagent-net presents on every tool call (FR10). It is a
// standalone library with no dependency on `whagent_net/` -- a consuming
// domain (audience_score_system for M1) can vendor it, mount its verifying
// middleware, and be usable by whagent-net sessions with no
// whagent-net-side change required (FR12).
//
// Two load-bearing decisions live here:
//
//   - LB3 -- one verifiable credential: a short-lived JWT minted and
//     signed with whagent-net's own key (see Signer, Verifier). A domain
//     server never accepts a bare Keycloak token or an unsigned claim in
//     its place (NFR4) -- whagent-net is the trust root for agent
//     actions; there is no per-domain Keycloak token-exchange
//     configuration.
//   - LB4 -- the idempotency-key field name, derivation, and guard scope
//     (see idempotency.go).
//
// # What a consuming domain does (FR12)
//
//  1. Mounts Middleware (this package) as a new per-tool-call
//     authentication path on its MCP server, alongside -- never in place
//     of -- any web-session caller-resolution flow it already has (e.g.
//     audience_score_system's mcp/server/auth.go PersonMiddleware).
//     Mounts HTTPMiddleware as the paired HTTP-layer half, standalone: it
//     does not require libs/go/mcpauth to be anywhere in the chain.
//  2. Resolves the verified Claim's (iss, sub) -- i.e. (SubjectIssuer,
//     Subject) -- on-behalf-of pair to its own user record, auto-
//     provisioning that record the first time a given (iss, sub) is seen
//     rather than requiring a pre-existing linked account. That record is
//     necessarily identity-key-only: FR10 forbids this claim from
//     carrying an email, display name, or any other profile attribute, so
//     a domain must acquire profile data through its own means.
//  3. Scopes its idempotency guard on (tool, resolved identity, key) per
//     LB4 -- see idempotency.go -- keeping one idempotency store across
//     all of its authentication paths, not one per path.
//
// See README.md for the full contract write-up.
package whagent

import (
	"github.com/go-jose/go-jose/v4/jwt"
)

// Actor identifies who whagent-net is acting as when it mints a Claim
// (LB3, FR10): the acting agent's own subject, plus the whagent-net
// agent_id (NFR6's versioned agent definition) the session is running as.
// This is distinct from Claim.Subject/Claim.SubjectIssuer, which always
// identify the on-behalf-of subject (for M1, always identical to the
// human operator who started the session -- no delegated-caller path
// exists yet) -- see FR10's on-behalf-of/acting-agent split.
type Actor struct {
	// Subject is the acting agent's own subject identifier.
	Subject string `json:"sub"`

	// AgentID is the whagent-net agent_id the session is running as.
	AgentID string `json:"agent_id"`
}

// Claim is the one whagent-net-signed, short-lived credential every tool
// call carries (LB3, FR10). It embeds jwt.Claims for the standard
// registered fields (iss, sub, aud, exp, iat, jti) and adds exactly the
// identity-key private claims FR10 allows: sub_iss, act, and
// whagent_session_id. FR10 is explicit that this claim carries no email,
// display name, or other profile attribute -- do not add a convenience
// profile field here "because it would be useful"; that is a contract
// change, not a nicety.
//
// Field-by-field JWT mapping:
//
//   - Subject (embedded jwt.Claims.Subject, "sub") -- the on-behalf-of
//     subject's sub.
//   - SubjectIssuer ("sub_iss") -- the on-behalf-of subject's iss (LB2's
//     shape verbatim).
//   - Actor ("act") -- the acting agent plus the acting agent_id; see
//     Actor.
//   - WhagentSessionID ("whagent_session_id") -- the whagent-net session
//     this call belongs to.
//   - Audience (embedded jwt.Claims.Audience, "aud") -- the target domain
//     server this Claim was minted for.
//   - Issuer (embedded jwt.Claims.Issuer, "iss") -- whagent-net's own
//     issuer, never a domain's.
//   - IssuedAt (embedded jwt.Claims.IssuedAt, "iat").
//   - Expiry (embedded jwt.Claims.Expiry, "exp") -- short-lived: minutes,
//     not hours (see DefaultTTL).
//   - ID (embedded jwt.Claims.ID, "jti").
//
// A Verifier must reject a token missing any of the above (ErrMissingClaim)
// -- see verify.go.
type Claim struct {
	jwt.Claims

	// SubjectIssuer is the on-behalf-of subject's iss (LB2's shape
	// verbatim) -- e.g. the Keycloak realm issuer URL the human
	// authenticated against when starting the session.
	SubjectIssuer string `json:"sub_iss"`

	// Actor identifies the acting whagent-net agent (see Actor).
	Actor Actor `json:"act"`

	// WhagentSessionID is the whagent-net session this call belongs to.
	WhagentSessionID string `json:"whagent_session_id"`
}
