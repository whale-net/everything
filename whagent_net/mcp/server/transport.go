package server

import (
	"net/http"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/libs/go/auth"
)

// ResourceMetadataConfig configures the RFC 9728 protected-resource
// metadata this `mcp` serves; see auth.ProtectedResourceMetadataConfig.
type ResourceMetadataConfig = auth.ProtectedResourceMetadataConfig

// NewHTTPHandler builds the mux `mcp`'s main.go binds to its listen
// address: an unauthenticated GET /healthz (k8s liveness/readiness), RFC
// 9728 protected-resource metadata at the fixed well-known path when
// resourceMeta is configured (auth.ProtectedResourceMetadataPath,
// registered at the mux root so an MCP client's fixed-location probe
// finds it), and the streamable-HTTP MCP endpoint at "/", guarded by
// sdkauth.RequireBearerToken(NewVerifier(credentials), ...) --
// NewVerifier (auth.go) is what actually classifies a presented bearer
// credential between the manual-token and FR9 OAuth2 paths; this
// function's only job with respect to that split is to decide whether
// credentials is even in play. When resourceMeta is enabled, the 401
// RequireBearerToken produces for a missing/invalid bearer token carries
// a `WWW-Authenticate: Bearer resource_metadata="..."` challenge pointing
// at that same metadata endpoint. AllowMissingExpiration is forced true
// because neither of NewVerifier's TokenInfo shapes carries an expiration
// of its own -- `api` checks a manually-forwarded token's `exp`, and
// auth credentials are revocable rather than time-boxed (see
// auth's own package doc). srv is reused as-is across every
// request/session: mcp holds no per-request state, only `api` (via its
// store) does.
//
// credentials may be nil (FR9 not configured, e.g. PG_DATABASE_URL
// unset, main.go's initializeAuthDeps) -- NewVerifier(nil)
// reproduces this package's pre-FR9 behavior exactly (issue #2249's
// Testing section, "the manual-token path still works end to end with no
// OAuth2 configuration present at all").
//
// resourceMeta left zero-valued (WHAGENT_MCP_PUBLIC_URL/
// WHAGENT_UI_PUBLIC_URL unset) skips mounting the metadata endpoint
// entirely -- the manual-token recipe never depends on it.
func NewHTTPHandler(srv *mcp.Server, credentials auth.CredentialStore, resourceMeta ResourceMetadataConfig) http.Handler {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return srv
	}, nil)

	opts := auth.ResourceServerBearerOptions(resourceMeta)
	opts.AllowMissingExpiration = true
	requireBearer := sdkauth.RequireBearerToken(NewVerifier(credentials), opts)

	return auth.NewResourceServerMux(resourceMeta, map[string]http.Handler{"/": requireBearer(mcpHandler)})
}
