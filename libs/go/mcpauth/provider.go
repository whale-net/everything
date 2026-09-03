package mcpauth

import (
	"errors"
	"net/http"
)

// ProviderConfig configures NewProvider.
type ProviderConfig struct {
	// Issuer is this authorization server's issuer identifier: an https URL
	// with no trailing slash. It is also the base every endpoint URL this
	// package advertises (in both metadata documents) is built from.
	// Required.
	//
	// Scaffold note: NewProvider currently only checks Issuer is non-empty.
	// The Implementation phase of #1641 adds the "must be an absolute URL"
	// format check the issue's Implementation section requires.
	Issuer string

	// Resource is the MCP server's resource identifier — the URL an MCP
	// client connects to — as it appears in protected-resource metadata's
	// `resource` field (RFC 9728 §2). Required. Same scaffold note as
	// Issuer applies: only a non-empty check runs today.
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
}

// Provider is the object every OAuth2 endpoint mcpauth serves hangs off:
// discovery metadata (RFC 9728/8414), dynamic client registration
// (RFC 7591), and — landed by #1642 — the authorization-code + PKCE
// `/authorize` and `/token` endpoints.
type Provider struct {
	cfg ProviderConfig
}

// NewProvider constructs a Provider from cfg, defaulting cfg.Clients to
// NewMemoryClientRegistry() when left nil.
//
// Scaffold note: this constructor currently only checks that Issuer and
// Resource are non-empty and that Resolver and Credentials are non-nil.
// The Implementation phase of #1641 adds the "Issuer/Resource must be an
// absolute URL" format validation the issue's Implementation section
// requires — see that section. It must never panic at request time either
// way; every failure here is a returned error.
func NewProvider(cfg ProviderConfig) (*Provider, error) {
	if cfg.Issuer == "" {
		return nil, errors.New("mcpauth: ProviderConfig.Issuer is required")
	}
	if cfg.Resource == "" {
		return nil, errors.New("mcpauth: ProviderConfig.Resource is required")
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

	return &Provider{cfg: cfg}, nil
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
// above).
//
// Scaffold note: every handler Mount wires in currently answers with a
// fixed 501 (see notImplementedHandler, and metadata.go/clients.go's
// per-handler stubs) — this settles the routing shape (which path serves
// which concern) that later phases fill in, not the response bodies.
// `/authorize` and `/token` in particular stay 501 for the lifetime of
// this issue; #1642 lands their authorization-code + PKCE bodies.
func (p *Provider) Mount(mux *http.ServeMux) {
	mux.Handle("GET "+protectedResourceMetadataPath, p.protectedResourceMetadataHandler())
	mux.Handle("GET "+authServerMetadataPath, p.authServerMetadataHandler())
	mux.HandleFunc("POST "+registerPath, p.handleRegister)
	mux.HandleFunc(authorizePath, notImplementedHandler)
	mux.HandleFunc(tokenPath, notImplementedHandler)
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
