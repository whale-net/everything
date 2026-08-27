package main

import (
	"context"

	"github.com/whale-net/everything/leaflab/api/contract"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"google.golang.org/grpc"
)

// RoleAdmin is the leaflab-admin realm role (FR12), service-prefixed per
// libs/go/grpcauth/KEYCLOAK.md gotcha 1 (realm roles are global to the
// realm; an unprefixed "admin" would collide with another service's role
// of the same name). Eligibility only -- there is no in-application
// mechanism to grant, hold or revoke it. Phase 1 read it only for
// recording (isAdminEligible below, read by the acting-subject logger);
// Phase 2 (FR10, FR7/FR12 activation) additionally gates the five admin
// RPCs (server.go's requireAdminEligible) on it, but confers nothing past
// that gate by itself -- reach is the standing lane's own minimal
// projection or an explicit, reasoned, time-boxed elevation (FR10.1),
// never eligibility alone.
const RoleAdmin = "leaflab-admin"

// healthFullMethod is GetHealth's full gRPC method name, matching the
// format grpc.UnaryServerInfo.FullMethod / grpc.StreamServerInfo.FullMethod
// use ("/<package>.<Service>/<Method>" -- see leaflab/api/proto/api.proto's
// "package leaflab.api.v1" and "service LeafLabAPI").
const healthFullMethod = "/leaflab.api.v1.LeafLabAPI/GetHealth"

// anonymousMethods is FR11.2/FR63.2's allowlist of RPCs reachable without
// an authenticated principal. Contains **exactly one** entry -- GetHealth,
// the only anonymous endpoint in this service. A package-level var (rather
// than an inline check) so a later validation/conformance task can assert
// on it directly instead of re-deriving the list from proto or interceptor
// logic.
var anonymousMethods = map[string]bool{
	healthFullMethod: true,
}

// NewAuthEnforcementUnaryInterceptor rejects any call that reaches it
// without Claims in context, unless the method is in anonymousMethods.
//
// This is what actually enforces FR11.1 ("every RPC requires an
// authenticated principal"): grpcauth's own interceptor (earlier in the
// chain -- see main.go) only verifies a *presented* token. In
// AuthModeOIDC, a request with no Authorization header at all is legal as
// far as grpcauth is concerned -- it "proceeds anonymously" with no Claims
// in context. Without this interceptor every RPC but GetHealth would be
// silently reachable by anyone who simply omits credentials.
func NewAuthEnforcementUnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if anonymousMethods[info.FullMethod] {
			return handler(ctx, req)
		}
		if _, ok := grpcauth.ClaimsFromContext(ctx); !ok {
			return nil, contract.Unauthenticated("request", "", "Sign in to use this API.")
		}
		return handler(ctx, req)
	}
}

// NewAuthEnforcementStreamInterceptor is the streaming counterpart of
// NewAuthEnforcementUnaryInterceptor.
func NewAuthEnforcementStreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if anonymousMethods[info.FullMethod] {
			return handler(srv, ss)
		}
		if _, ok := grpcauth.ClaimsFromContext(ss.Context()); !ok {
			return contract.Unauthenticated("request", "", "Sign in to use this API.")
		}
		return handler(srv, ss)
	}
}

// isAdminEligible reports whether ctx's Claims carry the leaflab-admin
// realm role (FR12) -- see RoleAdmin's doc comment for what eligibility
// does and does not confer. Read by the acting-subject logger
// (logging_interceptor.go), so eligibility is captured in the interim
// audit record (NFR12) regardless of whether a given request touches an
// admin RPC, and by server.go's requireAdminEligible, the gate every admin
// RPC (FR10) applies before doing anything else.
func isAdminEligible(ctx context.Context) bool {
	claims, ok := grpcauth.ClaimsFromContext(ctx)
	if !ok || claims == nil {
		return false
	}
	for _, role := range claims.Roles {
		if role == RoleAdmin {
			return true
		}
	}
	return false
}
