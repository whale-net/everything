package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/whagent_net/delegatedgrant"
)

// fullyConfiguredGrantConfig returns a config whose WHAGENT_GRANT_*/
// WHAGENT_OIDC_ISSUER fields are all set to distinct, recognizable
// values -- the base every test below mutates.
func fullyConfiguredGrantConfig() config {
	return config{
		OIDCIssuer:         "https://keycloak.example/realms/whale-net",
		GrantClientID:      "whagent-net-grant-client",
		GrantClientSecret:  "s3cr3t-client-secret",
		GrantRedirectURI:   "https://whagent.example.com/oauth/callback",
		GrantEncryptionKey: "s3cr3t-encryption-key-material",
	}
}

// TestInitializeDelegatedGrant_ClientSecretUnset_ErrorsLoudly is issue
// #2426's Testing section item verbatim, exercised at `ui`'s own
// composition-root boundary (not just //whagent_net/delegatedgrant's own
// unit test): the config-from-env path errors loudly when the client
// secret is unset, rather than silently degrading to "not configured" --
// this must be distinguishable from the entirely-unset case (see the
// sibling test below).
//
// A non-nil, never-dialed *pgxpool.Pool is safe to pass here:
// delegatedgrant.Build's partial-configuration check returns before pool
// is ever touched (see its doc comment) -- confirmed by this test not
// needing a real Postgres connection.
func TestInitializeDelegatedGrant_ClientSecretUnset_ErrorsLoudly(t *testing.T) {
	cfg := fullyConfiguredGrantConfig()
	cfg.GrantClientSecret = ""

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, err := initializeDelegatedGrant(context.Background(), cfg, &pgxpool.Pool{}, logger)

	if err == nil {
		t.Fatal("expected an error when WHAGENT_GRANT_CLIENT_SECRET is unset but every other WHAGENT_GRANT_*/WHAGENT_OIDC_ISSUER variable is set, got nil")
	}
	if errors.Is(err, delegatedgrant.ErrNotConfigured) {
		t.Fatalf("expected a loud partial-configuration error, not ErrNotConfigured (that sentinel means \"nothing set at all\"): %v", err)
	}
	if !strings.Contains(err.Error(), "WHAGENT_GRANT_CLIENT_SECRET") {
		t.Fatalf("error = %q, want it to name the missing WHAGENT_GRANT_CLIENT_SECRET variable", err.Error())
	}
	if strings.Contains(err.Error(), cfg.GrantEncryptionKey) {
		t.Fatalf("error leaked EncryptionKeySecret's value: %q", err.Error())
	}
}

// TestInitializeDelegatedGrant_EntirelyUnset_DegradesWithoutError proves
// the "not configured at all" case -- whagent_net/Tiltfile's local-dev
// default -- returns a zero-value Components and a nil error (a WARNING
// log, not a startup failure), distinguishing it from the loud partial
// -configuration case above.
func TestInitializeDelegatedGrant_EntirelyUnset_DegradesWithoutError(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	components, err := initializeDelegatedGrant(context.Background(), config{}, &pgxpool.Pool{}, logger)

	if err != nil {
		t.Fatalf("expected no error when every WHAGENT_GRANT_*/WHAGENT_OIDC_ISSUER variable is unset, got %v", err)
	}
	if components.Source != nil || components.Store != nil || components.Index != nil {
		t.Fatalf("expected a zero-value Components, got %+v", components)
	}
}
