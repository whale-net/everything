package grpcauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
		// No cached token; perform device flow to get initial token
		if err := creds.performDeviceFlow(context.Background()); err != nil {
			return nil, fmt.Errorf("device grant flow failed: %w", err)
		}
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

	// Check if token is expired and refresh if needed (with 1 minute buffer)
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
// Returns error if cache doesn't exist or is invalid.
func (d *deviceGrantCreds) loadCachedToken() error {
	// Check if cache file exists
	info, err := os.Stat(d.cacheFile)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no cached token")
		}
		return fmt.Errorf("failed to stat cache file: %w", err)
	}

	// Verify restrictive permissions (0600 = rw-------)
	if perm := info.Mode().Perm(); perm != 0600 {
		return fmt.Errorf("cache file has overly permissive mode %o, expected 0600", perm)
	}

	// Read the cache file
	data, err := os.ReadFile(d.cacheFile)
	if err != nil {
		return fmt.Errorf("failed to read cache file: %w", err)
	}

	// Parse cached token
	var ct cachedToken
	if err := json.Unmarshal(data, &ct); err != nil {
		return fmt.Errorf("failed to parse cached token: %w", err)
	}

	// Verify that refresh token hasn't expired
	if ct.ExpiresAt != 0 && time.Now().Unix() > ct.ExpiresAt {
		return fmt.Errorf("cached refresh token has expired")
	}

	// Reconstruct oauth2.Token
	token := &oauth2.Token{
		AccessToken:  ct.AccessToken,
		TokenType:    "Bearer",
		RefreshToken: ct.RefreshToken,
	}

	// Set expiry; if ExpiresAt is 0, the access token is considered expired and will be refreshed
	if ct.ExpiresAt != 0 {
		token.Expiry = time.Unix(ct.ExpiresAt, 0)
	} else {
		token.Expiry = time.Now()
	}

	d.mu.Lock()
	d.token = token
	d.mu.Unlock()

	return nil
}

// refreshToken refreshes the access token using the refresh token.
func (d *deviceGrantCreds) refreshToken(ctx context.Context) error {
	d.mu.RLock()
	if d.token == nil || d.token.RefreshToken == "" {
		d.mu.RUnlock()
		return fmt.Errorf("no refresh token available")
	}
	refreshToken := d.token.RefreshToken
	d.mu.RUnlock()

	// Build the refresh token request
	data := url.Values{
		"grant_type":    []string{"refresh_token"},
		"refresh_token": []string{refreshToken},
		"client_id":     []string{d.config.ClientID},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", d.config.TokenURL, bytes.NewBufferString(data.Encode()))
	if err != nil {
		return fmt.Errorf("failed to create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read refresh response: %w", err)
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return fmt.Errorf("failed to parse refresh response: %w", err)
	}

	if tr.Error != "" {
		// Handle specific error cases
		switch tr.Error {
		case "invalid_grant":
			return fmt.Errorf("refresh token is invalid or expired, user must re-authenticate")
		default:
			return fmt.Errorf("token refresh failed: %s (%s)", tr.Error, tr.ErrorDesc)
		}
	}

	if tr.AccessToken == "" {
		return fmt.Errorf("no access token in refresh response")
	}

	// Calculate new expiry
	expiresAt := time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second).Unix()

	// Update the cached token
	token := &oauth2.Token{
		AccessToken:  tr.AccessToken,
		TokenType:    "Bearer",
		RefreshToken: tr.RefreshToken,
		Expiry:       time.Unix(expiresAt, 0),
	}

	// Only use new refresh token if provided; otherwise keep the old one
	if tr.RefreshToken == "" {
		d.mu.RLock()
		if d.token != nil && d.token.RefreshToken != "" {
			token.RefreshToken = d.token.RefreshToken
		}
		d.mu.RUnlock()
	}

	// Update in-memory token
	d.mu.Lock()
	d.token = token
	d.mu.Unlock()

	// Save to cache
	if err := d.saveCachedToken(token); err != nil {
		return fmt.Errorf("failed to cache refreshed token: %w", err)
	}

	return nil
}

