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

// opsMountPath is where krill's operator surface (M5, issue #2867) is
// mounted -- its own pre-filtered endpoint, alongside specMountPath and
// designMountPath, never folded onto either. FR6-FR9 of the M5 root plan
// (#2851) all state their actor as "A Swarm Operator can ..."; this mount,
// not a per-call auth check layered onto an existing mount, is where that
// restriction is enforced: registry.go's ops-mount registration path
// fixes its allowed-persona set to PersonaSwarmOperator alone, so the
// mount itself is the authorization boundary, the same way
// specMountPath's absence of RegisterWrite calls is what keeps that mount
// read-only (this file's designMountPath comment) -- "no write/read tool
// crossover on a mount" stays a per-mount, grep-verifiable invariant
// rather than a per-tool one. Both the M5 operator verbs (write) and the
// M5 console queries (read, FR4/FR5/FR10/FR12) register here; neither
// belongs on specMountPath (open to every persona) or designMountPath
// (open to every resolved persona, not operator-restricted).
//
// The Swarm Operator persona check this mount enforces is MCP-only: M5's
// mutating HTTP endpoints keep using the existing gate(...) write gate
// (krill/api/routes.go, krill/api/handlers/gate.go) unchanged -- that gate
// has no persona concept, only a valid krill session. A later task must
// not add a second, divergent persona check on the HTTP side; this mount
// is the one place the Swarm Operator restriction lives.
const opsMountPath = "/mcp/ops"

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
// specSrv's specMountPath, the one at designSrv's designMountPath, and the
// one at opsSrv's opsMountPath -- all three guarded by
// mcpauth.RequireBearerToken(credentials, ...), the mcpauth-only door.
// `mcp`'s main.go calls NewDualAuthHTTPHandler instead once the
// whagent-net door is configured; this function stays exactly as the
// single-door shape for a caller that only ever wants the mcpauth door
// (mirrors audience_score_system/mcp/server/transport.go's own
// NewHTTPHandler/NewDualAuthHTTPHandler split).
func NewHTTPHandler(specSrv, designSrv, opsSrv *mcp.Server, credentials mcpauth.CredentialStore, resourceMeta ResourceMetadataConfig) http.Handler {
	opts := &sdkauth.RequireBearerTokenOptions{AllowMissingExpiration: true}
	if resourceMeta.enabled() {
		opts.ResourceMetadataURL = mcpauth.ProtectedResourceMetadataURL(resourceMeta.Resource)
	}
	requireBearer := mcpauth.RequireBearerToken(credentials, opts)

	return newMux(requireBearer(mcpHandlerFor(specSrv)), requireBearer(mcpHandlerFor(designSrv)), requireBearer(mcpHandlerFor(opsSrv)), resourceMeta)
}

// NewDualAuthHTTPHandler is NewHTTPHandler's two-front-door counterpart
// (NFR1): the same mux, specMountPath, designMountPath, and opsMountPath
// all guarded instead by DualAuthHTTPHandler (whagent_auth.go) so BOTH
// caller-authentication paths -- the mcpauth door (credentials) and the
// whagent-net door (whagentCfg) -- are mounted alongside one another, at
// EACH mount (issue #2547's Scope: "Both existing front doors ... apply to
// the new mount ... unchanged", carried forward to opsMountPath by this
// task).
func NewDualAuthHTTPHandler(specSrv, designSrv, opsSrv *mcp.Server, credentials mcpauth.CredentialStore, whagentCfg WhagentAuthConfig, resourceMeta ResourceMetadataConfig) http.Handler {
	opts := &sdkauth.RequireBearerTokenOptions{AllowMissingExpiration: true}
	if resourceMeta.enabled() {
		opts.ResourceMetadataURL = mcpauth.ProtectedResourceMetadataURL(resourceMeta.Resource)
	}

	specGuarded := DualAuthHTTPHandler(mcpHandlerFor(specSrv), credentials, whagentCfg, opts)
	designGuarded := DualAuthHTTPHandler(mcpHandlerFor(designSrv), credentials, whagentCfg, opts)
	opsGuarded := DualAuthHTTPHandler(mcpHandlerFor(opsSrv), credentials, whagentCfg, opts)

	return newMux(specGuarded, designGuarded, opsGuarded, resourceMeta)
}

// newMux builds `mcp`'s mux -- healthz, RFC 9728 protected-resource
// metadata, the streamable-HTTP MCP endpoint at specMountPath guarded by
// specGuarded, the one at designMountPath guarded by designGuarded, and
// the one at opsMountPath guarded by opsGuarded -- shared by
// NewHTTPHandler and NewDualAuthHTTPHandler so the two caller-auth entry
// points can never drift on the non-auth parts of the mux, or on which
// mounts exist at all.
func newMux(specGuarded, designGuarded, opsGuarded http.Handler, resourceMeta ResourceMetadataConfig) http.Handler {
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
	mux.Handle(opsMountPath, opsGuarded)
	return mux
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
