package main

import (
	"context"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// authenticatedMethods is the explicit allowlist of M1-added read RPCs that
// require a valid access token for this API's audience (NFR2). A method
// added later defaults to unauthenticated unless it is deliberately listed
// here. PushDeviceConfig, GetDeviceConfig, and ListBoards predate M1 and
// stay unauthenticated through M1 so leaflab/scripts/push-config.sh and
// leaflab/Tiltfile keep working unchanged; M2 brings those three inside the
// fence. Full method strings are written out literally so a proto rename
// breaks this visibly rather than silently.
var authenticatedMethods = map[string]bool{
	"/leaflab.api.v1.LeafLabAPI/ListBoardsWithState":     true,
	"/leaflab.api.v1.LeafLabAPI/GetBoardDetail":          true,
	"/leaflab.api.v1.LeafLabAPI/GetSensorReadingHistory": true,
}

// selectiveUnaryInterceptor routes calls to authenticatedMethods through
// auth, and passes every other method straight to its handler unauthenticated.
//
// grpcauth's OIDC interceptor lets a call with no bearer token proceed
// anonymously (no error, no claims in context) -- it has no notion of which
// methods require auth, since that policy is per-service. requireClaims
// closes that gap for the methods this map does require it: it rejects with
// codes.Unauthenticated iff auth ran and produced no claims, which happens
// when GRPC_AUTH_MODE=oidc and the caller sent no (or an invalid) token.
// grpcauth's oidcVerifier itself rejects an invalid or wrong-audience token
// before returning, so that case never reaches requireClaims at all.
func selectiveUnaryInterceptor(auth grpc.UnaryServerInterceptor) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if !authenticatedMethods[info.FullMethod] {
			return handler(ctx, req)
		}
		return auth(ctx, req, info, requireClaims(handler))
	}
}

// selectiveStreamInterceptor is the stream analog of selectiveUnaryInterceptor.
// None of the M1 RPCs are streaming today, but wiring both keeps this
// wrapper a drop-in replacement for grpcauth's interceptor pair if a
// streaming RPC is added to authenticatedMethods later.
func selectiveStreamInterceptor(auth grpc.StreamServerInterceptor) grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if !authenticatedMethods[info.FullMethod] {
			return handler(srv, ss)
		}
		return auth(srv, ss, info, requireClaimsStream(handler))
	}
}

// requireClaims wraps a unary handler so it rejects with
// codes.Unauthenticated when no grpcauth.Claims were placed in the context
// by the time it runs. In AuthModeNone, grpcauth always injects dev claims,
// so this never fires. In AuthModeOIDC, it is what turns "anonymous, no
// error" into the Unauthenticated response NFR2 requires for the three
// fenced RPCs.
func requireClaims(handler grpc.UnaryHandler) grpc.UnaryHandler {
	return func(ctx context.Context, req interface{}) (interface{}, error) {
		if _, ok := grpcauth.ClaimsFromContext(ctx); !ok {
			return nil, status.Error(codes.Unauthenticated, "authentication required")
		}
		return handler(ctx, req)
	}
}

// requireClaimsStream is requireClaims for a grpc.StreamHandler.
func requireClaimsStream(handler grpc.StreamHandler) grpc.StreamHandler {
	return func(srv interface{}, ss grpc.ServerStream) error {
		if _, ok := grpcauth.ClaimsFromContext(ss.Context()); !ok {
			return status.Error(codes.Unauthenticated, "authentication required")
		}
		return handler(srv, ss)
	}
}
