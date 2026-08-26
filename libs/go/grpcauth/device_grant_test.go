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
