package server

import (
	"net/http"
	"strings"

	"github.com/whale-net/everything/libs/go/auth"
	"github.com/whale-net/everything/libs/go/grpcauth"
)

// NewHandler serves mcpHandler behind HTTPAuth, with /healthz and RFC 9728
// protected-resource metadata mounted outside it so MCP clients can discover
// the authorization server. publicURL is the externally reachable MCP URL;
// metadataURLOverride, if set, replaces the derived metadata URL in 401
// challenges.
func NewHandler(mcpHandler http.Handler, v grpcauth.TokenVerifier, issuer, publicURL, metadataURLOverride string) http.Handler {
	publicURL = strings.TrimRight(publicURL, "/")
	metadataURL := metadataURLOverride
	if metadataURL == "" && publicURL != "" {
		metadataURL = auth.ProtectedResourceMetadataURL(publicURL)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	if publicURL != "" {
		mux.Handle(auth.ProtectedResourceMetadataPath, auth.NewProtectedResourceMetadataHandler(auth.ProtectedResourceMetadataConfig{
			Resource:            publicURL,
			AuthorizationServer: issuer,
			ResourceName:        "manmanv2 MCP",
		}))
	}
	mux.Handle("/", HTTPAuth(v, metadataURL)(mcpHandler))
	return mux
}
