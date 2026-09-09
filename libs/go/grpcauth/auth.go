package grpcauth

import (
	"context"
	"fmt"
	"strings"

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
	// ClientID is the client the token was issued to -- the `azp`
	// ("authorized party") claim, falling back to `client_id` for issuers
	// that use the OAuth2-native name instead. Present on every
	// Keycloak-issued access token, human or service account, since a token
	// always comes from some client. Empty in AuthModeNone (the injected dev
	// Claims carry no client identity) and for any TokenVerifier that
	// chooses not to populate it.
	ClientID string
	// IsServiceAccount reports whether this token was minted for a Keycloak
	// client-credentials service account rather than a human user. See
	// KEYCLOAK.md § "Service accounts" for the exact rule
	// (isServiceAccountUsername below) and why it is the one field callers
	// should branch on -- never ClientID alone, which is also populated for
	// human tokens.
	IsServiceAccount bool
}

// serviceAccountUsernamePrefix is the fixed prefix Keycloak gives every
// client-credentials service account's `preferred_username` claim:
// "service-account-<client-id>" (Keycloak's own naming, not configurable).
// This is the only marker distinguishing a service-account token from a
// human one -- there is no separate token type, scope, or claim for it.
// Keycloak reserves this prefix for service accounts, so a human user's
// preferred_username (an email, an sAMAccountName, ...) can never collide
// with it. Documented in KEYCLOAK.md § "Service accounts".
const serviceAccountUsernamePrefix = "service-account-"

// isServiceAccountUsername applies the serviceAccountUsernamePrefix rule.
// Split out from oidcVerifier.Verify so the classification itself is
// unit-testable against representative preferred_username values without
// going through real JWT/JWKS verification.
func isServiceAccountUsername(preferredUsername string) bool {
	return strings.HasPrefix(preferredUsername, serviceAccountUsernamePrefix)
}

// keycloakTokenClaims is the subset of a Keycloak access token's claims
// grpcauth reads beyond the standard OIDC claims oidc.IDToken already
// exposes (sub, aud). Its own type (rather than an inline anonymous struct)
// so classifyKeycloakClaims can be called from a unit test with a
// hand-built value, bypassing real token verification entirely.
type keycloakTokenClaims struct {
	RealmAccess struct {
		Roles []string `json:"roles"`
	} `json:"realm_access"`
	// Azp ("authorized party") is the client_id of the client the token was
	// issued to. Set on every Keycloak-issued access token; client_id is the
	// fallback for other OIDC issuers that use the OAuth2-native claim name.
	Azp      string `json:"azp"`
	ClientID string `json:"client_id"`
	// PreferredUsername classifies the caller -- see
	// serviceAccountUsernamePrefix.
	PreferredUsername string `json:"preferred_username"`
}

// classifyKeycloakClaims derives Claims.ClientID and Claims.IsServiceAccount
// from raw. Separated from oidcVerifier.Verify purely so it is unit-testable
// directly against representative Keycloak claim sets.
func classifyKeycloakClaims(raw keycloakTokenClaims) (clientID string, isServiceAccount bool) {
	clientID = raw.Azp
	if clientID == "" {
		clientID = raw.ClientID
	}
	return clientID, isServiceAccountUsername(raw.PreferredUsername)
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

	// DevRoles overrides the roles carried by the fake Claims injected in
	// AuthModeNone. Defaults to []string{"admin"} when left empty. Set this
	// when a service's authorization checks use service-prefixed role names
	// (e.g. "app-registry-builder") that plain "admin" would never satisfy —
	// otherwise local/Tilt development and any dev-mode CI path silently fail
	// every role check. Ignored in AuthModeOIDC, where roles come from the
	// verified token.
	DevRoles []string
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

// ContextWithClaims returns a context carrying claims exactly as the server
// interceptor would have set it. Exported so tests in other packages can
// exercise authorization logic against handlers called directly (bypassing
// NewServerInterceptors, e.g. against an in-memory fake repository) without
// duplicating the unexported context key. See
// tools/app_registry/server/auth for a worked example.
func ContextWithClaims(ctx context.Context, claims *Claims) context.Context {
	return context.WithValue(ctx, claimsKey{}, claims)
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

	var rawClaims keycloakTokenClaims
	if err := idToken.Claims(&rawClaims); err != nil {
		return nil, fmt.Errorf("failed to parse claims: %w", err)
	}

	clientID, isServiceAccount := classifyKeycloakClaims(rawClaims)

	return &Claims{
		Subject:          idToken.Subject,
		Roles:            rawClaims.RealmAccess.Roles,
		Audience:         idToken.Audience,
		ClientID:         clientID,
		IsServiceAccount: isServiceAccount,
	}, nil
}
