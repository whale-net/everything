package ratelimit

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// UnaryServerInterceptor returns a gRPC unary interceptor that applies rate limiting
// to read RPCs. It extracts the principal and session from the context and checks
// if the request is allowed under the configured limits.
//
// The limiter uses a named bucket that must be configured in the registry.
// If the bucket is not found, the request is allowed (fail-open).
//
// Rate-limited requests are refused with ResourceExhausted status code.
func UnaryServerInterceptor(limiter *Limiter, bucketName string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		// Extract principal and session from context
		key := extractKey(ctx)

		// Check rate limit
		if !limiter.Allow(key, bucketName) {
			return nil, status.Error(codes.ResourceExhausted, "rate limit exceeded")
		}

		// Call the actual handler
		return handler(ctx, req)
	}
}

// StreamServerInterceptor returns a gRPC stream interceptor that applies rate limiting
// to read RPCs. It extracts the principal and session from the context and checks
// if the request is allowed under the configured limits.
func StreamServerInterceptor(limiter *Limiter, bucketName string) grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		// Extract principal and session from context
		ctx := ss.Context()
		key := extractKey(ctx)

		// Check rate limit
		if !limiter.Allow(key, bucketName) {
			return status.Error(codes.ResourceExhausted, "rate limit exceeded")
		}

		// Call the actual handler
		return handler(srv, ss)
	}
}

// extractKey extracts a rate limiting key from the context.
// It first attempts to extract the authenticated principal.
// If authentication is not present, it uses a default anonymous key.
func extractKey(ctx context.Context) Key {
	// Try to extract principal from auth context
	// The grpcauth library adds the subject to the context under a specific key.
	// For now, we'll use a placeholder that can be overridden by callers.

	// Extract principal - could come from auth context or other sources
	principal, ok := ctx.Value("auth-subject").(string)
	if !ok || principal == "" {
		principal = "anonymous"
	}

	// Extract session if available
	session, ok := ctx.Value("session-id").(string)
	if ok && session != "" {
		return ForSession(principal, session)
	}

	return ForPrincipal(principal)
}
