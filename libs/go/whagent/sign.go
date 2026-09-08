package whagent

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"errors"
	"fmt"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/uuid"
)

// DefaultTTL is the Claim lifetime Signer.Mint stamps when MintRequest.TTL
// is left zero: short-lived per LB3/FR10 -- minutes, not hours.
const DefaultTTL = 5 * time.Minute

// MaxTTL is the longest MintRequest.TTL Signer.Mint accepts (LB3 -- minutes,
// not hours). A caller requesting anything longer gets an error rather than
// a silently clamped shorter Claim.
const MaxTTL = 30 * time.Minute

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
// Rejects a req.TTL longer than MaxTTL (LB3 -- minutes, not hours);
// defaults to DefaultTTL when req.TTL is zero.
func (s *Signer) Mint(ctx context.Context, req MintRequest) (string, error) {
	if req.Subject == "" {
		return "", errors.New("whagent: Mint requires a non-empty Subject")
	}
	if req.SubjectIssuer == "" {
		return "", errors.New("whagent: Mint requires a non-empty SubjectIssuer")
	}
	if req.Actor.Subject == "" || req.Actor.AgentID == "" {
		return "", errors.New("whagent: Mint requires a non-empty Actor.Subject and Actor.AgentID")
	}
	if req.SessionID == "" {
		return "", errors.New("whagent: Mint requires a non-empty SessionID")
	}
	if req.Audience == "" {
		return "", errors.New("whagent: Mint requires a non-empty Audience")
	}

	ttl := req.TTL
	if ttl == 0 {
		ttl = DefaultTTL
	}
	if ttl < 0 {
		return "", errors.New("whagent: Mint requires a non-negative TTL")
	}
	if ttl > MaxTTL {
		return "", fmt.Errorf("whagent: Mint TTL %s exceeds MaxTTL %s (LB3 -- minutes, not hours)", ttl, MaxTTL)
	}

	alg, err := signatureAlgorithmFor(s.privateKey.Public())
	if err != nil {
		return "", err
	}

	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: alg, Key: s.privateKey},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", s.keyID),
	)
	if err != nil {
		return "", fmt.Errorf("whagent: constructing signer: %w", err)
	}

	now := time.Now()
	claim := Claim{
		Claims: jwt.Claims{
			Issuer:   s.issuer,
			Subject:  req.Subject,
			Audience: jwt.Audience{req.Audience},
			Expiry:   jwt.NewNumericDate(now.Add(ttl)),
			IssuedAt: jwt.NewNumericDate(now),
			ID:       uuid.NewString(),
		},
		SubjectIssuer:    req.SubjectIssuer,
		Actor:            req.Actor,
		WhagentSessionID: req.SessionID,
	}

	token, err := jwt.Signed(signer).Claims(claim).Serialize()
	if err != nil {
		return "", fmt.Errorf("whagent: signing claim: %w", err)
	}
	return token, nil
}

// JWKS returns the public JWKS document (this Signer's public key, tagged
// with its keyID) the minting service serves so a Verifier constructed
// against a JWKS URL (NewVerifier) can fetch and cache it.
func (s *Signer) JWKS() (jose.JSONWebKeySet, error) {
	alg, err := signatureAlgorithmFor(s.privateKey.Public())
	if err != nil {
		return jose.JSONWebKeySet{}, err
	}
	return jose.JSONWebKeySet{
		Keys: []jose.JSONWebKey{
			{
				Key:       s.privateKey.Public(),
				KeyID:     s.keyID,
				Algorithm: string(alg),
				Use:       "sig",
			},
		},
	}, nil
}

// signatureAlgorithmFor picks the go-jose signature algorithm that matches
// pub's key type -- Signer.New requires an asymmetric key (LB3), so this
// only ever needs to distinguish among the asymmetric families go-jose
// supports; a symmetric ([]byte) key type falls through to the default
// error case.
func signatureAlgorithmFor(pub crypto.PublicKey) (jose.SignatureAlgorithm, error) {
	switch key := pub.(type) {
	case ed25519.PublicKey:
		return jose.EdDSA, nil
	case *ecdsa.PublicKey:
		switch key.Curve.Params().BitSize {
		case 256:
			return jose.ES256, nil
		case 384:
			return jose.ES384, nil
		case 521:
			return jose.ES512, nil
		default:
			return "", fmt.Errorf("whagent: unsupported ECDSA curve bit size %d", key.Curve.Params().BitSize)
		}
	case *rsa.PublicKey:
		return jose.RS256, nil
	default:
		return "", fmt.Errorf("whagent: unsupported asymmetric public key type %T", pub)
	}
}
