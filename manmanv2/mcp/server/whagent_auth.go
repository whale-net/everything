package server

import (
	"errors"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/libs/go/grpcauth/grantflow"
	"github.com/whale-net/everything/libs/go/whagent"
)

// Env vars enabling the whagent-net credential path.
const (
	EnvWhagentJWKSURL = "MCP_WHAGENT_JWKS_URL"
	EnvWhagentIssuer  = "MCP_WHAGENT_ISSUER"
)

var errNotImplemented = errors.New("whagent auth: not implemented")

// WhagentEnv is the whagent path's env-derived settings; zero means disabled.
type WhagentEnv struct {
	JWKSURL string
	Issuer  string
}

// Enabled reports whether any whagent variable is set.
func (e WhagentEnv) Enabled() bool { return e.JWKSURL != "" || e.Issuer != "" }

// WhagentEnvFromEnv reads the whagent variables and enforces the startup
// guards: both vars together, grant mode (grantSet), and a public URL
// (the required token audience).
func WhagentEnvFromEnv(getenv func(string) string, grantSet bool) (WhagentEnv, error) {
	return WhagentEnv{}, errNotImplemented
}

// WhagentAuthConfig wires the whagent path.
type WhagentAuthConfig struct {
	Verifier *whagent.Verifier
	// Audience is the MCP's own public URL.
	Audience string
	// UserIssuer is OIDC_ISSUER; a claim's SubjectIssuer must equal it.
	UserIssuer string
	// WhagentIssuer routes a token (by unverified iss) to the whagent path.
	WhagentIssuer string
}

// Agent is the verified whagent actor behind a Caller, kept for audit.
type Agent struct {
	Subject   string
	AgentID   string
	SessionID string
}

// WhagentHTTPAuth routes each bearer token to exactly one verifier: the
// whagent path for tokens issued by cfg.WhagentIssuer, otherwise existing.
func WhagentHTTPAuth(existing CallerVerifier, cfg WhagentAuthConfig, resourceMetadataURL string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, errNotImplemented.Error(), http.StatusInternalServerError)
		})
	}
}

// WhagentMiddleware resolves a whagent claim to the user's Caller at the
// MCP protocol layer.
func WhagentMiddleware(ex grantflow.Exchanger) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler { return next }
}
