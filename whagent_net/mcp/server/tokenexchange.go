// RFC 8693 token exchange (issue #2249's Implementation phase, FR9
// (C27)): mcp's own confidential Keycloak client exchanges an already
// -decoded (iss, sub) identity -- resolved from an mcpauth opaque
// credential by auth.go's dual-path verifier, never a raw token this
// package receives from the caller -- for a short-lived, real
// Keycloak-signed JWT asserting that subject. auth.go's AuthMiddleware is
// the only caller of Exchange; it places the result on the outgoing
// context via grpcauth.WithUserToken, the same mechanism the manual-token
// path already uses.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/whale-net/everything/libs/go/logging"
)

// logger is this package's shared logger (auth.go and this file both log
// against it). Per AGENTS.md's logging-level convention: a failed
// exchange is ERROR (Exchange, below); a cache miss/refresh is expected
// steady-state behavior, never logged as a WARNING; a rejected credential
// is expected control flow (auth.go), never logged as an ERROR.
var logger = logging.Get("whagent_net/mcp/server")

// TokenExchangeConfig is mcp's own confidential-client settings for the
// RFC 8693 Keycloak token exchange (NFR8, FR9's OAuth2 path) -- a
// distinct Keycloak client, with token-exchange/impersonation rights,
// from the WHAGENT_OIDC_CLIENT_ID/WHAGENT_OIDC_CLIENT_SECRET pair `ui`
// and `mcp` already read for the manual-token recipe (../ENV.md
// "Identity"): that client only ever verifies or forwards a token it did
// not mint itself, while this one actively mints a new Keycloak-signed
// JWT on an operator's behalf, so keeping the two separate keeps this
// credential's blast radius (NFR8) legible and independently rotatable.
// Read from the environment (WHAGENT_MCP_KEYCLOAK_CLIENT_ID/
// WHAGENT_MCP_KEYCLOAK_CLIENT_SECRET/WHAGENT_MCP_KEYCLOAK_TOKEN_URL,
// ../ENV.md "`mcp` server") and provisioned as a Kubernetes secret --
// never checked in, never logged, never echoed in an error (NFR8; see
// Exchange's doc comment for the exact contract).
type TokenExchangeConfig struct {
	// ClientID is mcp's own confidential client id in Keycloak.
	ClientID string

	// ClientSecret authenticates ClientID against TokenEndpoint. Never
	// logged and never included in any error message this package
	// returns (NFR8).
	ClientSecret string

	// TokenEndpoint is Keycloak's token endpoint URL for the realm
	// WHAGENT_OIDC_ISSUER names, e.g.
	// "https://keycloak.example.com/realms/whagent/protocol/openid-connect/token".
	TokenEndpoint string
}

// Enabled reports whether cfg carries everything Exchange needs to ever
// succeed. A zero-value TokenExchangeConfig -- the default today, since
// FR9's OAuth2 path is additive and opt-in -- is not itself an error:
// NewKeycloakExchanger always constructs successfully regardless of
// Enabled(). main.go's initializeTokenExchange is what fails startup
// loudly (NFR8) if it would otherwise wire the OAuth2 credential path
// (a non-nil mcpauth.CredentialStore) on top of a disabled cfg -- never
// this type, and never this file's Exchange, which stays a plain
// disabled-error return so a construction-time bug elsewhere fails loudly
// too rather than silently forwarding an empty credential.
func (cfg TokenExchangeConfig) Enabled() bool {
	return cfg.ClientID != "" && cfg.ClientSecret != "" && cfg.TokenEndpoint != ""
}

// Exchanger exchanges an already-decoded Keycloak (iss, sub) pair --
// never the raw mcpidentity-encoded string, and never a value
// mcpauth.CredentialStore.Verify returns directly (NFR7: this package
// introduces no local user table, no MCP-only identity column) -- for a
// short-lived, real Keycloak-signed JWT asserting that subject.
// auth.go's AuthMiddleware calls Exchange from the request path once it
// has decoded an opaque mcpauth credential's identity via
// whagent_net/mcpidentity.Decode, and places the result on the outgoing
// context via grpcauth.WithUserToken -- the same mechanism the
// manual-token path already uses (auth.go's AuthMiddleware).
type Exchanger interface {
	Exchange(ctx context.Context, iss, sub string) (jwt string, err error)
}

// errExchangeDisabled is returned by KeycloakExchanger.Exchange when
// cfg.Enabled() is false. main.go's initializeTokenExchange is the actual
// NFR8 enforcement point (it refuses to start if the OAuth2 credential
// path would be reachable with no exchange config at all) -- this is a
// defensive fallback for the case a future caller constructs
// KeycloakExchanger directly and calls Exchange without going through
// that guard.
var errExchangeDisabled = errors.New("mcp: RFC 8693 token exchange is not configured (WHAGENT_MCP_KEYCLOAK_CLIENT_ID/_CLIENT_SECRET/_TOKEN_URL)")

