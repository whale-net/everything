package server

import (
	"encoding/json"
	"net/http"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/audience_score_system/store"
)

// NewHTTPHandler builds the mux `mcp`'s main.go binds to ASS_MCP_ADDR: an
// unauthenticated GET /healthz (for k8s liveness/readiness) and the
// streamable-HTTP MCP endpoint at "/", guarded by
// auth.RequireBearerToken(TokenVerifier(credentials), ...) -- the HTTP
// half of this task's caller-auth design decision (auth.go's
// PersonMiddleware is the MCP-protocol half; see ../../ARCHITECTURE.md
// "MCP server: caller authentication"). srv is reused as-is across every
// request/session (LB4: statelessness lives in Postgres, never in
// per-request *mcp.Server construction).
func NewHTTPHandler(srv *mcp.Server, credentials store.CredentialStore) http.Handler {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return srv
	}, nil)

	requireBearer := sdkauth.RequireBearerToken(TokenVerifier(credentials), &sdkauth.RequireBearerTokenOptions{
		// mcp_credential tokens are revocable, not time-boxed (see
		// ../../ARCHITECTURE.md "MCP server: caller authentication") --
		// there is no per-token expiration claim to enforce here.
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
