package server

import (
	"encoding/json"
	"net/http"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewHTTPHandler builds the mux `mcp`'s main.go binds to its listen
// address: an unauthenticated GET /healthz (k8s liveness/readiness) and
// the streamable-HTTP MCP endpoint at "/", guarded by
// sdkauth.RequireBearerToken(PassthroughVerifier, ...) -- the HTTP half
// of this task's caller-identity design (auth.go's AuthMiddleware is the
// MCP-protocol half; see issue #2120's Implementation section, "Identity
// pass-through"). AllowMissingExpiration is forced true because
// PassthroughVerifier's TokenInfo never carries an expiration of its own
// -- `api`, not `mcp`, is what actually checks a token's `exp` claim.
// srv is reused as-is across every request/session: mcp holds no
// per-request state, only `api` (via its store) does.
func NewHTTPHandler(srv *mcp.Server) http.Handler {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return srv
	}, nil)

	requireBearer := sdkauth.RequireBearerToken(PassthroughVerifier, &sdkauth.RequireBearerTokenOptions{
		AllowMissingExpiration: true,
	})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.Handle("/", requireBearer(mcpHandler))
	return mux
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
