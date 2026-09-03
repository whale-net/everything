package mcpauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Credential is one row of the consuming domain's mcpauth credential table.
// TokenHash is always the hex-encoded SHA-256 hash of the raw bearer token
// (NFR1) — the raw token itself is never persisted and never appears on
// this struct.
type Credential struct {
	ID         uuid.UUID
	Identity   string
	TokenHash  string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// CredentialStore is the mint/verify/revoke/list lifecycle for MCP bearer
// credentials. Identity is a plain string so this interface stays generic
// across whatever a consuming domain keys credentials on (a Person UUID
// rendered as a string, a service-account name, ...) — see mcpauth.go's
// NFR2 boundary. StoreConfig.IdentityCast is how a consuming domain whose
// identity column is a non-text type (e.g. ASS's person_id UUID) tells this
// package how to cast the string identity parameter in generated SQL.
type CredentialStore interface {
	// Mint issues a new credential for identity: generates a high-entropy
	// random token, persists only its SHA-256 hash, and returns the raw
	// token exactly once — the caller must show it to the operator
	// immediately; it is never recoverable afterward (FR4, FR5, NFR1) —
	// plus the persisted Credential row.
	Mint(ctx context.Context, identity string) (rawToken string, cred Credential, err error)

	// Verify resolves rawToken (the raw bearer token presented by an MCP
	// client, NOT a precomputed hash — hashing happens inside this method)
	// to the live (RevokedAt == nil) credential's identity, stamping
	// LastUsedAt in the same round trip (FR9). An unrecognized, malformed,
	// and revoked token must all fail with the same opaque error (FR6,
	// NFR1) — see credential_test.go for the exact-match assertion once
	// Implementation lands.
	Verify(ctx context.Context, rawToken string) (identity string, cred Credential, err error)

	// Revoke closes the credential (sets RevokedAt) if it is currently
	// live and owned by identity. Not an error to revoke an
	// already-revoked, nonexistent, or not-owned credential — revocation
	// is idempotent by design (FR7) and must not leak whether a
	// not-owned id exists.
	Revoke(ctx context.Context, id uuid.UUID, identity string) error

	// List returns every credential (live and revoked) identity has ever
	// minted, most recent first (FR8).
	List(ctx context.Context, identity string) ([]Credential, error)
}

// Default StoreConfig values, used whenever the corresponding field is left
// zero-valued.
const (
	defaultTableName      = "mcp_credential"
	defaultIdentityColumn = "identity"
)

// ErrInvalidCredential is returned by Verify for any raw token that does not
// resolve to a live credential: unrecognized, malformed, and revoked tokens
// are all indistinguishable to a caller (FR6, NFR1) — the error value and
// its message never vary, and never include the presented token or its
// hash.
var ErrInvalidCredential = errors.New("mcpauth: invalid or revoked credential")

// identifierPattern is the strict allow-list StoreConfig.TableName,
// StoreConfig.IdentityColumn, and StoreConfig.IdentityCast must match.
// These three values are interpolated directly into generated SQL (they
// cannot be bound query parameters), so NewCredentialStore rejects
// anything not matching this pattern before ever building a query string —
// this is a hard requirement against SQL injection via configuration, not
// a style nicety.
var identifierPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// validateIdentifier rejects any name that is not a safe, lowercase SQL
// identifier for direct interpolation into generated SQL.
func validateIdentifier(name, label string) error {
	if !identifierPattern.MatchString(name) {
		return fmt.Errorf("mcpauth: StoreConfig.%s %q is not a valid SQL identifier (must match %s)", label, name, identifierPattern.String())
	}
	return nil
}

// StoreConfig configures NewCredentialStore. TableName, IdentityColumn, and
// IdentityCast are interpolated directly into generated SQL (they cannot be
// bound as query parameters), so NewCredentialStore validates them against
// a strict identifier regex before ever building a query string — see the
// Implementation-phase validateIdentifier for the exact pattern and
// rejection cases.
type StoreConfig struct {
	// Pool is the PostgreSQL connection pool. Required.
	Pool *pgxpool.Pool

	// TableName is the unqualified name of the consuming domain's
	// mcpauth-shaped table. Defaults to "mcp_credential". Unqualified so
	// the same search_path every other runtime query resolves against is
	// what the preflight probe exercises too (mirrors
	// libs/go/htmxauth.DBSessionManager's ui_sessionsTable convention).
	TableName string

	// IdentityColumn is the unqualified name of the identity column.
	// Defaults to "identity". ASS's table names this "person_id".
	IdentityColumn string

	// IdentityCast is an optional PostgreSQL type name (e.g. "uuid") to
	// cast the identity parameter to in generated SQL, producing
	// `<IdentityColumn> = $N::<IdentityCast>` instead of
	// `<IdentityColumn> = $N`.
	//
	// Resolved question (see README.md "Identity column and casting" and
	// libs/go/mcpauth/README.md for the full write-up): pgx v5's extended
	// query protocol *can* encode a Go string parameter against a
	// PostgreSQL uuid column, and can scan a uuid column into a Go
	// string, without any explicit cast — verified directly against a
	// real Postgres (crypto/rand-free throwaway spike using pgxpool,
	// mirroring //libs/go/dbtest's container setup) for INSERT, SELECT
	// ... WHERE, and UPDATE ... RETURNING. IdentityCast is therefore
	// optional in the common case; it stays as an explicit escape hatch
	// for identity columns pgx cannot infer a type for by context alone
	// (e.g. a custom domain/enum type), and because being explicit in
	// generated SQL is cheap insurance against a future pgx or Postgres
	// version regressing the implicit-cast behavior silently.
	IdentityCast string
}

// NewCredentialStore constructs a CredentialStore backed by cfg.Pool and
// the table/column names in cfg (defaults applied for anything left
// zero-valued).
//
// TableName, IdentityColumn, and IdentityCast are validated as safe SQL
// identifiers before any query is built, and the configured table is
// preflighted with a minimal query (mirroring
// htmxauth.DBSessionManager.probeSessionTable) using its unqualified name
// so the probe exercises the same search_path every runtime query
// resolves against. A failed preflight returns an error naming the table
// and does not silently degrade — the caller must apply their domain's
// migration and retry.
func NewCredentialStore(ctx context.Context, cfg StoreConfig) (CredentialStore, error) {
	if cfg.Pool == nil {
		return nil, errors.New("mcpauth: StoreConfig.Pool is required")
	}
	if cfg.TableName == "" {
		cfg.TableName = defaultTableName
	}
	if cfg.IdentityColumn == "" {
		cfg.IdentityColumn = defaultIdentityColumn
	}

	if err := validateIdentifier(cfg.TableName, "TableName"); err != nil {
		return nil, err
	}
	if err := validateIdentifier(cfg.IdentityColumn, "IdentityColumn"); err != nil {
		return nil, err
	}
	if cfg.IdentityCast != "" {
		if err := validateIdentifier(cfg.IdentityCast, "IdentityCast"); err != nil {
			return nil, err
		}
	}

	s := &pgxCredentialStore{cfg: cfg}

	if err := s.probeTable(ctx); err != nil {
		return nil, fmt.Errorf(
			"mcpauth: credential table preflight failed for table %q — apply your domain's mcp_credential migration (see libs/go/mcpauth/README.md schema contract) before calling NewCredentialStore: %w",
			cfg.TableName, err,
		)
	}

	return s, nil
}

// pgxCredentialStore is the pgx-backed CredentialStore implementation.
type pgxCredentialStore struct {
	cfg StoreConfig
}

var _ CredentialStore = (*pgxCredentialStore)(nil)

// probeTable runs a minimal query against the configured table to confirm
// it exists and is accessible. It uses the unqualified table name so it
// exercises the same search_path resolution every runtime query in this
// file uses (mirrors htmxauth.DBSessionManager.probeSessionTable).
func (s *pgxCredentialStore) probeTable(ctx context.Context) error {
	_, err := s.cfg.Pool.Exec(ctx, "SELECT 1 FROM "+s.cfg.TableName+" LIMIT 0")
	return err
}

// columns is the RETURNING/SELECT column list, in Credential scan order.
func (s *pgxCredentialStore) columns() string {
	return fmt.Sprintf("id, %s, token_hash, created_at, last_used_at, revoked_at", s.cfg.IdentityColumn)
}

// identityCastSuffix returns the "::<type>" suffix to append after an
// identity parameter placeholder, or "" if no cast was configured.
func (s *pgxCredentialStore) identityCastSuffix() string {
	if s.cfg.IdentityCast == "" {
		return ""
	}
	return "::" + s.cfg.IdentityCast
}

// identityPlaceholder renders "<IdentityColumn> = $<paramNum>[::<cast>]"
// for use in a WHERE clause.
func (s *pgxCredentialStore) identityPlaceholder(paramNum int) string {
	return fmt.Sprintf("%s = $%d%s", s.cfg.IdentityColumn, paramNum, s.identityCastSuffix())
}

func scanCredential(row pgx.Row) (Credential, error) {
	var c Credential
	err := row.Scan(&c.ID, &c.Identity, &c.TokenHash, &c.CreatedAt, &c.LastUsedAt, &c.RevokedAt)
	return c, err
}

// generateToken returns a high-entropy (crypto/rand), hex-encoded bearer
// token — mirrors audience_score_system/store/credential.go's
// generateCredentialToken, the behavioral bar this package reproduces.
func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// hashToken returns the hex-encoded SHA-256 hash of the raw token — the
// only form of the credential this store ever persists (NFR1). Byte-for-
// byte identical to audience_score_system/store/credential.go's hashToken,
// which credential_test.go asserts directly (FR13 parity).
func hashToken(rawToken string) string {
	sum := sha256.Sum256([]byte(rawToken))
	return hex.EncodeToString(sum[:])
}

// Mint generates a fresh high-entropy token, persists only its SHA-256
// hash, and returns the raw token (the caller must show it to the operator
// exactly once — it is never recoverable again) plus the persisted row.
func (s *pgxCredentialStore) Mint(ctx context.Context, identity string) (string, Credential, error) {
	rawToken, err := generateToken()
	if err != nil {
		return "", Credential{}, fmt.Errorf("mcpauth: generate credential token: %w", err)
	}

	query := fmt.Sprintf(`
		INSERT INTO %s (%s, token_hash)
		VALUES ($1%s, $2)
		RETURNING %s
	`, s.cfg.TableName, s.cfg.IdentityColumn, s.identityCastSuffix(), s.columns())

	cred, err := scanCredential(s.cfg.Pool.QueryRow(ctx, query, identity, hashToken(rawToken)))
	if err != nil {
		return "", Credential{}, fmt.Errorf("mcpauth: insert credential: %w", err)
	}
	return rawToken, cred, nil
}

// Verify hashes rawToken and resolves it to a live credential, stamping
// last_used_at in the same round trip (FR9) so repeated calls keep it an
// accurate "last seen" signal without a separate write. An unrecognized,
// malformed, or revoked token all fail identically with
// ErrInvalidCredential — the WHERE clause simply matches zero rows in
// every case, so there is no branch that could vary the error (FR6, NFR1).
func (s *pgxCredentialStore) Verify(ctx context.Context, rawToken string) (string, Credential, error) {
	query := fmt.Sprintf(`
		UPDATE %s SET last_used_at = NOW()
		WHERE token_hash = $1 AND revoked_at IS NULL
		RETURNING %s
	`, s.cfg.TableName, s.columns())

	cred, err := scanCredential(s.cfg.Pool.QueryRow(ctx, query, hashToken(rawToken)))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", Credential{}, ErrInvalidCredential
		}
		return "", Credential{}, fmt.Errorf("mcpauth: verify credential: %w", err)
	}
	return cred.Identity, cred, nil
}

