package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Credential is one row of the consuming domain's auth credential table.
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

	// Persona is the persona resolved at Mint time. Empty unless the store
	// was configured with StoreConfig.PersonaColumn (opt-in).
	Persona string

	// Name is the operator-chosen label on the credential. Empty unless the
	// store was configured with StoreConfig.NameColumn (opt-in) — a store
	// without one neither reads nor writes a name.
	Name string
}

// PersonaResolver resolves an identity to the persona persisted on a newly
// minted credential. Optional: only consulted when
// StoreConfig.PersonaColumn is set.
type PersonaResolver interface {
	ResolvePersona(ctx context.Context, identity string) (string, error)
}

// CredentialStore is the mint/verify/revoke/list lifecycle for MCP bearer
// credentials. Identity is a plain string so this interface stays generic
// across whatever a consuming domain keys credentials on (a Person UUID
// rendered as a string, a service-account name, ...) — see auth.go's
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
var ErrInvalidCredential = errors.New("auth: invalid or revoked credential")

// ErrCredentialNameRequired is returned by MintNamed for an empty or
// whitespace-only name — a refusal the caller must be able to state in its
// own words.
var ErrCredentialNameRequired = errors.New("auth: credential name is required")

// ErrCredentialNameTaken is returned by MintNamed when another of identity's
// credentials already holds the name and is still live. It is translated
// from the partial unique index's violation, so callers never see a driver
// message or an SQLSTATE.
var ErrCredentialNameTaken = errors.New("auth: credential name is already in use")

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
		return fmt.Errorf("auth: StoreConfig.%s %q is not a valid SQL identifier (must match %s)", label, name, identifierPattern.String())
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
	// auth-shaped table. Defaults to "mcp_credential". Unqualified so
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
	// libs/go/auth/README.md for the full write-up): pgx v5's extended
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

	// PersonaColumn, when set, names a nullable TEXT column the store
	// reads and writes the credential's persona through. Unset (the
	// default) leaves generated SQL unchanged.
	PersonaColumn string

	// PersonaResolver supplies the persona written at Mint when
	// PersonaColumn is set. With a nil resolver the column is written NULL.
	PersonaResolver PersonaResolver

	// NameColumn, when set, names a nullable TEXT column the store reads
	// and writes a credential's operator-chosen name through (only via
	// MintNamed). Unset (the default) leaves every generated SQL string
	// byte-for-byte unchanged, so a consuming domain whose table has no
	// name column is unaffected and needs no migration.
	NameColumn string
}

