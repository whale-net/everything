package grpcauth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// TestLoadCachedToken_Success verifies that a valid cached token is loaded correctly.
func TestLoadCachedToken_Success(t *testing.T) {
	tempDir := t.TempDir()
	cacheFile := filepath.Join(tempDir, "device_grant.json")

	// Create a valid cached token file
	ct := cachedToken{
		AccessToken:  "test-access-token",
		RefreshToken: "test-refresh-token",
		ExpiresAt:    time.Now().Add(1 * time.Hour).Unix(),
	}
	data, _ := json.Marshal(ct)
	if err := os.WriteFile(cacheFile, data, 0600); err != nil {
		t.Fatalf("failed to create cache file: %v", err)
	}

	creds := &deviceGrantCreds{
		cacheFile: cacheFile,
	}

	if err := creds.loadCachedToken(); err != nil {
		t.Fatalf("loadCachedToken failed: %v", err)
	}

	if creds.token == nil {
		t.Fatal("expected token to be loaded")
	}
	if creds.token.AccessToken != "test-access-token" {
		t.Errorf("got access token %q, want %q", creds.token.AccessToken, "test-access-token")
	}
	if creds.token.RefreshToken != "test-refresh-token" {
		t.Errorf("got refresh token %q, want %q", creds.token.RefreshToken, "test-refresh-token")
	}
}

// TestLoadCachedToken_MissingFile returns an error when cache doesn't exist.
func TestLoadCachedToken_MissingFile(t *testing.T) {
	tempDir := t.TempDir()
	cacheFile := filepath.Join(tempDir, "nonexistent.json")

	creds := &deviceGrantCreds{
		cacheFile: cacheFile,
	}

	err := creds.loadCachedToken()
	if err == nil {
		t.Fatal("expected error for missing cache file")
	}
	if !strings.Contains(err.Error(), "no cached token") {
		t.Errorf("expected 'no cached token' error, got: %v", err)
	}
}

// TestLoadCachedToken_BadPermissions returns an error when cache has overly permissive permissions.
func TestLoadCachedToken_BadPermissions(t *testing.T) {
	tempDir := t.TempDir()
	cacheFile := filepath.Join(tempDir, "device_grant.json")

	ct := cachedToken{
		AccessToken:  "test-token",
		RefreshToken: "test-refresh",
		ExpiresAt:    time.Now().Add(1 * time.Hour).Unix(),
	}
	data, _ := json.Marshal(ct)

	// Write with permissive permissions
	if err := os.WriteFile(cacheFile, data, 0644); err != nil {
		t.Fatalf("failed to create cache file: %v", err)
	}

	creds := &deviceGrantCreds{
		cacheFile: cacheFile,
	}

	err := creds.loadCachedToken()
	if err == nil {
		t.Fatal("expected error for bad permissions")
	}
	if !strings.Contains(err.Error(), "overly permissive") {
		t.Errorf("expected permissive error, got: %v", err)
	}
}

// TestLoadCachedToken_ExpiredToken returns an error when refresh token has expired.
func TestLoadCachedToken_ExpiredToken(t *testing.T) {
	tempDir := t.TempDir()
	cacheFile := filepath.Join(tempDir, "device_grant.json")

	// Create an expired cached token
	ct := cachedToken{
		AccessToken:  "test-token",
		RefreshToken: "test-refresh",
		ExpiresAt:    time.Now().Add(-1 * time.Hour).Unix(), // Already expired
	}
	data, _ := json.Marshal(ct)
	if err := os.WriteFile(cacheFile, data, 0600); err != nil {
		t.Fatalf("failed to create cache file: %v", err)
	}

	creds := &deviceGrantCreds{
		cacheFile: cacheFile,
	}

	err := creds.loadCachedToken()
	if err == nil {
		t.Fatal("expected error for expired token")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("expected expired error, got: %v", err)
	}
}

