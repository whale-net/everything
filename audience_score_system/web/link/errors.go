package link

import "errors"

// Distinct verification failures a Verifier reports, mirroring
// whagent.Verifier's error-shape convention (libs/go/whagent/verify.go)
// but as this package's OWN values -- never whagent's. A link.Verifier is
// not a second path into whagent-net's persona-credential trust root (see
// this package's doc comment in assertion.go), so it never returns, wraps,
// or aliases a whagent error.
var (
	// ErrInvalidSignature means the token's signature did not verify
	// against any key the Verifier's configured ui JWKS knows about --
	// including a token signed by an unrelated key, such as a genuine
	// whagent.Claim signed by whagent-net's own key.
	ErrInvalidSignature = errors.New("link: invalid signature")

	// ErrExpired means the token's exp claim has passed.
	ErrExpired = errors.New("link: assertion expired")

	// ErrUnknownIssuer means the token's iss claim does not match the
	// Verifier's configured ASS_WHAGENT_UI_ISSUER.
	ErrUnknownIssuer = errors.New("link: iss is not the configured ui issuer")

	// ErrMissingField means a required Assertion field (see assertion.go)
	// was absent from the token payload.
	ErrMissingField = errors.New("link: missing required assertion field")
)
