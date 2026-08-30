package main

import (
	"context"
	"os"
	"strings"
	"testing"
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
