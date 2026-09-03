package server

import (
	"encoding/json"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/libs/go/mcpauth"
)

// NewHTTPHandler builds the mux `mcp`'s main.go binds to ASS_MCP_ADDR: an
// unauthenticated GET /healthz (for k8s liveness/readiness) and the
// streamable-HTTP MCP endpoint at "/", guarded by
// mcpauth.RequireBearerToken(credentials, ...) -- the HTTP half of this
// task's caller-auth design decision (auth.go's PersonMiddleware is the
// MCP-protocol half; see ../../ARCHITECTURE.md "MCP server: caller
// authentication"). mcpauth.RequireBearerToken always forces
// AllowMissingExpiration: true internally (mcp_credential tokens are
// revocable, not time-boxed -- there is no per-token expiration claim to
// enforce), so nil opts are sufficient here. srv is reused as-is across
// every request/session (LB4: statelessness lives in Postgres, never in
// per-request *mcp.Server construction).
func NewHTTPHandler(srv *mcp.Server, credentials mcpauth.CredentialStore) http.Handler {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return srv
	}, nil)

	requireBearer := mcpauth.RequireBearerToken(credentials, nil)

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
