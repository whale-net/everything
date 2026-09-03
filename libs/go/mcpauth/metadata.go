package mcpauth

import (
	"errors"
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
	meta := &oauthex.ProtectedResourceMetadata{
		Resource:               p.cfg.Resource,
		AuthorizationServers:   []string{p.cfg.Issuer},
		ScopesSupported:        p.cfg.ScopesSupported,
		BearerMethodsSupported: []string{"header"},
		ResourceName:           p.cfg.ResourceName,
	}
	return sdkauth.ProtectedResourceMetadataHandler(meta)
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

// errMetadataNotImplemented is returned by authServerMetadataHandler's
// stub body until the Implementation phase of #1641 lands the real CORS
// + OPTIONS + JSON-encoding behavior the issue's Implementation section
// specifies (mirroring the CORS headers sdkauth.ProtectedResourceMetadataHandler
// sets). Scaffold exists to settle authServerMetadata's shape (including
// the JWKSURI decision above), not this handler's body.
var errMetadataNotImplemented = errors.New("mcpauth: authorization-server metadata handler not implemented yet (see issue #1641)")

// authServerMetadataHandler serves this Provider's RFC 8414
// authorization-server metadata.
//
// Scaffold note: stubbed to 501 — authServerMetadataDoc() above already
// settles the document's shape and content; #1641's Implementation phase
// wires it into an http.Handler that sets the same CORS headers
// sdkauth.ProtectedResourceMetadataHandler sets, answers OPTIONS, and
// encodes authServerMetadataDoc() as the response body.
func (p *Provider) authServerMetadataHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = errMetadataNotImplemented
		notImplementedHandler(w, r)
	})
}
