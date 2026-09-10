//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. See the go_test target's gotags in BUILD.bazel for how to run it.
//
// These tests exercise exactly what pgstore_test.go's pure-Go unit tests
// cannot: a real PostgreSQL round trip, genuine ciphertext-at-rest (NFR2),
// and status-first enforcement (FR5/FR6/FR7/FR11) backed by actual SQL
// rather than an in-memory fake -- mirrors
// libs/go/mcpauth/credential_integration_test.go's pattern via
// libs/go/dbtest. Each test creates the expected
// grpcauth_delegated_grant-shaped table itself (no shipped migration --
// FR13; see pgstore.go's package doc for the schema contract).
//
// TODO(Testing phase): see the task issue's "## Testing" section for the
// authoritative scenario list each stub below summarizes.
package pgstore

import "testing"

// TestGrantStore_RoundTrip_PersistStatusTokenMaterial is the base round
// trip: Persist -> Status == active -> TokenMaterial returns the original
// refresh token.
func TestGrantStore_RoundTrip_PersistStatusTokenMaterial(t *testing.T) {
	// TODO: Implement test.
}

// TestGrantStore_NFR2_CiphertextAtRest_NotPlaintextAndPerWriteNonce reads
// the material column directly with raw SQL and asserts the stored bytes
// are not the plaintext refresh token and do not contain it as a
// substring, and that persisting the same plaintext twice yields different
// stored bytes (per-write nonce).
func TestGrantStore_NFR2_CiphertextAtRest_NotPlaintextAndPerWriteNonce(t *testing.T) {
	// TODO: Implement test.
}

// TestGrantStore_FR7_RevokedAndNeedsReauth_ShortCircuitBeforeDecrypt
// asserts Revoke -> TokenMaterial returns ErrGrantRevoked and
// MarkNeedsReauth -> TokenMaterial returns ErrGrantNeedsReauth, in both
// cases proving the status check precedes decryption (e.g. by corrupting
// the stored ciphertext first and confirming the status error still
// surfaces rather than a decrypt error).
func TestGrantStore_FR7_RevokedAndNeedsReauth_ShortCircuitBeforeDecrypt(t *testing.T) {
	// TODO: Implement test.
}

// TestGrantStore_FR5_RevokeIsolatesOtherGrantsOfSameSubject asserts
// Revoke(alice, s1) leaves alice's s2 grant active and usable.
func TestGrantStore_FR5_RevokeIsolatesOtherGrantsOfSameSubject(t *testing.T) {
	// TODO: Implement test.
}

// TestGrantStore_FR6_CallerAgnosticRevoke asserts Revoke(bob, s1), invoked
// with no notion of a "current caller", produces the identical
// unconditional-write + TokenMaterial-short-circuit behaviour as the FR5
// self-service case, and does not affect bob's other grants or alice's
// grants.
func TestGrantStore_FR6_CallerAgnosticRevoke(t *testing.T) {
	// TODO: Implement test.
}

// TestGrantStore_FR11_ReauthThenPersist_ReusesSameRow asserts
// MarkNeedsReauth then Persist with new material yields Status == active
// again, the new material, and an unchanged row count for that subject
// (same key reused, no new grant identity).
func TestGrantStore_FR11_ReauthThenPersist_ReusesSameRow(t *testing.T) {
	// TODO: Implement test.
}

// TestGrantStore_UnknownGrant_ReturnsNotFound asserts Status,
// TokenMaterial, and Revoke on an unknown (subject, grant) all return
// ErrGrantNotFound without panicking.
func TestGrantStore_UnknownGrant_ReturnsNotFound(t *testing.T) {
	// TODO: Implement test.
}

// TestGrantStore_MultiInstance_SharedPool asserts Persist via one
// *grantStore and a read via a second one built on the same pool observe
// the same data (mirrors mcpauth's multi-replica coverage).
func TestGrantStore_MultiInstance_SharedPool(t *testing.T) {
	// TODO: Implement test.
}
