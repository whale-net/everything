package mcpauth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// OAuthClient is a client dynamically registered against this provider via
// RFC 7591 (see Provider.handleRegister). mcpauth issues no client secret
// (public PKCE clients only) — see NewProvider's TokenEndpointAuthMethod
// note.
type OAuthClient struct {
	ClientID     string
	RedirectURIs []string
	Metadata     oauthex.ClientRegistrationMetadata
	CreatedAt    time.Time
}

// ErrClientNotFound is returned by ClientRegistry.Get for a client_id that
// was never registered (or was registered against a different, isolated
// registry instance — see NewMemoryClientRegistry's doc). It is a
// distinguishable sentinel, not an opaque error, because callers (the
// #1642 `/authorize`/`/token` handlers) need to tell "unknown client" apart
// from a registry-level failure.
var ErrClientNotFound = errors.New("mcpauth: unknown client_id")

// ClientRegistry stores OAuth2 clients dynamically registered via RFC 7591.
type ClientRegistry interface {
	// Register mints a new client_id for meta and persists it. It never
	// mutates meta's TokenEndpointAuthMethod expectation itself — the
	// caller (Provider.handleRegister) is responsible for forcing "none"
	// and issuing no client_secret before this is called (Implementation
	// phase of #1641).
	Register(ctx context.Context, meta oauthex.ClientRegistrationMetadata) (OAuthClient, error)

	// Get returns the previously registered client for clientID, or
	// ErrClientNotFound if none exists.
	Get(ctx context.Context, clientID string) (OAuthClient, error)
}

// errClientsNotImplemented is returned by every ClientRegistry method stub
// below until the Implementation phase of #1641 lands the real logic.
// Scaffold exists to settle this package's public shape (ClientRegistry,
// OAuthClient, both constructors, and Provider.handleRegister's routing),
// not the method bodies.
var errClientsNotImplemented = errors.New("mcpauth: not implemented yet (scaffold phase, see issue #1641)")

// memoryClientRegistry is the in-process ClientRegistry implementation:
// enough for a single-replica server, but each process has its own
// isolated map — a client that registers against replica A is unknown to
// replica B. See NewPostgresClientRegistry for the multi-replica-safe
// alternative; the README states this trade-off plainly.
type memoryClientRegistry struct {
	mu      sync.Mutex
	clients map[string]OAuthClient
}

var _ ClientRegistry = (*memoryClientRegistry)(nil)

// NewMemoryClientRegistry constructs a ClientRegistry backed by an
// in-process map. It is the default ClientRegistry (see
// ProviderConfig.Clients) because it is sufficient for a single-replica
// deployment. A multi-replica deployment MUST use
// NewPostgresClientRegistry instead — see that constructor's doc and
// README.md for the consequence of getting this wrong (a client that
// registers against replica A is rejected at replica B).
func NewMemoryClientRegistry() ClientRegistry {
	return &memoryClientRegistry{clients: make(map[string]OAuthClient)}
}

func (m *memoryClientRegistry) Register(ctx context.Context, meta oauthex.ClientRegistrationMetadata) (OAuthClient, error) {
	return OAuthClient{}, errClientsNotImplemented
}

func (m *memoryClientRegistry) Get(ctx context.Context, clientID string) (OAuthClient, error) {
	return OAuthClient{}, errClientsNotImplemented
}

// defaultClientTableName is ClientRegistryConfig.TableName's default,
// mirroring StoreConfig's defaultTableName convention in credential.go.
const defaultClientTableName = "mcp_oauth_client"

// ClientRegistryConfig configures NewPostgresClientRegistry.
type ClientRegistryConfig struct {
	// Pool is the PostgreSQL connection pool. Required.
	Pool *pgxpool.Pool

	// TableName is the unqualified name of the consuming domain's
	// mcp_oauth_client-shaped table (see README.md's schema contract).
	// Defaults to "mcp_oauth_client". Unqualified for the same
	// search_path reason StoreConfig.TableName is (credential.go).
	TableName string
}

// pgxClientRegistry is the pgx-backed, multi-replica-safe ClientRegistry
// implementation.
type pgxClientRegistry struct {
	cfg ClientRegistryConfig
}

var _ ClientRegistry = (*pgxClientRegistry)(nil)

// NewPostgresClientRegistry constructs a ClientRegistry backed by cfg.Pool
// and cfg.TableName (defaulted to "mcp_oauth_client" when left empty). Use
// this instead of NewMemoryClientRegistry for any multi-replica deployment
// — see that constructor's doc.
//
// Scaffold note: this constructor currently only checks cfg.Pool is
// non-nil and applies the TableName default — mirroring
// NewCredentialStore's scaffold in credential.go. The Implementation phase
// of #1641 adds identifier validation and the boot-time table preflight
// probe (same shape as NewCredentialStore's probeTable) that fails loudly,
// naming the table, if the consuming domain's migration has not been
// applied.
func NewPostgresClientRegistry(ctx context.Context, cfg ClientRegistryConfig) (ClientRegistry, error) {
	if cfg.Pool == nil {
		return nil, errors.New("mcpauth: ClientRegistryConfig.Pool is required")
	}
	if cfg.TableName == "" {
		cfg.TableName = defaultClientTableName
	}

	return &pgxClientRegistry{cfg: cfg}, nil
}

func (r *pgxClientRegistry) Register(ctx context.Context, meta oauthex.ClientRegistrationMetadata) (OAuthClient, error) {
	return OAuthClient{}, errClientsNotImplemented
}

func (r *pgxClientRegistry) Get(ctx context.Context, clientID string) (OAuthClient, error) {
	return OAuthClient{}, errClientsNotImplemented
}

// handleRegister serves RFC 7591 dynamic client registration (POST
// /register). Registration is intentionally unauthenticated — see
// README.md's "Registration is open" note and the Implementation section
// of #1641 for the full validation/response-shape contract (redirect_uri
// validation, minted client_id, no client_secret, forced
// token_endpoint_auth_method: "none", RFC 7591 error bodies on bad input).
//
// Scaffold note: stubbed to 501 pending that Implementation phase.
func (p *Provider) handleRegister(w http.ResponseWriter, r *http.Request) {
	notImplementedHandler(w, r)
}