// performDeviceFlow initiates the device authorization grant flow.
func (d *deviceGrantCreds) performDeviceFlow(ctx context.Context) error {
	// Step 1: Request device code
	deviceData := url.Values{
		"client_id": []string{d.config.ClientID},
	}
	if len(d.config.Scopes) > 0 {
		deviceData.Set("scope", "openid profile email")
	}

	req, err := http.NewRequestWithContext(ctx, "POST", d.config.TokenURL, bytes.NewBufferString(deviceData.Encode()))
	if err != nil {
		return fmt.Errorf("failed to create device code request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	// Get the base URL for device endpoint (Keycloak uses /device endpoint)
	baseURL := d.config.TokenURL
	if idx := len(baseURL) - len("/protocol/openid-connect/token"); idx >= 0 && baseURL[idx:] == "/protocol/openid-connect/token" {
		baseURL = baseURL[:idx] + "/protocol/openid-connect/auth/device"
	}

	req.URL.Scheme = ""
	req.URL.Host = ""
	req.URL.Path = baseURL
	req.RequestURI = ""

	deviceReq, err := http.NewRequestWithContext(ctx, "POST", baseURL, bytes.NewBufferString(deviceData.Encode()))
	if err != nil {
		return fmt.Errorf("failed to create device code request: %w", err)
	}
	deviceReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(deviceReq)
	if err != nil {
		return fmt.Errorf("device code request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read device code response: %w", err)
	}

	var dfResp deviceFlowResponse
	if err := json.Unmarshal(body, &dfResp); err != nil {
		return fmt.Errorf("failed to parse device code response: %w", err)
	}

	if dfResp.DeviceCode == "" {
		return fmt.Errorf("no device code in response")
	}

	// Step 2: Display user code and verification URI
	fmt.Printf("Please authorize this application by visiting:\n\n%s\n\nEnter code: %s\n\n", dfResp.VerificationURIComplete, dfResp.UserCode)
	fmt.Print("Press Enter when authorized...")
	io.ReadAll(os.Stdin)

	// Step 3: Poll for authorization with backoff
	pollInterval := time.Duration(dfResp.Interval) * time.Second
	if pollInterval == 0 {
		pollInterval = 5 * time.Second
	}

	expiresAt := time.Now().Add(time.Duration(dfResp.ExpiresIn) * time.Second)
	slowDownInterval := pollInterval

	for {
		if time.Now().After(expiresAt) {
			return fmt.Errorf("device code expired, user took too long to authorize")
		}

		// Step 4: Poll token endpoint
		pollData := url.Values{
			"grant_type":  []string{"urn:ietf:params:oauth:grant-type:device_code"},
			"device_code": []string{dfResp.DeviceCode},
			"client_id":   []string{d.config.ClientID},
		}

		pollReq, err := http.NewRequestWithContext(ctx, "POST", d.config.TokenURL, bytes.NewBufferString(pollData.Encode()))
		if err != nil {
			return fmt.Errorf("failed to create polling request: %w", err)
		}
		pollReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		resp, err := http.DefaultClient.Do(pollReq)
		if err != nil {
			return fmt.Errorf("polling request failed: %w", err)
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to read polling response: %w", err)
		}

		var tr tokenResponse
		if err := json.Unmarshal(body, &tr); err != nil {
			return fmt.Errorf("failed to parse polling response: %w", err)
		}

		// Handle polling responses
		switch tr.Error {
		case "":
			// Success: we got a token
			if tr.AccessToken == "" {
				return fmt.Errorf("no access token in polling response")
			}

			// Step 5: Cache the token
			expiresAt := time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
			token := &oauth2.Token{
				AccessToken:  tr.AccessToken,
				TokenType:    "Bearer",
				RefreshToken: tr.RefreshToken,
				Expiry:       expiresAt,
			}

			if err := d.saveCachedToken(token); err != nil {
				return fmt.Errorf("failed to cache token: %w", err)
			}

			d.mu.Lock()
			d.token = token
			d.mu.Unlock()

			fmt.Println("Authorization successful!")
			return nil

		case "authorization_pending":
			// User hasn't authorized yet; continue polling
			time.Sleep(slowDownInterval)

		case "slow_down":
			// Server is rate-limiting; increase polling interval
			slowDownInterval += pollInterval
			time.Sleep(slowDownInterval)

		case "expired_token":
			return fmt.Errorf("device code expired, please try again")

		case "access_denied":
			return fmt.Errorf("user denied authorization")

		default:
			return fmt.Errorf("device flow error: %s (%s)", tr.Error, tr.ErrorDesc)
		}
	}
}

// saveCachedToken persists a token to disk with restrictive permissions.
func (d *deviceGrantCreds) saveCachedToken(token *oauth2.Token) error {
	if token == nil {
		return fmt.Errorf("token cannot be nil")
	}

	// Create cache file with restrictive permissions
	ct := cachedToken{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		ExpiresAt:    token.Expiry.Unix(),
	}

	data, err := json.Marshal(ct)
	if err != nil {
		return fmt.Errorf("failed to marshal token: %w", err)
	}

	// Write to a temporary file first, then atomically rename
	tmpFile := d.cacheFile + ".tmp"

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(d.cacheFile), 0700); err != nil {
		return fmt.Errorf("failed to create cache directory: %w", err)
	}

	// Write to temporary file with secure permissions
	if err := os.WriteFile(tmpFile, data, 0600); err != nil {
		return fmt.Errorf("failed to write temp cache file: %w", err)
	}

	// Atomically rename to final location
	if err := os.Rename(tmpFile, d.cacheFile); err != nil {
		os.Remove(tmpFile) // Clean up temp file on error
		return fmt.Errorf("failed to move cache file to final location: %w", err)
	}

	return nil
}
