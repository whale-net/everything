package grantindex

import "testing"

// TestNew_NilPool_Rejected asserts pool == nil is rejected with a clear
// error rather than panicking or silently proceeding.
func TestNew_NilPool_Rejected(t *testing.T) {
	// TODO: Implement test.
}

// TestNew_InvalidIdentifiers_Rejected asserts each caller-supplied
// table/column name that does not match identifierPattern is rejected
// with a clear error -- e.g. "foo; drop table" and "foo bar" -- for every
// one of TableName, SubjectIssColumn, SubjectSubColumn, DomainColumn,
// PreferredUsernameColumn, and GrantedAtColumn.
func TestNew_InvalidIdentifiers_Rejected(t *testing.T) {
	// TODO: Implement test.
}

// TestNew_DefaultsApplied asserts every Config name field left
// zero-valued resolves to its documented default (grpcauth_grant_index /
// subject_iss / subject_sub / domain / preferred_username / granted_at),
// and that those defaults themselves pass validateIdentifier.
func TestNew_DefaultsApplied(t *testing.T) {
	// TODO: Implement test.
}
