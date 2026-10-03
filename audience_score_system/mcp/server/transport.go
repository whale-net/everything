package server

import (
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/libs/go/auth"
)

// ResourceMetadataConfig configures the RFC 9728 protected-resource
// metadata this `mcp` serves; see auth.ProtectedResourceMetadataConfig.
type ResourceMetadataConfig = auth.ProtectedResourceMetadataConfig

// NewHTTPHandler builds the mux `mcp`'s main.go binds to ASS_MCP_ADDR: an
// unauthenticated GET /healthz (for k8s liveness/readiness), RFC 9728
// protected-resource metadata at the fixed well-known path (issue #1646,
// NFR4 -- auth.ProtectedResourceMetadataPath, registered at the mux
// root so MCP clients' fixed-location probe finds it), and the
// streamable-HTTP MCP endpoint at "/", guarded by
// auth.RequireBearerToken(credentials, ...) -- the HTTP half of this
// task's caller-auth design decision (auth.go's PersonMiddleware is the
// MCP-protocol half; see ../../ARCHITECTURE.md "MCP server: caller
// authentication"). The 401 auth.RequireBearerToken produces for a
// missing/invalid bearer token carries a `WWW-Authenticate: Bearer
// resource_metadata="..."` challenge pointing at that same metadata
// endpoint, per NFR4's bootstrap sequence. auth.RequireBearerToken
// always forces AllowMissingExpiration: true internally (mcp_credential
// tokens are revocable, not time-boxed -- there is no per-token expiration
// claim to enforce). srv is reused as-is across every request/session
// (LB4: statelessness lives in Postgres, never in per-request *mcp.Server
// construction).
func NewHTTPHandler(srv *mcp.Server, credentials auth.CredentialStore, resourceMeta ResourceMetadataConfig) http.Handler {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return srv
	}, nil)

	requireBearer := auth.RequireBearerToken(credentials, auth.ResourceServerBearerOptions(resourceMeta))

	return newMux(requireBearer(mcpHandler), resourceMeta)
}

// NewDualAuthHTTPHandler is NewHTTPHandler's FR12(a) counterpart (issue
// #2116): the same mux, `/` guarded instead by DualAuthHTTPHandler
// (whagent_auth.go) so BOTH caller-authentication paths -- the existing
// mcp_credential path (credentials, unchanged) and the new whagent-net
// path (whagentCfg) -- are mounted alongside one another, per FR12(a).
// `mcp`'s main.go calls this instead of NewHTTPHandler once the
// whagent-net path is configured; NewHTTPHandler itself is left exactly
// as it was (existing tests/callers keep building and passing unchanged)
// for any caller that only ever wants the single, pre-existing path.
func NewDualAuthHTTPHandler(srv *mcp.Server, credentials auth.CredentialStore, whagentCfg WhagentAuthConfig, resourceMeta ResourceMetadataConfig) http.Handler {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return srv
	}, nil)

	guarded := DualAuthHTTPHandler(mcpHandler, credentials, whagentCfg, auth.ResourceServerBearerOptions(resourceMeta))

	return newMux(guarded, resourceMeta)
}

// newMux builds `mcp`'s mux -- healthz, RFC 9728 protected-resource
// metadata, and the streamable-HTTP MCP endpoint at "/" guarded by
// guarded -- shared by NewHTTPHandler and NewDualAuthHTTPHandler so the
// two caller-auth entry points can never drift on the non-auth parts of
// the mux.
func newMux(guarded http.Handler, resourceMeta ResourceMetadataConfig) http.Handler {
	return auth.NewResourceServerMux(resourceMeta, map[string]http.Handler{"/": guarded})
}
