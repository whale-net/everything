// Caller authentication, agent front door (NFR1) -- new, parallel to
// (never built on top of) auth.go's own auth -> PersonaMiddleware flow.
// Mirrors audience_score_system/mcp/server/whagent_auth.go's design
// verbatim, adapted from resolving a store.Person to resolving a fixed
// Persona (PersonaAgent) -- see that file's package doc comment for the
// full rationale this file shares:
//
//  1. Routes each request's bearer token to exactly one of the two doors
//     at the HTTP layer (DualAuthHTTPHandler): an auth credential is an
//     opaque 64-character hex string with no dots
//     (libs/go/auth/credential.go's generateToken) and goes to the
//     credential door; a JWT whose unverified `iss` is whagent-net's goes
//     to the whagent door; any other JWT (e.g. a Keycloak token) is
//     rejected outright -- the OIDC door is deliberately not mounted here.
//     The peek only selects a verifier; it never grants anything.
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
//     A auth-routed call has no such Extra key, so
//     WhagentPersonaMiddleware calls next unchanged -- next IS
//     PersonaMiddleware, which authenticates it exactly as it always has.
package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/libs/go/auth"
	"github.com/whale-net/everything/libs/go/whagent"
)

// WhagentAuthConfig configures the agent front door (NFR1): the Verifier
// pinned to whagent-net's own JWKS/issuer, and the audience value this
// `mcp` instance expects every whagent Claim to carry (its own externally
// reachable URL -- the same value as ResourceMetadataConfig.Resource).
type WhagentAuthConfig struct {
	Verifier *whagent.Verifier
	Audience string

	// Issuer is whagent-net's issuer identifier: a JWT bearing any other
	// unverified `iss` is rejected without reaching Verifier. Empty routes
	// every JWT to Verifier, which pins its own issuer regardless.
	Issuer string
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
// dot-separated segments). An opaque auth credential never has dots, so
// this cleanly separates JWTs from credentials; whether a JWT is actually
// whagent's is decided by peekIssuer plus the verifier, never by shape
// alone.
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

// peekIssuer returns the unverified `iss` claim of a JWT-shaped token, or
// "" when the payload does not decode. It is used only to pick which
// verifier sees the token -- never to grant access.
func peekIssuer(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Iss string `json:"iss"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return claims.Iss
}

// bearerToken extracts the raw bearer token from r's Authorization header
// (RFC 6750), or "" if none is present. An empty return routes to the
// auth branch by default, which then rejects with its own standard
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

// forbiddenNoPersona is the 403 body for a credential with no recognized
// persona (e.g. minted before personas existed): authenticated, but not
// authorized for anything.
const forbiddenNoPersona = `{"error":"forbidden","error_description":"credential carries no persona; sign in again"}`

// personaGated wraps h so a verified credential with no recognized persona
// is refused 403 before any MCP handling.
func personaGated(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := sdkauth.TokenInfoFromContext(r.Context())
		if info == nil {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		if _, ok := credentialPersona(info); !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(forbiddenNoPersona))
			return
		}
		h.ServeHTTP(w, r)
	})
}

// credentialGuarded is the auth (opaque credential) door around h.
func credentialGuarded(h http.Handler, credentials auth.CredentialStore, opts *sdkauth.RequireBearerTokenOptions) http.Handler {
	return auth.RequireBearerToken(credentials, opts)(personaGated(h))
}

// DualAuthHTTPHandler wraps mcpHandler with BOTH caller-authentication
// front doors (NFR1): the auth (opaque credential) door and the
// whagent-net (agent) door, routed per-request -- opaque tokens to the
// credential door, whagent-issued JWTs to the whagent door, every other
// JWT rejected 401 without reaching either verifier. Each branch is its
// own independent sdkauth.RequireBearerToken instance around mcpHandler,
// so a rejection in either branch never invokes mcpHandler.
func DualAuthHTTPHandler(mcpHandler http.Handler, credentials auth.CredentialStore, cfg WhagentAuthConfig, authOpts *sdkauth.RequireBearerTokenOptions) http.Handler {
	credentialDoor := credentialGuarded(mcpHandler, credentials, authOpts)
	whagentGuarded := sdkauth.RequireBearerToken(whagentTokenVerifier(cfg), authOpts)(mcpHandler)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if !isWhagentShapedToken(token) {
			credentialDoor.ServeHTTP(w, r)
			return
		}
		if cfg.Issuer != "" && peekIssuer(token) != cfg.Issuer {
			http.Error(w, errWhagentTokenInvalid.Error(), http.StatusUnauthorized)
			return
		}
		whagentGuarded.ServeHTTP(w, r)
	})
}

// WhagentPersonaMiddleware is the MCP-protocol half of the agent front
// door (NFR1): for a call DualAuthHTTPHandler routed through the whagent
// path (i.e. whagentClaimExtraKey is present on the resolved
// TokenInfo.Extra), it places PersonaAgent on ctx unconditionally -- a
// whagent Claim never carries a human profile to resolve further (FR10 of
// //libs/go/whagent), so there is nothing to look up. A call
// DualAuthHTTPHandler routed through the auth door has no
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
