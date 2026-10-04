// Package whagentlink verifies the link assertion whagent-net's UI mints
// (whagent_net/ui/linkassert) when an operator asks to link their whagent-net
// identity to an account in another app. It is a browser-identity handshake,
// deliberately not a whagent.Claim: it carries no Actor or session fields, so
// whagent.Verifier would always reject it.
package whagentlink

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// Assertion is the verified claim set of a link assertion.
type Assertion struct {
	// Issuer is whagent-net's public URL (the token's iss).
	Issuer string
	// Subject and SubjectIssuer identify the operator in whagent-net's Keycloak.
	Subject       string
	SubjectIssuer string
	// ID is the single-use jti.
	ID     string
	Expiry time.Time
	// ReturnURL is where the browser goes back to with the outcome.
	ReturnURL string
}

var (
	ErrInvalidSignature = errors.New("whagentlink: invalid signature")
	ErrExpired          = errors.New("whagentlink: assertion expired")
	ErrUnknownIssuer    = errors.New("whagentlink: iss is not the configured whagent-net issuer")
	ErrMissingField     = errors.New("whagentlink: missing required assertion field")
)

// Verifier checks assertions against whagent-net's JWKS endpoint.
type Verifier struct {
	issuer string
	keySet oidc.KeySet
}

// NewVerifier builds a Verifier using a cached remote key set at jwksURL.
func NewVerifier(ctx context.Context, jwksURL, issuer string) (*Verifier, error) {
	if jwksURL == "" {
		return nil, errors.New("whagentlink: NewVerifier requires a non-empty JWKS URL")
	}
	if issuer == "" {
		return nil, errors.New("whagentlink: NewVerifier requires a non-empty issuer")
	}
	return &Verifier{issuer: issuer, keySet: oidc.NewRemoteKeySet(ctx, jwksURL)}, nil
}

type wireAssertion struct {
	Issuer        string `json:"iss"`
	Subject       string `json:"sub"`
	SubjectIssuer string `json:"sub_iss"`
	ID            string `json:"jti"`
	Expiry        int64  `json:"exp"`
	ReturnURL     string `json:"return_url"`
}

// Verify checks the signature first, then the claim shape, issuer and expiry.
// Replay protection (single use of ID) is the caller's job.
func (v *Verifier) Verify(ctx context.Context, token string) (*Assertion, error) {
	payload, err := v.keySet.VerifySignature(ctx, token)
	if err != nil {
		return nil, ErrInvalidSignature
	}
	var w wireAssertion
	if err := json.Unmarshal(payload, &w); err != nil {
		return nil, ErrInvalidSignature
	}
	if w.Issuer == "" || w.Subject == "" || w.SubjectIssuer == "" || w.ID == "" || w.Expiry == 0 || w.ReturnURL == "" {
		return nil, ErrMissingField
	}
	if w.Issuer != v.issuer {
		return nil, ErrUnknownIssuer
	}
	expiry := time.Unix(w.Expiry, 0)
	if expiry.Before(time.Now()) {
		return nil, ErrExpired
	}
	return &Assertion{
		Issuer: w.Issuer, Subject: w.Subject, SubjectIssuer: w.SubjectIssuer,
		ID: w.ID, Expiry: expiry, ReturnURL: w.ReturnURL,
	}, nil
}

// UnverifiedReturnURL reads return_url from an unverified token so a rejected
// request can still send the browser back; callers must origin-check it with
// ReturnURLOriginMatches before redirecting.
func UnverifiedReturnURL(token string) (string, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", false
	}
	var c struct {
		ReturnURL string `json:"return_url"`
	}
	if json.Unmarshal(payload, &c) != nil || c.ReturnURL == "" {
		return "", false
	}
	return c.ReturnURL, true
}

// ReturnURLOriginMatches reports whether returnURL has the same scheme and
// host as origin (whagent-net's public URL).
func ReturnURLOriginMatches(returnURL, origin string) bool {
	ru, err := url.Parse(returnURL)
	if err != nil || ru.Scheme == "" || ru.Host == "" {
		return false
	}
	ou, err := url.Parse(origin)
	if err != nil || ou.Scheme == "" || ou.Host == "" {
		return false
	}
	return ru.Scheme == ou.Scheme && ru.Host == ou.Host
}
