// Package persona is `api`'s implementation of whagent-net's trust root
// (LB3, FR10, NFR4, issue #2115): it owns the signing key(s), mints the
// persona Claim every tool call carries (Issuer, issuer.go), and serves
// the public JWKS a domain server's whagent.Verifier is pointed at
// (JWKSHandler, jwks.go). It delegates the Claim's field-by-field JWT
// mapping and short-TTL enforcement entirely to //libs/go/whagent
// (#2110) -- this package's own job is key material lifecycle (this
// file) and the two small surfaces built on top of it.
//
// Implementation phase (#2115) complete: key loading (LoadKeySet), the
// Issuer/KeySet, and the JWKS http.Handler are fully implemented and
// exercised by `bazel build //whagent_net/...`. The internal-only exposure
// decision for Issuer.Issue is documented in whagent_net/ARCHITECTURE.md
// "Identity and auth chaining" § "Issuance mechanism", and this package's
// env-var contract (read by both `api` and, later, `worker`) is documented
// in whagent_net/ENV.md § "Persona claim issuance".
package persona

import (
	"crypto"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"

	"github.com/go-jose/go-jose/v4"

	"github.com/whale-net/everything/libs/go/whagent"
)

// KeySet is `api`'s signing-key configuration (this task's Scaffold "Key
// material configuration" bullet): the one active key Issuer mints with,
// plus zero or more retired keys kept only to publish in JWKS() during a
// rotation window -- a token signed with a key moments before it was
// retired must still verify until it expires (LB3's short TTL bounds how
// long that window needs to stay open). Introducing a new active key, or
// dropping a retired one once its tokens have all expired, is a config
// change (an env var edit), never a restart-with-different-code (this
// task's Implementation section).
//
// Build with LoadKeySet -- see whagent_net/ENV.md § "Persona claim
// issuance" for the WHAGENT_SIGNING_KEY / WHAGENT_SIGNING_KEY_ID /
// WHAGENT_SIGNING_KEYS_ADDITIONAL variables LoadKeySet reads via its
// KeySetEnvConfig argument.
type KeySet struct {
	active  *whagent.Signer
	retired []*whagent.Signer
}

// ActiveSigner returns the *whagent.Signer Issuer mints with (see
// issuer.go).
func (ks *KeySet) ActiveSigner() *whagent.Signer {
	return ks.active
}

// JWKS returns the union of every key ks knows about -- active and
// retired alike -- so a Verifier can validate a token signed with any of
// them (rotation window). Retired keys are never used to mint (see
// ActiveSigner), only published here. Every entry is public-key-only
// (this task's Testing section: no `d`/private JWK parameter ever
// appears), because whagent.Signer.JWKS() itself only ever exposes its
// private key's Public() half.
func (ks *KeySet) JWKS() (jose.JSONWebKeySet, error) {
	out := jose.JSONWebKeySet{}
	signers := make([]*whagent.Signer, 0, 1+len(ks.retired))
	signers = append(signers, ks.active)
	signers = append(signers, ks.retired...)
	for _, s := range signers {
		set, err := s.JWKS()
		if err != nil {
			return jose.JSONWebKeySet{}, fmt.Errorf("persona: JWKS: %w", err)
		}
		out.Keys = append(out.Keys, set.Keys...)
	}
	return out, nil
}

// signingKeyConfig is one entry of KeySetEnvConfig.AdditionalKeysJSON's
// JSON array.
type signingKeyConfig struct {
	Kid           string `json:"kid"`
	PrivateKeyPEM string `json:"private_key_pem"`
}

