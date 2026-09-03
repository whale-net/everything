package mcpauth

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// defaultAuthCodeTTL is ProviderConfig.AuthCodeTTL's default: how long a
// minted authorization code remains redeemable before POST /token rejects
// it as expired, per OAuth 2.1 guidance that codes be short-lived.
const defaultAuthCodeTTL = 60 * time.Second

// ProviderConfig configures NewProvider.
type ProviderConfig struct {
	// Issuer is this authorization server's issuer identifier: an https URL
	// (or an http URL on loopback, for local dev/tests — see
	// validateAbsoluteURL) with no trailing slash. It is also the base
	// every endpoint URL this package advertises (in both metadata
	// documents) is built from. Required.
	Issuer string

	// Resource is the MCP server's resource identifier — the URL an MCP
	// client connects to — as it appears in protected-resource metadata's
	// `resource` field (RFC 9728 §2). Required; validated the same way as
	// Issuer (see validateAbsoluteURL).
	Resource string

	// ResourceName is the protected-resource metadata's human-readable
	// `resource_name` (RFC 9728 §2), shown to end users by some clients.
	ResourceName string

	// ScopesSupported is advertised on both metadata documents
	// (`scopes_supported`). May be left empty if this provider does not
	// use OAuth2 scopes.
	ScopesSupported []string

	// Resolver resolves the already-authenticated caller behind an
	// `/authorize` request to a stable identity (#1640). Required.
	Resolver CallerResolver

	// Credentials mints the opaque bearer credential `/token` (#1642)
	// exchanges an authorization code for. Required.
	Credentials CredentialStore

	// Clients stores dynamically registered OAuth2 clients (RFC 7591).
	// Defaults to NewMemoryClientRegistry() — see that constructor's doc
	// for why a multi-replica deployment must instead set this to
	// NewPostgresClientRegistry(...).
	Clients ClientRegistry

	// SignInURL, if set, is where `/authorize` redirects a caller Resolver
	// could not resolve, so they can establish a session and retry (see
	// #1642).
	SignInURL string

	// AuthCodes stores pending OAuth2 authorization codes minted by
	// `/authorize` and redeemed by `/token` (#1642). Defaults to
	// NewMemoryAuthCodeStore() — see that constructor's doc for why a
	// multi-replica deployment must instead set this to
	// NewPostgresAuthCodeStore(...): `/authorize` and `/token` can land
	// on different replicas.
	AuthCodes AuthCodeStore

	// AuthCodeTTL is how long a minted authorization code remains
	// redeemable before `/token` rejects it as expired. Defaults to 60s
	// (defaultAuthCodeTTL), per OAuth 2.1 guidance that codes be
	// short-lived.
	AuthCodeTTL time.Duration
}

// Provider is the object every OAuth2 endpoint mcpauth serves hangs off:
// discovery metadata (RFC 9728/8414), dynamic client registration
// (RFC 7591), and — landed by #1642 — the authorization-code + PKCE
// `/authorize` and `/token` endpoints.
type Provider struct {
	cfg ProviderConfig
}

// NewProvider constructs a Provider from cfg, defaulting cfg.Clients to
// NewMemoryClientRegistry() when left nil. Issuer and Resource are
// validated as absolute URLs (see validateAbsoluteURL) and Resolver and
// Credentials must be non-nil. NewProvider never panics — every
// construction-time failure is a returned error, not a request-time panic.
func NewProvider(cfg ProviderConfig) (*Provider, error) {
	if cfg.Issuer == "" {
		return nil, errors.New("mcpauth: ProviderConfig.Issuer is required")
	}
	if err := validateAbsoluteURL(cfg.Issuer); err != nil {
		return nil, fmt.Errorf("mcpauth: ProviderConfig.Issuer %q is invalid: %w", cfg.Issuer, err)
	}
	if strings.HasSuffix(cfg.Issuer, "/") {
		return nil, fmt.Errorf("mcpauth: ProviderConfig.Issuer %q must not have a trailing slash", cfg.Issuer)
	}
	if cfg.Resource == "" {
		return nil, errors.New("mcpauth: ProviderConfig.Resource is required")
	}
	if err := validateAbsoluteURL(cfg.Resource); err != nil {
		return nil, fmt.Errorf("mcpauth: ProviderConfig.Resource %q is invalid: %w", cfg.Resource, err)
	}
	if cfg.Resolver == nil {
		return nil, errors.New("mcpauth: ProviderConfig.Resolver is required")
	}
	if cfg.Credentials == nil {
		return nil, errors.New("mcpauth: ProviderConfig.Credentials is required")
	}
	if cfg.Clients == nil {
		cfg.Clients = NewMemoryClientRegistry()
	}
	if cfg.AuthCodes == nil {
		cfg.AuthCodes = NewMemoryAuthCodeStore()
	}
	if cfg.AuthCodeTTL == 0 {
		cfg.AuthCodeTTL = defaultAuthCodeTTL
	}

	return &Provider{cfg: cfg}, nil
}

