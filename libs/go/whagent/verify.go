package whagent

import (
	"context"
	"crypto"
	"encoding/json"
	"errors"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// Distinct verification failures a Verifier reports, per this package's
// published contract -- a domain server can rely on errors.Is against
// these rather than string-matching. See Verifier.Verify.
var (
	// ErrInvalidSignature means the token's signature did not verify
	// against any key the Verifier knows about -- including, per NFR4, a
	// token signed by a Keycloak (or any other non-whagent-net) key.
	ErrInvalidSignature = errors.New("whagent: invalid signature")

	// ErrInvalidAudience means the token's aud claim is absent or does
	// not contain the audience the caller expected.
	ErrInvalidAudience = errors.New("whagent: missing or wrong audience")

	// ErrExpired means the token's exp claim has passed.
	ErrExpired = errors.New("whagent: claim expired")

	// ErrMissingClaim means a required Claim field (see claim.go's
	// field-by-field mapping) was absent.
	ErrMissingClaim = errors.New("whagent: missing required claim field")

	// ErrUnknownIssuer means the token's iss claim is not the Verifier's
	// configured whagent-net issuer.
	ErrUnknownIssuer = errors.New("whagent: iss is not the configured whagent-net issuer")
)

// Verifier verifies a whagent-net-signed Claim (LB3, NFR4): it must reject
// a token signed by anything other than the configured whagent-net key --
// including a valid Keycloak token -- so a domain server never needs, and
// is never given, any per-domain Keycloak token-exchange configuration.
// Construct with NewVerifier (JWKS URL, for production, with caching and
// key rotation by `kid`) or NewVerifierFromKey (a static public key, for
// tests).
type Verifier struct {
	issuer string
	keySet oidc.KeySet
}

// NewVerifier constructs a Verifier that fetches and caches whagent-net's
// signing keys from jwksURL (oidc.RemoteKeySet's caching semantics),
// selecting the matching key by `kid` on rotation. issuer must equal the
// `iss` every genuine whagent-net-minted Claim carries -- never a
// domain's, and never a Keycloak realm's (NFR4).
func NewVerifier(ctx context.Context, jwksURL, issuer string) (*Verifier, error) {
	if jwksURL == "" {
		return nil, errors.New("whagent: NewVerifier requires a non-empty JWKS URL")
	}
	if issuer == "" {
		return nil, errors.New("whagent: NewVerifier requires a non-empty issuer")
	}
	return &Verifier{issuer: issuer, keySet: oidc.NewRemoteKeySet(ctx, jwksURL)}, nil
}

// NewVerifierFromKey constructs a Verifier against a single static public
// key -- for tests, where standing up a JWKS endpoint is unwarranted.
func NewVerifierFromKey(pub crypto.PublicKey, issuer string) (*Verifier, error) {
	if pub == nil {
		return nil, errors.New("whagent: NewVerifierFromKey requires a non-nil public key")
	}
	if issuer == "" {
		return nil, errors.New("whagent: NewVerifierFromKey requires a non-empty issuer")
	}
	return &Verifier{issuer: issuer, keySet: &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{pub}}}, nil
}

// Verify verifies that token was signed by the Verifier's configured
// whagent-net key (LB3, NFR4), that its aud claim contains
// expectedAudience, that it has not expired, that every required Claim
// field (see claim.go) is present, and that its iss matches the
// Verifier's configured whagent-net issuer -- rejecting with one of the
// distinct errors above otherwise.
//
// This never delegates audience/issuer/expiry checking to a generic
// verifier configured against a Keycloak (or any other) issuer or JWKS --
// v.keySet is always whagent-net's own, set once at construction
// (NewVerifier / NewVerifierFromKey), never per-call. v.keySet.VerifySignature
// alone decides whether token was signed by a key whagent-net's own JWKS
// (or, for tests, NewVerifierFromKey's static key) actually knows about --
// so a token signed by any other key, including a valid Keycloak token,
// fails here before any of the claim-shape checks below ever run (NFR4).
func (v *Verifier) Verify(ctx context.Context, token, expectedAudience string) (*Claim, error) {
	payload, err := v.keySet.VerifySignature(ctx, token)
	if err != nil {
		return nil, ErrInvalidSignature
	}

	var claim Claim
	if err := json.Unmarshal(payload, &claim); err != nil {
		// The signature verified, but the payload isn't even parseable as a
		// Claim -- this cannot be a genuine whagent-net-minted token, so it
		// is reported the same way as any other signature failure rather
		// than inventing a sixth error case the published contract doesn't
		// document.
		return nil, ErrInvalidSignature
	}

	if claim.Subject == "" ||
		claim.SubjectIssuer == "" ||
		claim.Actor.Subject == "" ||
		claim.Actor.AgentID == "" ||
		claim.WhagentSessionID == "" ||
		claim.Issuer == "" ||
		claim.ID == "" ||
		claim.IssuedAt == nil ||
		claim.Expiry == nil ||
		len(claim.Audience) == 0 {
		return nil, ErrMissingClaim
	}

	if claim.Issuer != v.issuer {
		return nil, ErrUnknownIssuer
	}

	if !claim.Audience.Contains(expectedAudience) {
		return nil, ErrInvalidAudience
	}

	if claim.Expiry.Time().Before(time.Now()) {
		return nil, ErrExpired
	}

	return &claim, nil
}
