package server

import (
	"encoding/json"
	"net/http"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/libs/go/mcpauth"
)

// ResourceMetadataConfig configures NewHTTPHandler's RFC 9728
// protected-resource discovery surface (FR9/C27, issue #2249's Scaffold
// phase): `mcp` is the OAuth2 protected resource in whagent-net's
// two-binary split, `ui` is the authorization server
// (whagent_net/ui/mcpauth.go's setupMCPAuth, issue #2245) -- mirrors
// audience_score_system/mcp/server/transport.go's own
// ResourceMetadataConfig exactly in shape, since that domain's `mcp`/`web`
// split is the same resource-server/authorization-server shape
// whagent-net's `mcp`/`ui` split is.
type ResourceMetadataConfig struct {
	// Resource is this `mcp` instance's own externally reachable URL
	// (WHAGENT_MCP_PUBLIC_URL) -- must be byte-identical to `ui`'s
	// mcpauth.ProviderConfig.Resource (also WHAGENT_MCP_PUBLIC_URL,
	// whagent_net/ui/mcpauth.go's setupMCPAuth) -- a mismatch silently
	// breaks an MCP client's RFC 9728 discovery chain
	// (audience_score_system/mcp/server/transport.go and main.go carry
	// the same warning).
	Resource string

	// AuthorizationServer is the issuer identifier of the OAuth2
	// authorization server protecting Resource -- `ui`'s own
	// mcpauth.ProviderConfig.Issuer (WHAGENT_UI_PUBLIC_URL).
	AuthorizationServer string

	// ResourceName is the metadata's human-readable `resource_name`.
	ResourceName string
}

// enabled reports whether cfg carries enough to serve RFC 9728
// protected-resource metadata at all -- see NewHTTPHandler's doc comment
// for why a zero-valued ResourceMetadataConfig is not an error.
func (cfg ResourceMetadataConfig) enabled() bool {
	return cfg.Resource != "" && cfg.AuthorizationServer != ""
}

// NewHTTPHandler builds the mux `mcp`'s main.go binds to its listen
// address: an unauthenticated GET /healthz (k8s liveness/readiness), RFC
// 9728 protected-resource metadata at the fixed well-known path when
// resourceMeta is configured (mcpauth.ProtectedResourceMetadataPath,
// registered at the mux root so an MCP client's fixed-location probe
// finds it), and the streamable-HTTP MCP endpoint at "/", guarded by
// sdkauth.RequireBearerToken(PassthroughVerifier, ...) -- unchanged by
// this task's Scaffold phase (auth.go's PassthroughVerifier/
// AuthMiddleware remain the manual-token recipe's HTTP/MCP-protocol
// halves; a dependent Implementation-phase change to auth.go is what
// actually adds FR9's OAuth2 path here, not this file). When
// resourceMeta is enabled, the 401 RequireBearerToken produces for a
// missing/invalid bearer token carries a `WWW-Authenticate: Bearer
// resource_metadata="..."` challenge pointing at that same metadata
// endpoint. AllowMissingExpiration is forced true because
// PassthroughVerifier's TokenInfo never carries an expiration of its own
// -- `api`, not `mcp`, is what actually checks a token's `exp` claim.
// srv is reused as-is across every request/session: mcp holds no
// per-request state, only `api` (via its store) does.
//
// resourceMeta left zero-valued (WHAGENT_MCP_PUBLIC_URL/
// WHAGENT_UI_PUBLIC_URL unset) skips mounting the metadata endpoint
// entirely -- the manual-token recipe never depends on it, and a
// deployment that hasn't rolled out FR9's discovery wiring yet keeps
// working exactly as before (issue #2249's Testing section, "the
// manual-token path still works end to end with no OAuth2 configuration
// present at all").
func NewHTTPHandler(srv *mcp.Server, resourceMeta ResourceMetadataConfig) http.Handler {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return srv
	}, nil)

	opts := &sdkauth.RequireBearerTokenOptions{
		AllowMissingExpiration: true,
	}
	if resourceMeta.enabled() {
		opts.ResourceMetadataURL = mcpauth.ProtectedResourceMetadataURL(resourceMeta.Resource)
	}
	requireBearer := sdkauth.RequireBearerToken(PassthroughVerifier, opts)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz)
	if resourceMeta.enabled() {
		mux.Handle(mcpauth.ProtectedResourceMetadataPath, mcpauth.NewProtectedResourceMetadataHandler(mcpauth.ProtectedResourceMetadataConfig{
			Resource:            resourceMeta.Resource,
			AuthorizationServer: resourceMeta.AuthorizationServer,
			ResourceName:        resourceMeta.ResourceName,
		}))
	}
	mux.Handle("/", requireBearer(mcpHandler))
	return mux
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
