package server

import (
	"net/http"
	"strings"

	"github.com/whale-net/everything/libs/go/auth"
	"github.com/whale-net/everything/libs/go/grpcauth"
)

// NewHandler serves mcpHandler behind HTTPAuth, with /healthz and RFC 9728
// protected-resource metadata outside it so MCP clients can discover the
// authorization server. publicURL is the externally reachable MCP URL;
// metadataURLOverride, if set, replaces the derived metadata URL in 401
// challenges.
func NewHandler(mcpHandler http.Handler, v grpcauth.TokenVerifier, issuer, publicURL, metadataURLOverride string) http.Handler {
	meta := auth.ProtectedResourceMetadataConfig{
		Resource:            strings.TrimRight(publicURL, "/"),
		AuthorizationServer: issuer,
		ResourceName:        "manmanv2 MCP",
	}
	metadataURL := auth.ResourceServerBearerOptions(meta).ResourceMetadataURL
	if metadataURLOverride != "" {
		metadataURL = metadataURLOverride
	}
	return auth.NewResourceServerMux(meta, map[string]http.Handler{"/": HTTPAuth(v, metadataURL)(mcpHandler)})
}
