// Package link is `web`'s link-assertion verification package (issue
// #2598, FR3's verification half, NFR5): the claim type and Verifier ASS
// `web` uses to check a link assertion whagent_net/ui's linkassert package
// mints (FR2), and nothing else.
//
// # Why this is not whagent.Verifier
//
// whagent.Verifier.Verify (libs/go/whagent/verify.go) unconditionally
// requires Claim.SubjectIssuer, Claim.Actor.Subject, Claim.Actor.AgentID,
// and Claim.WhagentSessionID to be non-empty, rejecting with
// ErrMissingClaim otherwise. An Assertion deliberately carries none of the
// Actor/session fields -- it is a browser-identity handshake, not a
// tool-call session credential -- so calling whagent.Verifier.Verify
// against one would always fail.
//
// This is also deliberately not a second generic verifier/claim pair added
// to libs/go/whagent. That would need its own scope justification against
// LB3's package-doc framing ("the one published contract... whagent-net is
// the trust root for agent actions"); keeping it here instead keeps that
// reasoning intact -- it is ASS's own code, verifying a claim shape only
// ASS's `web` consumes, against a key only `ui` holds. libs/go/whagent is
// unmodified by this package, and audience_score_system/mcp's existing
// whagent.Verifier usage is untouched.
//
// # NFR5
//
// This package is standalone: it must not import, extend, or hook into
// web/auth's mcpauth machinery (/authorize, /token, /register) --
// "alongside, never on top of".
package link

import "time"

// Assertion is the claim shape ui mints (whagent_net/ui/linkassert) --
// exactly what this handshake needs and nothing from whagent.Claim's
// session/Actor shape (see this package's doc comment above).
type Assertion struct {
	// Issuer is the token's iss -- ui's WHAGENT_UI_PUBLIC_URL.
	Issuer string

	// Subject is the Operator's Keycloak subject (sub).
	Subject string

	// SubjectIssuer is the Operator's Keycloak issuer.
	SubjectIssuer string

	// ID is the token's jti -- the single-use key (#2597).
	ID string

	// Expiry is the token's exp.
	Expiry time.Time

	// ReturnURL is where `web` redirects the Operator once the
	// assertion is verified.
	ReturnURL string
}
