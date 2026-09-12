package server

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/libs/go/mcpauth"
	"github.com/whale-net/everything/whagent_net/mcpidentity"
)

// logger is this package's shared logger. Per AGENTS.md's logging-level
// convention: a rejected credential is expected control flow, never logged
// as an ERROR (see NewVerifier); a corrupt stored identity is not expected
// control flow and is logged as an ERROR.
var logger = logging.Get("whagent_net/mcp/server")

// tokenExtraKey is the sdkauth.TokenInfo.Extra key the manual-token path
// stores the caller's raw bearer token under, for AuthMiddleware to read
// back off the request.
const tokenExtraKey = "raw_token"

// identityExtraKey is the sdkauth.TokenInfo.Extra key the OAuth2 path
// (an opaque mcpauth credential) stores its decoded (iss, sub) identity
// under, for AuthMiddleware to read back off the request and place on ctx
// via mcpidentity.ContextWithIdentity (issue #2430 -- no exchange happens
// here any more, see AuthMiddleware's own doc comment).
const identityExtraKey = "resolved_identity"

// resolvedIdentity is the value stored under identityExtraKey: the real
// Keycloak (iss, sub) pair a credentialVerifier decoded from an opaque
// mcpauth credential's identity (NFR7) -- never the encoded string
// itself, and never anything mcpauth.CredentialStore.Verify returns
// directly.
type resolvedIdentity struct {
	iss string
	sub string
}

// credentialShapePattern matches an mcpauth opaque bearer credential:
// exactly the 64 lowercase hex characters
// libs/go/mcpauth/credential.go's generateToken produces (32
// crypto/rand bytes, hex-encoded) -- with no dot anywhere, unlike a JWT
// compact serialization (three dot-separated segments). This mirrors
// audience_score_system/mcp/server/whagent_auth.go's own
// isWhagentShapedToken: the two encodings never overlap, so routing on
// shape alone is exact, not probabilistic (NFR4-equivalent for this
// domain's own two paths).
var credentialShapePattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// isCredentialShaped reports whether token is shaped like an mcpauth
// opaque credential (see credentialShapePattern) rather than a Keycloak
// JWT.
func isCredentialShaped(token string) bool {
	return credentialShapePattern.MatchString(token)
}

// NewVerifier builds the sdkauth.TokenVerifier NewHTTPHandler wraps every
// request in: whagent-net's identity pass-through design (issue #2120's
// Implementation section) is unchanged for a token that is not
// credential-shaped -- `mcp` never verifies a Keycloak JWT itself, it
// only captures it unexamined so AuthMiddleware can forward it, byte for
// byte, to `api` (FR10). FR9 (issue #2249) adds exactly one new
// classification: a token shaped like an mcpauth opaque credential
// (isCredentialShaped) is resolved via credentials.Verify (which also
// stamps last_used_at, mcpauth.CredentialStore's own contract) --
// success decodes to a real Keycloak (iss, sub) pair via
// whagent_net/mcpidentity.Decode (the shared helper issue #2245's `ui`
// side also uses, NFR7: no re-splitting the encoded string here) and
// routes the call onto AuthMiddleware's browser-OAuth2/resolved-identity
// branch;
// failure (unrecognized, malformed, or revoked -- mcpauth.CredentialStore
// makes all three indistinguishable, NFR1) is rejected here, before any
// tool handler runs -- this is expected control flow (a client presenting
// a stale/revoked credential), not something this package logs as an
// ERROR.
//
// credentials may be nil (FR9 not configured, e.g. PG_DATABASE_URL
// unset): every token is then treated as manual-token-shaped regardless
// of its actual shape, exactly reproducing this package's pre-FR9
// behavior -- the manual-token path never depends on the OAuth2 path
// being configured. A missing/empty bearer token is still rejected here
// either way, before any tool handler runs -- see NewHTTPHandler's
// AllowMissingExpiration:true (neither TokenInfo shape carries an
// expiration of its own; `api`'s verifier is what actually checks a
// manually-forwarded token's exp, and mcpauth credentials are revocable
// rather than time-boxed).
func NewVerifier(credentials mcpauth.CredentialStore) sdkauth.TokenVerifier {
	return func(ctx context.Context, token string, _ *http.Request) (*sdkauth.TokenInfo, error) {
		token = strings.TrimSpace(token)
		if token == "" {
			return nil, sdkauth.ErrInvalidToken
		}

		if credentials != nil && isCredentialShaped(token) {
			identity, _, err := credentials.Verify(ctx, token)
			if err != nil {
				// Expected control flow (an unrecognized, malformed, or
				// revoked credential) -- not an ERROR, and never a
				// fallback to the manual-token path: a credential-shaped
				// token that fails OAuth2-path verification is rejected
				// outright, exactly as an empty token is above.
				return nil, sdkauth.ErrInvalidToken
			}

			iss, sub, err := mcpidentity.Decode(identity)
			if err != nil {
				// This is not expected control flow: credentials.Verify
				// only ever returns identities `ui`'s mcpauth.Provider
				// minted via mcpidentity.Encode (issue #2245), so a
				// decode failure here means the stored identity is
				// corrupt -- an operator needs to know.
				logger.ErrorContext(ctx, "mcp: resolved mcpauth credential's identity failed to decode", "error", err)
				return nil, sdkauth.ErrInvalidToken
			}

			return &sdkauth.TokenInfo{
				Extra: map[string]any{identityExtraKey: resolvedIdentity{iss: iss, sub: sub}},
			}, nil
		}

		return &sdkauth.TokenInfo{
			Extra: map[string]any{tokenExtraKey: token},
		}, nil
	}
}

