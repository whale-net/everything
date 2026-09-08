package whagent

import (
	"context"
	"crypto"
	"errors"
	"time"

	"github.com/go-jose/go-jose/v4"
)

// DefaultTTL is the Claim lifetime Signer.Mint stamps when MintRequest.TTL
// is left zero: short-lived per LB3/FR10 -- minutes, not hours.
const DefaultTTL = 5 * time.Minute

// errNotImplemented is returned by every method stub in this package until
// the Implementation phase of issue #2110 lands the real logic. Scaffold
// exists to settle this package's public shape -- the claim struct, the
// Signer/Verifier/Middleware surface, and the idempotency contract -- not
// method bodies.
var errNotImplemented = errors.New("whagent: not implemented yet (scaffold phase, see issue #2110)")

// MintRequest is Signer.Mint's argument: everything needed to mint one
// Claim, for one target audience (domain server).
type MintRequest struct {
	// Subject and SubjectIssuer are the on-behalf-of subject's sub/iss
	// (LB2's shape verbatim) -- see Claim.Subject / Claim.SubjectIssuer.
	Subject       string
	SubjectIssuer string

	// Actor identifies the acting whagent-net agent -- see Actor.
	Actor Actor

	// SessionID is the whagent-net session this call belongs to.
	SessionID string

	// Audience is the target domain server this Claim is minted for
	// (Claim.Audience, "aud"). A Claim is only ever minted for one
	// audience at a time.
	Audience string

	// TTL overrides DefaultTTL when nonzero. Must stay short-lived
	// (minutes, not hours, per LB3) -- Mint's Implementation-phase body
	// enforces an upper bound.
	TTL time.Duration
}

// Signer mints whagent-net-signed Claims (LB3): asymmetric signing only,
// so a domain server (via Verifier) never needs -- and is never given --
// the signing key. Construct with New.
type Signer struct {
	privateKey crypto.Signer
	issuer     string
	keyID      string
}

// New constructs a Signer. privateKey must be an asymmetric private key
// go-jose can sign with (e.g. ed25519.PrivateKey or an *ecdsa.PrivateKey)
// -- never a symmetric secret. issuer is whagent-net's own `iss` value
// (never a domain's). keyID is the JWKS `kid` a Verifier uses to select
// the matching public key on rotation (see JWKS).
func New(privateKey crypto.Signer, issuer, keyID string) (*Signer, error) {
	if privateKey == nil {
		return nil, errors.New("whagent: Signer requires a non-nil private key")
	}
	if issuer == "" {
		return nil, errors.New("whagent: Signer requires a non-empty issuer")
	}
	if keyID == "" {
		return nil, errors.New("whagent: Signer requires a non-empty key ID")
	}
	return &Signer{privateKey: privateKey, issuer: issuer, keyID: keyID}, nil
}

// Mint produces a whagent-net-signed JWT carrying a Claim built from req,
// for req.Audience alone (see MintRequest and Claim for field mapping).
// Implementation-phase note: must reject a req.TTL longer than a sane
// short-lived upper bound (LB3 -- minutes, not hours), defaulting to
// DefaultTTL when req.TTL is zero.
func (s *Signer) Mint(ctx context.Context, req MintRequest) (string, error) {
	return "", errNotImplemented
}

// JWKS returns the public JWKS document (this Signer's public key, tagged
// with its keyID) the minting service serves so a Verifier constructed
// against a JWKS URL (NewVerifier) can fetch and cache it.
func (s *Signer) JWKS() (jose.JSONWebKeySet, error) {
	return jose.JSONWebKeySet{}, errNotImplemented
}