// NamedCredentialStore is a CredentialStore whose backing table also carries
// an operator-chosen credential name (StoreConfig.NameColumn).
//
// It is a separate interface rather than a widened CredentialStore on
// purpose: the roughly two dozen in-memory and Postgres fakes across krill,
// whagent_net, audience_score_system and this package that stand in for a
// store implement CredentialStore today, and a mint-with-a-name requirement
// does not apply to any of them.
type NamedCredentialStore interface {
	CredentialStore

	// MintNamed issues a new credential for identity carrying name, under
	// the same token/hash contract as Mint.
	//
	// An empty or whitespace-only name is refused with
	// ErrCredentialNameRequired, and a name already live on another of
	// identity's credentials with ErrCredentialNameTaken — so a caller can
	// tell the two refusals apart and never sees a raw driver message.
	// Revoking frees the name for reuse.
	//
	// Calling MintNamed on a store configured without NameColumn is a
	// misconfiguration and returns an error naming NameColumn.
	MintNamed(ctx context.Context, identity, name string) (rawToken string, cred Credential, err error)
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
		return nil, errors.New("auth: StoreConfig.Pool is required")
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

	if cfg.PersonaColumn != "" {
		if err := validateIdentifier(cfg.PersonaColumn, "PersonaColumn"); err != nil {
			return nil, err
		}
	}
	if cfg.NameColumn != "" {
		if err := validateIdentifier(cfg.NameColumn, "NameColumn"); err != nil {
			return nil, err
		}
	}

	s := &pgxCredentialStore{cfg: cfg}

	if err := s.probeTable(ctx); err != nil {
		return nil, fmt.Errorf(
			"auth: credential table preflight failed for table %q — apply your domain's mcp_credential migration (see libs/go/auth/README.md schema contract) before calling NewCredentialStore: %w",
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
var _ NamedCredentialStore = (*pgxCredentialStore)(nil)

// probeTable runs a minimal query against the configured table to confirm
// it exists and is accessible. It uses the unqualified table name so it
// exercises the same search_path resolution every runtime query in this
// file uses (mirrors htmxauth.DBSessionManager.probeSessionTable).
func (s *pgxCredentialStore) probeTable(ctx context.Context) error {
	_, err := s.cfg.Pool.Exec(ctx, "SELECT 1 FROM "+s.cfg.TableName+" LIMIT 0")
	return err
}

// columns is the RETURNING/SELECT column list, in Credential scan order.
// The opt-in columns are appended in the same fixed order scanCredential
// reads them.
func (s *pgxCredentialStore) columns() string {
	cols := fmt.Sprintf("id, %s, token_hash, created_at, last_used_at, revoked_at", s.cfg.IdentityColumn)
	if s.cfg.PersonaColumn != "" {
		cols += ", " + s.cfg.PersonaColumn
	}
	if s.cfg.NameColumn != "" {
		cols += ", " + s.cfg.NameColumn
	}
	return cols
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

func (s *pgxCredentialStore) scanCredential(row pgx.Row) (Credential, error) {
	var c Credential
	var persona, name *string
	dest := []any{&c.ID, &c.Identity, &c.TokenHash, &c.CreatedAt, &c.LastUsedAt, &c.RevokedAt}
	if s.cfg.PersonaColumn != "" {
		dest = append(dest, &persona)
	}
	if s.cfg.NameColumn != "" {
		dest = append(dest, &name)
	}
	err := row.Scan(dest...)
	if persona != nil {
		c.Persona = *persona
	}
	if name != nil {
		c.Name = *name
	}
	return c, err
}

// mintQuery renders the INSERT for Mint; the persona and name columns and
// their parameters appear only when the corresponding StoreConfig field is
// set. Mint never supplies a name, so on a NameColumn-configured store the
// name is written NULL.
func (s *pgxCredentialStore) mintQuery() string {
	return s.insertQuery(false)
}

// insertQuery renders the INSERT with the persona parameter included when
// PersonaColumn is set and, when withName is true, the name parameter
// included when NameColumn is set. Parameters follow the same order as
// columns: identity, token_hash, persona, name.
func (s *pgxCredentialStore) insertQuery(withName bool) string {
	cols := fmt.Sprintf("%s, token_hash", s.cfg.IdentityColumn)
	values := fmt.Sprintf("$1%s, $2", s.identityCastSuffix())
	next := 3
	if s.cfg.PersonaColumn != "" {
		cols += ", " + s.cfg.PersonaColumn
		values += fmt.Sprintf(", $%d", next)
		next++
	}
	if withName && s.cfg.NameColumn != "" {
		cols += ", " + s.cfg.NameColumn
		values += fmt.Sprintf(", $%d", next)
	}
	return fmt.Sprintf(`
		INSERT INTO %s (%s)
		VALUES (%s)
		RETURNING %s
	`, s.cfg.TableName, cols, values, s.columns())
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

// MintNamed issues a credential for identity carrying an operator-chosen
// name, under Mint's token and hash-at-rest contract.
//
// A name is only ever written through this path: a store configured with
// NameColumn still mints NULL-named rows from Mint, which is what keeps the
// self-serve JSON API and the authorization-code path unchanged.
func (s *pgxCredentialStore) MintNamed(ctx context.Context, identity, name string) (string, Credential, error) {
	if s.cfg.NameColumn == "" {
		return "", Credential{}, errors.New("auth: MintNamed requires StoreConfig.NameColumn — this store is configured without a credential name column")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", Credential{}, ErrCredentialNameRequired
	}

	rawToken, err := generateToken()
	if err != nil {
		return "", Credential{}, fmt.Errorf("auth: generate credential token: %w", err)
	}

	args := []any{identity, hashToken(rawToken)}
	persona, err := s.resolvePersonaArg(ctx, identity)
	if err != nil {
		return "", Credential{}, err
	}
	if persona != nil {
		args = append(args, persona)
	}
	args = append(args, name)

	cred, err := s.scanCredential(s.cfg.Pool.QueryRow(ctx, s.insertQuery(true), args...))
	if err != nil {
		if s.isCredentialNameTaken(err) {
			return "", Credential{}, ErrCredentialNameTaken
		}
		return "", Credential{}, fmt.Errorf("auth: insert credential: %w", err)
	}
	return rawToken, cred, nil
}

// Mint generates a fresh high-entropy token, persists only its SHA-256
// hash, and returns the raw token (the caller must show it to the operator
// exactly once — it is never recoverable again) plus the persisted row.
func (s *pgxCredentialStore) Mint(ctx context.Context, identity string) (string, Credential, error) {
	rawToken, err := generateToken()
	if err != nil {
		return "", Credential{}, fmt.Errorf("auth: generate credential token: %w", err)
	}

	args := []any{identity, hashToken(rawToken)}
	persona, err := s.resolvePersonaArg(ctx, identity)
	if err != nil {
		return "", Credential{}, err
	}
	if persona != nil {
		args = append(args, persona)
	}

	cred, err := s.scanCredential(s.cfg.Pool.QueryRow(ctx, s.mintQuery(), args...))
	if err != nil {
		return "", Credential{}, fmt.Errorf("auth: insert credential: %w", err)
	}
	return rawToken, cred, nil
}

// resolvePersonaArg returns the persona value to bind at Mint time, or nil
// to write NULL. Only consulted when PersonaColumn is set.
func (s *pgxCredentialStore) resolvePersonaArg(ctx context.Context, identity string) (*string, error) {
	if s.cfg.PersonaColumn == "" {
		return nil, nil
	}
	if p := personaFromContext(ctx); p != "" {
		return &p, nil
	}
	if s.cfg.PersonaResolver == nil {
		return nil, nil
	}
	p, err := s.cfg.PersonaResolver.ResolvePersona(ctx, identity)
	if err != nil {
		return nil, fmt.Errorf("auth: resolve persona: %w", err)
	}
	if p == "" {
		return nil, nil
	}
	return &p, nil
}

// isCredentialNameTaken reports whether err is the unique index on
// (identity, name) refusing a second live credential. The driver message
// itself never reaches a caller — only the named refusal does.
//
// Postgres names a unique-index violation after the index, so this matches
// the configured NameColumn appearing in that name. token_hash is excluded
// explicitly rather than relied on not matching: it is the table's other
// unique constraint, and a substring collision there would report a
// cryptographic near-impossible as an operator's name conflict.
func (s *pgxCredentialStore) isCredentialNameTaken(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	constraint := strings.ToLower(pgErr.ConstraintName)
	if strings.Contains(constraint, "token_hash") {
		return false
	}
	return s.cfg.NameColumn != "" && strings.Contains(constraint, s.cfg.NameColumn)
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

	cred, err := s.scanCredential(s.cfg.Pool.QueryRow(ctx, query, hashToken(rawToken)))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", Credential{}, ErrInvalidCredential
		}
		return "", Credential{}, fmt.Errorf("auth: verify credential: %w", err)
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
		return fmt.Errorf("auth: revoke credential: %w", err)
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
		return nil, fmt.Errorf("auth: list credentials: %w", err)
	}
	defer rows.Close()

	var creds []Credential
	for rows.Next() {
		c, err := s.scanCredential(rows)
		if err != nil {
			return nil, fmt.Errorf("auth: scan credential: %w", err)
		}
		creds = append(creds, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("auth: list credentials: %w", err)
	}
	return creds, nil
}
