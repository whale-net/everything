package mcpauth

import (
	"encoding/json"
	"net/http"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// protectedResourceMetadataHandler builds and serves RFC 9728
// protected-resource metadata for this Provider, via the MCP Go SDK's
// sdkauth.ProtectedResourceMetadataHandler (which also sets the CORS
// headers and answers OPTIONS an MCP client's browser-based discovery
// needs — see that function's doc). The metadata itself is assembled from
// p.cfg per the Implementation section of #1641: Resource,
// AuthorizationServers: [Issuer], ScopesSupported,
// BearerMethodsSupported: ["header"], ResourceName.
func (p *Provider) protectedResourceMetadataHandler() http.Handler {
	return NewProtectedResourceMetadataHandler(ProtectedResourceMetadataConfig{
		Resource:            p.cfg.Resource,
		AuthorizationServer: p.cfg.Issuer,
		ResourceName:        p.cfg.ResourceName,
		ScopesSupported:     p.cfg.ScopesSupported,
	})
}

// ProtectedResourceMetadataConfig configures
// NewProtectedResourceMetadataHandler for a resource server that hosts
// *only* RFC 9728 protected-resource discovery, because its OAuth2
// authorization server runs on a different process/binary entirely — e.g.
// audience_score_system's `mcp` resource server + `web` authorization
// server split (issue #1646's "Split across two binaries"). A resource
// server in this shape must not construct a full Provider: Provider
// requires a CallerResolver and CredentialStore it may not have (only
// `web`, the authorization server, resolves callers), and Provider.Mount
// would also wire /authorize, /token, and /register, which this task's
// Implementation section explicitly says `mcp` must never mount. This
// config and NewProtectedResourceMetadataHandler are the
// resource-server-only counterpart to Provider for exactly that case.
type ProtectedResourceMetadataConfig struct {
	// Resource is this resource server's own externally reachable URL (RFC
	// 9728 `resource`) — the same value the authorization server's
	// ProviderConfig.Resource must be set to, so the two binaries agree on
	// the OAuth2 resource identifier.
	Resource string

	// AuthorizationServer is the issuer identifier of the OAuth2
	// authorization server protecting Resource (RFC 9728
	// `authorization_servers`) — the same value as that authorization
	// server's own ProviderConfig.Issuer.
	AuthorizationServer string

	// ResourceName is the metadata's human-readable `resource_name` (RFC
	// 9728 §2).
	ResourceName string

	// ScopesSupported is advertised on the metadata document
	// (`scopes_supported`). May be left empty.
	ScopesSupported []string
}

// NewProtectedResourceMetadataHandler serves RFC 9728 protected-resource
// metadata naming cfg.AuthorizationServer as the sole entry in
// `authorization_servers`. Mount it at
// "/.well-known/oauth-protected-resource" on the resource server's own
// mux, at the mux root rather than under any prefix — MCP clients probe
// this fixed well-known location. Pair it with
// ProtectedResourceMetadataURL(cfg.Resource) as
// sdkauth.RequireBearerTokenOptions.ResourceMetadataURL so a 401 challenge
// points MCP clients at it (see RequireBearerToken, verify.go).
func NewProtectedResourceMetadataHandler(cfg ProtectedResourceMetadataConfig) http.Handler {
	meta := &oauthex.ProtectedResourceMetadata{
		Resource:               cfg.Resource,
		AuthorizationServers:   []string{cfg.AuthorizationServer},
		ScopesSupported:        cfg.ScopesSupported,
		BearerMethodsSupported: []string{"header"},
		ResourceName:           cfg.ResourceName,
	}
	return sdkauth.ProtectedResourceMetadataHandler(meta)
}

// ProtectedResourceMetadataURL returns the URL a resource server hosting
// NewProtectedResourceMetadataHandler serves its own protected-resource
// metadata at, given its own resource identifier (the same value passed as
// ProtectedResourceMetadataConfig.Resource). Pass this as
// sdkauth.RequireBearerTokenOptions.ResourceMetadataURL.
func ProtectedResourceMetadataURL(resource string) string {
	return resource + protectedResourceMetadataPath
}

// authServerMetadata is mcpauth's own RFC 8414 authorization-server
// metadata shape — deliberately not oauthex.AuthServerMeta. See the
// "Resolved: JWKSURI" doc comment below for why.
//
// Resolved: JWKSURI (Scaffold-phase decision required by #1641).
//
// oauthex.AuthServerMeta.JWKSURI has no `omitempty` json tag, and RFC 8414
// §2 marks jwks_uri REQUIRED. mcpauth, however, issues its own opaque
// bearer credentials (see mcpauth.go's package doc) — there is no JWKS
// document, no signing key, nothing to publish at a jwks_uri. Serving
// oauthex.AuthServerMeta directly would therefore always emit
// `"jwks_uri":""`: present, but neither absent nor a valid URL. That is a
// worse outcome for an RFC 8414 client than omitting the field outright —
// a client that treats "jwks_uri is present" as "fetch this document" gets
// a broken fetch against an empty string, whereas a client that treats a
// missing jwks_uri as "this authorization server publishes no JWKS"
// degrades gracefully (and none of the three target clients this issue's
// NFR4 names — Claude Desktop, GitHub Copilot, opencode — require
// jwks_uri to bootstrap the discovery chain this package implements: they
// only need issuer/authorization_endpoint/token_endpoint/
// registration_endpoint/code_challenge_methods_supported to get from
// unauthenticated request to a registered client). This package therefore
// defines its own metadata struct, below, that omits JWKSURI entirely
// rather than emitting oauthex.AuthServerMeta with an empty jwks_uri. If a
// future MCP client is found that hard-requires a present jwks_uri,
// revisit here.
type authServerMetadata struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	RegistrationEndpoint              string   `json:"registration_endpoint,omitempty"`
	ScopesSupported                   []string `json:"scopes_supported,omitempty"`
	ResponseTypesSupported            []string `json:"response_types_supported"`
	GrantTypesSupported               []string `json:"grant_types_supported,omitempty"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported,omitempty"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported,omitempty"`
}

