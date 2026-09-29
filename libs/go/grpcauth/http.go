package grpcauth

import (
	"context"
	"net/http"
	"strings"
)

// NewOIDCVerifier returns a TokenVerifier for JWT access tokens issued by
// issuerURL whose audience contains clientID. It performs OIDC discovery
// once, at construction.
func NewOIDCVerifier(ctx context.Context, issuerURL, clientID string) (TokenVerifier, error) {
	return newOIDCVerifier(ctx, issuerURL, clientID)
}

// BearerToken extracts the RFC 6750 bearer token from r, or "" if the
// Authorization header carries none.
func BearerToken(r *http.Request) string {
	fields := strings.Fields(r.Header.Get("Authorization"))
	if len(fields) != 2 || !strings.EqualFold(fields[0], "bearer") {
		return ""
	}
	return fields[1]
}

// HTTPMiddleware verifies the request's bearer token with v and, on
// success, puts the Claims on the request context (see ClaimsFromContext).
// A missing or invalid token gets a 401 and next is not invoked.
func HTTPMiddleware(v TokenVerifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := BearerToken(r)
			if token == "" {
				http.Error(w, "unauthenticated", http.StatusUnauthorized)
				return
			}
			claims, err := v.Verify(r.Context(), token)
			if err != nil {
				http.Error(w, "unauthenticated", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r.WithContext(ContextWithClaims(r.Context(), claims)))
		})
	}
}
