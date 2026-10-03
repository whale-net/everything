package server

import (
	"context"
	"net/http"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/whale-net/everything/libs/go/auth"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/grpcauth/grantflow"
)

const callerExtraKey = "manmanv2.mcp.caller"

// errInvalidToken is the single fixed error for every verification failure;
// it wraps ErrInvalidToken so the SDK answers 401.
var errInvalidToken = invalidTokenError{}

type invalidTokenError struct{}

func (invalidTokenError) Error() string { return "mcp: invalid or expired credential" }
func (invalidTokenError) Unwrap() error { return sdkauth.ErrInvalidToken }

// CallerVerifier resolves a presented bearer token to the verified Caller.
type CallerVerifier func(ctx context.Context, token string) (*Caller, error)

// OIDCCallerVerifier accepts Keycloak access tokens directly.
func OIDCCallerVerifier(v grpcauth.TokenVerifier) CallerVerifier {
	return func(ctx context.Context, token string) (*Caller, error) {
		claims, err := v.Verify(ctx, token)
		if err != nil || claims == nil {
			return nil, errInvalidToken
		}
		return callerFromClaims(claims, token), nil
	}
}

// CredentialCallerVerifier accepts the opaque credentials libs/go/auth
// issues (the same as krill, audience-score-system and whagent-net) and
// acts as the credential's user: the stored delegated grant yields a fresh
// Keycloak token whose verified claims decide the persona.
func CredentialCallerVerifier(store auth.CredentialStore, ex grantflow.Exchanger) CallerVerifier {
	verify := auth.TokenVerifier(store)
	return func(ctx context.Context, token string) (*Caller, error) {
		info, err := verify(ctx, token, nil)
		if err != nil {
			return nil, errInvalidToken
		}
		access, claims, err := ex.Exchange(ctx, info.UserID)
		if err != nil {
			return nil, errInvalidToken
		}
		return callerFromClaims(claims, access), nil
	}
}

func callerFromClaims(claims *grpcauth.Claims, token string) *Caller {
	return &Caller{
		Issuer:  claims.Issuer,
		Subject: claims.Subject,
		Roles:   claims.Roles,
		Persona: ResolvePersona(claims.Roles),
		Token:   token,
	}
}

// HTTPAuth is HTTPAuthWith over Keycloak access tokens.
func HTTPAuth(v grpcauth.TokenVerifier, resourceMetadataURL string) func(http.Handler) http.Handler {
	return HTTPAuthWith(OIDCCallerVerifier(v), resourceMetadataURL)
}

// HTTPAuthWith rejects any request without a verifiable bearer token (401,
// next never invoked) and otherwise stashes the resolved Caller where
// Middleware reads it. There is no anonymous path. resourceMetadataURL, if
// set, is advertised in the 401 challenge.
func HTTPAuthWith(v CallerVerifier, resourceMetadataURL string) func(http.Handler) http.Handler {
	verify := func(ctx context.Context, token string, _ *http.Request) (*sdkauth.TokenInfo, error) {
		caller, err := v(ctx, token)
		if err != nil || caller == nil {
			return nil, errInvalidToken
		}
		return &sdkauth.TokenInfo{UserID: caller.Subject, Extra: map[string]any{callerExtraKey: caller}}, nil
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
