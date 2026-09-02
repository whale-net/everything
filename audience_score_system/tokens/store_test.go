// Pure-Go tests for tokens.Store's error-classification and
// encrypt-at-rest mechanics -- no Docker, no live Google calls, runs as
// part of `bazel test //...`.
//
// Save/token (store.go) begin a real *pgxpool.Pool transaction to enforce
// the SCD2 close-and-open pattern and the SELECT ... FOR UPDATE
// single-flight refresh, so exercising the full Save -> TokenSource round
// trip, the invalid_grant -> needs-reauth mapping, the transient-error ->
// retryable-with-untouched-state mapping, the reconnect close-and-open, the
// data-retention guarantee, and the concurrent-refresh single-flight
// guarantee all require a real Postgres -- see store_integration_test.go
// (dbtest-backed) for those. What's covered here is everything that does
// NOT require a database round trip: isInvalidGrant's classification (the
// exact boundary between "needs-reauth" and "retryable"), and
// encrypt/decrypt's round trip plus its "ciphertext contains no plaintext
// substring" guarantee (encrypt/decrypt never touch the pool, so this much
// of the Postgres-backed acceptance criteria's ciphertext assertion is also
// provable without Docker).
package tokens

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func testEncKey() [32]byte {
	return sha256.Sum256([]byte("unit-test-token-encryption-key"))
}

// ── isInvalidGrant ───────────────────────────────────────────────────────

func TestIsInvalidGrant_RetrieveErrorWithInvalidGrantCode_True(t *testing.T) {
	err := &oauth2.RetrieveError{
		Response:  &http.Response{StatusCode: http.StatusBadRequest},
		ErrorCode: "invalid_grant",
	}
	assert.True(t, isInvalidGrant(err), "a RetrieveError with ErrorCode=invalid_grant must be classified as needs-reauth")
}

func TestIsInvalidGrant_RetrieveErrorWithOtherCode_False(t *testing.T) {
	err := &oauth2.RetrieveError{
		Response:  &http.Response{StatusCode: http.StatusBadRequest},
		ErrorCode: "invalid_client",
	}
	assert.False(t, isInvalidGrant(err), "a RetrieveError with a different error code must NOT trip needs-reauth")
}

func TestIsInvalidGrant_WrappedRetrieveError_StillDetected(t *testing.T) {
	inner := &oauth2.RetrieveError{ErrorCode: "invalid_grant"}
	wrapped := errors.Join(errors.New("refresh access token"), inner)
	assert.True(t, isInvalidGrant(wrapped), "isInvalidGrant must use errors.As, so a wrapped RetrieveError is still detected")
}

func TestIsInvalidGrant_NetworkError_False(t *testing.T) {
	err := errors.New("dial tcp: connection refused")
	assert.False(t, isInvalidGrant(err), "a plain network error must be treated as retryable, never needs-reauth")
}

func TestIsInvalidGrant_ContextDeadlineExceeded_False(t *testing.T) {
	assert.False(t, isInvalidGrant(context.DeadlineExceeded), "a context deadline (timeout) must be treated as retryable, never needs-reauth")
}

func TestIsInvalidGrant_NilError_False(t *testing.T) {
	assert.False(t, isInvalidGrant(nil))
}

// ── encrypt/decrypt ──────────────────────────────────────────────────────

func TestEncryptDecrypt_RoundTrips(t *testing.T) {
	s := tokenStore{encKey: testEncKey()}

	ciphertext, err := s.encrypt("ya29.super-secret-access-token")
	require.NoError(t, err)

	plaintext, err := s.decrypt(ciphertext)
	require.NoError(t, err)
	assert.Equal(t, "ya29.super-secret-access-token", plaintext)
}

func TestEncrypt_CiphertextContainsNoPlaintextSubstring(t *testing.T) {
	s := tokenStore{encKey: testEncKey()}
	const plaintext = "ya29.a0AfH6SMC-super-secret-refresh-token-value"

	ciphertext, err := s.encrypt(plaintext)
	require.NoError(t, err)

	assert.NotContains(t, string(ciphertext), plaintext, "the encrypted-at-rest value must never contain the plaintext token as a substring")
}

func TestEncrypt_DifferentNoncePerCall_SameCiphertextNeverRepeats(t *testing.T) {
	s := tokenStore{encKey: testEncKey()}

	c1, err := s.encrypt("same-plaintext")
	require.NoError(t, err)
	c2, err := s.encrypt("same-plaintext")
	require.NoError(t, err)

	assert.NotEqual(t, c1, c2, "AES-GCM must use a fresh random nonce per call, so encrypting the same plaintext twice must not yield identical ciphertext")
}

func TestDecrypt_TamperedCiphertext_Rejected(t *testing.T) {
	s := tokenStore{encKey: testEncKey()}

	ciphertext, err := s.encrypt("a-token")
	require.NoError(t, err)

	// Decode-flip-a-byte-reencode, rather than mutating a character of the
	// base64 text directly: a single character swap in the base64 alphabet
	// can occasionally decode to the SAME underlying byte (or the string
	// might not even contain the target character), which would make this
	// test flaky/no-op. Flipping a decoded byte always changes the
	// underlying nonce/ciphertext/tag deterministically.
	raw, err := base64.StdEncoding.DecodeString(string(ciphertext))
	require.NoError(t, err)
	require.NotEmpty(t, raw)
	raw[len(raw)-1] ^= 0xFF

	_, err = s.decrypt([]byte(base64.StdEncoding.EncodeToString(raw)))
	assert.Error(t, err, "a tampered ciphertext must fail GCM authentication, not silently decrypt to garbage")
}
