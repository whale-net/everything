// Caller authentication, whagent-net path (issue #2116, FR12) -- new,
// parallel to (never built on top of) auth.go's mcp_credential ->
// PersonMiddleware flow. See ../../ARCHITECTURE.md "MCP server: caller
// authentication" for the two-path design once Implementation lands it
// there (issue #2116's Implementation phase updates that doc); this file's
// job for now is settling the shape both phases build on.
//
// Design note for Implementation phase (and, per this task's Validation
// criterion, worth flagging back to issue #2116 / libs/go/whagent if it
// stays unresolved): //libs/go/whagent's Middleware unconditionally
// rejects a call carrying no verified whagent Claim, and its companion
// HTTPMiddleware is the only sanctioned way to populate the private
// extra-key Middleware reads back off the request -- there is no exported
// way to make either a no-op pass-through for a request that instead came
// in via this domain's OTHER, pre-existing auth path. Mounting
// HTTPMiddleware+Middleware verbatim in series alongside the existing
// mcpauth.RequireBearerToken -> PersonMiddleware chain would therefore
// reject every mcp_credential call outright (Middleware has no claim to
// find on such a call and unconditionally errors) -- exactly the "built on
// top of" failure mode FR12(a) forbids.
//
// The plan this file scaffolds instead:
//  1. Route each request's bearer token to exactly one of the two paths at
//     the HTTP layer (DualAuthHTTPHandler), keyed on token SHAPE: a
//     whagent Claim is always a three-segment, two-dot JWT compact
//     serialization; an ASS mcp_credential is always a 64-character hex
//     string with no dots (see libs/go/mcpauth/credential.go's
//     generateToken) -- the two encodings never overlap, so this is a
//     clean, not probabilistic, split.
//  2. For the whagent-shaped case, call whagent.Verifier.Verify directly
//     -- an equally-exported, equally-documented entry point on the same
//     published contract -- rather than whagent.HTTPMiddleware, so this
//     file can build its own TokenInfo/context marker that the combined
//     MCP-protocol middleware (WhagentPersonMiddleware) branches on,
//     instead of hitting the double-rejection problem above.
package server

import (
	"context"
	"errors"
	"net/http"

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

// errWhagentAuthNotImplemented is returned by this file's stubs until the
// Implementation phase of issue #2116 lands the routing/resolution logic
// described in the package doc comment above. Scaffold exists to settle
// the public shape here -- WhagentAuthConfig, DualAuthHTTPHandler's and
// WhagentPersonMiddleware's signatures, and where they're mounted
// (transport.go, main.go) -- not their bodies.
var errWhagentAuthNotImplemented = errors.New("mcp: whagent auth path not implemented yet (scaffold phase, see issue #2116)")

// isWhagentShapedToken reports whether token is shaped like a whagent
// Claim JWT (RFC 7519 compact serialization: exactly three dot-separated,
// non-empty segments) rather than an ASS mcp_credential (a 64-character
// hex string with no dots -- see libs/go/mcpauth/credential.go's
// generateToken). DualAuthHTTPHandler's Implementation-phase body uses
// this to decide which of the two verification paths a given request's
// bearer token belongs to, without ever trying a credential against the
// wrong path's verifier (NFR4).
func isWhagentShapedToken(token string) bool {
	// Implementation phase: three non-empty, dot-separated segments.
	return false
}

// DualAuthHTTPHandler wraps mcpHandler with BOTH caller-authentication
// paths (FR12(a)): the existing mcp_credential path (via credentials,
// unchanged from auth.go/transport.go's NewHTTPHandler) and the new
// whagent-net path (via cfg), routed per-request by isWhagentShapedToken
// so neither path is built on the other and neither can be satisfied by
// the other's credential (NFR4). See NewDualAuthHTTPHandler
// (transport.go) for how `mcp`'s mux mounts this. Scaffold stub: returns
// mcpHandler unwrapped, so wiring this in today changes no runtime
// behavior until Implementation lands the routing body.
func DualAuthHTTPHandler(mcpHandler http.Handler, credentials mcpauth.CredentialStore, cfg WhagentAuthConfig, mcpauthOpts *sdkauth.RequireBearerTokenOptions) http.Handler {
	return mcpHandler
}

// WhagentPersonMiddleware is the MCP-protocol half of the whagent-net auth
// path (FR12(a)/(b)): for a call DualAuthHTTPHandler routed through the
// whagent path, it must resolve the verified Claim's (iss, sub) to a
// Person -- auto-provisioning on first sight via
// identities.FindOrCreateByIssSub -- and place it on ctx exactly like
// PersonMiddleware does (withPerson, context.go), so every downstream tool
// handler, and the existing (tool, personID, key) idempotency guard
// (FR11, idempotency.go), sees one Person shape regardless of which auth
// path resolved it. A call DualAuthHTTPHandler routed through the
// existing mcp_credential path must instead fall through to
// PersonMiddleware, unchanged (server.go wires both as receiving
// middleware). Scaffold stub: always calls next unconditionally, so
// wiring this into server.go today changes no runtime behavior until
// Implementation lands the routing-marker check and identity resolution.
func WhagentPersonMiddleware(identities store.PersonIdentityStore) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			return next(ctx, method, req)
		}
	}
}
