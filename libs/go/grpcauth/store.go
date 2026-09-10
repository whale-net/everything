package grpcauth

import (
	"context"
	"time"
)

// TokenMaterial is the plaintext side of a delegated grant's persisted
// credential. Callers hand it to and receive it from a Store as plaintext;
// encryption and decryption happen inside the Store implementation and never
// in caller code.
//
// Do not log a TokenMaterial or embed one in an error (NFR1).
type TokenMaterial struct {
	// RefreshToken is the Keycloak offline/refresh token obtained from the
	// grantor's consent.
	RefreshToken string

	// ObtainedAt is when this refresh token was issued to us. It is metadata
	// only: staleness decisions belong to the caller, not the Store.
	ObtainedAt time.Time
}

// Store persists delegated-grant refresh-token material keyed by
// (subject, grant). It has no "list grants for a subject" surface by
// design — see README (FR12): a consuming domain's own schedule table
// already maps owner -> grant.
//
// Every method returns a TransientError (see IsTransient) for failures that
// say nothing about the grant itself, and the ErrGrant* sentinels for failures
// that do. The two classes are always distinguishable via errors.Is.
type Store interface {
	// Persist encrypts and writes token material for (subject, grant),
	// overwriting any existing material and resetting status to active.
	// Used by initial consent (FR2), re-consent (FR11) and refresh
	// write-back (FR13).
	Persist(ctx context.Context, subject, grant string, material TokenMaterial) error

	// TokenMaterial checks persisted status FIRST and returns
	// ErrGrantRevoked / ErrGrantNeedsReauth without decrypting anything
	// when the grant is not active; otherwise decrypts and returns the
	// currently persisted refresh token.
	TokenMaterial(ctx context.Context, subject, grant string) (TokenMaterial, error)

	// Status is a cheap plain read of persisted GrantStatus; no decrypt,
	// no Keycloak call (FR10).
	Status(ctx context.Context, subject, grant string) (GrantStatus, error)

	// MarkNeedsReauth transitions the grant to needs_reauth (FR8),
	// retaining history rather than deleting the row.
	MarkNeedsReauth(ctx context.Context, subject, grant string) error

	// Revoke unconditionally sets status to revoked (FR5/FR6/FR7). The
	// (subject, grant) key is explicit and the implementation enforces
	// no restriction tying the caller to that subject.
	Revoke(ctx context.Context, subject, grant string) error
}
