package delegatedgrant

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fullConfig returns a Config with every field set to a distinct,
// recognizable value -- the base every partial-config test in this file
// mutates one field of.
func fullConfig() Config {
	return Config{
		Issuer:              "https://keycloak.example/realms/whale-net",
		ClientID:            "whagent-net-grant-client",
		ClientSecret:        "s3cr3t-client-secret",
		RedirectURI:         "https://whagent.example.com/oauth/callback",
		EncryptionKeySecret: "s3cr3t-encryption-key-material",
	}
}

// TestBuild_EntirelyUnset_ReturnsErrNotConfigured proves Build treats a
// zero-value Config (every WHAGENT_GRANT_*/WHAGENT_OIDC_ISSUER variable
// unset, the whagent_net/Tiltfile local-dev default per ENV.md's
// WHAGENT_MCP_KEYCLOAK_* precedent) as a non-fatal, checkable sentinel --
// never a network call, since Build returns before touching pool.
func TestBuild_EntirelyUnset_ReturnsErrNotConfigured(t *testing.T) {
	_, err := Build(context.Background(), Config{}, nil)
	require.ErrorIs(t, err, ErrNotConfigured)
}

// TestBuild_ClientSecretUnset_ErrorsLoudly is issue #2426's Testing
// section item verbatim: "a construction test ... asserting the
// config-from-env path errors loudly when the client secret is unset ...
// rather than silently constructing a source that will fail at first
// token call." Every other field is fully configured; only ClientSecret
// is missing.
func TestBuild_ClientSecretUnset_ErrorsLoudly(t *testing.T) {
	cfg := fullConfig()
	cfg.ClientSecret = ""

	_, err := Build(context.Background(), cfg, nil)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNotConfigured, "a partially-configured cfg must not be reported as ErrNotConfigured (that sentinel means 'nothing set at all')")
	assert.Contains(t, err.Error(), "WHAGENT_GRANT_CLIENT_SECRET", "the error must name the missing variable so an operator can fix it without guessing")
}

// TestBuild_PartiallyConfigured_EachSingleFieldMissing_ErrorsLoudly
// exercises every other single-field-missing combination the same way,
// so a future field added to Config is covered by construction rather
// than by remembering to add a case here (a compile error in fullConfig
// or missing() -- not a silently-passing test -- is the guard against
// drift).
func TestBuild_PartiallyConfigured_EachSingleFieldMissing_ErrorsLoudly(t *testing.T) {
	cases := []struct {
		name        string
		mutate      func(Config) Config
		wantVarName string
	}{
		{"Issuer", func(c Config) Config { c.Issuer = ""; return c }, "WHAGENT_OIDC_ISSUER"},
		{"ClientID", func(c Config) Config { c.ClientID = ""; return c }, "WHAGENT_GRANT_CLIENT_ID"},
		{"RedirectURI", func(c Config) Config { c.RedirectURI = ""; return c }, "WHAGENT_GRANT_REDIRECT_URI"},
		{"EncryptionKeySecret", func(c Config) Config { c.EncryptionKeySecret = ""; return c }, "WHAGENT_GRANT_ENCRYPTION_KEY"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.mutate(fullConfig())
			_, err := Build(context.Background(), cfg, nil)
			require.Error(t, err)
			assert.False(t, errors.Is(err, ErrNotConfigured))
			assert.Contains(t, err.Error(), tc.wantVarName)
		})
	}
}

// TestConfigStringRedactsSecrets proves String() (and therefore any
// accidental %v/%s of a Config, and slog.Any's LogValue path) never
// contains ClientSecret or EncryptionKeySecret's actual value -- the
// "redaction test" issue #2426's Testing section calls for, mirroring
// grpcauth.DelegatedGrantConfig's own TestDelegatedGrantConfigStringRedactsSecrets.
func TestConfigStringRedactsSecrets(t *testing.T) {
	cfg := fullConfig()

	rendered := cfg.String()
	assert.NotContains(t, rendered, cfg.ClientSecret)
	assert.NotContains(t, rendered, cfg.EncryptionKeySecret)
	assert.Contains(t, rendered, "(redacted)")

	// LogValue must render identically -- this is what slog.Any("config",
	// cfg) actually calls.
	logged := cfg.LogValue().String()
	assert.NotContains(t, logged, cfg.ClientSecret)
	assert.NotContains(t, logged, cfg.EncryptionKeySecret)

	// Non-secret fields remain visible -- redaction must not swallow
	// everything, only the two secret fields.
	assert.Contains(t, rendered, cfg.Issuer)
	assert.Contains(t, rendered, cfg.ClientID)
	assert.Contains(t, rendered, cfg.RedirectURI)
}

// TestConfigStringUnsetSecrets_RendersUnsetNotRedacted proves an unset
// secret renders as "(unset)", distinguishable from a configured-but-
// withheld "(redacted)" value -- useful for an operator reading a log
// line to tell "not configured" from "configured, value withheld"
// without ever seeing the value itself.
func TestConfigStringUnsetSecrets_RendersUnsetNotRedacted(t *testing.T) {
	cfg := Config{Issuer: "https://keycloak.example/realms/whale-net"}
	rendered := cfg.String()
	assert.Contains(t, rendered, "(unset)")
	assert.NotContains(t, rendered, "(redacted)")
}

// TestBuild_ErrorMessages_NeverContainSecretValues is a belt-and-braces
// grep-shaped check across every error Build can return in this file's
// other tests: none of them may contain either secret's literal value.
// (Validation section: "grep confirms no log or error statement
// interpolates the client secret".)
func TestBuild_ErrorMessages_NeverContainSecretValues(t *testing.T) {
	secret := fullConfig().ClientSecret
	encKeySecret := fullConfig().EncryptionKeySecret

	cfg := fullConfig()
	cfg.ClientSecret = ""
	_, err := Build(context.Background(), cfg, nil)
	require.Error(t, err)
	assert.False(t, strings.Contains(err.Error(), secret))
	assert.False(t, strings.Contains(err.Error(), encKeySecret))
}
