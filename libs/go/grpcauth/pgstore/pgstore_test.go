package pgstore

import "testing"

// TestNewGrantStore_NilPool_Rejected asserts StoreConfig.Pool == nil is
// rejected with a clear error rather than panicking or silently proceeding.
func TestNewGrantStore_NilPool_Rejected(t *testing.T) {
	// TODO: Implement test.
}

// TestNewGrantStore_InvalidEncryptionKeySize_Rejected asserts any
// EncryptionKey that is not exactly grpcauth.GrantKeySize (32) bytes is
// rejected -- including nil/empty and both too-short and too-long keys.
func TestNewGrantStore_InvalidEncryptionKeySize_Rejected(t *testing.T) {
	// TODO: Implement test.
}

// TestNewGrantStore_InvalidIdentifiers_Rejected asserts each
// caller-supplied table/column/cast name that does not match
// identifierPattern is rejected with a clear error -- e.g.
// "foo; drop table" and "foo bar" -- for every one of TableName,
// SubjectColumn, GrantColumn, MaterialColumn, StatusColumn, and
// SubjectCast.
func TestNewGrantStore_InvalidIdentifiers_Rejected(t *testing.T) {
	// TODO: Implement test.
}

// TestNewGrantStore_DefaultsApplied asserts every StoreConfig name field
// left zero-valued resolves to its documented default
// (grpcauth_delegated_grant / subject / grant_key / token_material /
// status), and that those defaults themselves pass validateIdentifier.
func TestNewGrantStore_DefaultsApplied(t *testing.T) {
	// TODO: Implement test.
}
