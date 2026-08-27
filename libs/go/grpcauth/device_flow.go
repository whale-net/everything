package grpcauth

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/grpc"
)

// DeviceFlowConfig holds configuration for the OIDC device authorization
// grant (RFC 8628) credential -- the third client-side dial option beside
// NewServiceAccountDialOption and NewUserTokenDialOption. Unlike the
// service-account option, this resolves to a real human principal: the
// resulting access token carries that person's subject and realm roles,
// exactly as if they had logged in through a browser (FR81; A25 explicitly
// rejects a service account standing in for a person here).
type DeviceFlowConfig struct {
	// IssuerURL is the Keycloak realm URL, e.g.
	// https://auth.example.com/realms/whale. The device authorization and
	// token endpoints are discovered from
	// <IssuerURL>/.well-known/openid-configuration -- callers do not supply
	// them directly.
	IssuerURL string
	// ClientID is the Keycloak client id. Must be a PUBLIC client with the
	// device authorization flow enabled -- see KEYCLOAK.md. There is
	// deliberately no ClientSecret field: a public client has none, and a
	// confidential-client secret would make this indistinguishable from the
	// service-account credential A25 rejects.
	ClientID string
	// Scopes requested from the token endpoint. "openid" is always
	// implicitly included by the implementation.
	Scopes []string
	// TokenCachePath is where the refresh token is persisted (mode 0600) so
	// only the first invocation is interactive. Defaults to
	// "<user config dir>/grpcauth/device-flow-token.json" (via
	// os.UserConfigDir) when empty.
	TokenCachePath string
}

// NewDeviceFlowDialOption creates a gRPC DialOption for the OIDC device
// authorization grant (RFC 8628). On first use it prints a verification URI
// and user code and polls the token endpoint until the human approves (or
// the request expires or is denied); the resulting refresh token is cached
// so later invocations refresh silently. Matches the shape of
// NewServiceAccountDialOption and NewUserTokenDialOption: called once at
// startup, no per-RPC configuration.
//
// Implementation note for the next phase: golang.org/x/oauth2 already
// implements the RFC 8628 polling loop via oauth2.Config.DeviceAuth /
// DeviceAccessToken (see deviceauth.go in that module), including interval,
// slow_down, authorization_pending, and expired_token handling -- prefer
// wiring those over hand-rolling the HTTP calls.
func NewDeviceFlowDialOption(ctx context.Context, config DeviceFlowConfig) (grpc.DialOption, error) {
	if config.IssuerURL == "" {
		return nil, fmt.Errorf("IssuerURL is required for OIDC device flow auth")
	}
	if config.ClientID == "" {
		return nil, fmt.Errorf("ClientID is required for OIDC device flow auth")
	}

	cachePath, err := resolveTokenCachePath(config.TokenCachePath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve device flow token cache path: %w", err)
	}

	creds := &deviceFlowCreds{
		config:    config,
		cachePath: cachePath,
	}

	if err := creds.loadCachedToken(); err != nil {
		if err := creds.performDeviceFlow(ctx); err != nil {
			return nil, fmt.Errorf("device authorization grant failed: %w", err)
		}
	}

	return grpc.WithPerRPCCredentials(creds), nil
}

// resolveTokenCachePath returns explicit unmodified when set, else a
// default path under the user's config directory.
func resolveTokenCachePath(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("failed to determine user config directory: %w", err)
	}
	return filepath.Join(configDir, "grpcauth", "device-flow-token.json"), nil
}

// deviceFlowCreds implements credentials.PerRPCCredentials (duck-typed, as
// serviceAccountCreds and userTokenCreds do in credentials.go) by
// forwarding the cached -- and, when needed, refreshed -- access token
// exactly as userTokenCreds forwards a browser-issued token. The server's
// existing verifier accepts either unchanged and resolves them to the same
// subject and realm roles.
type deviceFlowCreds struct {
	config    DeviceFlowConfig
	cachePath string

	mu    sync.RWMutex
	token *oauth2.Token

	// oauthConfig is populated from issuer discovery, in performDeviceFlow.
	oauthConfig *oauth2.Config
}

// deviceEndpoints holds the subset of the issuer's
// .well-known/openid-configuration document this credential needs.
type deviceEndpoints struct {
	DeviceAuthorizationEndpoint string `json:"device_authorization_endpoint"`
	TokenEndpoint               string `json:"token_endpoint"`
}

