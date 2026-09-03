package mcpauth

import (
	"context"
	"errors"
	"sync"
	"time"

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

// errAuthCodesNotImplemented is returned by every AuthCodeStore method stub
// below until the Implementation phase of #1642 lands the real logic.
// Scaffold exists to settle this package's public shape (AuthCodeStore,
// AuthCode, both constructors, and ProviderConfig's AuthCodes/AuthCodeTTL
// fields), not the method bodies.
var errAuthCodesNotImplemented = errors.New("mcpauth: not implemented yet (scaffold phase, see issue #1642)")

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
// Scaffold note: pruning of expired entries (so this store cannot grow
// unbounded) is not yet implemented — see #1642's Implementation section.
func NewMemoryAuthCodeStore() AuthCodeStore {
	return &memoryAuthCodeStore{codes: make(map[string]AuthCode)}
}

func (m *memoryAuthCodeStore) Save(ctx context.Context, code AuthCode) error {
	return errAuthCodesNotImplemented
}

func (m *memoryAuthCodeStore) Consume(ctx context.Context, rawCode string) (AuthCode, error) {
	return AuthCode{}, errAuthCodesNotImplemented
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
// Scaffold note: this constructor currently only checks cfg.Pool is
// non-nil and applies the TableName default — mirroring
// NewPostgresClientRegistry's scaffold in clients.go. The Implementation
// phase of #1642 adds the boot-time table preflight probe (same shape as
// pgxClientRegistry.probeTable) that fails loudly, naming the table, if
// the consuming domain's migration has not been applied, plus the
// periodic-delete pruning of expired codes described in README.md.
func NewPostgresAuthCodeStore(ctx context.Context, cfg AuthCodeStoreConfig) (AuthCodeStore, error) {
	if cfg.Pool == nil {
		return nil, errors.New("mcpauth: AuthCodeStoreConfig.Pool is required")
	}
	if cfg.TableName == "" {
		cfg.TableName = defaultAuthCodeTableName
	}

	return &pgxAuthCodeStore{cfg: cfg}, nil
}

func (s *pgxAuthCodeStore) Save(ctx context.Context, code AuthCode) error {
	return errAuthCodesNotImplemented
}

func (s *pgxAuthCodeStore) Consume(ctx context.Context, rawCode string) (AuthCode, error) {
	return AuthCode{}, errAuthCodesNotImplemented
}
