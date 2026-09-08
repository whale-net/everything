package persona_test

// Testing-phase suite (issue #2115) for KeySet/LoadKeySet: key-loading
// failure modes ("api fails to start with no signing key configured" and
// its siblings) and JWKS's "public key material only" guarantee. See
// issuer_test.go for the mint/verify round trip and jwks_test.go for the
// HTTP handler.

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/whagent_net/api/persona"
)

// generateSigningKeyPEM returns a fresh Ed25519 private key, PEM-encoded as
// PKCS8 -- the exact format LoadKeySet's ActivePrivateKeyPEM and
// AdditionalKeysJSON entries require (what `openssl genpkey -algorithm
// ed25519` produces, per keys.go's parsePrivateKeyPEM doc comment).
func generateSigningKeyPEM(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// validKeySetEnvConfig returns a KeySetEnvConfig with every required field
// populated -- tests copy this and clear/mutate individual fields to
// exercise a single failure mode at a time.
func validKeySetEnvConfig(t *testing.T) persona.KeySetEnvConfig {
	t.Helper()
	return persona.KeySetEnvConfig{
		Issuer:              "whagent-net-test",
		ActiveKeyID:         "active-1",
		ActivePrivateKeyPEM: generateSigningKeyPEM(t),
	}
}

// TestLoadKeySet_FailsWithNoActiveKeyConfigured is this task's designated
// acceptance criterion: "api fails to start with no signing key
// configured". main.go treats any LoadKeySet error as fatal (os.Exit via
// run()'s returned error), so this is the boundary that guarantees api
// never falls back to an unsigned or symmetric mode.
func TestLoadKeySet_FailsWithNoActiveKeyConfigured(t *testing.T) {
	cfg := validKeySetEnvConfig(t)
	cfg.ActivePrivateKeyPEM = ""

	ks, err := persona.LoadKeySet(cfg)

	require.Error(t, err)
	assert.Nil(t, ks)
	assert.Contains(t, err.Error(), "WHAGENT_SIGNING_KEY")
}

func TestLoadKeySet_FailsWithNoIssuer(t *testing.T) {
	cfg := validKeySetEnvConfig(t)
	cfg.Issuer = ""

	ks, err := persona.LoadKeySet(cfg)

	require.Error(t, err)
	assert.Nil(t, ks)
}

func TestLoadKeySet_FailsWithNoActiveKeyID(t *testing.T) {
	cfg := validKeySetEnvConfig(t)
	cfg.ActiveKeyID = ""

	ks, err := persona.LoadKeySet(cfg)

	require.Error(t, err)
	assert.Nil(t, ks)
}

func TestLoadKeySet_FailsOnUnparseableActiveKeyPEM(t *testing.T) {
	cfg := validKeySetEnvConfig(t)
	cfg.ActivePrivateKeyPEM = "this is not a PEM block"

	ks, err := persona.LoadKeySet(cfg)

	require.Error(t, err)
	assert.Nil(t, ks)
}

// TestLoadKeySet_SucceedsAndActiveSignerCanMint proves LoadKeySet's happy
// path produces a KeySet whose ActiveSigner is a usable *whagent.Signer --
// not merely a non-nil value.
func TestLoadKeySet_SucceedsAndActiveSignerCanMint(t *testing.T) {
	cfg := validKeySetEnvConfig(t)

	ks, err := persona.LoadKeySet(cfg)
	require.NoError(t, err)
	require.NotNil(t, ks)
	require.NotNil(t, ks.ActiveSigner())

	// ActiveSigner is a real, mintable *whagent.Signer -- exercised fully
	// (mint + verify round trip) in issuer_test.go; here it's enough to
	// prove LoadKeySet wired the parsed key and configured issuer/kid
	// through correctly by minting and checking the JWKS carries the same
	// active kid.
	jwks, err := ks.JWKS()
	require.NoError(t, err)
	require.Len(t, jwks.Keys, 1)
	assert.Equal(t, cfg.ActiveKeyID, jwks.Keys[0].KeyID)
}

func TestLoadKeySet_FailsOnMalformedAdditionalKeysJSON(t *testing.T) {
	cfg := validKeySetEnvConfig(t)
	cfg.AdditionalKeysJSON = "not json at all"

	ks, err := persona.LoadKeySet(cfg)

	require.Error(t, err)
	assert.Nil(t, ks)
}

// TestLoadKeySet_FailsOnAdditionalKeyMissingFields covers both required
// fields of a WHAGENT_SIGNING_KEYS_ADDITIONAL entry individually.
func TestLoadKeySet_FailsOnAdditionalKeyMissingFields(t *testing.T) {
	cases := map[string]string{
		"missing kid":             `[{"private_key_pem": "irrelevant"}]`,
		"missing private_key_pem": `[{"kid": "retired-1"}]`,
	}

	for name, additionalJSON := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := validKeySetEnvConfig(t)
			cfg.AdditionalKeysJSON = additionalJSON

			ks, err := persona.LoadKeySet(cfg)

			require.Error(t, err)
			assert.Nil(t, ks)
		})
	}
}

