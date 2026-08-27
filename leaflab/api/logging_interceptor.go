package main

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// correlationIDKey is the unexported context key the correlation-id
// interceptor uses to carry a request's correlation id to later
// interceptors and handlers.
type correlationIDKey struct{}

// correlationIDHeader is the incoming metadata key a caller (or an upstream
// gateway) may set to propagate its own correlation id. When absent, the
// server generates one.
const correlationIDHeader = "x-correlation-id"

// CorrelationIDFromContext returns the correlation id attached to ctx by
// NewCorrelationUnaryInterceptor/NewCorrelationStreamInterceptor, or "" if
// none is present -- e.g. in a test that calls a handler directly without
// going through the interceptor chain.
func CorrelationIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(correlationIDKey{}).(string)
	return id
}

// NewCorrelationUnaryInterceptor extracts a caller-supplied correlation id
// from incoming metadata, or generates one, and attaches it to the request
// context. Runs first in the chain (NFR12): every later interceptor --
// including an auth rejection -- gets logged against the same id.
func NewCorrelationUnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		return handler(withCorrelationID(ctx), req)
	}
}

// NewCorrelationStreamInterceptor is the streaming counterpart of
// NewCorrelationUnaryInterceptor.
func NewCorrelationStreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		return handler(srv, &correlationServerStream{ServerStream: ss, ctx: withCorrelationID(ss.Context())})
	}
}

func withCorrelationID(ctx context.Context) context.Context {
	id := correlationIDFromIncoming(ctx)
	if id == "" {
		id = uuid.NewString()
	}
	return context.WithValue(ctx, correlationIDKey{}, id)
}

func correlationIDFromIncoming(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	values := md.Get(correlationIDHeader)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

// correlationServerStream wraps grpc.ServerStream to carry the enriched
// context back to the handler, mirroring grpcauth's wrappedStream.
type correlationServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *correlationServerStream) Context() context.Context { return s.ctx }

// NewSubjectLoggingUnaryInterceptor logs, per NFR12, the acting subject (from
// Claims set by the grpcauth interceptor earlier in the chain), the target
// household (empty in Phase 1 -- households do not exist yet; see FR8) and
// the request correlation id set by NewCorrelationUnaryInterceptor. This is
// the interim record standing in for FR8's audit log; it does not discharge
// FR8.
//
// Must run after the auth interceptor in the chain: Claims are not yet in
// context otherwise. See leaflab/api/main.go for chain wiring order
// (correlation-id -> auth -> subject-logging -> handler).
func NewSubjectLoggingUnaryInterceptor(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		logRPC(logger, ctx, info.FullMethod)
		return handler(ctx, req)
	}
}

// NewSubjectLoggingStreamInterceptor is the streaming counterpart of
// NewSubjectLoggingUnaryInterceptor.
func NewSubjectLoggingStreamInterceptor(logger *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		logRPC(logger, ss.Context(), info.FullMethod)
		return handler(srv, ss)
	}
}

func logRPC(logger *slog.Logger, ctx context.Context, method string) {
	// NFR13: never log a credential. Only the verified subject and role
	// eligibility are recorded here, never the bearer token itself.
	logger.Info("rpc",
		"method", method,
		"correlation_id", CorrelationIDFromContext(ctx),
		"subject", actingSubject(ctx),
		"household", "", // Phase 1: households do not exist yet (FR8 is Phase 2).
	)
}

func actingSubject(ctx context.Context) string {
	claims, ok := grpcauth.ClaimsFromContext(ctx)
	if !ok || claims == nil {
		return "anonymous"
	}
	return claims.Subject
}