// TestSaveCachedToken creates a token file with restrictive permissions.
func TestSaveCachedToken(t *testing.T) {
	tempDir := t.TempDir()
	cacheFile := filepath.Join(tempDir, "device_grant.json")

	creds := &deviceGrantCreds{
		cacheFile: cacheFile,
	}

	token := &oauth2.Token{
		AccessToken:  "new-access-token",
		TokenType:    "Bearer",
		RefreshToken: "new-refresh-token",
		Expiry:       time.Now().Add(1 * time.Hour),
	}

	if err := creds.saveCachedToken(token); err != nil {
		t.Fatalf("saveCachedToken failed: %v", err)
	}

	// Verify file exists and has correct permissions
	info, err := os.Stat(cacheFile)
	if err != nil {
		t.Fatalf("failed to stat cache file: %v", err)
	}

	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("cache file has mode %o, want 0600", perm)
	}

	// Verify file contents
	data, err := os.ReadFile(cacheFile)
	if err != nil {
		t.Fatalf("failed to read cache file: %v", err)
	}

	var ct cachedToken
	if err := json.Unmarshal(data, &ct); err != nil {
		t.Fatalf("failed to parse cache file: %v", err)
	}

	if ct.AccessToken != "new-access-token" {
		t.Errorf("got access token %q, want %q", ct.AccessToken, "new-access-token")
	}
}

// TestRefreshToken_Success verifies that refresh token correctly fetches a new access token.
func TestRefreshToken_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("failed to parse form: %v", err)
		}

		// Verify refresh request is correct
		if gt := r.FormValue("grant_type"); gt != "refresh_token" {
			t.Errorf("got grant_type %q, want refresh_token", gt)
		}
		if rt := r.FormValue("refresh_token"); rt != "old-refresh-token" {
			t.Errorf("got refresh_token %q, want old-refresh-token", rt)
		}

		// Return new token
		resp := tokenResponse{
			AccessToken:  "new-access-token",
			TokenType:    "Bearer",
			ExpiresIn:    3600,
			RefreshToken: "new-refresh-token",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	tempDir := t.TempDir()
	cacheFile := filepath.Join(tempDir, "device_grant.json")

	creds := &deviceGrantCreds{
		config: DeviceGrantConfig{
			TokenURL: server.URL,
			ClientID: "test-client",
		},
		cacheFile: cacheFile,
		token: &oauth2.Token{
			RefreshToken: "old-refresh-token",
			AccessToken:  "old-access-token",
		},
	}

	if err := creds.refreshToken(context.Background()); err != nil {
		t.Fatalf("refreshToken failed: %v", err)
	}

	creds.mu.RLock()
	newToken := creds.token
	creds.mu.RUnlock()

	if newToken.AccessToken != "new-access-token" {
		t.Errorf("got access token %q, want new-access-token", newToken.AccessToken)
	}
	if newToken.RefreshToken != "new-refresh-token" {
		t.Errorf("got refresh token %q, want new-refresh-token", newToken.RefreshToken)
	}
}

// TestRefreshToken_InvalidGrant verifies that an invalid grant error is properly handled.
func TestRefreshToken_InvalidGrant(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := tokenResponse{
			Error:     "invalid_grant",
			ErrorDesc: "Token has expired",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	creds := &deviceGrantCreds{
		config: DeviceGrantConfig{
			TokenURL: server.URL,
			ClientID: "test-client",
		},
		token: &oauth2.Token{
			RefreshToken: "expired-refresh-token",
		},
	}

	err := creds.refreshToken(context.Background())
	if err == nil {
		t.Fatal("expected error for invalid grant")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("expected expiry error, got: %v", err)
	}
}

// TestPerformDeviceFlow_AccessDenied verifies that access_denied error is properly handled.
func TestPerformDeviceFlow_AccessDenied(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/device") || r.Method == "POST" && r.FormValue("device_code") == "" {
			resp := deviceFlowResponse{
				DeviceCode:      "test-device-code",
				UserCode:        "TEST-CODE",
				VerificationURI: "https://auth.example.com/device",
				ExpiresIn:       600,
				Interval:        1,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		} else {
			resp := tokenResponse{
				Error: "access_denied",
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		}
	}))
	defer server.Close()

	tempDir := t.TempDir()
	cacheFile := filepath.Join(tempDir, "device_grant.json")

	creds := &deviceGrantCreds{
		config: DeviceGrantConfig{
			TokenURL: server.URL,
			ClientID: "test-client",
		},
		cacheFile: cacheFile,
	}

	// Mock stdin
	oldStdin := os.Stdin
	defer func() { os.Stdin = oldStdin }()
	r, w, _ := os.Pipe()
	os.Stdin = r
	go func() {
		io.WriteString(w, "\n")
		w.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := creds.performDeviceFlow(ctx)
	if err == nil {
		t.Fatal("expected error for access_denied")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Errorf("expected denied error, got: %v", err)
	}
}