// AuthMiddleware is the mcp.Middleware every request passes through
// (wired in server.New): it reads the TokenInfo NewHTTPHandler/
// NewVerifier already captured off the HTTP request
// (req.GetExtra().TokenInfo) and places either a real bearer token or a
// resolved identity on ctx, for ../tools/*.go's handlers to read back --
// the former via grpcauth.WithUserToken (the exact mechanism
// grpcauth.NewUserTokenDialOption reads from when a tool calls `api`,
// issue #2120's Implementation section, "Identity pass-through"), the
// latter via whagent_net/mcpidentity.ContextWithIdentity.
//
// Two branches, matching NewVerifier's two TokenInfo shapes:
//
//   - identityExtraKey present (the browser-OAuth2 path, FR9/issue #2249):
//     this middleware no longer exchanges or otherwise mints anything
//     itself (issue #2430, FR7/FR8/FR19 -- RFC 8693 impersonation exchange
//     and its tokenexchange.go cache are gone, not left dormant). It only
//     places the already-decoded (iss, sub) on ctx via
//     mcpidentity.ContextWithIdentity; no bearer token exists on ctx at
//     this point at all. A tool handler (../tools' dispatch-time
//     resolveGrantTokenForAgent/resolveGrantTokenForSession) is what
//     resolves the domain the call actually targets and acquires a
//     working credential from GrantSource immediately before forwarding
//     -- never this middleware, which has no notion of "which domain"
//     for any given call (that requires the request's own agent_id/
//     session_id, which only a tool handler has parsed).
//   - tokenExtraKey present (the manual-token path, unchanged): the raw
//     token is forwarded byte for byte via grpcauth.WithUserToken, exactly
//     as before FR9 -- a tool handler sees no Identity on ctx for this
//     path (mcpidentity.FromContext's second return is false) and
//     performs no domain resolution or grant acquisition of its own,
//     since a real, already-usable bearer token is already on ctx.
//
// A call carrying neither -- no bearer token forwarded from the HTTP
// layer at all -- is rejected here and next is never invoked: no tool
// handler, and therefore no call to `api`'s gRPC client, is reachable
// without one.
func AuthMiddleware() mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			extra := req.GetExtra()
			if extra == nil || extra.TokenInfo == nil {
				return nil, errors.New("unauthenticated: no bearer token forwarded")
			}

			if identity, ok := extra.TokenInfo.Extra[identityExtraKey].(resolvedIdentity); ok {
				return next(mcpidentity.ContextWithIdentity(ctx, mcpidentity.Identity{Iss: identity.iss, Sub: identity.sub}), method, req)
			}

			token, _ := extra.TokenInfo.Extra[tokenExtraKey].(string)
			if token == "" {
				return nil, errors.New("unauthenticated: no bearer token forwarded")
			}

			return next(grpcauth.WithUserToken(ctx, token), method, req)
		}
	}
}