// errExchangeFailed is the single fixed error Exchange returns for every
// HTTP/decode failure talking to cfg.TokenEndpoint. Its message never
// varies with, and never embeds, the request that produced it -- in
// particular never cfg.ClientSecret or any request body -- so it is safe
// to return all the way up to a tool call's error result (NFR8). The
// underlying detail is logged at ERROR (see Exchange) for an operator to
// actually diagnose a failure; the caller only ever sees this sentinel.
var errExchangeFailed = errors.New("mcp: RFC 8693 token exchange failed")

// grantType is the RFC 8693 grant_type value naming a token exchange
// request.
const grantType = "urn:ietf:params:oauth:grant-type:token-exchange"

// requestedTokenType is RFC 8693's optional requested_token_type
// parameter: mcp always asks Keycloak for an OAuth2 access token (the
// same shape a manual-token operator's own Keycloak access token has),
// never an ID token or SAML assertion.
const requestedTokenType = "urn:ietf:params:oauth:token-type:access_token"

// cacheExpiryMargin is how far ahead of an exchanged token's actual
// expiry Exchange treats it as stale and re-exchanges, so a cached token
// handed to a caller is never seconds away from expiring by the time it
// reaches `api`.
const cacheExpiryMargin = 10 * time.Second

// maxTokenResponseBytes bounds how much of cfg.TokenEndpoint's response
// body Exchange reads, so a misbehaving or compromised token endpoint
// cannot exhaust `mcp`'s memory via an oversized response.
const maxTokenResponseBytes = 1 << 20 // 1 MiB

// identityKey is the in-memory cache key for one resolved Keycloak
// identity. Deliberately not mcpidentity.Encode's packed-string format --
// that packing exists solely so mcpauth.CredentialStore's persisted
// Identity column can hold an (iss, sub) pair as a single opaque string
// (NFR7); this key never leaves process memory and is never persisted,
// so there is no format-drift risk in using a plain struct instead.
type identityKey struct {
	iss string
	sub string
}

// cachedExchange is one identityKey's cached exchange result.
type cachedExchange struct {
	jwt       string
	expiresAt time.Time
}

// KeycloakExchanger is the Exchanger implementation auth.go's dual-path
// verifier calls: cfg names the confidential client and token endpoint
// the real RFC 8693 request below uses. Successful exchanges are cached
// per identityKey until shortly before the exchanged token's own expiry
// (cacheExpiryMargin) -- see Exchange -- and the cache is pruned of
// expired entries on every write, so its size is bounded by the number of
// distinct identities with a currently-live cached token, never by total
// calls made. Nothing here is ever persisted to disk or a database.
type KeycloakExchanger struct {
	cfg TokenExchangeConfig

	httpClient *http.Client

	mu    sync.Mutex
	cache map[identityKey]cachedExchange
	now   func() time.Time // overridden by tests; time.Now otherwise
}

var _ Exchanger = (*KeycloakExchanger)(nil)

// ExchangerOption configures NewKeycloakExchanger.
type ExchangerOption func(*KeycloakExchanger)

// WithHTTPClient overrides the *http.Client Exchange uses to reach
// cfg.TokenEndpoint. Exported so a test can point Exchange at a fake
// Keycloak token endpoint's httptest.Server without any production code
// path having to accept a custom client.
func WithHTTPClient(c *http.Client) ExchangerOption {
	return func(e *KeycloakExchanger) { e.httpClient = c }
}

