// Caller authentication, whagent-net path (issue #2116, FR12) -- new,
// parallel to (never built on top of) auth.go's mcp_credential ->
// PersonMiddleware flow. See ../../architecture/02-mcp-server.md "MCP
// server: caller authentication" for the two-path design.
//
// Contract gap this file works around (flagged on issue #2116's Scaffold
// comment, still true as of Implementation): //libs/go/whagent's
// Middleware unconditionally rejects a call carrying no verified whagent
// Claim, and its companion HTTPMiddleware is the only sanctioned way to
// populate the private extra-key Middleware reads back off the request --
// there is no exported way to make either a no-op pass-through for a
// request that instead came in via this domain's OTHER, pre-existing auth
// path. Mounting HTTPMiddleware+Middleware verbatim in series alongside
// the existing mcpauth.RequireBearerToken -> PersonMiddleware chain would
// therefore reject every mcp_credential call outright -- exactly the
// "built on top of" failure mode FR12(a) forbids. This file instead:
//
//  1. Routes each request's bearer token to exactly one of the two paths
//     at the HTTP layer (DualAuthHTTPHandler), keyed on token SHAPE: a
//     whagent Claim is always a three-segment, two-dot JWT compact
//     serialization; an ASS mcp_credential is always a 64-character hex
//     string with no dots (see libs/go/mcpauth/credential.go's
//     generateToken) -- the two encodings never overlap, so this is a
//     clean, not probabilistic, split. Each branch is guarded by its own
//     independent sdkauth.RequireBearerToken instance -- a Keycloak
//     token, an ASS credential, or an unsigned claim can never satisfy
//     the other path's verifier (NFR4).
//  2. For the whagent-shaped case, calls whagent.Verifier.Verify directly
//     -- an equally-exported, equally-documented entry point on the same
//     published contract -- rather than whagent.HTTPMiddleware, wrapping
//     it in this file's own sdkauth.TokenVerifier so the resulting
//     sdkauth.TokenInfo carries the verified *whagent.Claim under this
//     file's own (not whagent's private) Extra key.
//  3. WhagentPersonMiddleware (the MCP-protocol half, mounted OUTSIDE --
//     i.e. executing BEFORE -- auth.go's PersonMiddleware; see
//     server.New/mcp/main.go's construction order) reads that Extra key.
//     A whagent-routed call resolves (iss, sub) to a Person here
//     (auto-provisioning on first sight, store.PersonIdentityStore) and
//     places it on ctx (withPerson) before calling next. A
//     mcp_credential-routed call has no such Extra key, so
//     WhagentPersonMiddleware calls next unchanged -- next IS
//     PersonMiddleware, which authenticates it exactly as it always has.
//     The one coexistence seam this composition needs is a single early
//     check in PersonMiddleware itself (auth.go): if a Person is already
//     on ctx (i.e. WhagentPersonMiddleware already resolved one), skip its
//     own mcp_credential-shaped resolution and call next directly, rather
//     than trying to reinterpret a whagent-routed call's TokenInfo as an
//     ASS credential and rejecting it. That check is a no-op for every
//     pre-#2116 call (nothing else ever put a Person on ctx before
//     PersonMiddleware ran), so the existing path's own behavior is
//     unchanged for mcp_credential callers (regression-safe).
package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/libs/go/mcpauth"
	"github.com/whale-net/everything/libs/go/whagent"
)

// WhagentAuthConfig configures the whagent-net authentication path
// (FR12): the Verifier pinned to whagent-net's own JWKS/issuer (NFR4,
// libs/go/whagent.NewVerifier / NewVerifierFromKey), and the audience
// value this `mcp` instance expects every whagent Claim to carry (its own
// externally reachable URL -- the same value as
// ResourceMetadataConfig.Resource).
type WhagentAuthConfig struct {
	Verifier *whagent.Verifier
	Audience string
}

// whagentClaimExtraKey is the sdkauth.TokenInfo.Extra key
// DualAuthHTTPHandler's whagent-shaped-token branch stashes a verified
// *whagent.Claim under, and WhagentPersonMiddleware reads it back from
// mcp.Request.GetExtra().TokenInfo.Extra. Deliberately this file's OWN key
// -- not libs/go/whagent's private claimExtraKey -- because this file
// builds its own TokenInfo via sdkauth.RequireBearerToken directly rather
// than going through whagent.HTTPMiddleware (see the package doc comment
// above for why).
const whagentClaimExtraKey = "audience_score_system/mcp/server.whagent_claim"

