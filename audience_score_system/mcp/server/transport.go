package server

import (
	"encoding/json"
	"net/http"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/libs/go/mcpauth"
)

// ResourceMetadataConfig configures NewHTTPHandler's protected-resource
// discovery surface (issue #1646, NFR4): `mcp` is the OAuth2 protected
// resource in ASS's two-binary split (`web` is the authorization server --
// see ../../ARCHITECTURE.md "MCP server: caller authentication"), so it
// serves only RFC 9728 discovery, never /authorize or /token.
type ResourceMetadataConfig struct {
	// Resource is this `mcp` instance's own externally reachable URL
	// (ASS_MCP_PUBLIC_URL) -- must equal `web`'s
	// mcpauth.ProviderConfig.Resource exactly, or MCP client discovery
	// breaks (RFC 9728).
	Resource string

	// AuthorizationServer is the issuer identifier of the OAuth2
	// authorization server protecting Resource -- `web`'s own
	// mcpauth.ProviderConfig.Issuer (ASS_OAUTH_REDIRECT_BASE_URL).
	AuthorizationServer string

	// ResourceName is the metadata's human-readable `resource_name`.
	ResourceName string
}

// NewHTTPHandler builds the mux `mcp`'s main.go binds to ASS_MCP_ADDR: an
// unauthenticated GET /healthz (for k8s liveness/readiness), RFC 9728
// protected-resource metadata at the fixed well-known path (issue #1646,
// NFR4 -- mcpauth.ProtectedResourceMetadataPath, registered at the mux
// root so MCP clients' fixed-location probe finds it), and the
// streamable-HTTP MCP endpoint at "/", guarded by
// mcpauth.RequireBearerToken(credentials, ...) -- the HTTP half of this
// task's caller-auth design decision (auth.go's PersonMiddleware is the
// MCP-protocol half; see ../../ARCHITECTURE.md "MCP server: caller
// authentication"). The 401 mcpauth.RequireBearerToken produces for a
// missing/invalid bearer token carries a `WWW-Authenticate: Bearer
// resource_metadata="..."` challenge pointing at that same metadata
// endpoint, per NFR4's bootstrap sequence. mcpauth.RequireBearerToken
// always forces AllowMissingExpiration: true internally (mcp_credential
// tokens are revocable, not time-boxed -- there is no per-token expiration
// claim to enforce). srv is reused as-is across every request/session
// (LB4: statelessness lives in Postgres, never in per-request *mcp.Server
// construction).
func NewHTTPHandler(srv *mcp.Server, credentials mcpauth.CredentialStore, resourceMeta ResourceMetadataConfig) http.Handler {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return srv
	}, nil)

	resourceMetadataURL := mcpauth.ProtectedResourceMetadataURL(resourceMeta.Resource)
	requireBearer := mcpauth.RequireBearerToken(credentials, &sdkauth.RequireBearerTokenOptions{
		ResourceMetadataURL: resourceMetadataURL,
	})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.Handle(mcpauth.ProtectedResourceMetadataPath, mcpauth.NewProtectedResourceMetadataHandler(mcpauth.ProtectedResourceMetadataConfig{
		Resource:            resourceMeta.Resource,
		AuthorizationServer: resourceMeta.AuthorizationServer,
		ResourceName:        resourceMeta.ResourceName,
	}))
	mux.Handle("/", requireBearer(mcpHandler))
	return mux
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
