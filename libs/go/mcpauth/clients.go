package mcpauth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
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
	// and issuing no client_secret before this is called.
	Register(ctx context.Context, meta oauthex.ClientRegistrationMetadata) (OAuthClient, error)

	// Get returns the previously registered client for clientID, or
	// ErrClientNotFound if none exists.
	Get(ctx context.Context, clientID string) (OAuthClient, error)
}

// generateClientID returns a high-entropy (crypto/rand), hex-encoded
// client_id. Distinct from generateToken (credential.go) only in byte
// length — client_id is a public identifier (echoed back in every
// subsequent /authorize and /token request), not a secret, but is still
// generated with crypto/rand so it is not guessable/enumerable.
func generateClientID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

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
	clientID, err := generateClientID()
	if err != nil {
		return OAuthClient{}, fmt.Errorf("mcpauth: generate client_id: %w", err)
	}

	client := OAuthClient{
		ClientID:     clientID,
		RedirectURIs: meta.RedirectURIs,
		Metadata:     meta,
		CreatedAt:    time.Now().UTC(),
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.clients[clientID] = client
	return client, nil
}

func (m *memoryClientRegistry) Get(ctx context.Context, clientID string) (OAuthClient, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	client, ok := m.clients[clientID]
	if !ok {
		return OAuthClient{}, ErrClientNotFound
	}
	return client, nil
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
// cfg.TableName is validated as a safe SQL identifier (it is interpolated
// directly into generated SQL, mirroring credential.go's
// validateIdentifier requirement) and the configured table is preflighted
// with a minimal query (same shape as pgxCredentialStore.probeTable) so a
// missing migration fails loudly, naming the table, at construction time
// rather than on the first real request.
func NewPostgresClientRegistry(ctx context.Context, cfg ClientRegistryConfig) (ClientRegistry, error) {
	if cfg.Pool == nil {
		return nil, errors.New("mcpauth: ClientRegistryConfig.Pool is required")
	}
	if cfg.TableName == "" {
		cfg.TableName = defaultClientTableName
	}
	if err := validateIdentifier(cfg.TableName, "TableName"); err != nil {
		return nil, err
	}

	r := &pgxClientRegistry{cfg: cfg}

	if err := r.probeTable(ctx); err != nil {
		return nil, fmt.Errorf(
			"mcpauth: oauth client table preflight failed for table %q — apply your domain's mcp_oauth_client migration (see libs/go/mcpauth/README.md schema contract) before calling NewPostgresClientRegistry: %w",
			cfg.TableName, err,
		)
	}

	return r, nil
}

// probeTable runs a minimal query against the configured table to confirm
// it exists and is accessible, using the unqualified table name so it
// exercises the same search_path resolution every runtime query in this
// file uses (mirrors pgxCredentialStore.probeTable in credential.go).
func (r *pgxClientRegistry) probeTable(ctx context.Context) error {
	_, err := r.cfg.Pool.Exec(ctx, "SELECT 1 FROM "+r.cfg.TableName+" LIMIT 0")
	return err
}

func (r *pgxClientRegistry) Register(ctx context.Context, meta oauthex.ClientRegistrationMetadata) (OAuthClient, error) {
	clientID, err := generateClientID()
	if err != nil {
		return OAuthClient{}, fmt.Errorf("mcpauth: generate client_id: %w", err)
	}

	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return OAuthClient{}, fmt.Errorf("mcpauth: marshal client metadata: %w", err)
	}

	query := fmt.Sprintf(`
		INSERT INTO %s (client_id, metadata)
		VALUES ($1, $2)
		RETURNING created_at
	`, r.cfg.TableName)

	var createdAt time.Time
	if err := r.cfg.Pool.QueryRow(ctx, query, clientID, metaJSON).Scan(&createdAt); err != nil {
		return OAuthClient{}, fmt.Errorf("mcpauth: insert oauth client: %w", err)
	}

	return OAuthClient{
		ClientID:     clientID,
		RedirectURIs: meta.RedirectURIs,
		Metadata:     meta,
		CreatedAt:    createdAt,
	}, nil
}

func (r *pgxClientRegistry) Get(ctx context.Context, clientID string) (OAuthClient, error) {
	query := fmt.Sprintf(`
		SELECT client_id, metadata, created_at
		FROM %s
		WHERE client_id = $1
	`, r.cfg.TableName)

	var id string
	var metaJSON []byte
	var createdAt time.Time
	err := r.cfg.Pool.QueryRow(ctx, query, clientID).Scan(&id, &metaJSON, &createdAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return OAuthClient{}, ErrClientNotFound
		}
		return OAuthClient{}, fmt.Errorf("mcpauth: get oauth client: %w", err)
	}

	var meta oauthex.ClientRegistrationMetadata
	if err := json.Unmarshal(metaJSON, &meta); err != nil {
		return OAuthClient{}, fmt.Errorf("mcpauth: unmarshal oauth client metadata: %w", err)
	}

	return OAuthClient{
		ClientID:     id,
		RedirectURIs: meta.RedirectURIs,
		Metadata:     meta,
		CreatedAt:    createdAt,
	}, nil
}

