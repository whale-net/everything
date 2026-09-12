// Caller authentication, agent front door (NFR1) -- new, parallel to
// (never built on top of) auth.go's mcpauth -> PersonaMiddleware flow.
// Mirrors audience_score_system/mcp/server/whagent_auth.go's design
// verbatim, adapted from resolving a store.Person to resolving a fixed
// Persona (PersonaAgent) -- see that file's package doc comment for the
// full rationale this file shares:
//
//  1. Routes each request's bearer token to exactly one of the two doors
//     at the HTTP layer (DualAuthHTTPHandler), keyed on token SHAPE: a
//     whagent Claim is always a three-segment, two-dot JWT compact
//     serialization; an mcpauth credential is always a 64-character hex
//     string with no dots (libs/go/mcpauth/credential.go's
//     generateToken) -- the two encodings never overlap, so this split
//     is exact, not probabilistic.
//  2. For the whagent-shaped case, calls whagent.Verifier.Verify
//     directly (never whagent.HTTPMiddleware -- see
//     audience_score_system's file for the exact contract gap this
//     works around), wrapping it in this file's own sdkauth.TokenVerifier
//     so the resulting sdkauth.TokenInfo carries the verified
//     *whagent.Claim under this file's own Extra key.
//  3. WhagentPersonaMiddleware (the MCP-protocol half, mounted OUTSIDE --
//     i.e. executing BEFORE -- auth.go's PersonaMiddleware; see
//     mcp/main.go's construction order) reads that Extra key and resolves
//     every whagent-routed call to PersonaAgent, unconditionally -- a
//     whagent Claim never carries a human profile (FR10 of
//     //libs/go/whagent), so there is no further identity to resolve.
//     A mcpauth-routed call has no such Extra key, so
//     WhagentPersonaMiddleware calls next unchanged -- next IS
//     PersonaMiddleware, which authenticates it exactly as it always has.
package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/libs/go/mcpauth"
	"github.com/whale-net/everything/libs/go/whagent"
)

// WhagentAuthConfig configures the agent front door (NFR1): the Verifier
// pinned to whagent-net's own JWKS/issuer, and the audience value this
// `mcp` instance expects every whagent Claim to carry (its own externally
// reachable URL -- the same value as ResourceMetadataConfig.Resource).
type WhagentAuthConfig struct {
	Verifier *whagent.Verifier
	Audience string
}

// whagentClaimExtraKey is the sdkauth.TokenInfo.Extra key
// DualAuthHTTPHandler's whagent-shaped-token branch stashes a verified
// *whagent.Claim under, and WhagentPersonaMiddleware reads it back from
// mcp.Request.GetExtra().TokenInfo.Extra.
const whagentClaimExtraKey = "krill/mcp/server.whagent_claim"

// errWhagentTokenInvalid is the single fixed error this file's
// sdkauth.TokenVerifier returns for every whagent.Verifier.Verify
// failure -- an unsigned claim, a valid OAuth2 token, a token signed by a
// non-whagent key, a wrong-audience token, an expired token, or a
// malformed one are all indistinguishable to a caller.
var errWhagentTokenInvalid = fmt.Errorf("mcp: invalid, expired, or unverifiable whagent-net credential: %w", sdkauth.ErrInvalidToken)

// isWhagentShapedToken reports whether token is shaped like a whagent
// Claim JWT (RFC 7519 compact serialization: exactly three non-empty,
// dot-separated segments) rather than an mcpauth credential (a
// 64-character hex string with no dots). DualAuthHTTPHandler uses this to
// decide which of the two verification paths a given request's bearer
// token belongs to, without ever trying a credential against the wrong
// path's verifier (NFR1): the two encodings never overlap, so this split
// is exact, not probabilistic.
func isWhagentShapedToken(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
	}
	return true
}

// bearerToken extracts the raw bearer token from r's Authorization header
// (RFC 6750), or "" if none is present. An empty return routes to the
// mcpauth branch by default, which then rejects with its own standard
// "no bearer token" 401.
func bearerToken(r *http.Request) string {
	fields := strings.Fields(r.Header.Get("Authorization"))
	if len(fields) != 2 || !strings.EqualFold(fields[0], "bearer") {
		return ""
	}
	return fields[1]
}

// whagentTokenVerifier adapts cfg to an sdkauth.TokenVerifier: it calls
// cfg.Verifier.Verify directly, and on success stashes the verified
// *whagent.Claim under whagentClaimExtraKey so WhagentPersonaMiddleware
// can read it back off the MCP-protocol layer's RequestExtra.
func whagentTokenVerifier(cfg WhagentAuthConfig) sdkauth.TokenVerifier {
	return func(ctx context.Context, token string, _ *http.Request) (*sdkauth.TokenInfo, error) {
		claim, err := cfg.Verifier.Verify(ctx, token, cfg.Audience)
		if err != nil {
			return nil, errWhagentTokenInvalid
		}
		return &sdkauth.TokenInfo{
			UserID:     claim.Subject,
			Expiration: claim.Expiry.Time(),
			Extra:      map[string]any{whagentClaimExtraKey: claim},
		}, nil
	}
}

// DualAuthHTTPHandler wraps mcpHandler with BOTH caller-authentication
// front doors (NFR1): the mcpauth (human OAuth2) door (via credentials)
// and the whagent-net (agent) door (via cfg), routed per-request by
// isWhagentShapedToken so neither door is built on the other and neither
// can be satisfied by the other's credential. Each branch is its own
// independent sdkauth.RequireBearerToken instance around mcpHandler, so a
// rejection in either branch never invokes mcpHandler.
func DualAuthHTTPHandler(mcpHandler http.Handler, credentials mcpauth.CredentialStore, cfg WhagentAuthConfig, mcpauthOpts *sdkauth.RequireBearerTokenOptions) http.Handler {
	credentialGuarded := mcpauth.RequireBearerToken(credentials, mcpauthOpts)(mcpHandler)
	whagentGuarded := sdkauth.RequireBearerToken(whagentTokenVerifier(cfg), mcpauthOpts)(mcpHandler)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isWhagentShapedToken(bearerToken(r)) {
			whagentGuarded.ServeHTTP(w, r)
			return
		}
		credentialGuarded.ServeHTTP(w, r)
	})
}

// WhagentPersonaMiddleware is the MCP-protocol half of the agent front
// door (NFR1): for a call DualAuthHTTPHandler routed through the whagent
// path (i.e. whagentClaimExtraKey is present on the resolved
// TokenInfo.Extra), it places PersonaAgent on ctx unconditionally -- a
// whagent Claim never carries a human profile to resolve further (FR10 of
// //libs/go/whagent), so there is nothing to look up. A call
// DualAuthHTTPHandler routed through the mcpauth door has no
// whagentClaimExtraKey to find, so this middleware calls next unchanged
// -- next is PersonaMiddleware (server.go wires both as receiving
// middleware, this one mounted to run first by mcp/main.go), which
// authenticates it exactly as it always has.
func WhagentPersonaMiddleware() mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			extra := req.GetExtra()
			if extra == nil || extra.TokenInfo == nil {
				return next(ctx, method, req)
			}

			if _, ok := extra.TokenInfo.Extra[whagentClaimExtraKey].(*whagent.Claim); !ok {
				// Not a whagent-routed call -- fall through to
				// PersonaMiddleware, unchanged.
				return next(ctx, method, req)
			}

			return next(withPersona(ctx, PersonaAgent), method, req)
		}
	}
}
