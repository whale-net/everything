package grpcauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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

	// Discovery happens up front -- once here, at startup -- rather than
	// lazily inside performDeviceFlow, because the resulting oauth2.Config
	// is also needed later by refreshToken on cache-hit paths that never
	// call performDeviceFlow at all.
	endpoints, err := discoverDeviceEndpoints(ctx, config.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("failed to discover device flow endpoints: %w", err)
	}

	creds := &deviceFlowCreds{
		config:    config,
		cachePath: cachePath,
		oauthConfig: &oauth2.Config{
			ClientID: config.ClientID,
			Scopes:   withOpenIDScope(config.Scopes),
			Endpoint: oauth2.Endpoint{
				DeviceAuthURL: endpoints.DeviceAuthorizationEndpoint,
				TokenURL:      endpoints.TokenEndpoint,
			},
		},
	}

	if err := creds.loadCachedToken(); err != nil {
		if err := creds.performDeviceFlow(ctx); err != nil {
			return nil, fmt.Errorf("device authorization grant failed: %w", err)
		}
	}

	return grpc.WithPerRPCCredentials(creds), nil
}

// withOpenIDScope returns scopes with "openid" included, without mutating
// the caller's slice.
func withOpenIDScope(scopes []string) []string {
	for _, s := range scopes {
		if s == "openid" {
			return scopes
		}
	}
	out := make([]string, 0, len(scopes)+1)
	out = append(out, "openid")
	out = append(out, scopes...)
	return out
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
// NFR13: never log the file's contents or include token material in the
// returned error -- every error path below is a fixed string or refers only
// to the file path / mode, never to cachedToken's fields.
func (d *deviceFlowCreds) loadCachedToken() error {
	info, err := os.Stat(d.cachePath)
	if err != nil {
		return fmt.Errorf("no cached device flow token")
	}

	if info.Mode().Perm()&0o077 != 0 {
		// A world- or group-readable cache file is untrustworthy (it may
		// have been tampered with, or copied somewhere insecure) --
		// re-authenticate rather than trust it.
		return fmt.Errorf("cached device flow token file has permissive mode %o (want 0600); re-authenticating", info.Mode().Perm())
	}

	data, err := os.ReadFile(d.cachePath)
	if err != nil {
		return fmt.Errorf("failed to read cached device flow token")
	}

	var cached cachedToken
	if err := json.Unmarshal(data, &cached); err != nil {
		return fmt.Errorf("failed to parse cached device flow token")
	}
	if cached.AccessToken == "" || cached.RefreshToken == "" {
		return fmt.Errorf("cached device flow token is incomplete")
	}

	d.mu.Lock()
	d.token = &oauth2.Token{
		AccessToken:  cached.AccessToken,
		RefreshToken: cached.RefreshToken,
		Expiry:       time.Unix(cached.ExpiresAt, 0),
	}
	d.mu.Unlock()
	return nil
}

// saveCachedToken persists token to d.cachePath with mode 0600.
//
// Writes atomically (temp file + rename) with mode 0600 set at file
// creation time via os.CreateTemp + Chmod, before any data (including the
// refresh token) is written to it -- never a permissive default tightened
// after the fact, which would leave a window where the file is readable by
// others.
func (d *deviceFlowCreds) saveCachedToken(token *oauth2.Token) error {
	dir := filepath.Dir(d.cachePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create device flow token cache directory: %w", err)
	}

	data, err := json.Marshal(cachedToken{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		ExpiresAt:    token.Expiry.Unix(),
	})
	if err != nil {
		return fmt.Errorf("failed to marshal device flow token cache")
	}

	tmp, err := os.CreateTemp(dir, ".device-flow-token-*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create device flow token cache temp file: %w", err)
	}
	tmpPath := tmp.Name()
	// Best-effort cleanup; a no-op once the rename below succeeds since the
	// temp path no longer exists at that name.
	defer os.Remove(tmpPath)

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("failed to set device flow token cache file permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("failed to write device flow token cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close device flow token cache temp file: %w", err)
	}
	if err := os.Rename(tmpPath, d.cachePath); err != nil {
		return fmt.Errorf("failed to persist device flow token cache: %w", err)
	}
	return nil
}

