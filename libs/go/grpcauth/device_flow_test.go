package grpcauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// fakeDeviceOIDCServer is a minimal RFC 8628 + OIDC-discovery double: it
// serves .well-known/openid-configuration and a device_authorization
// endpoint with fixed, canned responses, and delegates the token endpoint to
// a per-test tokenHandler so each test can script the exact poll sequence
// (authorization_pending, slow_down, expired_token, success, ...) it needs.
type fakeDeviceOIDCServer struct {
	*httptest.Server

	// interval is seconds returned in the device_authorization response;
	// kept small (1s) so tests exercising the real x/oauth2 poll ticker
	// don't run for long, but still large enough to observe backoff
	// (slow_down adds 5s per RFC 8628 3.5) deterministically.
	interval int
	// tokenHandler decides the token endpoint's response for the pollN'th
	// (1-indexed) request against it -- across both device_code and
	// refresh_token grants, since deviceFlowCreds.refreshToken posts to the
	// same discovered token endpoint.
	tokenHandler func(pollN int, r *http.Request) (status int, body map[string]any)

	mu         sync.Mutex
	deviceAuth int
	tokenPolls int
	pollTimes  []time.Time
}

func newFakeDeviceOIDCServer(t *testing.T, interval int, tokenHandler func(pollN int, r *http.Request) (int, map[string]any)) *fakeDeviceOIDCServer {
	t.Helper()

	f := &fakeDeviceOIDCServer{interval: interval, tokenHandler: tokenHandler}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"device_authorization_endpoint": f.Server.URL + "/device_authorization",
			"token_endpoint":                f.Server.URL + "/token",
		})
	})
	mux.HandleFunc("/device_authorization", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.deviceAuth++
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code":               "test-device-code",
			"user_code":                 "TEST-CODE",
			"verification_uri":          f.Server.URL + "/verify",
			"verification_uri_complete": f.Server.URL + "/verify?user_code=TEST-CODE",
			"expires_in":                600,
			"interval":                  f.interval,
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.tokenPolls++
		n := f.tokenPolls
		f.pollTimes = append(f.pollTimes, time.Now())
		f.mu.Unlock()

		status, body := f.tokenHandler(n, r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	})

	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Server.Close)
	return f
}

func (f *fakeDeviceOIDCServer) deviceAuthCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deviceAuth
}

func (f *fakeDeviceOIDCServer) tokenCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokenPolls
}

func (f *fakeDeviceOIDCServer) pollGap(i, j int) time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pollTimes[j].Sub(f.pollTimes[i])
}

func successBody(accessToken, refreshToken string) map[string]any {
	return map[string]any{
		"access_token":  accessToken,
		"refresh_token": refreshToken,
		"token_type":    "Bearer",
		"expires_in":    3600,
	}
}

func errorBody(code string) map[string]any {
	return map[string]any{"error": code}
}

