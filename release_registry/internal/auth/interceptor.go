// Package auth provides gRPC server-side authentication interceptors for the release registry.
package auth

import (
	"context"
	"os"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"google.golang.org/grpc"
)

// NewServerInterceptors returns unary and stream server interceptors that validate Keycloak OIDC
// JWTs from the "authorization: Bearer <token>" metadata header.
//
// When GRPC_AUTH_MODE is "none" (the default), dev Claims are injected without verification,
// so the server runs fully open for local development with no Keycloak needed.
// When set to "oidc", every request must carry a valid access token issued by the configured
// Keycloak realm; invalid tokens return codes.Unauthenticated.
//
// Protected RPCs (all unary — handled by UnaryServerInterceptor):
//   - RegisterApp       — registers an application with the release registry
//   - RegisterCommit    — records a git commit for version mapping
//   - RegisterArtifact  — binds an artifact to an app + version
//   - Promote           — promotes an artifact version into an environment
//   - Resolve           — returns the active version and commit for an app in an env
//
// No RegistryService RPC uses streaming, so StreamServerInterceptor is a no-op fallback
// but is registered alongside UnaryInterceptor via grpc.NewServer() for completeness.
func NewServerInterceptors(ctx context.Context) (grpc.UnaryServerInterceptor, grpc.StreamServerInterceptor, error) {
	return grpcauth.NewServerInterceptors(ctx, grpcauth.ServerConfig{
		Mode:      grpcauth.AuthMode(os.Getenv("GRPC_AUTH_MODE")),
		IssuerURL: os.Getenv("GRPC_OIDC_ISSUER"),
		ClientID:  os.Getenv("GRPC_OIDC_CLIENT_ID"),
	})
}

// AuthEnabled returns true when the server is configured for OIDC auth mode.
func AuthEnabled() bool {
	return os.Getenv("GRPC_AUTH_MODE") == "oidc"
}
