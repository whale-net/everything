package handlers

import (
	"context"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RequireClaimsUnaryInterceptor rejects a call with codes.Unauthenticated
// when no verified grpcauth.Claims are present in its context by the time
// it runs. grpcauth's own OIDC interceptor lets a call with no bearer
// token proceed anonymously (no error, no claims) -- this interceptor is
// what turns that into an error.
//
// Unlike leaflab/api and manmanv2/api (which fence a subset of RPCs behind
// an explicit method allowlist because some of their RPCs predate
// authentication), whagent_net's SessionService authenticates every RPC
// with no exception (this task's Implementation section,
// ARCHITECTURE.md "Identity and auth chaining") -- so this interceptor
// applies unconditionally to every method, with no allowlist to keep in
// sync as RPCs are added.
//
// In GRPC_AUTH_MODE=none, grpcauth always injects dev claims, so this
// never rejects a call; in GRPC_AUTH_MODE=oidc it is what makes an
// anonymous or invalid-token call to any RPC fail before the handler
// runs.
func RequireClaimsUnaryInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	if _, ok := grpcauth.ClaimsFromContext(ctx); !ok {
		return nil, status.Error(codes.Unauthenticated, "authentication required")
	}
	return handler(ctx, req)
}

// RequireClaimsStreamInterceptor is RequireClaimsUnaryInterceptor's stream
// analog. None of SessionService's RPCs stream today; this is wired so a
// future streaming RPC (e.g. StreamEvents, ARCHITECTURE.md "Open items")
// is authenticated for free.
func RequireClaimsStreamInterceptor(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	if _, ok := grpcauth.ClaimsFromContext(ss.Context()); !ok {
		return status.Error(codes.Unauthenticated, "authentication required")
	}
	return handler(srv, ss)
}
