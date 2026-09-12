package link

import (
	"context"
	"fmt"

	"github.com/coreos/go-oidc/v3/oidc"
)

// Verifier verifies a ui-minted Assertion (FR3) -- a browser-identity
// handshake token, distinct from whagent.Claim's tool-call session
// credential (see this package's doc comment in assertion.go). Construct
// with NewVerifier.
type Verifier struct {
	issuer string
	keySet oidc.KeySet
}

// NewVerifier constructs a Verifier that fetches and caches ui's signing
// keys from jwksURL (ASS_WHAGENT_UI_JWKS_URL), selecting the matching key
// by `kid` on rotation -- the same construction pattern
// whagent.NewVerifier uses (libs/go/whagent/verify.go), with Assertion
// substituted for whagent.Claim. issuer must equal the `iss` every
// genuine ui-minted Assertion carries (ASS_WHAGENT_UI_ISSUER) -- never
// api's/whagent-net's issuer, and never defaulted from or falling back to
// it (see ENV.md).
//
// Scaffold stub: body filled in during issue #2598's Implementation
// phase.
func NewVerifier(ctx context.Context, jwksURL, issuer string) (*Verifier, error) {
	return nil, fmt.Errorf("link: NewVerifier not implemented -- see issue #2598's Implementation phase")
}

// Verify checks the signature FIRST, then the claim shape -- an unsigned
// or wrongly-signed token must never reach issuer/expiry/field
// validation, and must never be partially trusted; any parse of the
// payload prior to signature verification is for key selection (`kid`)
// only -- and returns the parsed Assertion. Fails closed.
//
// Scaffold stub: body filled in during issue #2598's Implementation
// phase.
func (v *Verifier) Verify(ctx context.Context, token string) (*Assertion, error) {
	return nil, fmt.Errorf("link: Verify not implemented -- see issue #2598's Implementation phase")
}
