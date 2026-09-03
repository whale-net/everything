package mcpauth

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
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
// Scaffold note: this constructor currently only applies StoreConfig
// defaults and captures configuration; it does not yet validate
// TableName/IdentityColumn/IdentityCast as SQL identifiers, and does not
// yet run the boot-time preflight probe against the configured table. Both
// land in the Implementation phase of issue #1639 — see that issue's
// "Implementation" section for the exact preflight and validation
// requirements (mirroring htmxauth.DBSessionManager.probeSessionTable).
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

	// TODO(Implementation, issue #1639): validate cfg.TableName,
	// cfg.IdentityColumn, and cfg.IdentityCast (if set) against
	// identifierPattern, and run the boot-time preflight probe of
	// cfg.TableName before returning — see mcpauth.go's schema-ownership
	// section and README.md for the required error shape.
	return &pgxCredentialStore{cfg: cfg}, nil
}

// pgxCredentialStore is the pgx-backed CredentialStore implementation.
type pgxCredentialStore struct {
	cfg StoreConfig
}

var _ CredentialStore = (*pgxCredentialStore)(nil)

// errNotImplemented is returned by every method stub below until the
// Implementation phase of issue #1639 lands the real SQL. Scaffold exists
// to settle the package's public shape and the identity-cast schema
// question, not the method bodies.
var errNotImplemented = errors.New("mcpauth: not implemented yet (scaffold phase, see issue #1639)")

func (s *pgxCredentialStore) Mint(ctx context.Context, identity string) (string, Credential, error) {
	return "", Credential{}, errNotImplemented
}

func (s *pgxCredentialStore) Verify(ctx context.Context, rawToken string) (string, Credential, error) {
	return "", Credential{}, errNotImplemented
}

func (s *pgxCredentialStore) Revoke(ctx context.Context, id uuid.UUID, identity string) error {
	return errNotImplemented
}

func (s *pgxCredentialStore) List(ctx context.Context, identity string) ([]Credential, error) {
	return nil, errNotImplemented
}
