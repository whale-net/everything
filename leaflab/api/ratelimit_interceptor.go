package main

import (
	"context"
	"time"

	"github.com/whale-net/everything/leaflab/api/contract"
	"github.com/whale-net/everything/leaflab/api/ratelimit"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/peer"
)

// rateLimitBucketByMethod maps a full gRPC method name (grpc.UnaryServerInfo
// /StreamServerInfo.FullMethod format, e.g. "/leaflab.api.v1.LeafLabAPI/
// GetHealth") to the named ratelimit.Bucket enforced for it, in addition to
// ratelimit.BucketReadDefault (every RPC always gets the default check --
// see NewRateLimitUnaryInterceptor). FR42's ResendDeviceConfig is the first
// entry (NFR10: "rate-limited despite writing no config row" -- the resend
// bucket, not a new one); FR47's ack_wait_concurrent, FR76's
// claim_open/claim_round and FR80's support_reference_resolve add their own
// entries as those RPCs are built, without touching this interceptor.
// GetResendAvailability deliberately has no entry: it is a plain read,
// covered by BucketReadDefault like every other read RPC -- the resend
// bucket protects the write (the actual re-send), not the availability
// check that precedes it.
var rateLimitBucketByMethod = map[string]ratelimit.Bucket{
	resendDeviceConfigFullMethod: ratelimit.BucketResend,
}

// principalKey derives NFR10's rate-limit Key for ctx's caller. An
// authenticated principal is keyed on its verified subject -- not the raw
// grpcauth.Claims type, so this stays an opaque ratelimit.Key per the
// scaffold's "principal is not defined as a human IdP subject" constraint.
// The one anonymous RPC (GetHealth; see auth.go's anonymousMethods) has no
// subject, so it falls back to the connection's peer address: still a
// caller-supplied dimension, so one noisy unauthenticated caller cannot
// exhaust every other caller's health-probe budget by sharing a single
// "anonymous" bucket.
func principalKey(ctx context.Context) ratelimit.Key {
	if claims, ok := grpcauth.ClaimsFromContext(ctx); ok && claims != nil && claims.Subject != "" {
		return ratelimit.Key("principal:" + claims.Subject)
	}
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		return ratelimit.Key("peer:" + p.Addr.String())
	}
	return ratelimit.Key("anonymous")
}

// NewRateLimitUnaryInterceptor enforces NFR10 against every unary RPC:
// ratelimit.BucketReadDefault always, plus any bucket registered for the
// specific method in rateLimitBucketByMethod. Placed after auth in the
// chain (see main.go's buildServer) for two reasons: key derivation needs
// Claims already injected by the earlier auth interceptors, and a request
// auth has already rejected never consumes rate-limit budget.
func NewRateLimitUnaryInterceptor(limiter ratelimit.Limiter) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if err := checkRateLimitBuckets(ctx, limiter, info.FullMethod); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// NewRateLimitStreamInterceptor is the streaming counterpart of
// NewRateLimitUnaryInterceptor.
func NewRateLimitStreamInterceptor(limiter ratelimit.Limiter) grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if err := checkRateLimitBuckets(ss.Context(), limiter, info.FullMethod); err != nil {
			return err
		}
		return handler(srv, ss)
	}
}

func checkRateLimitBuckets(ctx context.Context, limiter ratelimit.Limiter, fullMethod string) error {
	key := principalKey(ctx)

	if allowed, retryAfter := limiter.Allow(ctx, key, ratelimit.BucketReadDefault); !allowed {
		return rateLimitedError(retryAfter)
	}
	if bucket, ok := rateLimitBucketByMethod[fullMethod]; ok {
		if allowed, retryAfter := limiter.Allow(ctx, key, bucket); !allowed {
			return rateLimitedError(retryAfter)
		}
	}
	return nil
}

// rateLimitedError builds FR59's rate_limited failure class with a
// structured retry hint (NFR10) -- see contract.RateLimitedWithRetry.
func rateLimitedError(retryAfter time.Duration) error {
	return contract.RateLimitedWithRetry("request", "", "Too many requests. Try again shortly.", retryAfter)
}
