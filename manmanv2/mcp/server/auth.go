package server

import (
	"context"
	"net/http"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/whale-net/everything/libs/go/grpcauth"
)

const callerExtraKey = "manmanv2.mcp.caller"

// errInvalidToken is the single fixed error for every verification failure;
// it wraps ErrInvalidToken so the SDK answers 401.
var errInvalidToken = invalidTokenError{}

type invalidTokenError struct{}

func (invalidTokenError) Error() string { return "mcp: invalid or expired credential" }
func (invalidTokenError) Unwrap() error { return sdkauth.ErrInvalidToken }

// HTTPAuth rejects any request without a verifiable bearer token (401, next
// never invoked) and otherwise stashes the resolved Caller where Middleware
// reads it. There is no anonymous path. resourceMetadataURL, if set, is
// advertised in the 401 challenge.
func HTTPAuth(v grpcauth.TokenVerifier, resourceMetadataURL string) func(http.Handler) http.Handler {
	verify := func(ctx context.Context, token string, _ *http.Request) (*sdkauth.TokenInfo, error) {
		claims, err := v.Verify(ctx, token)
		if err != nil || claims == nil {
			return nil, errInvalidToken
		}
		caller := &Caller{
			Issuer:  claims.Issuer,
			Subject: claims.Subject,
			Roles:   claims.Roles,
			Persona: ResolvePersona(claims.Roles),
			Token:   token,
		}
		return &sdkauth.TokenInfo{UserID: claims.Subject, Extra: map[string]any{callerExtraKey: caller}}, nil
	}
	return sdkauth.RequireBearerToken(verify, &sdkauth.RequireBearerTokenOptions{
		ResourceMetadataURL:    resourceMetadataURL,
		AllowMissingExpiration: true,
	})
}

func callerFromExtra(info *sdkauth.TokenInfo) *Caller {
	if info == nil {
		return nil
	}
	c, _ := info.Extra[callerExtraKey].(*Caller)
	return c
}