// cachedToken is the on-disk (mode 0600) representation of the refresh
// token. NFR13: this struct, and everything that reads or writes it, must
// never be logged or embedded in an error.
type cachedToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"`
}

// GetRequestMetadata implements credentials.PerRPCCredentials. It refreshes
// the access token when it is close to expiry, then forwards it exactly as
// userTokenCreds does, so the server-side verifier resolves it to the same
// subject and realm roles as a browser-issued token from the same human.
func (d *deviceFlowCreds) GetRequestMetadata(ctx context.Context, uri ...string) (map[string]string, error) {
	d.mu.RLock()
	token := d.token
	d.mu.RUnlock()

	if token == nil {
		return nil, fmt.Errorf("device flow credential has no token; NewDeviceFlowDialOption should have populated one")
	}

	if token.Expiry.Before(time.Now().Add(time.Minute)) {
		if err := d.refreshToken(ctx); err != nil {
			// NFR13: never include token material in the error.
			return nil, fmt.Errorf("failed to refresh device flow token: %w", err)
		}
	}

	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.token == nil || d.token.AccessToken == "" {
		return nil, fmt.Errorf("device flow credential has no valid access token")
	}
	return map[string]string{
		"authorization": "Bearer " + d.token.AccessToken,
	}, nil
}

// RequireTransportSecurity implements credentials.PerRPCCredentials. Unlike
// userTokenCreds (internal cluster traffic), a device-flow credential runs
// on a human's own terminal and typically crosses the public internet to
// reach the API, so it defaults to requiring transport security.
func (d *deviceFlowCreds) RequireTransportSecurity() bool {
	return true
}

// loadCachedToken reads a previously persisted refresh token from
// d.cachePath and populates d.token. Returns an error (triggering the
// interactive device flow) when no usable cache exists.
//
// TODO(Implementation phase): read and parse the cache file, verify its
// mode is 0600 (reject and re-authenticate rather than trust a
// world-readable file), and reconstruct an *oauth2.Token. NFR13: do not log
// the file's contents or include token material in the returned error.
func (d *deviceFlowCreds) loadCachedToken() error {
	return fmt.Errorf("no cached device flow token")
}

// saveCachedToken persists token to d.cachePath with mode 0600.
//
// TODO(Implementation phase): create the parent directory, marshal
// cachedToken to JSON, and write atomically (temp file + rename) with mode
// 0600 set at creation time -- never a permissive default tightened later.
func (d *deviceFlowCreds) saveCachedToken(token *oauth2.Token) error {
	return fmt.Errorf("device flow token cache persistence not yet implemented")
}

// refreshToken exchanges the cached refresh token for a new access token
// and updates both the in-memory and on-disk cache.
//
// TODO(Implementation phase): use d.oauthConfig (populated by
// performDeviceFlow via issuer discovery) with oauth2's token source, or a
// manual refresh_token grant against the discovered token endpoint. NFR13:
// never log the refresh or access token.
func (d *deviceFlowCreds) refreshToken(ctx context.Context) error {
	return fmt.Errorf("device flow token refresh not yet implemented")
}

// performDeviceFlow discovers the issuer's device authorization and token
// endpoints, runs the RFC 8628 flow (printing the verification URI and user
// code, then polling), and populates d.token on success.
//
// TODO(Implementation phase): discover endpoints via
// discoverDeviceEndpoints, build an oauth2.Config from them, call
// Config.DeviceAuth then Config.DeviceAccessToken (see deviceauth.go in
// golang.org/x/oauth2 -- it already handles interval, slow_down,
// authorization_pending, and expired_token per RFC 8628 section 3.5), and
// cache the result via saveCachedToken. NFR13: print only the verification
// URI and user code, never a token.
func (d *deviceFlowCreds) performDeviceFlow(ctx context.Context) error {
	return fmt.Errorf("device authorization grant flow not yet implemented")
}

// discoverDeviceEndpoints fetches and parses
// <issuerURL>/.well-known/openid-configuration into deviceEndpoints.
//
// TODO(Implementation phase): HTTP GET with ctx, decode JSON, validate both
// endpoints are non-empty -- an issuer missing device_authorization_endpoint
// means the Keycloak client/realm has not enabled the device flow, which
// should be a clear error pointing at KEYCLOAK.md rather than a generic
// parse failure.
func discoverDeviceEndpoints(ctx context.Context, issuerURL string) (*deviceEndpoints, error) {
	return nil, fmt.Errorf("device flow issuer discovery not yet implemented")
}