// refreshToken exchanges the cached refresh token for a new access token
// and updates both the in-memory and on-disk cache.
//
// Uses d.oauthConfig's TokenSource (populated from issuer discovery in
// NewDeviceFlowDialOption) rather than a hand-rolled refresh_token POST. A
// bare *oauth2.Token{RefreshToken: ...} with no AccessToken is intentionally
// passed in: Token.Valid() treats an empty AccessToken as invalid
// regardless of Expiry, so reuseTokenSource always takes the "refresh via
// tokenRefresher" path instead of the "still valid, reuse it" path -- this
// forces an actual refresh_token grant every time refreshToken is called,
// rather than only once the access token is fully expired.
func (d *deviceFlowCreds) refreshToken(ctx context.Context) error {
	d.mu.RLock()
	current := d.token
	oauthConfig := d.oauthConfig
	d.mu.RUnlock()

	if current == nil || current.RefreshToken == "" {
		return fmt.Errorf("no refresh token available; re-run the device authorization grant")
	}
	if oauthConfig == nil {
		return fmt.Errorf("device flow credential is missing endpoint configuration")
	}

	ts := oauthConfig.TokenSource(ctx, &oauth2.Token{RefreshToken: current.RefreshToken})
	newToken, err := ts.Token()
	if err != nil {
		// NFR13: never include token material in the error. RetrieveError's
		// Error() surfaces only the RFC 6749 error/error_description/
		// error_uri fields, never token values.
		return fmt.Errorf("failed to refresh device flow token: %w", err)
	}

	d.mu.Lock()
	d.token = newToken
	d.mu.Unlock()

	return d.saveCachedToken(newToken)
}

// performDeviceFlow runs the RFC 8628 flow against d.oauthConfig (already
// populated from issuer discovery by NewDeviceFlowDialOption): requests a
// device code, prints the verification URI and user code, polls the token
// endpoint until the human approves it (or it is denied or expires), and
// populates d.token on success.
//
// Delegates the polling loop itself to oauth2.Config.DeviceAuth /
// DeviceAccessToken (deviceauth.go in golang.org/x/oauth2), which already
// implements RFC 8628 section 3.5's interval, slow_down,
// authorization_pending, and expired_token handling -- this function only
// wires it up, prints the human-facing prompt, and persists the result.
//
// NFR13: prints only the verification URI and user code, never a token.
func (d *deviceFlowCreds) performDeviceFlow(ctx context.Context) error {
	if d.oauthConfig == nil {
		return fmt.Errorf("device flow credential is missing endpoint configuration")
	}

	da, err := d.oauthConfig.DeviceAuth(ctx)
	if err != nil {
		return fmt.Errorf("failed to start device authorization: %w", err)
	}

	if da.VerificationURIComplete != "" {
		fmt.Fprintf(os.Stderr, "To sign in, visit:\n\n    %s\n\n", da.VerificationURIComplete)
	} else {
		fmt.Fprintf(os.Stderr, "To sign in, visit %s and enter code: %s\n", da.VerificationURI, da.UserCode)
	}

	token, err := d.oauthConfig.DeviceAccessToken(ctx, da)
	if err != nil {
		return classifyDeviceAuthError(err)
	}

	d.mu.Lock()
	d.token = token
	d.mu.Unlock()

	if err := d.saveCachedToken(token); err != nil {
		return fmt.Errorf("failed to persist device flow token after successful authorization: %w", err)
	}
	return nil
}

// classifyDeviceAuthError turns DeviceAccessToken's terminal RFC 8628
// errors into clear, human-facing messages instead of a generic wrapped
// error. NFR13: RetrieveError.Error() surfaces only the RFC 6749
// error/error_description/error_uri fields -- never token material -- so
// wrapping it with %w in the default case is safe.
func classifyDeviceAuthError(err error) error {
	var retrieveErr *oauth2.RetrieveError
	if errors.As(err, &retrieveErr) {
		switch retrieveErr.ErrorCode {
		case "expired_token":
			return fmt.Errorf("device authorization code expired before it was approved; run the command again")
		case "access_denied":
			return fmt.Errorf("device authorization was denied")
		}
	}
	return fmt.Errorf("device authorization grant failed: %w", err)
}

// discoverDeviceEndpoints fetches and parses
// <issuerURL>/.well-known/openid-configuration into deviceEndpoints.
func discoverDeviceEndpoints(ctx context.Context, issuerURL string) (*deviceEndpoints, error) {
	discoveryURL := strings.TrimSuffix(issuerURL, "/") + "/.well-known/openid-configuration"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build OIDC discovery request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to reach OIDC discovery endpoint %s: %w", discoveryURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OIDC discovery endpoint %s returned HTTP %d", discoveryURL, resp.StatusCode)
	}

	var doc deviceEndpoints
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, fmt.Errorf("failed to parse OIDC discovery document from %s: %w", discoveryURL, err)
	}

	if doc.DeviceAuthorizationEndpoint == "" {
		// A missing device_authorization_endpoint means the Keycloak
		// client/realm has not enabled the device flow -- point at
		// KEYCLOAK.md rather than surfacing a generic parse failure.
		return nil, fmt.Errorf("issuer %s does not advertise a device_authorization_endpoint -- the Keycloak client or realm may not have the device authorization grant enabled; see KEYCLOAK.md", issuerURL)
	}
	if doc.TokenEndpoint == "" {
		return nil, fmt.Errorf("issuer %s discovery document is missing token_endpoint", issuerURL)
	}

	return &doc, nil
}
