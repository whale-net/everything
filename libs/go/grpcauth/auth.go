package grpcauth

import (
	"context"
	"fmt"

	"github.com/coreos/go-oidc/v3/oidc"
)

// AuthMode defines the authentication mode
type AuthMode string

const (
	AuthModeNone AuthMode = "none"
	AuthModeOIDC AuthMode = "oidc"
)

// Claims holds authenticated user/service account claims
type Claims struct {
	Subject  string
	Roles    []string
	Audience []string
}

// TokenVerifier verifies tokens and returns Claims
type TokenVerifier interface {
	Verify(ctx context.Context, token string) (*Claims, error)
}

// ServerConfig holds server-side auth configuration
type ServerConfig struct {
	Mode      AuthMode
	IssuerURL string // required for OIDC
	ClientID  string // expected audience
}

// ClientConfig holds client-side auth configuration
type ClientConfig struct {
	Mode                     AuthMode
	TokenURL                 string // full Keycloak token endpoint URL
	ClientID                 string
	ClientSecret             string
	RequireTransportSecurity bool // false for internal cluster services
}

// claimsKey is the unexported context key for Claims
type claimsKey struct{}

// ClaimsFromContext retrieves Claims injected by the server interceptor
func ClaimsFromContext(ctx context.Context) (*Claims, bool) {
	claims, ok := ctx.Value(claimsKey{}).(*Claims)
	return claims, ok
}

// oidcVerifier implements TokenVerifier using go-oidc
type oidcVerifier struct {
	verifier *oidc.IDTokenVerifier
}

// newOIDCVerifier creates a new OIDC token verifier
func newOIDCVerifier(ctx context.Context, issuerURL, clientID string) (*oidcVerifier, error) {
	provider, err := oidc.NewProvider(ctx, issuerURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create OIDC provider: %w", err)
	}
	verifier := provider.Verifier(&oidc.Config{ClientID: clientID})
	return &oidcVerifier{verifier: verifier}, nil
}

// Verify verifies a JWT access token and extracts Claims
func (v *oidcVerifier) Verify(ctx context.Context, token string) (*Claims, error) {
	idToken, err := v.verifier.Verify(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("failed to verify token: %w", err)
	}

	var rawClaims struct {
		RealmAccess struct {
			Roles []string `json:"roles"`
		} `json:"realm_access"`
	}
	if err := idToken.Claims(&rawClaims); err != nil {
		return nil, fmt.Errorf("failed to parse claims: %w", err)
	}

	return &Claims{
		Subject:  idToken.Subject,
		Roles:    rawClaims.RealmAccess.Roles,
		Audience: idToken.Audience,
	}, nil
}