// KeySetEnvConfig is LoadKeySet's argument, read directly via os.Getenv in
// whagent_net/api/main.go -- the "api server" ENV.md convention this
// domain already follows for whagent_net/api/main.go's own variables
// (PORT, GRPC_AUTH_MODE), not the //libs/go/db-style ConfigFromEnv
// pattern. Kept as a struct rather than this package reading os.Getenv
// directly so a test can construct one without mutating the process
// environment.
type KeySetEnvConfig struct {
	// Issuer is whagent-net's own `iss` value every minted Claim carries
	// (never a domain's) -- see whagent.New.
	Issuer string

	// ActiveKeyID is the JWKS `kid` for ActivePrivateKeyPEM -- the key
	// Issuer mints with.
	ActiveKeyID string

	// ActivePrivateKeyPEM is a PEM-encoded PKCS8 asymmetric private key
	// (e.g. `openssl genpkey -algorithm ed25519`) -- loaded from
	// config/secret, never checked in. Required: LoadKeySet fails loudly
	// when this is empty rather than falling back to an unsigned or
	// symmetric mode (this task's Implementation section).
	ActivePrivateKeyPEM string

	// AdditionalKeysJSON is an optional JSON array of
	// {"kid": "...", "private_key_pem": "..."} entries -- retired keys
	// published in JWKS() (for a rotation window) but never used to mint.
	// Leave empty when there is no key in rotation.
	AdditionalKeysJSON string
}

// LoadKeySet builds a KeySet from cfg. Returns an error -- never a
// zero-value KeySet -- when the active key is missing or unparseable;
// whagent_net/api/main.go must treat any error here as fatal ("fail
// startup loudly if no key is configured", this task's Implementation
// section).
func LoadKeySet(cfg KeySetEnvConfig) (*KeySet, error) {
	if cfg.Issuer == "" {
		return nil, errors.New("persona: LoadKeySet requires a non-empty Issuer")
	}
	if cfg.ActivePrivateKeyPEM == "" {
		return nil, errors.New("persona: no signing key configured (WHAGENT_SIGNING_KEY) -- api never falls back to an unsigned or symmetric mode")
	}
	if cfg.ActiveKeyID == "" {
		return nil, errors.New("persona: WHAGENT_SIGNING_KEY_ID is required alongside WHAGENT_SIGNING_KEY")
	}

	activeKey, err := parsePrivateKeyPEM(cfg.ActivePrivateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("persona: active signing key: %w", err)
	}
	activeSigner, err := whagent.New(activeKey, cfg.Issuer, cfg.ActiveKeyID)
	if err != nil {
		return nil, fmt.Errorf("persona: active signing key: %w", err)
	}

	var retired []*whagent.Signer
	if cfg.AdditionalKeysJSON != "" {
		var entries []signingKeyConfig
		if err := json.Unmarshal([]byte(cfg.AdditionalKeysJSON), &entries); err != nil {
			return nil, fmt.Errorf("persona: WHAGENT_SIGNING_KEYS_ADDITIONAL: %w", err)
		}
		for _, entry := range entries {
			if entry.Kid == "" || entry.PrivateKeyPEM == "" {
				return nil, errors.New("persona: WHAGENT_SIGNING_KEYS_ADDITIONAL entry requires both kid and private_key_pem")
			}
			if entry.Kid == cfg.ActiveKeyID {
				return nil, fmt.Errorf("persona: WHAGENT_SIGNING_KEYS_ADDITIONAL kid %q collides with the active key id", entry.Kid)
			}
			retiredKey, err := parsePrivateKeyPEM(entry.PrivateKeyPEM)
			if err != nil {
				return nil, fmt.Errorf("persona: retired signing key %q: %w", entry.Kid, err)
			}
			retiredSigner, err := whagent.New(retiredKey, cfg.Issuer, entry.Kid)
			if err != nil {
				return nil, fmt.Errorf("persona: retired signing key %q: %w", entry.Kid, err)
			}
			retired = append(retired, retiredSigner)
		}
	}

	return &KeySet{active: activeSigner, retired: retired}, nil
}

// parsePrivateKeyPEM decodes a PEM-encoded PKCS8 private key -- the format
// `openssl genpkey -algorithm ed25519` (or -algorithm EC / RSA) produces
// -- into the crypto.Signer whagent.New requires. whagent.New itself
// rejects anything not asymmetric (see libs/go/whagent/sign.go's
// signatureAlgorithmFor), so this function only needs to get from PEM
// bytes to a crypto.Signer -- it does not re-validate the key type.
func parsePrivateKeyPEM(pemStr string) (crypto.Signer, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("not a valid PEM block")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing PKCS8 private key: %w", err)
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("key type %T does not implement crypto.Signer", key)
	}
	return signer, nil
}
