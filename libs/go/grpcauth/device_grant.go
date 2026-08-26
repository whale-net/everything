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

// DeviceGrantConfig holds configuration for the device authorization grant flow.
type DeviceGrantConfig struct {
	TokenURL string // Keycloak token endpoint
	ClientID string // public client ID
	Scopes   []string
	// CacheDir is where to persist the refresh token.
	// If empty, defaults to $HOME/.cache/grpcauth
	CacheDir string
	// RequireTransportSecurity indicates if TLS is required
	RequireTransportSecurity bool
}

// deviceGrantCreds implements PerRPCCredentials using the device authorization grant.
type deviceGrantCreds struct {
	config    DeviceGrantConfig
	token     *oauth2.Token
	mu        sync.RWMutex
	cacheFile string
}

// deviceFlowResponse models the response from the device authorization endpoint.
type deviceFlowResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// tokenResponse models the response from the token endpoint.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

// cachedToken represents a token persisted on disk.
type cachedToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"`
}

// NewDeviceGrantDialOption creates a gRPC DialOption for device authorization grant auth.
// In AuthModeNone, returns a no-op option. In AuthModeOIDC, performs the device flow
// (prompting the user once, then refreshing non-interactively) and provides the token.
func NewDeviceGrantDialOption(config DeviceGrantConfig) (grpc.DialOption, error) {
	if config.TokenURL == "" {
		return nil, fmt.Errorf("TokenURL is required for device grant auth")
	}
	if config.ClientID == "" {
		return nil, fmt.Errorf("ClientID is required for device grant auth")
	}

	cacheDir := config.CacheDir
	if cacheDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to determine home directory: %w", err)
		}
		cacheDir = filepath.Join(home, ".cache", "grpcauth")
	}

	// Ensure cache directory exists with restrictive permissions
	if err := os.MkdirAll(cacheDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create cache directory: %w", err)
	}

	cacheFile := filepath.Join(cacheDir, "device_grant.json")

	creds := &deviceGrantCreds{
		config:    config,
		cacheFile: cacheFile,
	}

	// Try to load cached token first
	if err := creds.loadCachedToken(); err != nil {
		// TODO: Implement device flow to get initial token
		// This is a placeholder for the implementation phase
		return nil, fmt.Errorf("device grant not yet implemented: %w", err)
	}

	return grpc.WithPerRPCCredentials(creds), nil
}

// GetRequestMetadata implements PerRPCCredentials.GetRequestMetadata
func (d *deviceGrantCreds) GetRequestMetadata(ctx context.Context, uri ...string) (map[string]string, error) {
	d.mu.RLock()
	token := d.token
	d.mu.RUnlock()

	if token == nil {
		return nil, fmt.Errorf("no token available")
	}

	// Check if token is expired and refresh if needed
	if token.Expiry.Before(time.Now().Add(time.Minute)) {
		if err := d.refreshToken(ctx); err != nil {
			return nil, fmt.Errorf("failed to refresh token: %w", err)
		}
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

	if d.token == nil || d.token.AccessToken == "" {
		return nil, fmt.Errorf("no valid access token")
	}

	return map[string]string{
		"authorization": "Bearer " + d.token.AccessToken,
	}, nil
}

// RequireTransportSecurity implements PerRPCCredentials.RequireTransportSecurity
func (d *deviceGrantCreds) RequireTransportSecurity() bool {
	return d.config.RequireTransportSecurity
}

// loadCachedToken loads a previously saved token from disk.
// TODO: Implement in Implementation phase
func (d *deviceGrantCreds) loadCachedToken() error {
	// Placeholder for loading cached token
	// This should:
	// 1. Read the cache file
	// 2. Verify it's readable only by the current user (permissions check)
	// 3. Parse and load the token
	// 4. Return error if cache doesn't exist (will trigger device flow)
	return fmt.Errorf("cache loading not yet implemented")
}

// refreshToken refreshes the access token using the refresh token.
// TODO: Implement in Implementation phase
func (d *deviceGrantCreds) refreshToken(ctx context.Context) error {
	// Placeholder for token refresh
	// This should:
	// 1. Use the refresh token to get a new access token
	// 2. Handle refresh token expiry
	// 3. Update the cached token
	// 4. Return error if refresh fails
	return fmt.Errorf("token refresh not yet implemented")
}

// performDeviceFlow initiates the device authorization grant flow.
// TODO: Implement in Implementation phase
func (d *deviceGrantCreds) performDeviceFlow(ctx context.Context) error {
	// Placeholder for device flow
	// This should:
	// 1. Request device code from the token endpoint
	// 2. Display user code and verification URI to the user
	// 3. Poll for completion with correct interval and slow-down handling
	// 4. Handle authorization_pending, slow_down, expired_token, access_denied
	// 5. Cache the resulting token with restrictive permissions
	return fmt.Errorf("device flow not yet implemented")
}

// saveCachedToken persists a token to disk with restrictive permissions.
// TODO: Implement in Implementation phase
func (d *deviceGrantCreds) saveCachedToken(token *oauth2.Token) error {
	// Placeholder for saving cached token
	// This should:
	// 1. Create cache file with mode 0600 (readable only by owner)
	// 2. Marshal token to JSON
	// 3. Write to cache file atomically
	return fmt.Errorf("cache saving not yet implemented")
}
