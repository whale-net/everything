package mcpauth

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── token generation ────────────────────────────────────────────────────

func TestGenerateToken_ProducesDistinctHex64CharValues(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 200; i++ {
		tok, err := generateToken()
		require.NoError(t, err)

		require.Len(t, tok, 64, "32 bytes hex-encoded must be 64 characters")
		_, decodeErr := hex.DecodeString(tok)
		require.NoError(t, decodeErr, "token must be valid hex")

		require.False(t, seen[tok], "generateToken must not repeat across calls")
		seen[tok] = true
	}
}

// ── hashing ──────────────────────────────────────────────────────────────

// TestHashToken_MatchesKnownSHA256Vector pins hashToken against a
// known-answer SHA-256 vector so a future refactor cannot silently change
// the hash algorithm.
func TestHashToken_MatchesKnownSHA256Vector(t *testing.T) {
	// echo -n "hello" | sha256sum
	const want = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	got := hashToken("hello")
	assert.Equal(t, want, got)
}

// TestHashToken_MatchesASSHashTokenAlgorithm proves hashToken produces the
// exact same output audience_score_system/store/credential.go's unexported
// hashToken produces for the same input: hex.EncodeToString(sha256.Sum256(raw)).
// store.hashToken is unexported so it cannot be called directly from this
// package; this test instead reproduces its algorithm inline (identical to
// the one-line body in credential.go) and asserts byte-for-byte equality —
// this is what makes FR13 (existing ASS credentials keep verifying after
// migrating to this library, because the hash of a given raw token is
// unchanged) provable.
func TestHashToken_MatchesASSHashTokenAlgorithm(t *testing.T) {
	inputs := []string{
		"",
		"a",
		"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		"not-actually-hex-but-still-a-valid-token-string",
	}
	for _, in := range inputs {
		sum := sha256.Sum256([]byte(in))
		assWant := hex.EncodeToString(sum[:]) // audience_score_system/store/credential.go's hashToken body
		assert.Equal(t, assWant, hashToken(in), "hashToken(%q) must match ASS's hashToken algorithm byte-for-byte", in)
	}
}

// ── StoreConfig defaults ────────────────────────────────────────────────

func TestStoreConfigDefaults_ResolveToExpectedNames(t *testing.T) {
	assert.Equal(t, "mcp_credential", defaultTableName)
	assert.Equal(t, "identity", defaultIdentityColumn)

	// Defaults must themselves be valid identifiers, since NewCredentialStore
	// runs them through the same validateIdentifier check as any explicit
	// override.
	assert.NoError(t, validateIdentifier(defaultTableName, "TableName"))
	assert.NoError(t, validateIdentifier(defaultIdentityColumn, "IdentityColumn"))
}

// ── identifier validation ───────────────────────────────────────────────

func TestValidateIdentifier_RejectsUnsafeNames(t *testing.T) {
	cases := []string{
		"a; DROP TABLE x",
		"A",
		"1x",
		"",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			err := validateIdentifier(name, "TableName")
			assert.Error(t, err, "validateIdentifier must reject %q", name)
		})
	}
}

func TestValidateIdentifier_AcceptsSafeNames(t *testing.T) {
	cases := []string{"person_id", "mcp_credential"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			err := validateIdentifier(name, "TableName")
			assert.NoError(t, err, "validateIdentifier must accept %q", name)
		})
	}
}
