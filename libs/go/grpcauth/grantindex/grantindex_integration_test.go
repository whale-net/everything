//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. See the go_test target's gotags in BUILD.bazel for how to run it.
//
// These tests exercise exactly what grantindex_test.go's pure-Go unit
// tests cannot: a real PostgreSQL round trip and cross-subject/cross-domain
// isolation backed by actual SQL rather than an in-memory fake -- mirrors
// grpcauth/pgstore/pgstore_integration_test.go's pattern via
// libs/go/dbtest. Each test creates the expected grpcauth_grant_index-shaped
// table itself (no shipped migration -- FR13; see grantindex.go's package
// doc for the schema contract).
//
// TODO(Testing phase): see the task issue's "## Testing" section for the
// authoritative scenario list each stub below summarizes.
package grantindex

import "testing"

// TestIndex_RecordThenListBySubject_ReturnsExactlyThatEntry asserts
// Record then ListBySubject returns exactly the recorded entry.
func TestIndex_RecordThenListBySubject_ReturnsExactlyThatEntry(t *testing.T) {
	// TODO: Implement test.
}

// TestIndex_RecordTwice_IdempotentNoDuplicateNoGrantedAtChange asserts
// Record twice for the same (subject_iss, subject_sub, domain) does not
// error and does not duplicate the row or change granted_at.
func TestIndex_RecordTwice_IdempotentNoDuplicateNoGrantedAtChange(t *testing.T) {
	// TODO: Implement test.
}

// TestIndex_ListBySubject_NeverReturnsAnotherSubjectsRows asserts
// ListBySubject for operator A never returns operator B's rows.
func TestIndex_ListBySubject_NeverReturnsAnotherSubjectsRows(t *testing.T) {
	// TODO: Implement test.
}

// TestIndex_ListAll_MultipleOperatorsAndDomains_DeterministicOrder
// asserts ListAll returns entries for multiple operators and multiple
// domains, deterministically ordered.
func TestIndex_ListAll_MultipleOperatorsAndDomains_DeterministicOrder(t *testing.T) {
	// TODO: Implement test.
}

// TestIndex_SameSubjectDifferentDomains_AreDistinctRows asserts an entry
// for (A, audience_score_system) and one for (A, manmanv2) are distinct
// rows.
func TestIndex_SameSubjectDifferentDomains_AreDistinctRows(t *testing.T) {
	// TODO: Implement test.
}
