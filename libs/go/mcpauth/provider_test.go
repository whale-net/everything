package mcpauth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testResourcePath is appended to the httptest server's own URL to build
// ProviderConfig.Resource, so tests can tell "the issuer" and "the
// resource" apart even though both point at the same test server.
const testResourcePath = "/mcp"

// newTestServer builds a *Provider mounted on a fresh httptest.Server and
// returns both. It resolves the chicken-and-egg problem of Issuer needing
// to be known before NewProvider is called, but the server's URL (which
// becomes Issuer) only being known after httptest.NewServer starts: it
// starts the server against an as-yet-empty http.ServeMux, builds the
// Provider from the now-known srv.URL, then calls p.Mount(mux) on that
// same mux — safe because httptest.Server holds a reference to mux, not a
// copy, and no request is served until after Mount returns.
//
// Credentials is a fakeCredentialStore (verify_test.go); Resolver never
// resolves anyone, because /authorize (the only consumer of Resolver) is
// #1642's concern, not this issue's.
func newTestServer(t *testing.T) (*httptest.Server, *Provider) {
	t.Helper()

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	p, err := NewProvider(ProviderConfig{
		Issuer:          srv.URL,
		Resource:        srv.URL + testResourcePath,
		ResourceName:    "Test MCP Resource",
		ScopesSupported: []string{"mcp"},
		Resolver: CallerResolverFunc(func(r *http.Request) (string, bool) {
			return "", false
		}),
		Credentials: newFakeCredentialStore(),
	})
	require.NoError(t, err)

	p.Mount(mux)
	return srv, p
}

// ── NewProvider validation ──────────────────────────────────────────────

func validProviderConfig() ProviderConfig {
	return ProviderConfig{
		Issuer:      "https://mcp.example.com",
		Resource:    "https://mcp.example.com/mcp",
		Resolver:    CallerResolverFunc(func(r *http.Request) (string, bool) { return "", false }),
		Credentials: newFakeCredentialStore(),
	}
}

func TestNewProvider_ValidConfig_Succeeds(t *testing.T) {
	p, err := NewProvider(validProviderConfig())
	require.NoError(t, err)
	require.NotNil(t, p)
}

func TestNewProvider_DefaultsClientsToMemoryRegistry(t *testing.T) {
	p, err := NewProvider(validProviderConfig())
	require.NoError(t, err)
	assert.IsType(t, &memoryClientRegistry{}, p.cfg.Clients)
}

func TestNewProvider_RejectsMissingOrInvalidRequiredFields(t *testing.T) {
	cases := map[string]func(cfg *ProviderConfig){
		"empty issuer":               func(cfg *ProviderConfig) { cfg.Issuer = "" },
		"issuer not absolute":        func(cfg *ProviderConfig) { cfg.Issuer = "not-a-url" },
		"issuer trailing slash":      func(cfg *ProviderConfig) { cfg.Issuer = "https://mcp.example.com/" },
		"issuer non-loopback http":   func(cfg *ProviderConfig) { cfg.Issuer = "http://mcp.example.com" },
		"empty resource":             func(cfg *ProviderConfig) { cfg.Resource = "" },
		"resource not absolute":      func(cfg *ProviderConfig) { cfg.Resource = "not-a-url" },
		"resource non-loopback http": func(cfg *ProviderConfig) { cfg.Resource = "http://mcp.example.com/mcp" },
		"nil resolver":               func(cfg *ProviderConfig) { cfg.Resolver = nil },
		"nil credentials":            func(cfg *ProviderConfig) { cfg.Credentials = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := validProviderConfig()
			mutate(&cfg)
			p, err := NewProvider(cfg)
			assert.Error(t, err, "case %q must be rejected", name)
			assert.Nil(t, p)
		})
	}
}

func TestNewProvider_AcceptsLoopbackHTTPIssuerAndResource(t *testing.T) {
	cfg := validProviderConfig()
	cfg.Issuer = "http://127.0.0.1:4000"
	cfg.Resource = "http://localhost:4000/mcp"
	p, err := NewProvider(cfg)
	require.NoError(t, err)
	require.NotNil(t, p)
}

func TestNewProvider_NeverPanics(t *testing.T) {
	// Every field zero-valued at once must still return a clean error, not
	// panic.
	assert.NotPanics(t, func() {
		_, _ = NewProvider(ProviderConfig{})
	})
}

// ── validateAbsoluteURL / isLoopbackHost ────────────────────────────────

func TestValidateAbsoluteURL(t *testing.T) {
	valid := []string{
		"https://example.com",
		"https://example.com/path",
		"http://127.0.0.1:8080",
		"http://localhost:8080",
		"http://[::1]:8080",
	}
	for _, u := range valid {
		t.Run(u, func(t *testing.T) {
			assert.NoError(t, validateAbsoluteURL(u))
		})
	}

	invalid := []string{
		"",
		"not-a-url",
		"/relative/path",
		"ftp://example.com",
		"http://example.com", // http, non-loopback
		"https://",           // no host
		"javascript:alert(1)",
	}
	for _, u := range invalid {
		t.Run(u, func(t *testing.T) {
			assert.Error(t, validateAbsoluteURL(u))
		})
	}
}

func TestIsLoopbackHost(t *testing.T) {
	loopback := []string{"localhost", "LOCALHOST", "127.0.0.1", "127.5.5.5", "::1"}
	for _, h := range loopback {
		t.Run(h, func(t *testing.T) {
			assert.True(t, isLoopbackHost(h))
		})
	}

	notLoopback := []string{"", "example.com", "8.8.8.8", "0.0.0.0"}
	for _, h := range notLoopback {
		t.Run(h, func(t *testing.T) {
			assert.False(t, isLoopbackHost(h))
		})
	}
}
