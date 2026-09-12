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
//
// Scaffold phase (this task): types and function signatures only, every
// body returns errNotImplemented. Implementation phase fills in LoadKey's
// PEM parsing, Mint's claim/signing, and JWKSHandler's response body.
package linkassert

import (
	"errors"
	"fmt"
	"net/http"
	"time"
)

// JWKSPath is the fixed, unauthenticated well-known path this package's
// JWKS endpoint is served at -- mirrors persona.JWKSPath's convention.
// `ui`'s main.go registers this alongside "/healthz", never behind
// app.auth.RequireAuthFunc.
const JWKSPath = "/.well-known/jwks.json"

// errNotImplemented tags every scaffolded function/method below.
// Implementation phase replaces each body and removes this sentinel from
// that function.
var errNotImplemented = errors.New("linkassert: not implemented")

// Key is the loaded PKCS8 asymmetric private key `ui` signs link
// assertions with, plus its kid. Exactly one active key -- see the
// package doc comment's Rotation section.
type Key struct {
	// TODO(Implementation phase): the parsed crypto.Signer and its kid.
}

// LoadKey parses pem as a PEM-encoded PKCS8 asymmetric private key (same
// parse path persona.LoadKeySet uses: x509.ParsePKCS8PrivateKey) and pairs
// it with kid.
//
// TODO(Implementation phase): reject empty/unparseable pem and empty kid
// -- never fall back to a symmetric or unsigned mode. Mirrors
// persona.parsePrivateKeyPEM's validation, not persona.LoadKeySet's
// broader env-config shape (this package takes the two values directly,
// see ../main.go's config-loading block).
func LoadKey(pem, kid string) (*Key, error) {
	return nil, fmt.Errorf("linkassert: LoadKey: %w", errNotImplemented)
}

// Mint returns a signed compact JWS asserting that subject (issued by
// subjectIssuer, the Operator's Keycloak issuer) is who they claim to be,
// for consumption at returnURL. The token carries exactly: `iss` (=
// issuer, `ui`'s own WHAGENT_UI_PUBLIC_URL), `sub` and `sub_iss` (=
// subject, subjectIssuer), a fresh random `jti`, `exp` = now +
// whagent.DefaultTTL, `iat`, and the return URL -- nothing from
// whagent.Claim's Actor/WhagentSessionID shape.
//
// TODO(Implementation phase): mint via go-jose directly against k's
// signer (not //libs/go/whagent.Signer.Mint -- see the package doc
// comment for why), with a fresh jti per call and exp computed from
// whagent.DefaultTTL, referenced as that constant rather than a locally
// re-declared 5 minutes.
func (k *Key) Mint(issuer, subject, subjectIssuer, returnURL string, now time.Time) (string, error) {
	return "", fmt.Errorf("linkassert: Mint: %w", errNotImplemented)
}

// JWKSHandler serves k's public JWKS document -- public-key-only, modelled
// on persona.JWKSHandler.
//
// TODO(Implementation phase): serve k's single active public key as a
// JWKS document on GET/HEAD, mirroring persona.JWKSHandler's
// method-not-allowed handling for anything else. Must remain reachable
// without an authenticated session -- see ../main.go's route registration.
func JWKSHandler(k *Key) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, errNotImplemented.Error(), http.StatusNotImplemented)
	})
}
