package server

import (
	"net/http"
	"strings"

	"github.com/whale-net/everything/libs/go/auth"
)

// NewHandler serves mcpHandler behind HTTPAuthWith, with /healthz and RFC
// 9728 protected-resource metadata outside it so MCP clients can discover
// the authorization server (authorizationServer: the UI hosting
// /authorize, /token and /register, or the OIDC issuer in direct-Keycloak
// mode). publicURL is the externally reachable MCP URL; metadataURLOverride,
// if set, replaces the derived metadata URL in 401 challenges.
func NewHandler(mcpHandler http.Handler, verify CallerVerifier, authorizationServer, publicURL, metadataURLOverride string, wrap ...func(resourceMetadataURL string) func(http.Handler) http.Handler) http.Handler {
	meta := auth.ProtectedResourceMetadataConfig{
		Resource:            strings.TrimRight(publicURL, "/"),
		AuthorizationServer: authorizationServer,
		ResourceName:        "manmanv2 MCP",
	}
	metadataURL := auth.ResourceServerBearerOptions(meta).ResourceMetadataURL
	if metadataURLOverride != "" {
		metadataURL = metadataURLOverride
	}
	guard := HTTPAuthWith(verify, metadataURL)
	if len(wrap) > 0 && wrap[0] != nil {
		guard = wrap[0](metadataURL) // replaces the plain guard; it routes to verify itself
	}
	return auth.NewResourceServerMux(meta, map[string]http.Handler{"/": guard(mcpHandler)})
}