// TestPerformDeviceFlow_ExpiredToken verifies that expired_token error is properly handled.
func TestPerformDeviceFlow_ExpiredToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/device") || r.Method == "POST" && r.FormValue("device_code") == "" {
			resp := deviceFlowResponse{
				DeviceCode:      "test-device-code",
				UserCode:        "TEST-CODE",
				VerificationURI: "https://auth.example.com/device",
				ExpiresIn:       600,
				Interval:        1,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		} else {
			resp := tokenResponse{
				Error: "expired_token",
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		}
	}))
	defer server.Close()

	tempDir := t.TempDir()
	cacheFile := filepath.Join(tempDir, "device_grant.json")

	creds := &deviceGrantCreds{
		config: DeviceGrantConfig{
			TokenURL: server.URL,
			ClientID: "test-client",
		},
		cacheFile: cacheFile,
	}

	// Mock stdin
	oldStdin := os.Stdin
	defer func() { os.Stdin = oldStdin }()
	r, w, _ := os.Pipe()
	os.Stdin = r
	go func() {
		io.WriteString(w, "\n")
		w.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := creds.performDeviceFlow(ctx)
	if err == nil {
		t.Fatal("expected error for expired_token")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("expected expired error, got: %v", err)
	}
}

// TestGetRequestMetadata_TokenRefresh verifies that expired tokens trigger refresh before sending.
func TestGetRequestMetadata_TokenRefresh(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := tokenResponse{
			AccessToken:  "refreshed-token",
			TokenType:    "Bearer",
			ExpiresIn:    3600,
			RefreshToken: "test-refresh",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	tempDir := t.TempDir()
	cacheFile := filepath.Join(tempDir, "device_grant.json")

	creds := &deviceGrantCreds{
		config: DeviceGrantConfig{
			TokenURL: server.URL,
			ClientID: "test-client",
		},
		cacheFile: cacheFile,
		token: &oauth2.Token{
			AccessToken:  "old-token",
			RefreshToken: "old-refresh",
			Expiry:       time.Now().Add(-1 * time.Second), // Already expired
		},
	}

	metadata, err := creds.GetRequestMetadata(context.Background())
	if err != nil {
		t.Fatalf("GetRequestMetadata failed: %v", err)
	}

	if auth := metadata["authorization"]; !strings.HasPrefix(auth, "Bearer refreshed-token") {
		t.Errorf("expected Bearer refreshed-token, got %q", auth)
	}
}

// TestGetRequestMetadata_NoToken returns error when no token is available.
func TestGetRequestMetadata_NoToken(t *testing.T) {
	creds := &deviceGrantCreds{}

	_, err := creds.GetRequestMetadata(context.Background())
	if err == nil {
		t.Fatal("expected error when no token available")
	}
}

// TestRequireTransportSecurity returns the config value.
func TestRequireTransportSecurity(t *testing.T) {
	creds := &deviceGrantCreds{
		config: DeviceGrantConfig{
			RequireTransportSecurity: true,
		},
	}

	if !creds.RequireTransportSecurity() {
		t.Errorf("expected RequireTransportSecurity to be true")
	}
}

// TestPerformDeviceFlow_AuthorizationPending verifies polling continues when authorization_pending.
func TestPerformDeviceFlow_AuthorizationPending(t *testing.T) {
	pollCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/device") || r.Method == "POST" && r.FormValue("device_code") == "" {
			// Device code request
			resp := deviceFlowResponse{
				DeviceCode:      "test-device-code",
				UserCode:        "TEST-CODE",
				VerificationURI: "https://auth.example.com/device",
				ExpiresIn:       600,
				Interval:        1,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		} else {
			// Token polling request
			pollCount++
			var resp interface{}
			if pollCount <= 2 {
				// First two polls: still pending
				resp = tokenResponse{
					Error:     "authorization_pending",
					ErrorDesc: "User has not yet authorized",
				}
			} else {
				// Third poll: success
				resp = tokenResponse{
					AccessToken:  "final-access-token",
					TokenType:    "Bearer",
					ExpiresIn:    3600,
					RefreshToken: "final-refresh-token",
				}
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		}
	}))
	defer server.Close()

	tempDir := t.TempDir()
	cacheFile := filepath.Join(tempDir, "device_grant.json")

	creds := &deviceGrantCreds{
		config: DeviceGrantConfig{
			TokenURL: server.URL,
			ClientID: "test-client",
		},
		cacheFile: cacheFile,
	}

	// Mock stdin
	oldStdin := os.Stdin
	defer func() { os.Stdin = oldStdin }()
	r, w, _ := os.Pipe()
	os.Stdin = r
	go func() {
		io.WriteString(w, "\n")
		w.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := creds.performDeviceFlow(ctx)
	if err != nil {
		t.Fatalf("performDeviceFlow failed: %v", err)
	}

	creds.mu.RLock()
	token := creds.token
	creds.mu.RUnlock()

	if token.AccessToken != "final-access-token" {
		t.Errorf("got access token %q, want final-access-token", token.AccessToken)
	}
	if pollCount != 3 {
		t.Errorf("expected 3 polls, got %d", pollCount)
	}
}

