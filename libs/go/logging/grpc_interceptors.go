package logging

import (
	"context"
	"fmt"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

const (
	// CorrelationIDMetadataKey is the gRPC metadata key for correlation IDs
	CorrelationIDMetadataKey = "correlation-id"
	// correlationIDContextKey is used to store the correlation ID in context
	correlationIDContextKey = "correlation-id"
)

var correlationIDKeyType struct{}

// NewCorrelationIDUnaryInterceptor creates a unary server interceptor that
// extracts or generates a correlation ID and adds it to the context.
func NewCorrelationIDUnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		ctx = extractOrGenerateCorrelationID(ctx)
		return handler(ctx, req)
	}
}

// NewCorrelationIDStreamInterceptor creates a stream server interceptor that
// extracts or generates a correlation ID and adds it to the context.
func NewCorrelationIDStreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx := extractOrGenerateCorrelationID(ss.Context())
		return handler(srv, &wrappedStreamWithContext{ss, ctx})
	}
}

// extractOrGenerateCorrelationID extracts a correlation ID from incoming metadata,
// or generates a new one if not present.
func extractOrGenerateCorrelationID(ctx context.Context) context.Context {
	md, ok := metadata.FromIncomingContext(ctx)
	var correlationID string

	if ok {
		// Try to extract existing correlation ID from metadata
		ids := md.Get(CorrelationIDMetadataKey)
		if len(ids) > 0 && strings.TrimSpace(ids[0]) != "" {
			correlationID = ids[0]
		}
	}

	// Generate a new one if not found
	if correlationID == "" {
		correlationID = generateCorrelationID()
	}

	// Store in context
	return context.WithValue(ctx, correlationIDKeyType, correlationID)
}

// generateCorrelationID generates a new correlation ID.
func generateCorrelationID() string {
	return fmt.Sprintf("req-%d", time.Now().UnixNano())
}

// CorrelationIDFromContext retrieves the correlation ID from the context.
func CorrelationIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(correlationIDKeyType).(string)
	return id, ok
}

// WithCorrelationID returns a new context with the given correlation ID.
func WithCorrelationID(ctx context.Context, correlationID string) context.Context {
	return context.WithValue(ctx, correlationIDKeyType, correlationID)
}

// wrappedStreamWithContext wraps grpc.ServerStream with a modified context
type wrappedStreamWithContext struct {
	grpc.ServerStream
	ctx context.Context
}

// Context returns the wrapped context
func (w *wrappedStreamWithContext) Context() context.Context {
	return w.ctx
}
