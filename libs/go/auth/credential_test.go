package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
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

// TestValidateIdentifier_RejectsInjectionPayloads is a dedicated,
// injection-focused pass beyond TestValidateIdentifier_RejectsUnsafeNames:
// StoreConfig.TableName/IdentityColumn/IdentityCast are interpolated
// directly into generated SQL (see identifierPattern's doc comment), so any
// of these getting past validateIdentifier would be a SQL injection via
// configuration. Each case below targets a distinct injection technique.
func TestValidateIdentifier_RejectsInjectionPayloads(t *testing.T) {
	cases := map[string]string{
		"statement terminator + stacked query": "mcp_credential; DROP TABLE mcp_credential;--",
		"inline SQL comment":                   "mcp_credential--",
		"block comment":                        "mcp_credential/*",
		"single quote (string escape)":         "mcp_credential' OR '1'='1",
		"double quote (identifier escape)":     `mcp_credential"`,
		"backtick":                             "mcp_credential`",
		"whitespace inside identifier":         "mcp cred",
		"leading whitespace":                   " mcp_credential",
		"trailing whitespace":                  "mcp_credential ",
		"newline injection":                    "mcp_credential\nDROP TABLE x",
		"null byte":                            "mcp_credential\x00",
		"parenthesis (subquery attempt)":       "mcp_credential)",
		"percent wildcard":                     "mcp_credential%",
		"unicode homoglyph (fullwidth semi)":   "mcp_credential；",
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			err := validateIdentifier(payload, "TableName")
			assert.Error(t, err, "validateIdentifier must reject injection payload %q (%s)", payload, name)
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

// ── opt-in persona column ────────────────────────────────────────────────

func TestPersonaColumn_UnsetLeavesSQLUnchanged(t *testing.T) {
	s := &pgxCredentialStore{cfg: StoreConfig{TableName: "mcp_credential", IdentityColumn: "identity"}}
	assert.Equal(t, "id, identity, token_hash, created_at, last_used_at, revoked_at", s.columns())
	assert.Equal(t, `
		INSERT INTO mcp_credential (identity, token_hash)
		VALUES ($1, $2)
		RETURNING id, identity, token_hash, created_at, last_used_at, revoked_at
	`, s.mintQuery())
}

func TestPersonaColumn_SetAddsColumnAndParam(t *testing.T) {
	s := &pgxCredentialStore{cfg: StoreConfig{TableName: "mcp_credential", IdentityColumn: "identity", PersonaColumn: "persona"}}
	assert.Equal(t, "id, identity, token_hash, created_at, last_used_at, revoked_at, persona", s.columns())
	assert.Equal(t, `
		INSERT INTO mcp_credential (identity, token_hash, persona)
		VALUES ($1, $2, $3)
		RETURNING id, identity, token_hash, created_at, last_used_at, revoked_at, persona
	`, s.mintQuery())
}

// ── opt-in name column ───────────────────────────────────────────────────

// TestNameColumn_UnsetLeavesSQLUnchanged is the contract the other domains
// rely on: a store with no NameColumn -- manmanv2, audience_score_system,
// whagent_net -- must generate exactly the SQL it generated before this
// column existed, so their tables need no migration.
func TestNameColumn_UnsetLeavesSQLUnchanged(t *testing.T) {
	s := &pgxCredentialStore{cfg: StoreConfig{TableName: "mcp_credential", IdentityColumn: "identity"}}
	assert.Equal(t, "id, identity, token_hash, created_at, last_used_at, revoked_at", s.columns())
	assert.Equal(t, `
		INSERT INTO mcp_credential (identity, token_hash)
		VALUES ($1, $2)
		RETURNING id, identity, token_hash, created_at, last_used_at, revoked_at
	`, s.mintQuery())
	assert.NotContains(t, s.mintQuery(), "name")
}

// TestNameColumn_SetAddsColumnToReadsButNotToUnnamedMint: the name column
// joins the RETURNING/SELECT list so List surfaces it, while Mint -- which
// takes no name -- leaves the parameter out and writes NULL. This is what
// keeps selfserve.go's JSON API and token.go's authorization-code path
// unchanged on a name-configured table.
func TestNameColumn_SetAddsColumnToReadsButNotToUnnamedMint(t *testing.T) {
	s := &pgxCredentialStore{cfg: StoreConfig{TableName: "mcp_credential", IdentityColumn: "identity", NameColumn: "name"}}
	assert.Equal(t, "id, identity, token_hash, created_at, last_used_at, revoked_at, name", s.columns())
	assert.Equal(t, `
		INSERT INTO mcp_credential (identity, token_hash)
		VALUES ($1, $2)
		RETURNING id, identity, token_hash, created_at, last_used_at, revoked_at, name
	`, s.mintQuery())
	assert.Equal(t, `
		INSERT INTO mcp_credential (identity, token_hash, name)
		VALUES ($1, $2, $3)
		RETURNING id, identity, token_hash, created_at, last_used_at, revoked_at, name
	`, s.insertQuery(true))
}

// TestNameColumn_WithPersona_NumbersParametersInOrder pins the parameter
// order across both opt-in columns -- a mismatch here is a runtime bind
// error, not a compile error.
func TestNameColumn_WithPersona_NumbersParametersInOrder(t *testing.T) {
	s := &pgxCredentialStore{cfg: StoreConfig{
		TableName: "mcp_credential", IdentityColumn: "identity",
		PersonaColumn: "persona", NameColumn: "name",
	}}
	assert.Equal(t, "id, identity, token_hash, created_at, last_used_at, revoked_at, persona, name", s.columns())
	assert.Equal(t, `
		INSERT INTO mcp_credential (identity, token_hash, persona)
		VALUES ($1, $2, $3)
		RETURNING id, identity, token_hash, created_at, last_used_at, revoked_at, persona, name
	`, s.mintQuery())
	assert.Equal(t, `
		INSERT INTO mcp_credential (identity, token_hash, persona, name)
		VALUES ($1, $2, $3, $4)
		RETURNING id, identity, token_hash, created_at, last_used_at, revoked_at, persona, name
	`, s.insertQuery(true))
}

func TestNameColumn_IdentityCastStillRendersOnTheNamedInsert(t *testing.T) {
	s := &pgxCredentialStore{cfg: StoreConfig{
		TableName: "mcp_credential", IdentityColumn: "person_id",
		IdentityCast: "uuid", NameColumn: "name",
	}}
	assert.Contains(t, s.insertQuery(true), "VALUES ($1::uuid, $2, $3)")
}

// ── name refusals ────────────────────────────────────────────────────────

// TestMintNamed_RefusesEmptyName covers every shape of "no name" a UI can
// post, each refused with the same named error rather than a bound failure.
func TestMintNamed_RefusesEmptyName(t *testing.T) {
	s := &pgxCredentialStore{cfg: StoreConfig{Pool: nil, NameColumn: "name"}}
	for _, name := range []string{"", " ", "\t", "\n", "   \t\n  "} {
		_, _, err := s.MintNamed(t.Context(), "identity-1", name)
		assert.ErrorIs(t, err, ErrCredentialNameRequired, "name %q must be refused as required", name)
	}
}

// TestMintNamed_TrimsBeforeJudgingEmpty: a name that is nothing but
// whitespace must be refused before any query is built, so a UI that posts
// " " gets the required-name error rather than a bound insert. (The
// trimming of a name that IS stored is asserted against a real Postgres in
// credential_integration_test.go.)
func TestMintNamed_WhitespaceOnlyNameNeverReachesTheDatabase(t *testing.T) {
	// A nil Pool would panic if the empty check came after the query, so a
	// clean return here proves the refusal happens first.
	s := &pgxCredentialStore{cfg: StoreConfig{NameColumn: "name"}}
	_, _, err := s.MintNamed(t.Context(), "identity-1", "   ")
	assert.ErrorIs(t, err, ErrCredentialNameRequired)
}

// TestMintNamed_WithoutNameColumn_FailsNamingTheMisconfiguration: a store
// that never opted in must not silently drop the name, and the error must
// say which config field is missing.
func TestMintNamed_WithoutNameColumn_FailsNamingTheMisconfiguration(t *testing.T) {
	s := &pgxCredentialStore{cfg: StoreConfig{TableName: "mcp_credential", IdentityColumn: "identity"}}
	_, _, err := s.MintNamed(t.Context(), "identity-1", "deploy")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NameColumn")
	assert.NotErrorIs(t, err, ErrCredentialNameRequired, "a misconfiguration is neither refusal")
	assert.NotErrorIs(t, err, ErrCredentialNameTaken)
}

// TestIsCredentialNameTaken pins the translation itself, since it is the
// only thing standing between an operator and a raw driver message: a 23505
// whose constraint is the name index is the name-taken refusal, and any
// other unique violation (token_hash) is not.
func TestIsCredentialNameTaken(t *testing.T) {
	s := &pgxCredentialStore{cfg: StoreConfig{NameColumn: "name"}}

	assert.True(t, s.isCredentialNameTaken(&pgconn.PgError{
		Code: "23505", ConstraintName: "mcp_credential_identity_name_live",
	}))
	assert.False(t, s.isCredentialNameTaken(&pgconn.PgError{
		Code: "23505", ConstraintName: "mcp_credential_token_hash_key",
	}), "a token_hash collision is not a name conflict")
	// ...including when the token_hash constraint's name also happens to
	// contain the name column, which substring matching alone would misread.
	assert.False(t, s.isCredentialNameTaken(&pgconn.PgError{
		Code: "23505", ConstraintName: "mcp_credential_identity_name_token_hash_key",
	}))
	assert.False(t, s.isCredentialNameTaken(&pgconn.PgError{
		Code: "23503", ConstraintName: "mcp_credential_identity_name_live",
	}))
	assert.False(t, s.isCredentialNameTaken(errors.New("connection refused")))
	assert.False(t, s.isCredentialNameTaken(nil))
}

// TestIsCredentialNameTaken_UnwrapsWrappedPgError: the error reaches this
// check through pgx's own wrapping, so a bare type assertion would miss it.
func TestIsCredentialNameTaken_UnwrapsWrappedPgError(t *testing.T) {
	s := &pgxCredentialStore{cfg: StoreConfig{NameColumn: "name"}}
	wrapped := fmt.Errorf("auth: insert credential: %w", &pgconn.PgError{
		Code: "23505", ConstraintName: "mcp_credential_identity_name_live",
	})
	assert.True(t, s.isCredentialNameTaken(wrapped))
}
