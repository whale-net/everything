package whagent

import (
	"context"
	"errors"
	"net/http"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// errUnauthenticated is the single fixed error Middleware returns for every
// rejection case -- an absent, malformed, or wrong-audience/issuer verified
// credential -- mirroring mcpauth's own single-fixed-error convention
// (libs/go/mcpauth/verify.go's errInvalidToken) rather than echoing which
// specific check failed back to an MCP caller.
var errUnauthenticated = errors.New("whagent: unauthenticated: no verified whagent-net credential")

// errAuthenticationFailed is the single fixed error HTTPMiddleware's
// sdkauth.TokenVerifier returns for every Verifier.Verify failure. It wraps
// sdkauth.ErrInvalidToken so sdkauth.RequireBearerToken's own
// errors.Is(err, sdkauth.ErrInvalidToken) branch treats it as a 401, not a
// 500 -- and, like mcpauth's errInvalidToken, its Error() string is a
// compile-time constant that never varies with, or reveals, which of
// Verify's distinct rejection cases actually occurred, since that string is
// what sdkauth.RequireBearerToken writes directly to the HTTP response
// body.
var errAuthenticationFailed = errAuthenticationFailedWrap{}

type errAuthenticationFailedWrap struct{}

func (errAuthenticationFailedWrap) Error() string {
	return "whagent: invalid, expired, or unverifiable credential"
}

func (errAuthenticationFailedWrap) Unwrap() error { return sdkauth.ErrInvalidToken }

var _ error = errAuthenticationFailedWrap{}

// claimExtraKey is the sdkauth.TokenInfo.Extra key HTTPMiddleware stashes
// a verified *Claim under, and Middleware reads it back from
// mcp.Request.GetExtra().TokenInfo.Extra -- the same RequestExtra the
// mcp-go-sdk streamable transport already threads through from the HTTP
// layer to the MCP-protocol layer for any bearer-token auth (see
// audience_score_system/mcp/server/transport.go and auth.go for the
// mcpauth-based precedent this mirrors).
const claimExtraKey = "whagent.claim"

// claimContextKey is the unexported context key ClaimFromContext /
// Middleware use to carry a verified Claim.
type claimContextKey struct{}

// ClaimFromContext returns the Claim Middleware verified and placed on
// ctx, or nil if none -- e.g. called outside of a request Middleware
// wrapped, or (per FR12(a)) a request that came in through a domain's
// other, non-whagent auth path instead.
func ClaimFromContext(ctx context.Context) *Claim {
	claim, _ := ctx.Value(claimContextKey{}).(*Claim)
	return claim
}

// withClaim returns a copy of ctx carrying claim -- the Implementation-
// phase Middleware body's only sanctioned way to set it (mirrors ASS's
// own withPerson/PersonFromContext pair in
// audience_score_system/mcp/server/context.go).
func withClaim(ctx context.Context, claim *Claim) context.Context {
	return context.WithValue(ctx, claimContextKey{}, claim)
}

// Middleware is the MCP-protocol half of this package's verifying
// middleware (FR12(a)): mounted via mcp.Server.AddReceivingMiddleware
// alongside -- never replacing -- whatever web-session auth path a domain
// server already has (see audience_score_system/mcp/server/auth.go's
// PersonMiddleware for the shape this mirrors). It reads the Claim
// HTTPMiddleware already verified and stashed on the request (via
// claimExtraKey), places it on ctx (ClaimFromContext / withClaim), and
// calls next; on any verification failure it rejects and never invokes
// next. v and aud are the same Verifier/audience HTTPMiddleware was
// constructed with -- used here as a defense-in-depth re-check (the
// stashed Claim's issuer and audience still match what this exact
// Middleware instance expects) rather than a second signature
// verification, which HTTPMiddleware already performed.
func Middleware(v *Verifier, aud string) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			extra := req.GetExtra()
			if extra == nil || extra.TokenInfo == nil {
				return nil, errUnauthenticated
			}

			claim, ok := extra.TokenInfo.Extra[claimExtraKey].(*Claim)
			if !ok || claim == nil {
				return nil, errUnauthenticated
			}

			if claim.Issuer != v.issuer || !claim.Audience.Contains(aud) {
				return nil, errUnauthenticated
			}

			return next(withClaim(ctx, claim), method, req)
		}
	}
}

// HTTPMiddleware is the HTTP-layer half of this package's verifying
// middleware (FR12(a)): it extracts the bearer credential from the
// request, verifies it against v for aud, and -- on success -- stashes
// the resulting Claim (via claimExtraKey) where Middleware can read it
// back off mcp.Request.GetExtra(). Built on sdkauth.RequireBearerToken
// (the same bearer-extraction machinery libs/go/mcpauth.RequireBearerToken
// already uses), but with its own TokenVerifier calling v.Verify --
// mountable as a standalone authentication path that does not require
// libs/go/mcpauth to be anywhere in the chain (FR12(a)).
func HTTPMiddleware(v *Verifier, aud string) func(http.Handler) http.Handler {
	verifier := sdkauth.TokenVerifier(func(ctx context.Context, token string, _ *http.Request) (*sdkauth.TokenInfo, error) {
		claim, err := v.Verify(ctx, token, aud)
		if err != nil {
			return nil, errAuthenticationFailed
		}
		return &sdkauth.TokenInfo{
			UserID:     claim.Subject,
			Expiration: claim.Expiry.Time(),
			Extra:      map[string]any{claimExtraKey: claim},
		}, nil
	})
	return sdkauth.RequireBearerToken(verifier, nil)
}
