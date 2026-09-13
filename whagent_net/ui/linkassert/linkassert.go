// Package linkassert is `ui`'s own link-assertion signing surface (FR2,
// NFR1, NFR6, issue #2595): a purpose-built asymmetric key, JWKS endpoint,
// and minting helper for the short-lived assertion FR1/FR2's browser flow
// (#B) mints so a signed-in operator can prove, to another whagent-net
// surface, who they are without a shared session cookie.
//
// This is deliberately NOT a reuse of `api`/`worker`'s persona-credential
// key (WHAGENT_SIGNING_KEY, //libs/go/whagent's Signer/Claim, LB3) -- see
// the root plan's FR2 for the rejected alternatives (a) copy the persona
// key and (b) a new RPC on `api`'s persona.Issuer. Mirrors the shape of
// whagent_net/api/persona (keys.go + jwks.go) but with its own claim shape
// (Mint's doc comment) and a single active key -- no rotation ledger (see
// Rotation below).
//
// # Rotation (NFR1)
//
// Hard cutover. There is no WHAGENT_SIGNING_KEYS_ADDITIONAL-equivalent
// grace-period ledger here: linkassert holds exactly one active key and
// its JWKS publishes exactly that key. An assertion signed moments before
// a rotation simply fails verification like any other stale assertion;
// the operator re-clicks FR1's action. Do not add a retired-key list "for
// symmetry with persona.KeySet" -- that symmetry is deliberately absent.
//
// # Custody (NFR1)
//
// WHAGENT_UI_SIGNING_KEY / WHAGENT_UI_SIGNING_KEY_ID are required at
// startup -- `ui` fails startup loudly when either is missing or
// unparseable (see ../main.go), never the SECRET_KEY-style
// default-on-unset behaviour or the non-fatal degradation
// RABBITMQ_URL/WHAGENT_S3_BUCKET/WHAGENT_GRANT_* use. No unsigned or
// symmetric fallback mode exists at all: compromise of this key lets an
// attacker mint a valid assertion for any (iss, sub) pair, not just their
// own -- the signature check is the entire safeguard behind FR5's human
// confirmation.
package linkassert

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/uuid"

	"github.com/whale-net/everything/libs/go/whagent"
)

// JWKSPath is the fixed, unauthenticated well-known path this package's
// JWKS endpoint is served at -- mirrors persona.JWKSPath's convention.
// `ui`'s main.go registers this alongside "/healthz", never behind
// app.auth.RequireAuthFunc.
const JWKSPath = "/.well-known/jwks.json"

// Key is the loaded PKCS8 asymmetric private key `ui` signs link
// assertions with, plus its kid. Exactly one active key -- see the
// package doc comment's Rotation section.
type Key struct {
	signer crypto.Signer
	kid    string
}

// LoadKey parses pemStr as a PEM-encoded PKCS8 asymmetric private key
// (same parse path persona.LoadKeySet uses: x509.ParsePKCS8PrivateKey)
// and pairs it with kid. Returns an error -- never a usable Key -- for an
// empty pemStr or kid, non-PEM garbage, a PEM block that isn't PKCS8, or
// a symmetric/HMAC secret (which never parses as PKCS8 in the first
// place); never falls back to a symmetric or unsigned mode.
func LoadKey(pemStr, kid string) (*Key, error) {
	if pemStr == "" {
		return nil, errors.New("linkassert: no signing key configured (WHAGENT_UI_SIGNING_KEY) -- ui never falls back to an unsigned or symmetric mode")
	}
	if kid == "" {
		return nil, errors.New("linkassert: WHAGENT_UI_SIGNING_KEY_ID is required alongside WHAGENT_UI_SIGNING_KEY")
	}

	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("linkassert: WHAGENT_UI_SIGNING_KEY is not a valid PEM block")
	}

	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("linkassert: WHAGENT_UI_SIGNING_KEY: parsing PKCS8 private key: %w", err)
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("linkassert: WHAGENT_UI_SIGNING_KEY: key type %T does not implement crypto.Signer", key)
	}

	// Confirms the algorithm now, at load time, rather than surfacing an
	// unsupported-key-type error only on the first Mint call.
	if _, err := signatureAlgorithmFor(signer.Public()); err != nil {
		return nil, fmt.Errorf("linkassert: WHAGENT_UI_SIGNING_KEY: %w", err)
	}

	return &Key{signer: signer, kid: kid}, nil
}

