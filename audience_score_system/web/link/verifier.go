package link

import (
	"context"
	"encoding/json"
	"errors"
	"time"

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
func NewVerifier(ctx context.Context, jwksURL, issuer string) (*Verifier, error) {
	if jwksURL == "" {
		return nil, errors.New("link: NewVerifier requires a non-empty JWKS URL")
	}
	if issuer == "" {
		return nil, errors.New("link: NewVerifier requires a non-empty issuer")
	}
	return &Verifier{issuer: issuer, keySet: oidc.NewRemoteKeySet(ctx, jwksURL)}, nil
}

// wireAssertion is the JSON payload shape ui's linkassert.Mint produces --
// see Assertion's doc comment in assertion.go for the field-by-field
// mapping this unmarshals into.
type wireAssertion struct {
	Issuer        string `json:"iss"`
	Subject       string `json:"sub"`
	SubjectIssuer string `json:"sub_iss"`
	ID            string `json:"jti"`
	Expiry        int64  `json:"exp"`
	ReturnURL     string `json:"return_url"`
}

// Verify checks the signature FIRST, then the claim shape -- an unsigned
// or wrongly-signed token must never reach issuer/expiry/field
// validation, and must never be partially trusted; any parse of the
// payload prior to signature verification is for key selection (`kid`)
// only -- and returns the parsed Assertion. Fails closed.
//
// v.keySet.VerifySignature alone decides whether token was signed by a
// key ui's own JWKS (as fetched at construction) actually knows about --
// so a token signed by any other key, including a genuine
// whagent.Claim-shaped persona credential signed by whagent-net's own
// key, fails here before any claim-shape check below ever runs.
func (v *Verifier) Verify(ctx context.Context, token string) (*Assertion, error) {
	payload, err := v.keySet.VerifySignature(ctx, token)
	if err != nil {
		return nil, ErrInvalidSignature
	}

	var wire wireAssertion
	if err := json.Unmarshal(payload, &wire); err != nil {
		// The signature verified, but the payload isn't even parseable as
		// a wireAssertion -- this cannot be a genuine ui-minted token, so
		// it is reported the same way as any other signature failure
		// rather than inventing a fifth error case the published contract
		// doesn't document.
		return nil, ErrInvalidSignature
	}

	if wire.Issuer == "" ||
		wire.Subject == "" ||
		wire.SubjectIssuer == "" ||
		wire.ID == "" ||
		wire.Expiry == 0 ||
		wire.ReturnURL == "" {
		return nil, ErrMissingField
	}

	if wire.Issuer != v.issuer {
		return nil, ErrUnknownIssuer
	}

	expiry := time.Unix(wire.Expiry, 0)
	if expiry.Before(time.Now()) {
		return nil, ErrExpired
	}

	return &Assertion{
		Issuer:        wire.Issuer,
		Subject:       wire.Subject,
		SubjectIssuer: wire.SubjectIssuer,
		ID:            wire.ID,
		Expiry:        expiry,
		ReturnURL:     wire.ReturnURL,
	}, nil
}