// errWhagentTokenInvalid is the single fixed error this file's
// sdkauth.TokenVerifier returns for every whagent.Verifier.Verify failure
// -- an unsigned claim, a valid Keycloak token, a token signed by a
// non-whagent key, a wrong-audience token, an expired token, or a
// malformed one are all indistinguishable to a caller (mirrors
// libs/go/mcpauth's own errInvalidToken / libs/go/whagent's own
// errAuthenticationFailed convention). It wraps sdkauth.ErrInvalidToken so
// sdkauth.RequireBearerToken's own errors.Is check treats it as a 401, not
// a 500.
var errWhagentTokenInvalid = fmt.Errorf("mcp: invalid, expired, or unverifiable whagent-net credential: %w", sdkauth.ErrInvalidToken)

// isWhagentShapedToken reports whether token is shaped like a whagent
// Claim JWT (RFC 7519 compact serialization: exactly three non-empty,
// dot-separated segments) rather than an ASS mcp_credential (a
// 64-character hex string with no dots -- see
// libs/go/mcpauth/credential.go's generateToken). DualAuthHTTPHandler uses
// this to decide which of the two verification paths a given request's
// bearer token belongs to, without ever trying a credential against the
// wrong path's verifier (NFR4): the two encodings never overlap, so this
// split is exact, not probabilistic.
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
// (RFC 6750), or "" if none is present -- the same extraction
// sdkauth.RequireBearerToken performs internally, duplicated here (rather
// than exported by that package) purely so DualAuthHTTPHandler can inspect
// the token's shape before deciding which of the two
// sdkauth.RequireBearerToken-guarded branches actually handles the
// request. An empty return routes to the mcp_credential branch by default,
// which then rejects with its own standard "no bearer token" 401 -- the
// same behavior NewHTTPHandler already has for a missing token.
func bearerToken(r *http.Request) string {
	fields := strings.Fields(r.Header.Get("Authorization"))
	if len(fields) != 2 || !strings.EqualFold(fields[0], "bearer") {
		return ""
	}
	return fields[1]
}

// whagentTokenVerifier adapts cfg to an sdkauth.TokenVerifier: it calls
// cfg.Verifier.Verify directly (never whagent.HTTPMiddleware -- see the
// package doc comment), and on success stashes the verified *whagent.Claim
// under whagentClaimExtraKey so WhagentPersonMiddleware can read it back
// off the MCP-protocol layer's RequestExtra.
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
// paths (FR12(a)): the existing mcp_credential path (via credentials,
// unchanged from auth.go/transport.go's NewHTTPHandler) and the new
// whagent-net path (via cfg), routed per-request by isWhagentShapedToken
// so neither path is built on the other and neither can be satisfied by
// the other's credential (NFR4). Each branch is its own independent
// sdkauth.RequireBearerToken instance around mcpHandler, so a rejection in
// either branch never invokes mcpHandler (the tool handler is never
// entered on a failed verification). See NewDualAuthHTTPHandler
// (transport.go) for how `mcp`'s mux mounts this.
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

// WhagentPersonMiddleware is the MCP-protocol half of the whagent-net auth
// path (FR12(a)/(b)): for a call DualAuthHTTPHandler routed through the
// whagent path (i.e. whagentClaimExtraKey is present on the resolved
// TokenInfo.Extra), it resolves the verified Claim's (iss, sub) to a
// Person -- auto-provisioning on first sight via
// identities.FindOrCreateByIssSub -- and places it on ctx exactly like
// PersonMiddleware does (withPerson, context.go), so every downstream tool
// handler, and the existing (tool, personID, key) idempotency guard
// (FR11, idempotency.go), sees one Person shape regardless of which auth
// path resolved it. A call DualAuthHTTPHandler routed through the
// existing mcp_credential path has no whagentClaimExtraKey to find, so
// this middleware calls next unchanged -- next is PersonMiddleware
// (server.go wires both as receiving middleware, this one mounted to run
// first), which authenticates it exactly as it always has.
func WhagentPersonMiddleware(identities store.PersonIdentityStore) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			extra := req.GetExtra()
			if extra == nil || extra.TokenInfo == nil {
				return next(ctx, method, req)
			}

			claim, ok := extra.TokenInfo.Extra[whagentClaimExtraKey].(*whagent.Claim)
			if !ok || claim == nil {
				// Not a whagent-routed call -- fall through to
				// PersonMiddleware, unchanged.
				return next(ctx, method, req)
			}

			person, _, err := identities.FindOrCreateByIssSub(ctx, claim.SubjectIssuer, claim.Subject)
			if err != nil {
				logger.WarnContext(ctx, "mcp call rejected: whagent identity could not be resolved", "method", method, "error", err)
				return nil, fmt.Errorf("unauthenticated: whagent identity could not be resolved")
			}

			return next(withPerson(ctx, person, AuthPathWhagent), method, req)
		}
	}
}
