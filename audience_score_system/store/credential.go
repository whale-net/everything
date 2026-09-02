package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
type credentialStore struct{ pool *pgxpool.Pool }

var _ CredentialStore = credentialStore{}

const credentialColumns = `id, person_id, token_hash, created_at, last_used_at, revoked_at`

func scanCredential(row pgx.Row) (MCPCredential, error) {
	var c MCPCredential
	err := row.Scan(&c.ID, &c.PersonID, &c.TokenHash, &c.CreatedAt, &c.LastUsedAt, &c.RevokedAt)
	return c, err
}

// hashToken returns the hex-encoded SHA-256 hash of the raw token --
// migration 005's token_hash column, and the only form of the credential
// this store ever persists.
func hashToken(rawToken string) string {
	sum := sha256.Sum256([]byte(rawToken))
	return hex.EncodeToString(sum[:])
}

// generateCredentialToken returns a high-entropy (crypto/rand), hex-encoded
// bearer token -- mirrors invite.go's generateInviteCode.
func generateCredentialToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Mint generates a fresh high-entropy token, persists only its SHA-256
// hash, and returns the raw token (shown to the Person exactly once by
// the caller -- see ../ARCHITECTURE.md "MCP server: caller
// authentication") plus the persisted row.
func (s credentialStore) Mint(ctx context.Context, personID uuid.UUID) (string, MCPCredential, error) {
	rawToken, err := generateCredentialToken()
	if err != nil {
		return "", MCPCredential{}, fmt.Errorf("generate credential token: %w", err)
	}

	cred, err := scanCredential(s.pool.QueryRow(ctx, `
		INSERT INTO mcp_credential (person_id, token_hash)
		VALUES ($1, $2)
		RETURNING `+credentialColumns,
		personID, hashToken(rawToken)))
	if err != nil {
		return "", MCPCredential{}, fmt.Errorf("insert mcp_credential: %w", err)
	}
	return rawToken, cred, nil
}

// VerifyTokenHash resolves tokenHash to a live credential's PersonID,
// touching last_used_at in the same round trip so repeated MCP calls keep
// this an accurate "last seen" signal without a separate write.
func (s credentialStore) VerifyTokenHash(ctx context.Context, tokenHash string) (uuid.UUID, error) {
	var personID uuid.UUID
	err := s.pool.QueryRow(ctx, `
		UPDATE mcp_credential SET last_used_at = NOW()
		WHERE token_hash = $1 AND revoked_at IS NULL
		RETURNING person_id
	`, tokenHash).Scan(&personID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, errors.New("credential not found or revoked")
		}
		return uuid.Nil, fmt.Errorf("verify token hash: %w", err)
	}
	return personID, nil
}

// Revoke sets revoked_at on id if it is live and owned by personID.
// Idempotent by design (NFR2): revoking an already-revoked or nonexistent
// (or not-owned) credential is not an error -- the WHERE clause simply
// matches zero rows.
func (s credentialStore) Revoke(ctx context.Context, id uuid.UUID, personID uuid.UUID) error {
	if _, err := s.pool.Exec(ctx, `
		UPDATE mcp_credential SET revoked_at = NOW()
		WHERE id = $1 AND person_id = $2 AND revoked_at IS NULL
	`, id, personID); err != nil {
		return fmt.Errorf("revoke mcp_credential: %w", err)
	}
	return nil
}

// ListForPerson returns every credential (live and revoked) personID has
// minted, most recent first.
func (s credentialStore) ListForPerson(ctx context.Context, personID uuid.UUID) ([]MCPCredential, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+credentialColumns+`
		FROM mcp_credential
		WHERE person_id = $1
		ORDER BY created_at DESC, id
	`, personID)
	if err != nil {
		return nil, fmt.Errorf("list credentials for person: %w", err)
	}
	defer rows.Close()

	var creds []MCPCredential
	for rows.Next() {
		c, err := scanCredential(rows)
		if err != nil {
			return nil, fmt.Errorf("scan mcp_credential: %w", err)
		}
		creds = append(creds, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list credentials for person: %w", err)
	}
	return creds, nil
}
