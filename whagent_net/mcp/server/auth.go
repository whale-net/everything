package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/libs/go/grpcauth"
)

// tokenExtraKey is the sdkauth.TokenInfo.Extra key PassthroughVerifier
// stores the caller's raw bearer token under, for AuthMiddleware to read
// back off the request.
const tokenExtraKey = "raw_token"

// PassthroughVerifier is an sdkauth.TokenVerifier that performs no
// verification of its own: whagent-net's identity pass-through design
// (issue #2120's Implementation section) puts token verification solely
// at `api` (grpcauth.NewServerInterceptors, whagent_net/api/main.go) --
// `mcp` never runs its own OIDC verifier, never holds a Keycloak client
// secret for verification purposes, and never decides whether a token is
// valid. Its only job is to capture the raw bearer token unexamined so
// AuthMiddleware (below) can forward it, byte for byte, to `api` -- the
// same token, not a re-minted one, so `api` sees the operator's own
// Keycloak identity as the caller (FR10). A missing/malformed
// "Authorization: Bearer <token>" header is still rejected here, before
// any tool handler runs -- see NewHTTPHandler's
// AllowMissingExpiration:true (this token carries no expiration
// PassthroughVerifier itself enforces; `api`'s verifier is what actually
// checks exp).
func PassthroughVerifier(_ context.Context, token string, _ *http.Request) (*sdkauth.TokenInfo, error) {
	if strings.TrimSpace(token) == "" {
		return nil, sdkauth.ErrInvalidToken
	}
	return &sdkauth.TokenInfo{
		Extra: map[string]any{tokenExtraKey: token},
	}, nil
}

// AuthMiddleware is the mcp.Middleware every request passes through
// (wired in server.New): it reads the bearer token
// NewHTTPHandler/PassthroughVerifier already captured off the HTTP
// request (req.GetExtra().TokenInfo) and places it on ctx via
// grpcauth.WithUserToken -- the exact mechanism
// grpcauth.NewUserTokenDialOption reads from when a tool
// (../tools/*.go) calls `api` (issue #2120's Implementation section,
// "Identity pass-through"). A call with no bearer token forwarded from
// the HTTP layer is rejected here and next is never invoked -- no tool
// handler, and therefore no call to `api`'s gRPC client, is reachable
// without one.
func AuthMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		extra := req.GetExtra()
		if extra == nil || extra.TokenInfo == nil {
			return nil, errors.New("unauthenticated: no bearer token forwarded")
		}

		token, _ := extra.TokenInfo.Extra[tokenExtraKey].(string)
		if token == "" {
			return nil, errors.New("unauthenticated: no bearer token forwarded")
		}

		return next(grpcauth.WithUserToken(ctx, token), method, req)
	}
}
