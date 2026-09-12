package server

import (
	"encoding/json"
	"net/http"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/libs/go/mcpauth"
)

// specMountPath is where krill's spec-scoped MCP surface is mounted --
// its own pre-filtered endpoint, distinct from the future work-axis
// surface (M4, no endpoint exists for it yet). Follows
// whagent_net's `/mcp/readonly` vs `/mcp/ops` split (ARCHITECTURE.md
// "Domain-owned MCP servers and the tool contract"): mounting under a
// granularity-named path now is cheap, and folding every granularity onto
// bare "/" would make a later M4 mount awkward to add without breaking
// this one's clients.
const specMountPath = "/mcp/spec"

// designMountPath is where krill's design-session MCP surface (issue
// #2547, FR1-FR10 over MCP) is mounted -- its own pre-filtered endpoint,
// alongside specMountPath, never folded onto it. This is the first write
// tool this package registers (registry.go's RegisterWrite): keeping the
// six FR1-FR10 tools off specMountPath means the existing "no write/
// mutation tool is registered on the spec endpoint" negative test
// (registry_tools_test.go) keeps proving something real about
// specMountPath specifically, rather than needing to be reinterpreted as
// "on the whole binary."
const designMountPath = "/mcp/design"

// ResourceMetadataConfig configures NewHTTPHandler's RFC 9728
// protected-resource discovery surface: `mcp` is the OAuth2 protected
// resource, mirroring audience_score_system/mcp/server/transport.go's own
// ResourceMetadataConfig exactly in shape.
type ResourceMetadataConfig struct {
	// Resource is this `mcp` instance's own externally reachable URL --
	// must equal the OAuth2 authorization server's own configured
	// resource value exactly, or MCP client discovery breaks (RFC 9728).
	Resource string

	// AuthorizationServer is the issuer identifier of the OAuth2
	// authorization server protecting Resource.
	AuthorizationServer string

	// ResourceName is the metadata's human-readable `resource_name`.
	ResourceName string
}

// enabled reports whether cfg carries enough to serve RFC 9728
// protected-resource metadata at all.
func (cfg ResourceMetadataConfig) enabled() bool {
	return cfg.Resource != "" && cfg.AuthorizationServer != ""
}

// mcpHandlerFor adapts srv to a streamable-HTTP handler that always serves
// that one *mcp.Server -- the per-mount unit both NewHTTPHandler and
// NewDualAuthHTTPHandler wrap in their own caller-auth guard before handing
// to newMux.
func mcpHandlerFor(srv *mcp.Server) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return srv
	}, nil)
}

// NewHTTPHandler builds the mux `mcp`'s main.go binds to its listen
// address: an unauthenticated GET /healthz (k8s liveness/readiness), RFC
// 9728 protected-resource metadata at the fixed well-known path (when
// resourceMeta is configured), the streamable-HTTP MCP endpoint at
// specSrv's specMountPath, and the one at designSrv's designMountPath --
// both guarded by mcpauth.RequireBearerToken(credentials, ...), the
// mcpauth-only door. `mcp`'s main.go calls NewDualAuthHTTPHandler instead
// once the whagent-net door is configured; this function stays exactly as
// the single-door shape for a caller that only ever wants the mcpauth door
// (mirrors audience_score_system/mcp/server/transport.go's own
// NewHTTPHandler/NewDualAuthHTTPHandler split).
func NewHTTPHandler(specSrv, designSrv *mcp.Server, credentials mcpauth.CredentialStore, resourceMeta ResourceMetadataConfig) http.Handler {
	opts := &sdkauth.RequireBearerTokenOptions{AllowMissingExpiration: true}
	if resourceMeta.enabled() {
		opts.ResourceMetadataURL = mcpauth.ProtectedResourceMetadataURL(resourceMeta.Resource)
	}
	requireBearer := mcpauth.RequireBearerToken(credentials, opts)

	return newMux(requireBearer(mcpHandlerFor(specSrv)), requireBearer(mcpHandlerFor(designSrv)), resourceMeta)
}

// NewDualAuthHTTPHandler is NewHTTPHandler's two-front-door counterpart
// (NFR1): the same mux, both specMountPath and designMountPath guarded
// instead by DualAuthHTTPHandler (whagent_auth.go) so BOTH
// caller-authentication paths -- the mcpauth door (credentials) and the
// whagent-net door (whagentCfg) -- are mounted alongside one another, at
// EACH mount (issue #2547's Scope: "Both existing front doors ... apply to
// the new mount ... unchanged").
func NewDualAuthHTTPHandler(specSrv, designSrv *mcp.Server, credentials mcpauth.CredentialStore, whagentCfg WhagentAuthConfig, resourceMeta ResourceMetadataConfig) http.Handler {
	opts := &sdkauth.RequireBearerTokenOptions{AllowMissingExpiration: true}
	if resourceMeta.enabled() {
		opts.ResourceMetadataURL = mcpauth.ProtectedResourceMetadataURL(resourceMeta.Resource)
	}

	specGuarded := DualAuthHTTPHandler(mcpHandlerFor(specSrv), credentials, whagentCfg, opts)
	designGuarded := DualAuthHTTPHandler(mcpHandlerFor(designSrv), credentials, whagentCfg, opts)

	return newMux(specGuarded, designGuarded, resourceMeta)
}

// newMux builds `mcp`'s mux -- healthz, RFC 9728 protected-resource
// metadata, the streamable-HTTP MCP endpoint at specMountPath guarded by
// specGuarded, and the one at designMountPath guarded by designGuarded --
// shared by NewHTTPHandler and NewDualAuthHTTPHandler so the two
// caller-auth entry points can never drift on the non-auth parts of the
// mux, or on which mounts exist at all.
func newMux(specGuarded, designGuarded http.Handler, resourceMeta ResourceMetadataConfig) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz)
	if resourceMeta.enabled() {
		mux.Handle(mcpauth.ProtectedResourceMetadataPath, mcpauth.NewProtectedResourceMetadataHandler(mcpauth.ProtectedResourceMetadataConfig{
			Resource:            resourceMeta.Resource,
			AuthorizationServer: resourceMeta.AuthorizationServer,
			ResourceName:        resourceMeta.ResourceName,
		}))
	}
	mux.Handle(specMountPath, specGuarded)
	mux.Handle(designMountPath, designGuarded)
	return mux
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