// TestPerformDeviceFlow_SlowDown verifies polling interval increases with slow_down.
func TestPerformDeviceFlow_SlowDown(t *testing.T) {
	pollCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/device") || r.Method == "POST" && r.FormValue("device_code") == "" {
			// Device code request
			resp := deviceFlowResponse{
				DeviceCode:      "test-device-code",
				UserCode:        "TEST-CODE",
				VerificationURI: "https://auth.example.com/device",
				ExpiresIn:       600,
				Interval:        2,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		} else {
			// Token polling request
			pollCount++
			var resp interface{}
			if pollCount == 1 {
				// First poll: slow_down
				resp = tokenResponse{
					Error:     "slow_down",
					ErrorDesc: "Polling too fast",
				}
			} else {
				// Second poll: success
				resp = tokenResponse{
					AccessToken:  "slowdown-token",
					TokenType:    "Bearer",
					ExpiresIn:    3600,
					RefreshToken: "slowdown-refresh",
				}
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		}
	}))
	defer server.Close()

	tempDir := t.TempDir()
	cacheFile := filepath.Join(tempDir, "device_grant.json")

	creds := &deviceGrantCreds{
		config: DeviceGrantConfig{
			TokenURL: server.URL,
			ClientID: "test-client",
		},
		cacheFile: cacheFile,
	}

	// Mock stdin
	oldStdin := os.Stdin
	defer func() { os.Stdin = oldStdin }()
	r, w, _ := os.Pipe()
	os.Stdin = r
	go func() {
		io.WriteString(w, "\n")
		w.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := creds.performDeviceFlow(ctx)
	if err != nil {
		t.Fatalf("performDeviceFlow failed: %v", err)
	}

	creds.mu.RLock()
	token := creds.token
	creds.mu.RUnlock()

	if token.AccessToken != "slowdown-token" {
		t.Errorf("got access token %q, want slowdown-token", token.AccessToken)
	}
	if pollCount != 2 {
		t.Errorf("expected 2 polls, got %d", pollCount)
	}
}

