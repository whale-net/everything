package mcpauth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AuthCode is one pending (unredeemed) OAuth2 authorization code issued by
// GET /authorize and redeemed exactly once by POST /token (RFC 7636 PKCE).
// Like the bearer credential (credential.go), it is persisted only as a
// SHA-256 hash (NFR1) — see AuthCodeStore's doc — so a leaked table read
// must not yield a redeemable code.
type AuthCode struct {
	Code                string // stored hashed, never in plaintext
	ClientID            string
	RedirectURI         string
	Identity            string
	CodeChallenge       string
	CodeChallengeMethod string // always "S256"
	ExpiresAt           time.Time
}

// AuthCodeStore is the save/consume lifecycle for pending OAuth2
// authorization codes minted by GET /authorize and redeemed by POST /token
// (#1642).
type AuthCodeStore interface {
	// Save persists a pending authorization code.
	Save(ctx context.Context, code AuthCode) error

	// Consume atomically redeems a code exactly once: a second Consume of
	// the same code must fail, even under concurrency.
	Consume(ctx context.Context, rawCode string) (AuthCode, error)
}

// ErrAuthCodeNotFound is returned by AuthCodeStore.Consume for a code that
// was never issued, has already been consumed, or was issued by a
// different, isolated store instance (see NewMemoryAuthCodeStore's doc). It
// is a distinguishable sentinel, not an opaque error, because the /token
// handler (token.go, #1642) needs to render "unknown code" the same way it
// renders every other invalid_grant case — see token.go's Implementation
// phase.
var ErrAuthCodeNotFound = errors.New("mcpauth: unknown or already-consumed authorization code")

// generateAuthCode returns a high-entropy (crypto/rand), hex-encoded raw
// authorization code — same shape as credential.go's generateToken (32
// random bytes, hex-encoded), satisfying #1642's "≥32 bytes" requirement.
// The caller (authorize.go) hashes the result with hashToken before handing
// it to AuthCodeStore.Save — see AuthCode.Code's doc — and hands the raw
// value back to the client in the redirect; it is never persisted.
func generateAuthCode() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// defaultAuthCodeTableName is AuthCodeStoreConfig.TableName's default,
// mirroring StoreConfig's defaultTableName convention in credential.go and
// ClientRegistryConfig's defaultClientTableName convention in clients.go.
const defaultAuthCodeTableName = "mcp_auth_code"

// memoryAuthCodeStore is the in-process AuthCodeStore implementation:
// enough for a single-replica server, but each process has its own
// isolated map — a code minted against replica A is unknown to replica B.
// See NewPostgresAuthCodeStore for the multi-replica-safe alternative;
// /authorize and /token can land on different replicas, so a
// multi-replica deployment MUST use it instead (same warning as
// clients.go's memoryClientRegistry).
type memoryAuthCodeStore struct {
	mu    sync.Mutex
	codes map[string]AuthCode
}

var _ AuthCodeStore = (*memoryAuthCodeStore)(nil)

// NewMemoryAuthCodeStore constructs an AuthCodeStore backed by an
// in-process map. It is ProviderConfig.AuthCodes's default because it is
// sufficient for a single-replica deployment. A multi-replica deployment
// MUST use NewPostgresAuthCodeStore instead — see that constructor's doc
// and README.md for the consequence of getting this wrong.
//
// Expired entries are pruned lazily, on every Save and Consume call (under
// the same mutex as the operation itself), so this store cannot grow
// unbounded without needing a background goroutine of its own.
func NewMemoryAuthCodeStore() AuthCodeStore {
	return &memoryAuthCodeStore{codes: make(map[string]AuthCode)}
}

// pruneLocked deletes every expired entry from m.codes. Callers must hold
// m.mu.
func (m *memoryAuthCodeStore) pruneLocked() {
	now := time.Now()
	for hash, code := range m.codes {
		if now.After(code.ExpiresAt) {
			delete(m.codes, hash)
		}
	}
}

// Save persists code, keyed by code.Code — the SHA-256 hash the caller
// (authorize.go) already computed from the raw code via hashToken; this
// store never sees the raw value.
func (m *memoryAuthCodeStore) Save(ctx context.Context, code AuthCode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	m.codes[code.Code] = code
	return nil
}

// Consume hashes rawCode (mirroring credential.go's Verify) and atomically
// looks up and deletes the matching entry under m.mu, so a second Consume
// of the same code — even from a concurrent goroutine — always misses.
func (m *memoryAuthCodeStore) Consume(ctx context.Context, rawCode string) (AuthCode, error) {
	hash := hashToken(rawCode)

	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()

	code, ok := m.codes[hash]
	if !ok {
		return AuthCode{}, ErrAuthCodeNotFound
	}
	delete(m.codes, hash)
	return code, nil
}

// AuthCodeStoreConfig configures NewPostgresAuthCodeStore.
type AuthCodeStoreConfig struct {
	// Pool is the PostgreSQL connection pool. Required.
	Pool *pgxpool.Pool

	// TableName is the unqualified name of the consuming domain's
	// mcp_auth_code-shaped table (see README.md's schema contract).
	// Defaults to "mcp_auth_code". Unqualified for the same search_path
	// reason StoreConfig.TableName is (credential.go).
	TableName string
}

