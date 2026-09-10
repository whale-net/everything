package grpcauth

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/whale-net/everything/libs/go/grpcauth/internal/keycloakfake"
)

// captureLogs redirects the package-level slog default to a buffer for the
// duration of the calling test, restoring the previous default on cleanup --
// mirrors manmanv2/host/workshop/cache_client_test.go's captureLogs, used
// here for NFR1 (never log the refresh token or client secret) and the
// "log once, not per-revoke-storm" assertion.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(original) })
	return buf
}

// newRevokeTestSource builds a *DelegatedGrantSource pointed at fake's
// endpoints verbatim (NFR4: never a live Keycloak).
func newRevokeTestSource(t *testing.T, fake *keycloakfake.Server) *DelegatedGrantSource {
	t.Helper()
	src, err := NewDelegatedGrantSource(context.Background(), validDelegatedGrantConfig(fake))
	if err != nil {
		t.Fatalf("NewDelegatedGrantSource: %v", err)
	}
	return src
}

// TestRevokeRefreshToken_SendsExpectedRequest proves RevokeRefreshToken
// sends RFC 7009's token/token_type_hint form fields plus this source's
// client credentials (FR14), and that the fake recorded exactly one revoke
// call.
func TestRevokeRefreshToken_SendsExpectedRequest(t *testing.T) {
	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)

	cfg := validDelegatedGrantConfig(fake)
	src, err := NewDelegatedGrantSource(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewDelegatedGrantSource: %v", err)
	}

	const refreshToken = "the-refresh-token-value"
	if err := src.RevokeRefreshToken(context.Background(), refreshToken); err != nil {
		t.Fatalf("RevokeRefreshToken: %v", err)
	}

	if got := fake.RevokeCalls(); got != 1 {
		t.Fatalf("RevokeCalls() = %d, want 1", got)
	}

	got := fake.LastRevokeRequest()
	if !got.Called {
		t.Fatal("LastRevokeRequest().Called = false, want true")
	}
	if got.Token != refreshToken {
		t.Fatalf("LastRevokeRequest().Token = %q, want %q", got.Token, refreshToken)
	}
	if got.TokenTypeHint != "refresh_token" {
		t.Fatalf("LastRevokeRequest().TokenTypeHint = %q, want %q", got.TokenTypeHint, "refresh_token")
	}
	if !got.BasicAuthPresent {
		t.Fatal("LastRevokeRequest().BasicAuthPresent = false, want true (client must authenticate, FR14)")
	}
	if got.ClientID != cfg.ClientID {
		t.Fatalf("LastRevokeRequest().ClientID = %q, want %q", got.ClientID, cfg.ClientID)
	}
	if got.ClientSecret != cfg.ClientSecret {
		t.Fatalf("LastRevokeRequest().ClientSecret = %q, want %q", got.ClientSecret, cfg.ClientSecret)
	}
}

// TestRevokeRefreshToken_NonOKStatus_ReturnsError proves a non-2xx response
// from the revocation endpoint surfaces as an error to the caller -- it is
// pgstore.Revoke's job to swallow it, not this method's.
func TestRevokeRefreshToken_NonOKStatus_ReturnsError(t *testing.T) {
	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)
	fake.SetRevokeMode(keycloakfake.ModeServerError)

	src := newRevokeTestSource(t, fake)

	if err := src.RevokeRefreshToken(context.Background(), "some-refresh-token"); err == nil {
		t.Fatal("RevokeRefreshToken with ModeServerError: got nil error, want error")
	}
}

// TestRevokeRefreshToken_ConnectionFailure_ReturnsError proves a transport
// failure (connection dropped, per keycloakfake.ModeConnectionFailure) also
// surfaces as an error.
func TestRevokeRefreshToken_ConnectionFailure_ReturnsError(t *testing.T) {
	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)
	fake.SetRevokeMode(keycloakfake.ModeConnectionFailure)

	src := newRevokeTestSource(t, fake)

	if err := src.RevokeRefreshToken(context.Background(), "some-refresh-token"); err == nil {
		t.Fatal("RevokeRefreshToken with ModeConnectionFailure: got nil error, want error")
	}
}

// TestRevokeRefreshToken_NFR1_NoSecretsInErrorOrLogs proves neither the
// returned error nor any emitted log line ever contains the refresh token
// or the client secret, across both the non-OK-response failure path and
// the missing-revocation_endpoint skip path.
func TestRevokeRefreshToken_NFR1_NoSecretsInErrorOrLogs(t *testing.T) {
	const refreshToken = "nfr1-super-secret-refresh-token"

	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)
	fake.SetRevokeMode(keycloakfake.ModeServerError)

	cfg := validDelegatedGrantConfig(fake)
	src, err := NewDelegatedGrantSource(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewDelegatedGrantSource: %v", err)
	}

	logs := captureLogs(t)
	err = src.RevokeRefreshToken(context.Background(), refreshToken)
	if err == nil {
		t.Fatal("RevokeRefreshToken with ModeServerError: got nil error, want error")
	}
	assertNoSecrets(t, err.Error(), refreshToken, cfg.ClientSecret)
	assertNoSecrets(t, logs.String(), refreshToken, cfg.ClientSecret)
}

// TestRevokeRefreshToken_MissingRevocationEndpoint_SkipsAndLogsOnce proves
// that when Endpoints.Revocation is empty (discovery did not advertise
// one), RevokeRefreshToken returns nil without ever calling the fake, and
// logs exactly one WARNING regardless of how many times it is called --
// not once per call, which would storm the log during a revoke-heavy
// workload.
func TestRevokeRefreshToken_MissingRevocationEndpoint_SkipsAndLogsOnce(t *testing.T) {
	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)
	fake.OmitRevocationEndpoint()

	cfg := validDelegatedGrantConfig(fake)
	cfg.Endpoints = Endpoints{} // force discovery, which will omit Revocation
	src, err := NewDelegatedGrantSource(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewDelegatedGrantSource: %v", err)
	}
	if src.Endpoints().Revocation != "" {
		t.Fatalf("Endpoints().Revocation = %q, want empty", src.Endpoints().Revocation)
	}

	logs := captureLogs(t)

	for i := 0; i < 3; i++ {
		if err := src.RevokeRefreshToken(context.Background(), "some-refresh-token"); err != nil {
			t.Fatalf("RevokeRefreshToken call %d: %v", i, err)
		}
	}

	if got := fake.RevokeCalls(); got != 0 {
		t.Fatalf("RevokeCalls() = %d, want 0 (no revocation_endpoint to call)", got)
	}

	warnCount := strings.Count(logs.String(), "level=WARN")
	if warnCount != 1 {
		t.Fatalf("WARN log lines = %d, want exactly 1 across 3 calls (log once, not per-revoke-storm); log:\n%s", warnCount, logs.String())
	}
	assertNoSecrets(t, logs.String(), "some-refresh-token", cfg.ClientSecret)
}

// assertNoSecrets fails the test if s contains any of the given secret
// values.
func assertNoSecrets(t *testing.T, s string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if strings.Contains(s, secret) {
			t.Fatalf("value unexpectedly contains a secret %q:\n%s", secret, s)
		}
	}
}
