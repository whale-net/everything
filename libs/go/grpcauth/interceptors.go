package grpcauth

import (
	"context"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// NewServerInterceptors creates unary and stream server interceptors for authentication.
// In AuthModeNone, dev Claims are injected without verification.
// In AuthModeOIDC, the Bearer token from the authorization metadata is verified via OIDC.
func NewServerInterceptors(ctx context.Context, config ServerConfig) (grpc.UnaryServerInterceptor, grpc.StreamServerInterceptor, error) {
	var verifier TokenVerifier
	if config.Mode == AuthModeOIDC {
		v, err := newOIDCVerifier(ctx, config.IssuerURL, config.ClientID)
		if err != nil {
			return nil, nil, err
		}
		verifier = v
	}

	devClaims := &Claims{
		Subject: "dev-user",
		Roles:   []string{"admin"},
	}

	unary := func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		ctx, err := authenticate(ctx, config.Mode, verifier, devClaims)
		if err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}

	stream := func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx, err := authenticate(ss.Context(), config.Mode, verifier, devClaims)
		if err != nil {
			return err
		}
		return handler(srv, &wrappedStream{ss, ctx})
	}

	return unary, stream, nil
}

// authenticate extracts and verifies the token, returning an enriched context
func authenticate(ctx context.Context, mode AuthMode, verifier TokenVerifier, devClaims *Claims) (context.Context, error) {
	if mode == AuthModeNone {
		return context.WithValue(ctx, claimsKey{}, devClaims), nil
	}

	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "missing metadata")
	}

	authValues := md.Get("authorization")
	if len(authValues) == 0 {
		return nil, status.Error(codes.Unauthenticated, "missing authorization header")
	}

	rawToken := authValues[0]
	if !strings.HasPrefix(rawToken, "Bearer ") {
		return nil, status.Error(codes.Unauthenticated, "invalid authorization format, expected Bearer token")
	}
	token := strings.TrimPrefix(rawToken, "Bearer ")

	claims, err := verifier.Verify(ctx, token)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "invalid token: %v", err)
	}

	return context.WithValue(ctx, claimsKey{}, claims), nil
}

// wrappedStream wraps grpc.ServerStream with a modified context
type wrappedStream struct {
	grpc.ServerStream
	ctx context.Context
}

// Context returns the wrapped context
func (w *wrappedStream) Context() context.Context {
	return w.ctx
}
