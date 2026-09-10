package grpcauth

// GrantStatus is the persisted lifecycle state of a delegated grant, keyed by
// (subject, grant). It is a plain, cheap-to-read value: reading it never
// decrypts token material and never calls Keycloak (FR10).
//
// There are exactly three states. A grant that has no persisted row at all is
// not a fourth state — that case is reported as ErrGrantNotFound.
type GrantStatus string

const (
	// GrantStatusActive means token material is present and believed usable.
	// A grant becomes active on Persist (initial consent, re-consent, or
	// refresh write-back) and stays active until something moves it.
	GrantStatusActive GrantStatus = "active"

	// GrantStatusNeedsReauth means the stored refresh token was rejected by
	// Keycloak in a way that only a human re-consent can fix (FR8). The row
	// is retained rather than deleted so history survives re-consent.
	GrantStatusNeedsReauth GrantStatus = "needs_reauth"

	// GrantStatusRevoked means the grant was deliberately withdrawn, either
	// by the grantor or by an operator/admin (FR5/FR6/FR7). Revocation is
	// unconditional and is not undone by anything except a fresh Persist.
	GrantStatusRevoked GrantStatus = "revoked"
)

// String returns the wire/storage representation of the status.
func (s GrantStatus) String() string {
	return string(s)
}

// Valid reports whether s is one of the three defined GrantStatus values.
// Storage implementations use this to reject rows carrying an unknown status
// rather than silently treating them as active.
func (s GrantStatus) Valid() bool {
	switch s {
	case GrantStatusActive, GrantStatusNeedsReauth, GrantStatusRevoked:
		return true
	default:
		return false
	}
}