// disallowedRedirectSchemes are schemes that must never be accepted as a
// redirect_uri regardless of host, because they let a malicious client
// registration turn /authorize's eventual redirect into script execution
// or a data exfiltration sink rather than a navigation.
var disallowedRedirectSchemes = map[string]bool{
	"javascript": true,
	"data":       true,
	"vbscript":   true,
}

// validateRedirectURI enforces the redirect_uri allow-list RFC 7591
// registration must apply: an absolute URL that is either https, http on
// loopback (native/CLI MCP clients run a local callback listener, e.g.
// http://127.0.0.1:PORT/callback), or a private-use custom scheme (native
// app deep links, e.g. "com.example.app:/callback") — and never
// javascript:/data:/vbscript:, which could turn a redirect into script
// execution. The returned error's text is written into a caller-composed
// RFC 7591 error body — see writeClientRegistrationError — never a raw Go
// stdlib error (net/url's parse errors are not user-facing-safe), so this
// function never wraps one.
func validateRedirectURI(raw string) error {
	if raw == "" {
		return errors.New("redirect_uris must not contain an empty URI")
	}

	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() {
		return fmt.Errorf("redirect_uris contains a URI that is not a valid absolute URL: %q", raw)
	}

	scheme := strings.ToLower(u.Scheme)
	if disallowedRedirectSchemes[scheme] {
		return fmt.Errorf("redirect_uris contains a disallowed scheme %q: %q", scheme, raw)
	}

	switch scheme {
	case "https":
		if u.Host == "" {
			return fmt.Errorf("redirect_uris contains an https URI with no host: %q", raw)
		}
	case "http":
		if !isLoopbackHost(u.Hostname()) {
			return fmt.Errorf("redirect_uris contains a non-loopback http URI (use https, or http on loopback): %q", raw)
		}
	default:
		// Private-use/custom scheme (native app deep link, RFC 8252 §7.1)
		// — allowed. url.Parse already guarantees a non-empty scheme for
		// an absolute URL, so there is nothing further to check.
	}

	return nil
}

// writeClientRegistrationError writes an RFC 7591 §3.2.2 error body
// ({"error": code, "error_description": description}) at the given HTTP
// status. code must be one of RFC 7591's registered error codes
// ("invalid_redirect_uri", "invalid_client_metadata", ...); description is
// a fixed, human-authored string — callers must never pass a raw Go error
// (err.Error()) through here, since that could leak implementation detail
// a client should not see and is not guaranteed to be free of the error
// text this function exists to avoid.
func writeClientRegistrationError(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(oauthex.ClientRegistrationError{
		ErrorCode:        code,
		ErrorDescription: description,
	})
}

// handleRegister serves RFC 7591 dynamic client registration (POST
// /register). Registration is intentionally unauthenticated — see
// README.md's "Registration is open" note — a client registration confers
// no access on its own; access still requires a signed-in caller
// completing /authorize (#1642).
//
// On success: mints a random client_id (generateClientID), issues no
// client_secret, forces token_endpoint_auth_method to "none" (mcpauth
// issues only public PKCE clients), and responds 201 with an RFC 7591
// ClientRegistrationResponse. On bad input: responds 400 with an RFC 7591
// ClientRegistrationError body — "invalid_redirect_uri" for a redirect URI
// that fails validateRedirectURI, "invalid_client_metadata" for anything
// else (malformed JSON, no redirect_uris at all) — never a raw Go error
// string.
func (p *Provider) handleRegister(w http.ResponseWriter, r *http.Request) {
	var meta oauthex.ClientRegistrationMetadata
	if err := json.NewDecoder(r.Body).Decode(&meta); err != nil {
		writeClientRegistrationError(w, http.StatusBadRequest, "invalid_client_metadata", "request body is not valid JSON")
		return
	}

	if len(meta.RedirectURIs) == 0 {
		writeClientRegistrationError(w, http.StatusBadRequest, "invalid_client_metadata", "redirect_uris must contain at least one URI")
		return
	}
	for _, redirect := range meta.RedirectURIs {
		if err := validateRedirectURI(redirect); err != nil {
			writeClientRegistrationError(w, http.StatusBadRequest, "invalid_redirect_uri", err.Error())
			return
		}
	}

	// Public PKCE clients only: mcpauth issues no client secret, so every
	// registered client is forced to "none" regardless of what the
	// request asked for.
	meta.TokenEndpointAuthMethod = "none"

	client, err := p.cfg.Clients.Register(r.Context(), meta)
	if err != nil {
		writeClientRegistrationError(w, http.StatusInternalServerError, "invalid_client_metadata", "failed to register client")
		return
	}

	resp := oauthex.ClientRegistrationResponse{
		ClientRegistrationMetadata: client.Metadata,
		ClientID:                   client.ClientID,
		ClientIDIssuedAt:           client.CreatedAt,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(&resp)
}
