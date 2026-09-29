// Package authdoor is the api binary's auth front door: it verifies a
// presented bearer token (opaque mcpauth credential or Keycloak JWT),
// resolves the caller's identity and persona, and attaches both to the
// request context. Every route except /healthz requires a token (401);
// reads need reader or operator, writes need operator (403).
package authdoor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/whale-net/everything/krill/caller"
	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/libs/go/auth"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/logging"
)

var logger = logging.Get("krill/api/authdoor")

// Config wires the two doors. A nil Credentials or OIDC verifier disables
// that door (its tokens are rejected).
type Config struct {
	// Credentials verifies opaque mcpauth tokens and supplies their persona.
	Credentials auth.CredentialStore
	// OIDC verifies Keycloak JWTs; used only when a token's unverified iss
	// exactly equals OIDCIssuer.
	OIDC       grpcauth.TokenVerifier
	OIDCIssuer string
	Roles      server.RoleConfig
}

// Caller is the verified caller attached to the request context.
type Caller struct {
	Identity caller.Identity
	Persona  server.Persona
}

type ctxKey struct{}

// FromContext returns the Caller the middleware attached, if any.
func FromContext(ctx context.Context) (Caller, bool) {
	c, ok := ctx.Value(ctxKey{}).(Caller)
	return c, ok
}

var errForbidden = errors.New("no persona")

// isProbe reports whether path is an unauthenticated health/readiness probe.
func isProbe(path string) bool { return path == "/healthz" || path == "/readyz" }

// isRead reports whether the request only reads; every other method is a write.
func isRead(r *http.Request) bool {
	return r.Method == http.MethodGet || r.Method == http.MethodHead
}

// Middleware rejects a missing or invalid token (401) and a persona that may
// not perform the request (403), on every route but the probes.
func Middleware(cfg Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isProbe(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			token := grpcauth.BearerToken(r)
			if token == "" {
				http.Error(w, "unauthenticated", http.StatusUnauthorized)
				return
			}
			c, err := cfg.resolve(r.Context(), token)
			switch {
			case errors.Is(err, errForbidden):
				http.Error(w, "forbidden", http.StatusForbidden)
			case err != nil:
				logger.WarnContext(r.Context(), "api credential rejected", "error", err)
				http.Error(w, "unauthenticated", http.StatusUnauthorized)
			case !isRead(r) && c.Persona != server.PersonaSwarmOperator:
				http.Error(w, "forbidden: operator role required", http.StatusForbidden)
			default:
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, c)))
			}
		})
	}
}

func (cfg Config) resolve(ctx context.Context, token string) (Caller, error) {
	if !isJWTShaped(token) {
		return cfg.resolveOpaque(ctx, token)
	}
	iss, ok := unverifiedIssuer(token)
	if !ok || cfg.OIDC == nil || cfg.OIDCIssuer == "" || iss != cfg.OIDCIssuer {
		return Caller{}, caller.ErrUnauthenticated
	}
	claims, err := cfg.OIDC.Verify(ctx, token)
	if err != nil {
		return Caller{}, err
	}
	id, err := caller.FromOIDCClaims(claims)
	if err != nil {
		return Caller{}, err
	}
	persona, ok := cfg.Roles.ResolvePersona(claims.Roles)
	if !ok {
		return Caller{}, errForbidden
	}
	return Caller{Identity: id, Persona: persona}, nil
}

func (cfg Config) resolveOpaque(ctx context.Context, token string) (Caller, error) {
	if cfg.Credentials == nil {
		return Caller{}, caller.ErrUnauthenticated
	}
	identity, cred, err := cfg.Credentials.Verify(ctx, token)
	if err != nil {
		return Caller{}, caller.ErrUnauthenticated
	}
	id, err := caller.FromTokenInfo(&sdkauth.TokenInfo{UserID: identity})
	if err != nil {
		return Caller{}, err
	}
	persona := server.Persona(cred.Persona)
	if persona != server.PersonaSwarmOperator && persona != server.PersonaReader {
		return Caller{}, errForbidden
	}
	return Caller{Identity: id, Persona: persona}, nil
}

func isJWTShaped(token string) bool {
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

// unverifiedIssuer reads the payload's iss without verifying the
// signature; it only routes, the chosen verifier still checks everything.
func unverifiedIssuer(token string) (string, bool) {
	payload, err := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[1])
	if err != nil {
		return "", false
	}
	var c struct {
		Iss string `json:"iss"`
	}
	if json.Unmarshal(payload, &c) != nil || c.Iss == "" {
		return "", false
	}
	return c.Iss, true
}