// authServerMetadata builds this Provider's RFC 8414 metadata document.
// Per the Implementation section of #1641: ResponseTypesSupported: ["code"],
// GrantTypesSupported: ["authorization_code"],
// CodeChallengeMethodsSupported: ["S256"] (S256 only — "plain" must never
// be advertised or accepted), TokenEndpointAuthMethodsSupported: ["none"]
// (public PKCE clients; mcpauth issues no client secret).
func (p *Provider) authServerMetadataDoc() authServerMetadata {
	return authServerMetadata{
		Issuer:                            p.cfg.Issuer,
		AuthorizationEndpoint:             p.cfg.Issuer + authorizePath,
		TokenEndpoint:                     p.cfg.Issuer + tokenPath,
		RegistrationEndpoint:              p.cfg.Issuer + registerPath,
		ScopesSupported:                   p.cfg.ScopesSupported,
		ResponseTypesSupported:            []string{"code"},
		GrantTypesSupported:               []string{"authorization_code"},
		TokenEndpointAuthMethodsSupported: []string{"none"},
		CodeChallengeMethodsSupported:     []string{"S256"},
	}
}

// authServerMetadataHandler serves this Provider's RFC 8414
// authorization-server metadata (authServerMetadataDoc()), setting the
// same CORS headers sdkauth.ProtectedResourceMetadataHandler sets (OAuth
// metadata is public, discovery-time information — see that function's
// doc) and answering OPTIONS the same way: 204 with no body.
func (p *Provider) authServerMetadataHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(p.authServerMetadataDoc()); err != nil {
			http.Error(w, "mcpauth: failed to encode authorization-server metadata", http.StatusInternalServerError)
			return
		}
	})
}
