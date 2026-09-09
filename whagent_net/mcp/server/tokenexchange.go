// RFC 8693 token-exchange skeleton (issue #2249's Scaffold phase, FR9
// (C27)): the config plumbing and interface shape a dependent
// Implementation-phase change to auth.go will use to exchange a resolved
// mcpauth identity for a short-lived, real Keycloak-signed JWT. This file
// deliberately does not implement the actual RFC 8693
// grant_type=urn:ietf:params:oauth:grant-type:token-exchange HTTP call,
// its per-identity in-memory cache, or the dual-path
// PassthroughVerifier replacement -- see this issue's Implementation
// section. Nothing here is wired into the request path yet: auth.go's
// PassthroughVerifier/AuthMiddleware are untouched, so `mcp`'s behavior
// is unchanged by this file (bazel build //whagent_net/mcp/... is green,
// but no caller-facing behavior differs).
package server

import (
	"context"
	"errors"
)

// TokenExchangeConfig is mcp's own confidential-client settings for the
// RFC 8693 Keycloak token exchange (NFR8, FR9's OAuth2 path) -- a
// distinct Keycloak client, with token-exchange/impersonation rights,
// from the WHAGENT_OIDC_CLIENT_ID/WHAGENT_OIDC_CLIENT_SECRET pair `ui`
// and `mcp` already read for the manual-token recipe (../ENV.md
// "Identity"): that client only ever verifies or forwards a token it did
// not mint itself, while this one actively mints a new Keycloak-signed
// JWT on an operator's behalf, so keeping the two separate keeps this
// credential's blast radius (NFR8) legible and independently rotatable.
// Read from the environment (WHAGENT_MCP_KEYCLOAK_CLIENT_ID/
// WHAGENT_MCP_KEYCLOAK_CLIENT_SECRET/WHAGENT_MCP_KEYCLOAK_TOKEN_URL,
// ../ENV.md "`mcp` server") and provisioned as a Kubernetes secret --
// never checked in, never logged, never echoed in an error (NFR8; see a
// dependent Implementation-phase change's doc comment on Exchange for
// the exact contract once that lands).
type TokenExchangeConfig struct {
	// ClientID is mcp's own confidential client id in Keycloak.
	ClientID string

	// ClientSecret authenticates ClientID against TokenEndpoint. Never
	// logged and never included in any error message this package
	// returns (NFR8).
	ClientSecret string

	// TokenEndpoint is Keycloak's token endpoint URL for the realm
	// WHAGENT_OIDC_ISSUER names, e.g.
	// "https://keycloak.example.com/realms/whagent/protocol/openid-connect/token".
	TokenEndpoint string
}

// Enabled reports whether cfg carries everything Exchange needs to ever
// succeed. A zero-value TokenExchangeConfig -- the default today, since
// FR9's OAuth2 path is additive and opt-in -- is not itself an error:
// NewKeycloakExchanger always constructs successfully regardless of
// Enabled(). It is a dependent Implementation-phase change to auth.go's
// dual-path verifier that must fail startup loudly (NFR8) if it tries to
// route a call onto the OAuth2 path while Enabled() is false -- never
// this type, and never main.go's non-fatal construction (mirrors
// whagent_net/ui/main.go's initializeSSEHub degrade-and-log convention
// for every other optional dependency in this binary).
func (cfg TokenExchangeConfig) Enabled() bool {
	return cfg.ClientID != "" && cfg.ClientSecret != "" && cfg.TokenEndpoint != ""
}

// Exchanger exchanges an already-decoded Keycloak (iss, sub) pair --
// never the raw mcpidentity-encoded string, and never a value
// mcpauth.CredentialStore.Verify returns directly (NFR7: this package
// introduces no local user table, no MCP-only identity column) -- for a
// short-lived, real Keycloak-signed JWT asserting that subject. A
// dependent Implementation-phase change to auth.go is what actually
// calls Exchange from the request path, once it has decoded an opaque
// mcpauth credential's identity via whagent_net/mcpidentity.Decode, and
// places the result on the outgoing context via grpcauth.WithUserToken
// -- the same mechanism the manual-token path already uses (auth.go's
// AuthMiddleware).
type Exchanger interface {
	Exchange(ctx context.Context, iss, sub string) (jwt string, err error)
}

// errExchangeNotImplemented is KeycloakExchanger.Exchange's placeholder
// return until a dependent Implementation-phase change lands the actual
// RFC 8693 grant_type=urn:ietf:params:oauth:grant-type:token-exchange
// HTTP call and its per-identity, bounded, in-memory cache. A single
// fixed sentinel (mirrors mcpauth.ErrInvalidCredential's shape) so
// nothing that already builds against this Scaffold-phase skeleton needs
// to change once Exchange is filled in.
var errExchangeNotImplemented = errors.New("mcp: RFC 8693 token exchange not yet implemented (issue #2249 Implementation phase)")

// KeycloakExchanger is the Exchanger implementation a dependent
// Implementation-phase change wires into auth.go's dual-path verifier:
// cfg names the confidential client and token endpoint the real RFC 8693
// request (and its cache -- never persisted) will use. This
// Scaffold-phase skeleton fixes the shape only, so main.go and that
// dependent change can both build against it today with no request-path
// behavior change.
type KeycloakExchanger struct {
	cfg TokenExchangeConfig
}

var _ Exchanger = (*KeycloakExchanger)(nil)

// NewKeycloakExchanger constructs a KeycloakExchanger. cfg.Enabled()
// reports whether Exchange can ever succeed; constructing one with a
// disabled cfg is not itself an error (see TokenExchangeConfig.Enabled's
// doc comment).
func NewKeycloakExchanger(cfg TokenExchangeConfig) *KeycloakExchanger {
	return &KeycloakExchanger{cfg: cfg}
}

// Exchange is not yet implemented -- see errExchangeNotImplemented and
// this file's package doc comment. A dependent Implementation-phase
// change replaces this body with the real RFC 8693
// grant_type=urn:ietf:params:oauth:grant-type:token-exchange request
// against cfg.TokenEndpoint, cached per (iss, sub) until shortly before
// the exchanged token's expiry.
func (e *KeycloakExchanger) Exchange(_ context.Context, _, _ string) (string, error) {
	return "", errExchangeNotImplemented
}