// pgxAuthCodeStore is the pgx-backed, multi-replica-safe AuthCodeStore
// implementation.
type pgxAuthCodeStore struct {
	cfg AuthCodeStoreConfig
}

var _ AuthCodeStore = (*pgxAuthCodeStore)(nil)

// NewPostgresAuthCodeStore constructs an AuthCodeStore backed by cfg.Pool
// and cfg.TableName (defaulted to "mcp_auth_code" when left empty). Use
// this instead of NewMemoryAuthCodeStore for any multi-replica deployment
// — see that constructor's doc.
//
// cfg.TableName is validated as a safe SQL identifier (it is interpolated
// directly into generated SQL, mirroring credential.go's validateIdentifier
// requirement) and the configured table is preflighted with a minimal
// query (same shape as pgxClientRegistry.probeTable) so a missing
// migration fails loudly, naming the table, at construction time rather
// than on the first real request.
func NewPostgresAuthCodeStore(ctx context.Context, cfg AuthCodeStoreConfig) (AuthCodeStore, error) {
	if cfg.Pool == nil {
		return nil, errors.New("mcpauth: AuthCodeStoreConfig.Pool is required")
	}
	if cfg.TableName == "" {
		cfg.TableName = defaultAuthCodeTableName
	}
	if err := validateIdentifier(cfg.TableName, "TableName"); err != nil {
		return nil, err
	}

	s := &pgxAuthCodeStore{cfg: cfg}

	if err := s.probeTable(ctx); err != nil {
		return nil, fmt.Errorf(
			"mcpauth: auth code table preflight failed for table %q — apply your domain's mcp_auth_code migration (see libs/go/mcpauth/README.md schema contract) before calling NewPostgresAuthCodeStore: %w",
			cfg.TableName, err,
		)
	}

	return s, nil
}

// probeTable runs a minimal query against the configured table to confirm
// it exists and is accessible, using the unqualified table name so it
// exercises the same search_path resolution every runtime query in this
// file uses (mirrors pgxClientRegistry.probeTable in clients.go).
func (s *pgxAuthCodeStore) probeTable(ctx context.Context) error {
	_, err := s.cfg.Pool.Exec(ctx, "SELECT 1 FROM "+s.cfg.TableName+" LIMIT 0")
	return err
}

// pruneExpired best-effort deletes every row past its expires_at. Called
// opportunistically from Save and Consume — "periodic" in the sense that it
// runs on every real access this store sees — rather than from a dedicated
// background goroutine, so this store's lifetime never needs to be managed
// independently of cfg.Pool's (mirrors this package's existing convention
// of not owning any goroutine or Close() lifecycle — see clients.go/
// credential.go). A failure here is logged nowhere (this package has no
// logger) and never fails the caller's Save/Consume — pruning is
// best-effort cleanup, not correctness-critical: Consume's DELETE ...
// RETURNING already treats an expired-but-not-yet-pruned row as
// consumable, and the /token handler (token.go) independently rejects it
// as expired.
func (s *pgxAuthCodeStore) pruneExpired(ctx context.Context) {
	_, _ = s.cfg.Pool.Exec(ctx, "DELETE FROM "+s.cfg.TableName+" WHERE expires_at < NOW()")
}

// Save persists code, keyed by code.Code — the SHA-256 hash the caller
// (authorize.go) already computed from the raw code via hashToken; this
// store never sees the raw value.
func (s *pgxAuthCodeStore) Save(ctx context.Context, code AuthCode) error {
	s.pruneExpired(ctx)

	query := fmt.Sprintf(`
		INSERT INTO %s (code_hash, client_id, redirect_uri, identity, code_challenge, code_challenge_method, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, s.cfg.TableName)

	_, err := s.cfg.Pool.Exec(ctx, query,
		code.Code, code.ClientID, code.RedirectURI, code.Identity,
		code.CodeChallenge, code.CodeChallengeMethod, code.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("mcpauth: insert auth code: %w", err)
	}
	return nil
}

// Consume hashes rawCode (mirroring credential.go's Verify) and atomically
// deletes the matching row, returning its data. DELETE ... RETURNING is
// Postgres's own atomic single-use guarantee — a second concurrent Consume
// of the same code races on the same row lock and the loser's DELETE
// matches zero rows, so single-use is enforced by Postgres itself, not an
// in-process lock (the guarantee memoryAuthCodeStore cannot give across
// replicas).
func (s *pgxAuthCodeStore) Consume(ctx context.Context, rawCode string) (AuthCode, error) {
	s.pruneExpired(ctx)

	query := fmt.Sprintf(`
		DELETE FROM %s
		WHERE code_hash = $1
		RETURNING client_id, redirect_uri, identity, code_challenge, code_challenge_method, expires_at
	`, s.cfg.TableName)

	hash := hashToken(rawCode)
	var code AuthCode
	err := s.cfg.Pool.QueryRow(ctx, query, hash).Scan(
		&code.ClientID, &code.RedirectURI, &code.Identity,
		&code.CodeChallenge, &code.CodeChallengeMethod, &code.ExpiresAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AuthCode{}, ErrAuthCodeNotFound
		}
		return AuthCode{}, fmt.Errorf("mcpauth: consume auth code: %w", err)
	}
	code.Code = hash
	return code, nil
}