// Revoke sets revoked_at on id if it is live and owned by identity.
// Idempotent by design (FR7): revoking an already-revoked, nonexistent, or
// not-owned credential is not an error and does not leak whether a
// not-owned id exists — the WHERE clause simply matches zero rows.
func (s *pgxCredentialStore) Revoke(ctx context.Context, id uuid.UUID, identity string) error {
	query := fmt.Sprintf(`
		UPDATE %s SET revoked_at = NOW()
		WHERE id = $1 AND %s AND revoked_at IS NULL
	`, s.cfg.TableName, s.identityPlaceholder(2))

	if _, err := s.cfg.Pool.Exec(ctx, query, id, identity); err != nil {
		return fmt.Errorf("mcpauth: revoke credential: %w", err)
	}
	return nil
}

// List returns every credential (live and revoked) identity has ever
// minted, most recent first (FR8).
func (s *pgxCredentialStore) List(ctx context.Context, identity string) ([]Credential, error) {
	query := fmt.Sprintf(`
		SELECT %s
		FROM %s
		WHERE %s
		ORDER BY created_at DESC, id
	`, s.columns(), s.cfg.TableName, s.identityPlaceholder(1))

	rows, err := s.cfg.Pool.Query(ctx, query, identity)
	if err != nil {
		return nil, fmt.Errorf("mcpauth: list credentials: %w", err)
	}
	defer rows.Close()

	var creds []Credential
	for rows.Next() {
		c, err := scanCredential(rows)
		if err != nil {
			return nil, fmt.Errorf("mcpauth: scan credential: %w", err)
		}
		creds = append(creds, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mcpauth: list credentials: %w", err)
	}
	return creds, nil
}
