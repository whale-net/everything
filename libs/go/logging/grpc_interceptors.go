package logging

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// NewUnaryServerLoggingInterceptor returns a grpc.UnaryServerInterceptor that
// emits one structured log line per unary RPC (method, status code, duration,
// peer address). otelgrpc's StatsHandler gives spans for free, but produces
// no log output of its own -- this is what actually puts gRPC requests in
// the logs.
//
// Register it ahead of other interceptors (e.g. auth) in
// grpc.ChainUnaryInterceptor so it also covers requests those interceptors
// reject, and so trace_id/span_id (already bound to ctx by the StatsHandler)
// are injected into the log line by the slog handler.
func NewUnaryServerLoggingInterceptor(loggerName string) grpc.UnaryServerInterceptor {
	logger := Get(loggerName)
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		logGRPCCall(ctx, logger, info.FullMethod, time.Since(start), err)
		return resp, err
	}
}

// NewStreamServerLoggingInterceptor is the streaming equivalent of
// NewUnaryServerLoggingInterceptor: one log line per stream, emitted when the
// stream completes.
func NewStreamServerLoggingInterceptor(loggerName string) grpc.StreamServerInterceptor {
	logger := Get(loggerName)
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		start := time.Now()
		err := handler(srv, ss)
		logGRPCCall(ss.Context(), logger, info.FullMethod, time.Since(start), err)
		return err
	}
}

func logGRPCCall(ctx context.Context, logger *slog.Logger, method string, duration time.Duration, err error) {
	code := status.Code(err)
	attrs := []any{
		"grpc_method", method,
		"grpc_code", code.String(),
		"duration_ms", duration.Milliseconds(),
	}
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		attrs = append(attrs, "peer", p.Addr.String())
	}
	if err != nil {
		attrs = append(attrs, "error", err.Error())
	}

	switch code {
	case codes.OK:
		logger.InfoContext(ctx, "grpc request", attrs...)
	case codes.Unknown, codes.Internal, codes.DataLoss, codes.Unavailable:
		logger.ErrorContext(ctx, "grpc request", attrs...)
	default:
		logger.WarnContext(ctx, "grpc request", attrs...)
	}
}