// NewKeycloakExchanger constructs a KeycloakExchanger. cfg.Enabled()
// reports whether Exchange can ever succeed; constructing one with a
// disabled cfg is not itself an error (see TokenExchangeConfig.Enabled's
// doc comment).
func NewKeycloakExchanger(cfg TokenExchangeConfig, opts ...ExchangerOption) *KeycloakExchanger {
	e := &KeycloakExchanger{
		cfg:   cfg,
		cache: make(map[identityKey]cachedExchange),
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// client returns the *http.Client Exchange uses: e.httpClient if
// WithHTTPClient set one, otherwise http.DefaultClient.
func (e *KeycloakExchanger) client() *http.Client {
	if e.httpClient != nil {
		return e.httpClient
	}
	return http.DefaultClient
}

// clock returns e.now() if a test overrode it, otherwise time.Now().
func (e *KeycloakExchanger) clock() time.Time {
	if e.now != nil {
		return e.now()
	}
	return time.Now()
}

// Exchange resolves (iss, sub) to a short-lived, real Keycloak-signed
// JWT: a cached, not-yet-stale exchange for this exact identity is
// reused; otherwise Exchange performs the real RFC 8693
// grant_type=urn:ietf:params:oauth:grant-type:token-exchange request
// against cfg.TokenEndpoint (requestExchange) and, if the response
// carries a positive expires_in, caches the result until
// cacheExpiryMargin before that expiry.
//
// Every failure -- cfg disabled, an HTTP-level failure, a non-2xx
// response, or a malformed response body -- is logged at ERROR with the
// underlying detail (never including cfg.ClientSecret, which never
// appears in any request detail logged here) and reported to the caller
// as the single fixed errExchangeFailed (or errExchangeDisabled), so
// nothing this method returns can ever leak the client secret into a log
// line or a tool call's error result (NFR8).
func (e *KeycloakExchanger) Exchange(ctx context.Context, iss, sub string) (string, error) {
	if !e.cfg.Enabled() {
		return "", errExchangeDisabled
	}

	key := identityKey{iss: iss, sub: sub}
	if jwt, ok := e.cachedToken(key); ok {
		return jwt, nil
	}

	jwt, expiresIn, err := e.requestExchange(ctx, sub)
	if err != nil {
		logger.ErrorContext(ctx, "mcp: RFC 8693 token exchange failed", "error", err)
		return "", errExchangeFailed
	}

	if expiresIn > cacheExpiryMargin {
		e.storeCachedToken(key, jwt, expiresIn)
	}

	return jwt, nil
}

// cachedToken returns the cached JWT for key if one exists and has not
// yet crossed cacheExpiryMargin before its own expiry. Also opportunistically
// prunes every expired entry (not just key's), which is what keeps the
// cache's size bounded to currently-live entries (see KeycloakExchanger's
// doc comment).
func (e *KeycloakExchanger) cachedToken(key identityKey) (string, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	now := e.clock()
	e.pruneExpiredLocked(now)

	entry, ok := e.cache[key]
	if !ok {
		return "", false
	}
	return entry.jwt, true
}

// storeCachedToken caches jwt for key, valid until ttl minus
// cacheExpiryMargin from now.
func (e *KeycloakExchanger) storeCachedToken(key identityKey, jwt string, ttl time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.cache == nil {
		e.cache = make(map[identityKey]cachedExchange)
	}
	e.cache[key] = cachedExchange{
		jwt:       jwt,
		expiresAt: e.clock().Add(ttl - cacheExpiryMargin),
	}
}

// pruneExpiredLocked removes every cache entry whose expiresAt is at or
// before now. Callers must hold e.mu.
func (e *KeycloakExchanger) pruneExpiredLocked(now time.Time) {
	for k, v := range e.cache {
		if !now.Before(v.expiresAt) {
			delete(e.cache, k)
		}
	}
}

// tokenExchangeResponse is the subset of Keycloak's OAuth2 token response
// this package reads. access_token is the exchanged Keycloak-signed JWT;
// expires_in (seconds) drives storeCachedToken's TTL -- a response with
// no positive expires_in is still a successful exchange, it is simply
// never cached (Exchange re-exchanges on every call for that identity
// rather than risk caching an unbounded-lifetime token).
type tokenExchangeResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int64  `json:"expires_in"`
}

// requestExchange performs the real RFC 8693 token-exchange HTTP request
// against e.cfg.TokenEndpoint: mcp authenticates as its own confidential
// client (client_id/client_secret) and asks Keycloak to mint a token for
// requested_subject=sub -- mcp never holds, and RFC 8693's subject_token
// therefore never carries, a token belonging to sub; the confidential
// client's own token-exchange/impersonation grant on the Keycloak side
// (libs/go/grpcauth/KEYCLOAK.md's token-exchange setup section) is what
// authorizes minting a token for a subject mcp does not otherwise hold
// credentials for.
func (e *KeycloakExchanger) requestExchange(ctx context.Context, sub string) (jwt string, expiresIn time.Duration, err error) {
	form := url.Values{
		"grant_type":           {grantType},
		"client_id":            {e.cfg.ClientID},
		"client_secret":        {e.cfg.ClientSecret},
		"requested_subject":    {sub},
		"requested_token_type": {requestedTokenType},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.cfg.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, fmt.Errorf("build token-exchange request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := e.client().Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("token-exchange request to token endpoint: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenResponseBytes))
	if err != nil {
		return "", 0, fmt.Errorf("read token-exchange response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		// body is Keycloak's own OAuth2 error response (e.g.
		// {"error":"invalid_client"}); it never echoes back anything
		// mcp itself considers secret, so it is safe to fold into this
		// wrapped error, which only ever reaches the ERROR log line
		// above -- never the caller (Exchange returns errExchangeFailed,
		// not this wrapped error, to whoever called it).
		return "", 0, fmt.Errorf("token endpoint returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed tokenExchangeResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", 0, fmt.Errorf("decode token-exchange response: %w", err)
	}
	if parsed.AccessToken == "" {
		return "", 0, errors.New("token-exchange response carried no access_token")
	}

	return parsed.AccessToken, time.Duration(parsed.ExpiresIn) * time.Second, nil
}