// TestPerformDeviceFlow_Success verifies successful device flow with proper token caching.
func TestPerformDeviceFlow_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/device") || r.Method == "POST" && r.FormValue("device_code") == "" {
			// Device code request
			resp := deviceFlowResponse{
				DeviceCode:      "success-device-code",
				UserCode:        "SUCCESS",
				VerificationURI: "https://auth.example.com/device",
				ExpiresIn:       600,
				Interval:        1,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		} else {
			// Token request
			resp := tokenResponse{
				AccessToken:  "success-access-token",
				TokenType:    "Bearer",
				ExpiresIn:    3600,
				RefreshToken: "success-refresh-token",
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		}
	}))
	defer server.Close()

	tempDir := t.TempDir()
	cacheFile := filepath.Join(tempDir, "device_grant.json")

	creds := &deviceGrantCreds{
		config: DeviceGrantConfig{
			TokenURL: server.URL,
			ClientID: "test-client",
		},
		cacheFile: cacheFile,
	}

	// Mock stdin
	oldStdin := os.Stdin
	defer func() { os.Stdin = oldStdin }()
	r, w, _ := os.Pipe()
	os.Stdin = r
	go func() {
		io.WriteString(w, "\n")
		w.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := creds.performDeviceFlow(ctx)
	if err != nil {
		t.Fatalf("performDeviceFlow failed: %v", err)
	}

	creds.mu.RLock()
	token := creds.token
	creds.mu.RUnlock()

	if token.AccessToken != "success-access-token" {
		t.Errorf("got access token %q, want success-access-token", token.AccessToken)
	}
	if token.RefreshToken != "success-refresh-token" {
		t.Errorf("got refresh token %q, want success-refresh-token", token.RefreshToken)
	}

	// Verify token was cached
	if _, err := os.Stat(cacheFile); err != nil {
		t.Fatalf("expected cache file to exist: %v", err)
	}

	// Verify cache has restrictive permissions
	info, _ := os.Stat(cacheFile)
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("cache file has mode %o, want 0600", perm)
	}
}

// TestRefreshToken_ErrorHandling verifies various token refresh error cases.
func TestRefreshToken_ErrorHandling(t *testing.T) {
	tests := []struct {
		name      string
		errorCode string
		errorDesc string
		wantErr   string
	}{
		{
			name:      "invalid_grant",
			errorCode: "invalid_grant",
			errorDesc: "Token has expired",
			wantErr:   "expired",
		},
		{
			name:      "server_error",
			errorCode: "server_error",
			errorDesc: "Internal server error",
			wantErr:   "server_error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				resp := tokenResponse{
					Error:     tt.errorCode,
					ErrorDesc: tt.errorDesc,
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(resp)
			}))
			defer server.Close()

			creds := &deviceGrantCreds{
				config: DeviceGrantConfig{
					TokenURL: server.URL,
					ClientID: "test-client",
				},
				token: &oauth2.Token{
					RefreshToken: "old-refresh",
				},
			}

			err := creds.refreshToken(context.Background())
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("expected %q in error, got: %v", tt.wantErr, err)
			}
		})
	}
}

// TestNoTokenInErrorMessages verifies token material never appears in error messages.
func TestNoTokenInErrorMessages(t *testing.T) {
	tests := []struct {
		name       string
		testFunc   func(t *testing.T) error
		shouldHave []string // strings that should NOT be in the error
	}{
		{
			name: "refresh_token_error",
			testFunc: func(t *testing.T) error {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					resp := tokenResponse{
						Error:     "invalid_grant",
						ErrorDesc: "Token expired",
					}
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(resp)
				}))
				defer server.Close()

				creds := &deviceGrantCreds{
					config: DeviceGrantConfig{
						TokenURL: server.URL,
						ClientID: "test-client",
					},
					token: &oauth2.Token{
						RefreshToken: "secret-refresh-token-12345",
					},
				}

				return creds.refreshToken(context.Background())
			},
			shouldHave: []string{
				"secret-refresh-token-12345",
				"refresh_token=secret",
			},
		},
		{
			name: "load_cached_token_error",
			testFunc: func(t *testing.T) error {
				tempDir := t.TempDir()
				cacheFile := filepath.Join(tempDir, "device_grant.json")

				ct := cachedToken{
					AccessToken:  "secret-access-token-67890",
					RefreshToken: "secret-refresh-token-67890",
					ExpiresAt:    time.Now().Add(-1 * time.Hour).Unix(),
				}
				data, _ := json.Marshal(ct)
				os.WriteFile(cacheFile, data, 0600)

				creds := &deviceGrantCreds{
					cacheFile: cacheFile,
				}

				return creds.loadCachedToken()
			},
			shouldHave: []string{
				"secret-access-token-67890",
				"secret-refresh-token-67890",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.testFunc(t)
			if err == nil {
				t.Fatal("expected error")
			}

			errMsg := err.Error()
			for _, forbidden := range tt.shouldHave {
				if strings.Contains(errMsg, forbidden) {
					t.Errorf("token material leaked in error message: %q found in %q", forbidden, errMsg)
				}
			}
		})
	}
}
