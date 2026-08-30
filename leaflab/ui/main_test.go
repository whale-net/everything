package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/whale-net/everything/libs/go/htmxauth"
)

func TestLoadConfig_Defaults(t *testing.T) {
	for _, key := range []string{"HOST", "PORT", "AUTH_MODE", "PG_DATABASE_URL", "LEAFLAB_API_URL", "GRPC_AUTH_MODE"} {
		os.Unsetenv(key)
	}

	cfg := LoadConfig()

	if cfg.Host != "0.0.0.0" {
		t.Errorf("Host = %q, want 0.0.0.0", cfg.Host)
	}
	if cfg.Port != "8000" {
		t.Errorf("Port = %q, want 8000", cfg.Port)
	}
	if cfg.AuthMode != "none" {
		t.Errorf("AuthMode = %q, want none", cfg.AuthMode)
	}
	if cfg.DatabaseURL != "" {
		t.Errorf("DatabaseURL = %q, want empty (no fallback DSN baked in)", cfg.DatabaseURL)
	}
}

// TestNewApp_MissingDatabaseURLHardFails guards NFR3's enforced half:
// leaflab-ui must hard-fail at startup when PG_DATABASE_URL is unset,
// naming the reason (DB-backed sessions), rather than silently falling
// back to cookie-only sessions the way manmanv2/ui does.
func TestNewApp_MissingDatabaseURLHardFails(t *testing.T) {
	cfg := &Config{
		AuthMode:    "none",
		DatabaseURL: "",
	}

	_, err := NewApp(context.Background(), cfg)
	if err == nil {
		t.Fatal("NewApp() with empty DatabaseURL = nil error, want an error naming DB-backed sessions")
	}
	if !strings.Contains(err.Error(), "PG_DATABASE_URL") {
		t.Errorf("NewApp() error = %q, want it to name PG_DATABASE_URL", err.Error())
	}
	if !strings.Contains(strings.ToLower(err.Error()), "db-backed session") {
		t.Errorf("NewApp() error = %q, want it to name DB-backed sessions (NFR3)", err.Error())
	}
}

// TestSetupRoutes_AuthModeNone_HomeRendersWithDevUser covers the Testing
// section's "AUTH_MODE=none: a page route renders with the dev user;
// htmxauth.GetUser is non-nil" requirement end to end through the real
// route wiring in setupRoutes (RequireAuthFunc + WithAccessToken +
// handleHome), without needing a real Postgres: it builds the
// Authenticator directly via htmxauth.NewAuthenticator (cookie-backed, no
// DB) the same way NewApp would for AuthModeNone, sidestepping only the
// PG_DATABASE_URL hard-fail this test isn't about.
//
// This also stands in for the Testing section's "gRPC calls carry the user
// token: assert grpcauth.WithUserToken is applied on the request context
// path" item for leaflab-ui's own wiring: handleHome only runs at all if
// WithAccessToken's token lookup succeeded (see
// htmxauth.Authenticator.WithAccessToken — on failure it redirects instead
// of calling next), and WithAccessToken is what applies
// grpcauth.WithUserToken to the request context before calling next. The
// grpcauth.WithUserToken mechanism itself (a redirect on missing token vs.
// context injection + pass-through on success) is unit-tested directly in
// libs/go/htmxauth/auth_test.go's TestWithAccessToken_* tests; duplicating
// that here would just re-test the shared library instead of this app's
// wiring of it.
func TestSetupRoutes_AuthModeNone_HomeRendersWithDevUser(t *testing.T) {
	auth, err := htmxauth.NewAuthenticator(context.Background(), htmxauth.Config{
		Mode:          htmxauth.AuthModeNone,
		SessionSecret: "test-secret",
		SessionName:   "leaflab-ui-test",
	})
	if err != nil {
		t.Fatalf("htmxauth.NewAuthenticator() error = %v", err)
	}

	app := &App{
		config: &Config{AuthMode: "none"},
		auth:   auth,
	}

	mux := http.NewServeMux()
	app.setupRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Signed in as developer") {
		t.Errorf("GET / body does not show the dev user (htmxauth.GetUser must be non-nil and rendered); body: %s", body)
	}
}