// validateAbsoluteURL checks raw is an absolute URL with a host, using
// https — or http, but only when the host is loopback (127.0.0.1, ::1, or
// "localhost"): the shape a local dev server or an httptest-backed test
// harness uses. This is the same scheme rule clients.go's
// validateRedirectURI applies to registered redirect URIs, because Issuer
// and Resource are both URLs a browser or native MCP client dereferences
// directly, exactly like a redirect URI.
func validateAbsoluteURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("must be a valid URL")
	}
	if !u.IsAbs() || u.Host == "" {
		return errors.New("must be an absolute URL with a scheme and host")
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
		return errors.New("must use https (http is only allowed on loopback)")
	default:
		return errors.New("must use http or https")
	}
}

// isLoopbackHost reports whether host (already stripped of any port, e.g.
// via url.URL.Hostname()) is a loopback address: "localhost", or an IP
// address for which net.IP.IsLoopback reports true (127.0.0.0/8, ::1).
func isLoopbackHost(host string) bool {
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Fixed endpoint paths. These are not configurable: Mount registers every
// OAuth2 endpoint this provider serves at exactly these paths, and the
// metadata this provider advertises (metadata.go) is built from them —
// metadata/route drift is exactly what metadata_test.go's advertised-URL
// probe (Testing phase) guards against.
const (
	protectedResourceMetadataPath = "/.well-known/oauth-protected-resource"
	authServerMetadataPath        = "/.well-known/oauth-authorization-server"
	registerPath                  = "/register"
	authorizePath                 = "/authorize"
	tokenPath                     = "/token"
)

// Mount registers every OAuth2 endpoint this provider serves on mux, at
// paths matching the metadata it advertises (see the path constants
// above). Discovery metadata (RFC 9728/8414) and dynamic client
// registration (RFC 7591) are fully implemented here; `/authorize`
// (authorize.go) and `/token` (token.go) route to their real handlers but
// those handler bodies stay 501 (notImplementedHandler) until the
// Implementation phase of #1642 lands their authorization-code + PKCE
// logic.
func (p *Provider) Mount(mux *http.ServeMux) {
	// The two metadata endpoints are registered on the bare path, not a
	// "GET <path>" method-restricted pattern: both handlers answer OPTIONS
	// themselves (CORS preflight, 204 no body) as well as GET, and Go's
	// ServeMux would otherwise 405 an OPTIONS request against a
	// GET-restricted pattern before the handler ever ran.
	mux.Handle(protectedResourceMetadataPath, p.protectedResourceMetadataHandler())
	mux.Handle(authServerMetadataPath, p.authServerMetadataHandler())
	mux.HandleFunc("POST "+registerPath, p.handleRegister)
	mux.HandleFunc(authorizePath, p.handleAuthorize)
	mux.HandleFunc(tokenPath, p.handleToken)
}

// notImplementedHandler is the fixed 501 body shared by every endpoint this
// package has not yet implemented behind its real logic.
func notImplementedHandler(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "mcpauth: not implemented yet", http.StatusNotImplemented)
}

// ResourceMetadataURL is the value a consuming domain passes as
// sdkauth.RequireBearerTokenOptions.ResourceMetadataURL (#1640) so the 401
// challenge points discovery at this provider.
func (p *Provider) ResourceMetadataURL() string {
	return p.cfg.Issuer + protectedResourceMetadataPath
}