// TestDeviceFlow_FullRoundTrip_AuthorizationPendingThenSuccess exercises the
// full RFC 8628 flow against a fake OIDC endpoint: device code request,
// polling that first returns authorization_pending, then a successful token
// response.
func TestDeviceFlow_FullRoundTrip_AuthorizationPendingThenSuccess(t *testing.T) {
	server := newFakeDeviceOIDCServer(t, 1, func(pollN int, r *http.Request) (int, map[string]any) {
		if pollN == 1 {
			return http.StatusBadRequest, errorBody("authorization_pending")
		}
		return http.StatusOK, successBody("round-trip-access-token", "round-trip-refresh-token")
	})

	cachePath := filepath.Join(t.TempDir(), "token.json")
	config := DeviceFlowConfig{IssuerURL: server.URL, ClientID: "test-client", TokenCachePath: cachePath}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	opt, err := NewDeviceFlowDialOption(ctx, config)
	if err != nil {
		t.Fatalf("NewDeviceFlowDialOption: %v", err)
	}
	if opt == nil {
		t.Fatal("expected a non-nil grpc.DialOption")
	}

	if got := server.deviceAuthCalls(); got != 1 {
		t.Errorf("device authorization endpoint called %d times, want 1", got)
	}
	// 2, not 1: x/oauth2's DeviceAccessToken retries once with an alternate
	// client-auth style whenever a poll errors (see
	// golang.org/x/oauth2/internal.RetrieveToken's needsAuthStyleProbe), so
	// the single authorization_pending response is followed immediately --
	// same tick, no wait -- by the retry, which lands on our handler's
	// success response.
	if got := server.tokenCalls(); got != 2 {
		t.Errorf("token endpoint polled %d times, want 2 (pending, then success)", got)
	}

	info, err := os.Stat(cachePath)
	if err != nil {
		t.Fatalf("stat cache file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("cache file mode = %o, want 0600", perm)
	}

	creds := &deviceFlowCreds{cachePath: cachePath}
	if err := creds.loadCachedToken(); err != nil {
		t.Fatalf("loadCachedToken: %v", err)
	}
	md, err := creds.GetRequestMetadata(ctx)
	if err != nil {
		t.Fatalf("GetRequestMetadata: %v", err)
	}
	if want := "Bearer round-trip-access-token"; md["authorization"] != want {
		t.Errorf("authorization metadata = %q, want %q", md["authorization"], want)
	}
	if !creds.RequireTransportSecurity() {
		t.Error("expected RequireTransportSecurity to be true for a device-flow credential")
	}
}

// TestDeviceFlow_SlowDownBackoff asserts the poller increases its interval
// on a slow_down response per RFC 8628 section 3.5, rather than continuing
// to poll at the original interval.
func TestDeviceFlow_SlowDownBackoff(t *testing.T) {
	server := newFakeDeviceOIDCServer(t, 1, func(pollN int, r *http.Request) (int, map[string]any) {
		switch pollN {
		case 1:
			return http.StatusBadRequest, errorBody("authorization_pending")
		case 2:
			return http.StatusBadRequest, errorBody("slow_down")
		default:
			return http.StatusOK, successBody("slow-down-access-token", "slow-down-refresh-token")
		}
	})

	cachePath := filepath.Join(t.TempDir(), "token.json")
	config := DeviceFlowConfig{IssuerURL: server.URL, ClientID: "test-client", TokenCachePath: cachePath}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := NewDeviceFlowDialOption(ctx, config); err != nil {
		t.Fatalf("NewDeviceFlowDialOption: %v", err)
	}

	// 3, not 2: the pending response (poll 1) is immediately followed, same
	// tick, by x/oauth2's alternate-auth-style retry (poll 2), which lands
	// on our handler's slow_down response; only then does the ticker fire
	// again for the (now backed-off) success poll (poll 3). See the
	// FullRoundTrip test above for the same request-doubling-on-error
	// mechanism.
	if got := server.tokenCalls(); got != 3 {
		t.Fatalf("token endpoint polled %d times, want 3 (pending, slow_down, success)", got)
	}

	// Gap between poll 1 (pending) and poll 2 (slow_down) is near-instant --
	// both happen within the same tick, before any ticker wait. Gap between
	// poll 2 (slow_down) and poll 3 (success) must reflect the RFC 8628
	// 3.5-mandated +5s backoff on top of the original 1s interval -- i.e. be
	// substantially larger than the first gap, not merely nonzero.
	firstGap := server.pollGap(0, 1)
	backoffGap := server.pollGap(1, 2)
	if backoffGap <= firstGap {
		t.Errorf("expected the post-slow_down gap (%s) to exceed the pre-slow_down gap (%s)", backoffGap, firstGap)
	}
	if backoffGap < 4*time.Second {
		t.Errorf("post-slow_down gap = %s, want at least ~5s (RFC 8628 slow_down increment)", backoffGap)
	}
}

// TestDeviceFlow_ExpiredToken asserts an expired_token response from the
// token endpoint produces a clear terminal error (no infinite poll).
func TestDeviceFlow_ExpiredToken(t *testing.T) {
	server := newFakeDeviceOIDCServer(t, 1, func(pollN int, r *http.Request) (int, map[string]any) {
		return http.StatusBadRequest, errorBody("expired_token")
	})

	cachePath := filepath.Join(t.TempDir(), "token.json")
	config := DeviceFlowConfig{IssuerURL: server.URL, ClientID: "test-client", TokenCachePath: cachePath}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	_, err := NewDeviceFlowDialOption(ctx, config)
	if err == nil {
		t.Fatal("expected an error for an expired device code, got nil")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("error %q does not clearly describe expiry", err.Error())
	}
	// x/oauth2's DeviceAccessToken makes at most 2 raw HTTP requests per
	// ticker tick when every response is an error (it retries once with an
	// alternate client-auth style before giving up on that tick -- see
	// golang.org/x/oauth2/internal.RetrieveToken's needsAuthStyleProbe). The
	// property this test guards is boundedness -- no infinite poll after a
	// terminal expired_token -- not the literal request count.
	if got := server.tokenCalls(); got == 0 || got > 2 {
		t.Errorf("token endpoint polled %d times, want a small bounded number (<=2), not zero or unbounded", got)
	}
	if _, statErr := os.Stat(cachePath); statErr == nil {
		t.Error("expected no cache file to be written after a failed device authorization")
	}
}

// TestDeviceFlow_CachedRefreshToken_NoPrompt asserts that when a valid
// refresh token is already cached, a second call to
// NewDeviceFlowDialOption uses it silently -- no interactive device code
// prompt, no call to the device authorization endpoint.
func TestDeviceFlow_CachedRefreshToken_NoPrompt(t *testing.T) {
	server := newFakeDeviceOIDCServer(t, 1, func(pollN int, r *http.Request) (int, map[string]any) {
		return http.StatusOK, successBody("cached-flow-access-token", "cached-flow-refresh-token")
	})

	cachePath := filepath.Join(t.TempDir(), "token.json")
	config := DeviceFlowConfig{IssuerURL: server.URL, ClientID: "test-client", TokenCachePath: cachePath}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if _, err := NewDeviceFlowDialOption(ctx, config); err != nil {
		t.Fatalf("first NewDeviceFlowDialOption: %v", err)
	}
	if got := server.deviceAuthCalls(); got != 1 {
		t.Fatalf("expected 1 device authorization call after the first (interactive) invocation, got %d", got)
	}
	if got := server.tokenCalls(); got != 1 {
		t.Fatalf("expected 1 token poll after the first invocation, got %d", got)
	}

	// Second invocation: a valid refresh token is already cached, so this
	// must succeed with no additional HTTP calls to either the device
	// authorization or token endpoints.
	if _, err := NewDeviceFlowDialOption(ctx, config); err != nil {
		t.Fatalf("second NewDeviceFlowDialOption (cache hit): %v", err)
	}
	if got := server.deviceAuthCalls(); got != 1 {
		t.Errorf("device authorization endpoint called again on cache hit: now %d calls, want still 1", got)
	}
	if got := server.tokenCalls(); got != 1 {
		t.Errorf("token endpoint called again on cache hit: now %d calls, want still 1", got)
	}
}

// TestDeviceFlow_TokenCacheFilePermissions asserts the token cache file is
// created with mode 0600 and rejected (triggering re-authentication rather
// than being trusted) if found with a more permissive mode.
func TestDeviceFlow_TokenCacheFilePermissions(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "token.json")
	creds := &deviceFlowCreds{cachePath: cachePath}

	tok := &oauth2.Token{
		AccessToken:  "perm-test-access-token",
		RefreshToken: "perm-test-refresh-token",
		Expiry:       time.Now().Add(time.Hour),
	}
	if err := creds.saveCachedToken(tok); err != nil {
		t.Fatalf("saveCachedToken: %v", err)
	}

	info, err := os.Stat(cachePath)
	if err != nil {
		t.Fatalf("stat cache file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("cache file mode = %o, want 0600", perm)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("cache file mode %o is world- or group-readable", perm)
	}

	fresh := &deviceFlowCreds{cachePath: cachePath}
	if err := fresh.loadCachedToken(); err != nil {
		t.Fatalf("loadCachedToken on a freshly-written 0600 file: %v", err)
	}

	// Widen permissions and confirm the cache is rejected rather than
	// trusted -- this is the re-authenticate-on-tamper path, not a
	// best-effort warning.
	if err := os.Chmod(cachePath, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	permissive := &deviceFlowCreds{cachePath: cachePath}
	if err := permissive.loadCachedToken(); err == nil {
		t.Fatal("expected loadCachedToken to reject a 0644 cache file, got nil error")
	}
}

// TestDeviceFlow_NoTokenInLogsOrErrors asserts NFR13: no access or refresh
// token value appears in captured log output or in any error returned by
// this package's device flow code paths, including failure paths.
func TestDeviceFlow_NoTokenInLogsOrErrors(t *testing.T) {
	const secretAccess = "super-secret-access-token-value"
	const secretRefresh = "super-secret-refresh-token-value"

	server := newFakeDeviceOIDCServer(t, 1, func(pollN int, r *http.Request) (int, map[string]any) {
		return http.StatusOK, successBody(secretAccess, secretRefresh)
	})

	cachePath := filepath.Join(t.TempDir(), "token.json")
	config := DeviceFlowConfig{IssuerURL: server.URL, ClientID: "test-client", TokenCachePath: cachePath}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Capture everything performDeviceFlow prints (verification URI, user
	// code) and confirm neither secret appears in it.
	origStderr := os.Stderr
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("os.Pipe: %v", pipeErr)
	}
	os.Stderr = w
	_, err := NewDeviceFlowDialOption(ctx, config)
	w.Close()
	os.Stderr = origStderr
	if err != nil {
		t.Fatalf("NewDeviceFlowDialOption: %v", err)
	}

	var captured bytes.Buffer
	if _, copyErr := io.Copy(&captured, r); copyErr != nil {
		t.Fatalf("reading captured stderr: %v", copyErr)
	}
	if strings.Contains(captured.String(), secretAccess) {
		t.Errorf("stderr output contains the access token value:\n%s", captured.String())
	}
	if strings.Contains(captured.String(), secretRefresh) {
		t.Errorf("stderr output contains the refresh token value:\n%s", captured.String())
	}

	// A permission-rejected cache file: the file on disk still contains the
	// secrets, but the returned error must not.
	if err := os.Chmod(cachePath, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	permissive := &deviceFlowCreds{cachePath: cachePath}
	loadErr := permissive.loadCachedToken()
	if loadErr == nil {
		t.Fatal("expected loadCachedToken to reject the widened-permission cache file")
	}
	if strings.Contains(loadErr.Error(), secretAccess) || strings.Contains(loadErr.Error(), secretRefresh) {
		t.Errorf("loadCachedToken permission error leaks token value: %v", loadErr)
	}

	// A failed refresh must not leak the refresh token it attempted to use.
	refreshFailServer := newFakeDeviceOIDCServer(t, 1, func(pollN int, r *http.Request) (int, map[string]any) {
		return http.StatusBadRequest, map[string]any{"error": "invalid_grant", "error_description": "refresh token is invalid or expired"}
	})
	endpoints, discErr := discoverDeviceEndpoints(ctx, refreshFailServer.URL)
	if discErr != nil {
		t.Fatalf("discoverDeviceEndpoints: %v", discErr)
	}
	failingCreds := &deviceFlowCreds{
		cachePath: filepath.Join(t.TempDir(), "unused.json"),
		oauthConfig: &oauth2.Config{
			ClientID: "test-client",
			Endpoint: oauth2.Endpoint{
				DeviceAuthURL: endpoints.DeviceAuthorizationEndpoint,
				TokenURL:      endpoints.TokenEndpoint,
			},
		},
		token: &oauth2.Token{
			AccessToken:  "expired-access-token",
			RefreshToken: secretRefresh,
			Expiry:       time.Now().Add(-time.Hour),
		},
	}
	refreshErr := failingCreds.refreshToken(ctx)
	if refreshErr == nil {
		t.Fatal("expected refreshToken to fail against a server returning invalid_grant")
	}
	if strings.Contains(refreshErr.Error(), secretRefresh) {
		t.Errorf("refreshToken error leaks the refresh token value: %v", refreshErr)
	}
}

// TestDeviceFlow_ClaimsMatchBrowserToken asserts the credential produces
// the same Claims (subject, realm roles) as a browser-issued token from the
// same user, using this package's verifier from interceptors_test.go --
// proving FR81's "same principal, same authorization outcome" requirement
// rather than merely that a token is sent.
func TestDeviceFlow_ClaimsMatchBrowserToken(t *testing.T) {
	const deviceIssuedToken = "device-flow-access-token-for-alice"
	const browserIssuedToken = "browser-session-access-token-for-alice"

	// Models what a real Keycloak instance does: both tokens belong to the
	// same human, alice, so the server-side verifier resolves them to
	// identical Claims regardless of which flow produced the token.
	expectedClaims := &Claims{Subject: "alice", Roles: []string{"leaflab-operator"}}
	verifier := &mockVerifier{
		verifyFn: func(ctx context.Context, rawToken string) (*Claims, error) {
			switch rawToken {
			case deviceIssuedToken, browserIssuedToken:
				return expectedClaims, nil
			default:
				return nil, errors.New("unrecognized token")
			}
		},
	}

	server := newFakeDeviceOIDCServer(t, 1, func(pollN int, r *http.Request) (int, map[string]any) {
		return http.StatusOK, successBody(deviceIssuedToken, "device-flow-refresh-token-for-alice")
	})

	cachePath := filepath.Join(t.TempDir(), "token.json")
	config := DeviceFlowConfig{IssuerURL: server.URL, ClientID: "test-client", TokenCachePath: cachePath}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if _, err := NewDeviceFlowDialOption(ctx, config); err != nil {
		t.Fatalf("NewDeviceFlowDialOption: %v", err)
	}

	// Pull the credential's forwarded access token via GetRequestMetadata,
	// exactly as a real gRPC call would.
	creds := &deviceFlowCreds{cachePath: cachePath}
	if err := creds.loadCachedToken(); err != nil {
		t.Fatalf("loadCachedToken: %v", err)
	}
	md, err := creds.GetRequestMetadata(ctx)
	if err != nil {
		t.Fatalf("GetRequestMetadata: %v", err)
	}
	forwarded := strings.TrimPrefix(md["authorization"], "Bearer ")
	if forwarded != deviceIssuedToken {
		t.Fatalf("forwarded token = %q, want %q", forwarded, deviceIssuedToken)
	}

	deviceClaims, err := verifier.Verify(ctx, forwarded)
	if err != nil {
		t.Fatalf("verifying the device-flow token: %v", err)
	}
	browserClaims, err := verifier.Verify(ctx, browserIssuedToken)
	if err != nil {
		t.Fatalf("verifying the browser-issued token: %v", err)
	}

	if deviceClaims.Subject != browserClaims.Subject {
		t.Errorf("subject mismatch: device=%q browser=%q", deviceClaims.Subject, browserClaims.Subject)
	}
	if !reflect.DeepEqual(deviceClaims.Roles, browserClaims.Roles) {
		t.Errorf("roles mismatch: device=%v browser=%v", deviceClaims.Roles, browserClaims.Roles)
	}
}
