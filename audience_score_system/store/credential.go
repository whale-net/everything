package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CredentialStore covers `mcp_credential` (migration 005, issue #1575) --
// the bearer credential `mcp`'s auth middleware resolves to a Person. See
// ../ARCHITECTURE.md "MCP server: caller authentication" for the full
// obtain/revoke/NFR3 design decision this store backs.
type CredentialStore interface {
	// Mint issues a new credential for personID: generates a high-entropy
	// random token, persists only its SHA-256 hash, and returns the raw
	// token exactly once (the caller -- `web`'s mint endpoint -- must show
	// it to the Person immediately; it is never recoverable afterward) plus
	// the persisted MCPCredential row.
	Mint(ctx context.Context, personID uuid.UUID) (rawToken string, cred MCPCredential, err error)

	// VerifyTokenHash resolves tokenHash (the SHA-256 hash of a raw bearer
	// token presented to `mcp`) to the live (RevokedAt == nil) credential's
	// PersonID. Returns an error if no live credential matches -- an
	// unrecognized, mistyped, or revoked token all fail identically here;
	// distinguishing "unauthenticated" from other failures is the auth
	// middleware's job (mcp/server), not this store's.
	VerifyTokenHash(ctx context.Context, tokenHash string) (personID uuid.UUID, err error)

	// Revoke closes the credential (sets RevokedAt) if it is currently live
	// and owned by personID. Not an error to revoke an already-revoked or
	// nonexistent credential -- revocation is idempotent by design (NFR2).
	Revoke(ctx context.Context, id uuid.UUID, personID uuid.UUID) error

	// ListForPerson returns every credential (live and revoked) personID
	// has ever minted, most recent first -- the data source for a future
	// "manage your MCP credentials" affordance on `web`.
	ListForPerson(ctx context.Context, personID uuid.UUID) ([]MCPCredential, error)
}

// credentialStore implements CredentialStore against `mcp_credential`
// (migration 005).
//
// Scaffold only -- every method below is a stub. Full implementation lands
// in the Implementation phase (issue #1575's "Implementation" scope).
type credentialStore struct{ pool *pgxpool.Pool }

var _ CredentialStore = credentialStore{}

func (s credentialStore) Mint(ctx context.Context, personID uuid.UUID) (string, MCPCredential, error) {
	return "", MCPCredential{}, errors.New("not implemented")
}

func (s credentialStore) VerifyTokenHash(ctx context.Context, tokenHash string) (uuid.UUID, error) {
	return uuid.Nil, errors.New("not implemented")
}

func (s credentialStore) Revoke(ctx context.Context, id uuid.UUID, personID uuid.UUID) error {
	return errors.New("not implemented")
}

func (s credentialStore) ListForPerson(ctx context.Context, personID uuid.UUID) ([]MCPCredential, error) {
	return nil, errors.New("not implemented")
}