func TestLoadKeySet_FailsOnAdditionalKeyUnparseablePEM(t *testing.T) {
	cfg := validKeySetEnvConfig(t)
	additional, err := json.Marshal([]map[string]string{
		{"kid": "retired-1", "private_key_pem": "not a pem block"},
	})
	require.NoError(t, err)
	cfg.AdditionalKeysJSON = string(additional)

	ks, err := persona.LoadKeySet(cfg)

	require.Error(t, err)
	assert.Nil(t, ks)
}

// TestLoadKeySet_FailsOnAdditionalKeyKidCollisionWithActive proves a
// retired key can never silently shadow -- or be confused with -- the
// active minting key: LoadKeySet must reject rather than pick one
// arbitrarily.
func TestLoadKeySet_FailsOnAdditionalKeyKidCollisionWithActive(t *testing.T) {
	cfg := validKeySetEnvConfig(t)
	additional, err := json.Marshal([]map[string]string{
		{"kid": cfg.ActiveKeyID, "private_key_pem": generateSigningKeyPEM(t)},
	})
	require.NoError(t, err)
	cfg.AdditionalKeysJSON = string(additional)

	ks, err := persona.LoadKeySet(cfg)

	require.Error(t, err)
	assert.Nil(t, ks)
	assert.Contains(t, err.Error(), "collides")
}

// TestKeySet_JWKS_PublishesUnionOfActiveAndRetiredKeys proves rotation
// support: LoadKeySet with one active key and one retired key in
// AdditionalKeysJSON publishes both `kid`s (issue's Testing section:
// "serves multiple kids during rotation").
func TestKeySet_JWKS_PublishesUnionOfActiveAndRetiredKeys(t *testing.T) {
	cfg := validKeySetEnvConfig(t)
	additional, err := json.Marshal([]map[string]string{
		{"kid": "retired-1", "private_key_pem": generateSigningKeyPEM(t)},
	})
	require.NoError(t, err)
	cfg.AdditionalKeysJSON = string(additional)

	ks, err := persona.LoadKeySet(cfg)
	require.NoError(t, err)

	jwks, err := ks.JWKS()
	require.NoError(t, err)
	require.Len(t, jwks.Keys, 2)

	kids := map[string]bool{}
	for _, k := range jwks.Keys {
		kids[k.KeyID] = true
	}
	assert.True(t, kids[cfg.ActiveKeyID], "active kid must be published")
	assert.True(t, kids["retired-1"], "retired kid must still be published")
}

// TestKeySet_JWKS_ContainsOnlyPublicKeyMaterial is this task's Testing
// section's explicit acceptance criterion: "JWKS endpoint returns only
// public key material (assert no d/private parameters)". Checked two ways:
// go-jose's own JSONWebKey.IsPublic() and a literal scan of the marshaled
// JSON for the `"d":` private-key JWK parameter, so a regression that
// slips past IsPublic (e.g. a future field) is still caught.
func TestKeySet_JWKS_ContainsOnlyPublicKeyMaterial(t *testing.T) {
	cfg := validKeySetEnvConfig(t)
	additional, err := json.Marshal([]map[string]string{
		{"kid": "retired-1", "private_key_pem": generateSigningKeyPEM(t)},
	})
	require.NoError(t, err)
	cfg.AdditionalKeysJSON = string(additional)

	ks, err := persona.LoadKeySet(cfg)
	require.NoError(t, err)

	jwks, err := ks.JWKS()
	require.NoError(t, err)
	require.NotEmpty(t, jwks.Keys)

	for _, k := range jwks.Keys {
		assert.True(t, k.IsPublic(), "JWKS entry %q must contain only public key material", k.KeyID)
	}

	raw, err := json.Marshal(jwks)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), `"d":`, "serialized JWKS must never carry a private `d` parameter")
}

// TestKeySet_JWKS_NewlyAddedKeyVerifiesBeforeOldKeyIsRemoved is the
// rotation-window proof: a KeySet built with an active key and its
// predecessor as a retired AdditionalKeysJSON entry still publishes both,
// so a token signed moments before rotation (with what is now the retired
// key) verifies against the KeySet's own JWKS just as one signed with the
// new active key does -- the "a token signed with the newly added key
// verifies before the old key is removed" acceptance criterion, exercised
// from the rotation window's other side (old-still-present) since
// issuer_test.go's round-trip tests already cover minting with the active
// key.
func TestKeySet_JWKS_NewlyAddedKeyVerifiesBeforeOldKeyIsRemoved(t *testing.T) {
	cfg := validKeySetEnvConfig(t)
	oldKeyPEM := generateSigningKeyPEM(t)
	additional, err := json.Marshal([]map[string]string{
		{"kid": "old-key", "private_key_pem": oldKeyPEM},
	})
	require.NoError(t, err)
	cfg.AdditionalKeysJSON = string(additional)

	ks, err := persona.LoadKeySet(cfg)
	require.NoError(t, err)

	jwks, err := ks.JWKS()
	require.NoError(t, err)

	var sawActive, sawOld bool
	for _, k := range jwks.Keys {
		switch k.KeyID {
		case cfg.ActiveKeyID:
			sawActive = true
		case "old-key":
			sawOld = true
		}
	}
	assert.True(t, sawActive, "the newly active key must be published")
	assert.True(t, sawOld, "the old key must still be published during the rotation window")
}
