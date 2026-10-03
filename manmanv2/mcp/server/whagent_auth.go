package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/libs/go/grpcauth/grantflow"
	"github.com/whale-net/everything/libs/go/whagent"
)

// Env vars enabling the whagent-net credential path.
const (
	EnvWhagentJWKSURL = "MCP_WHAGENT_JWKS_URL"
	EnvWhagentIssuer  = "MCP_WHAGENT_ISSUER"
)

// ErrWhagentUnresolved is the exact tool-call error text when a verified
// whagent identity cannot be resolved to a user.
const ErrWhagentUnresolved = "unauthenticated: whagent identity could not be resolved"

const whagentClaimExtraKey = "manmanv2.mcp.whagent_claim"

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
	e := WhagentEnv{JWKSURL: getenv(EnvWhagentJWKSURL), Issuer: getenv(EnvWhagentIssuer)}
	if !e.Enabled() {
		return WhagentEnv{}, nil
	}
	if e.JWKSURL == "" || e.Issuer == "" {
		return WhagentEnv{}, fmt.Errorf("%s and %s must be set together", EnvWhagentJWKSURL, EnvWhagentIssuer)
	}
	if !grantSet {
		return WhagentEnv{}, errors.New("whagent settings require grant mode: set GRANT_* so the agent's user can be resolved")
	}
	if getenv("MCP_PUBLIC_URL") == "" {
		return WhagentEnv{}, errors.New("whagent settings require MCP_PUBLIC_URL: it is the required token audience")
	}
	return e, nil
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

func peekIssuer(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var c struct {
		Iss string `json:"iss"`
	}
	if json.Unmarshal(payload, &c) != nil {
		return ""
	}
	return c.Iss
}

func bearerToken(r *http.Request) string {
	f := strings.Fields(r.Header.Get("Authorization"))
	if len(f) != 2 || !strings.EqualFold(f[0], "bearer") {
		return ""
	}
	return f[1]
}

// WhagentHTTPAuth routes each bearer token to exactly one verifier: the
// whagent path for tokens issued by cfg.WhagentIssuer, otherwise existing.
// The unverified iss only selects a verifier; it never grants. The claim is
// resolved to a user later, at the MCP layer (WhagentMiddleware), so a
// missing grant surfaces as a tool error rather than a connection error.
func WhagentHTTPAuth(existing CallerVerifier, cfg WhagentAuthConfig, resourceMetadataURL string) func(http.Handler) http.Handler {
	opts := &sdkauth.RequireBearerTokenOptions{ResourceMetadataURL: resourceMetadataURL, AllowMissingExpiration: true}
	whagentVerify := func(ctx context.Context, token string, _ *http.Request) (*sdkauth.TokenInfo, error) {
		claim, err := cfg.Verifier.Verify(ctx, token, cfg.Audience)
		if err != nil || claim == nil || claim.SubjectIssuer != cfg.UserIssuer {
			return nil, errInvalidToken
		}
		return &sdkauth.TokenInfo{
			UserID:     claim.Subject,
			Expiration: claim.Expiry.Time(),
			Extra:      map[string]any{whagentClaimExtraKey: claim},
		}, nil
	}
	return func(next http.Handler) http.Handler {
		whagentDoor := sdkauth.RequireBearerToken(whagentVerify, opts)(next)
		existingDoor := HTTPAuthWith(existing, resourceMetadataURL)(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if cfg.WhagentIssuer != "" && peekIssuer(bearerToken(r)) == cfg.WhagentIssuer {
				whagentDoor.ServeHTTP(w, r)
				return
			}
			existingDoor.ServeHTTP(w, r)
		})
	}
}

// WhagentMiddleware resolves a whagent claim to the user's Caller at the MCP
// protocol layer; mount it outermost. Requests without a whagent claim pass
// through unchanged. Resolution failure is a tool-call error for tools/call
// and a protocol error carrying the same text for every other method.
func WhagentMiddleware(ex grantflow.Exchanger) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			extra := req.GetExtra()
			if extra == nil || extra.TokenInfo == nil {
				return next(ctx, method, req)
			}
			claim, ok := extra.TokenInfo.Extra[whagentClaimExtraKey].(*whagent.Claim)
			if !ok {
				return next(ctx, method, req)
			}
			access, claims, err := ex.Exchange(ctx, claim.Subject)
			if err != nil || claims == nil {
				slog.Warn("whagent identity could not be resolved", "subject", claim.Subject, "whagent_session_id", claim.WhagentSessionID, "error", err)
				if method == "tools/call" {
					return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: ErrWhagentUnresolved}}}, nil
				}
				return nil, errors.New(ErrWhagentUnresolved)
			}
			c := callerFromClaims(claims, access)
			c.Agent = &Agent{Subject: claim.Actor.Subject, AgentID: claim.Actor.AgentID, SessionID: claim.WhagentSessionID}
			// Middleware reads the caller from ctx when the request carries none.
			return next(ContextWithCaller(ctx, c), method, req)
		}
	}
}