// assertionClaims is the JWT claim shape Mint produces: exactly iss, sub,
// sub_iss, jti, exp, iat, and return_url -- nothing from whagent.Claim's
// Actor/WhagentSessionID shape (this is a browser-identity handshake, not
// a tool-call session credential).
type assertionClaims struct {
	jwt.Claims

	// SubjectIssuer is the Operator's Keycloak issuer (the on-behalf-of
	// subject's iss), mirroring whagent.Claim's "sub_iss" field name.
	SubjectIssuer string `json:"sub_iss"`

	// ReturnURL is where the receiving surface sends the Operator's
	// browser back to once the handshake completes.
	ReturnURL string `json:"return_url"`
}

// Mint returns a signed compact JWS carrying exactly: `iss` (= issuer,
// `ui`'s own WHAGENT_UI_PUBLIC_URL), `sub` and `sub_iss` (= subject,
// subjectIssuer -- the Operator's Keycloak (iss, sub) pair), a fresh
// random `jti`, `exp` = now + whagent.DefaultTTL, `iat`, and returnURL.
// Signed directly against k's key via go-jose (never
// //libs/go/whagent.Signer.Mint -- see the package doc comment for why),
// so the result carries none of whagent.Claim's Actor/session fields.
func (k *Key) Mint(issuer, subject, subjectIssuer, returnURL string, now time.Time) (string, error) {
	if issuer == "" {
		return "", errors.New("linkassert: Mint requires a non-empty issuer")
	}
	if subject == "" {
		return "", errors.New("linkassert: Mint requires a non-empty subject")
	}
	if subjectIssuer == "" {
		return "", errors.New("linkassert: Mint requires a non-empty subjectIssuer")
	}
	if returnURL == "" {
		return "", errors.New("linkassert: Mint requires a non-empty returnURL")
	}

	alg, err := signatureAlgorithmFor(k.signer.Public())
	if err != nil {
		return "", fmt.Errorf("linkassert: Mint: %w", err)
	}

	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: alg, Key: k.signer},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", k.kid),
	)
	if err != nil {
		return "", fmt.Errorf("linkassert: Mint: constructing signer: %w", err)
	}

	claims := assertionClaims{
		Claims: jwt.Claims{
			Issuer:   issuer,
			Subject:  subject,
			Expiry:   jwt.NewNumericDate(now.Add(whagent.DefaultTTL)),
			IssuedAt: jwt.NewNumericDate(now),
			ID:       uuid.NewString(),
		},
		SubjectIssuer: subjectIssuer,
		ReturnURL:     returnURL,
	}

	token, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		return "", fmt.Errorf("linkassert: Mint: signing claim: %w", err)
	}
	return token, nil
}

// jwks returns k's public JWKS document -- public-key-only, modelled on
// persona.JWKSHandler; k.signer.Public() is the only key material ever
// touched here, so no private JWK parameter (`d`, `p`, `q`, ...) can
// appear in the output.
func (k *Key) jwks() (jose.JSONWebKeySet, error) {
	alg, err := signatureAlgorithmFor(k.signer.Public())
	if err != nil {
		return jose.JSONWebKeySet{}, err
	}
	return jose.JSONWebKeySet{
		Keys: []jose.JSONWebKey{
			{
				Key:       k.signer.Public(),
				KeyID:     k.kid,
				Algorithm: string(alg),
				Use:       "sig",
			},
		},
	}, nil
}

// JWKSHandler serves k's public JWKS document -- public-key-only,
// modelled on persona.JWKSHandler. Must remain reachable without an
// authenticated session (see ../main.go's route registration, which
// never wraps this in app.auth.RequireAuthFunc).
func JWKSHandler(k *Key) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		set, err := k.jwks()
		if err != nil {
			http.Error(w, "jwks unavailable", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodHead {
			return
		}
		_ = json.NewEncoder(w).Encode(set)
	})
}

// signatureAlgorithmFor picks the go-jose signature algorithm that
// matches pub's key type. A deliberate local copy of
// whagent.signatureAlgorithmFor's logic (that function is unexported, and
// this package intentionally never imports whagent.Signer -- see the
// package doc comment) rather than a shared helper extracted into
// //libs/go/whagent, which would widen that package's published surface
// for a single internal helper.
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
			return "", fmt.Errorf("linkassert: unsupported ECDSA curve bit size %d", key.Curve.Params().BitSize)
		}
	case *rsa.PublicKey:
		return jose.RS256, nil
	default:
		return "", fmt.Errorf("linkassert: unsupported asymmetric public key type %T", pub)
	}
}
