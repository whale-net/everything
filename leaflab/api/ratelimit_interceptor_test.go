package main

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/leaflab/api/ratelimit"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// claimsContext stands in for a request that already passed grpcauth (i.e.
// the point in the chain -- after auth, before rate limiting -- where
// principalKey and checkRateLimitBuckets actually run; see buildServer's
// chain-order doc comment in main.go).
func claimsContext(subject string) context.Context {
	return grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: subject})
}

func TestPrincipalKey_AuthenticatedSubject_UsesPrincipalPrefix(t *testing.T) {
	key := principalKey(claimsContext("device-owner-1"))
	if want := ratelimit.Key("principal:device-owner-1"); key != want {
		t.Errorf("principalKey = %q, want %q", key, want)
	}
}

// TestPrincipalKey_DistinctSubjects_DistinctKeys is the derivation-level
// half of "two distinct principals do not share a bucket": principalKey
// itself must map distinct authenticated subjects to distinct Keys before
// InMemoryLimiter ever sees them (InMemoryLimiter's own independence is
// covered by leaflab/api/ratelimit's TestInMemoryLimiter_DistinctPrincipalKeys_Independent).
func TestPrincipalKey_DistinctSubjects_DistinctKeys(t *testing.T) {
	keyA := principalKey(claimsContext("p1"))
	keyB := principalKey(claimsContext("p2"))
	if keyA == keyB {
		t.Errorf("principalKey(p1) == principalKey(p2) == %q, want distinct keys for distinct subjects", keyA)
	}
}

// TestPrincipalKey_NoClaims_FallsBackToPeerAddress covers GetHealth's
// anonymous path (see principalKey's doc comment): with no Claims in
// context, the key falls back to the connection's peer address so one
// noisy unauthenticated caller cannot exhaust every other caller's budget
// by sharing a single "anonymous" bucket.
func TestPrincipalKey_NoClaims_FallsBackToPeerAddress(t *testing.T) {
	addr := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 12345}
	ctx := peer.NewContext(context.Background(), &peer.Peer{Addr: addr})

	key := principalKey(ctx)
	if want := ratelimit.Key("peer:" + addr.String()); key != want {
		t.Errorf("principalKey = %q, want %q", key, want)
	}
}

// TestPrincipalKey_DistinctPeers_DistinctKeys proves two distinct
// unauthenticated peers get distinct keys -- the anonymous fallback must
// not collapse every unauthenticated caller into one shared bucket.
func TestPrincipalKey_DistinctPeers_DistinctKeys(t *testing.T) {
	ctxA := peer.NewContext(context.Background(), &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1}})
	ctxB := peer.NewContext(context.Background(), &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 2}})

	if principalKey(ctxA) == principalKey(ctxB) {
		t.Errorf("principalKey collapsed two distinct peer addresses into one key: %q", principalKey(ctxA))
	}
}

func TestPrincipalKey_NoClaimsNoPeer_FallsBackToAnonymous(t *testing.T) {
	key := principalKey(context.Background())
	if want := ratelimit.Key("anonymous"); key != want {
		t.Errorf("principalKey = %q, want %q", key, want)
	}
}

// TestCheckRateLimitBuckets_ExceedsReadDefault_ReturnsRateLimitedWithRetryHint
// covers the Testing section's first bullet at the interceptor's error-
// construction level: exceeding read_default returns FR59's rate_limited
// class carrying NFR10's structured retry hint.
func TestCheckRateLimitBuckets_ExceedsReadDefault_ReturnsRateLimitedWithRetryHint(t *testing.T) {
	limiter := ratelimit.NewInMemoryLimiter(map[ratelimit.Bucket]ratelimit.WindowConfig{
		ratelimit.BucketReadDefault: {Limit: 1, Window: time.Minute},
	})
	ctx := claimsContext("p1")
	const method = "/leaflab.api.v1.LeafLabAPI/GetDeviceConfig"

	if err := checkRateLimitBuckets(ctx, limiter, method); err != nil {
		t.Fatalf("first call: unexpected error: %v", err)
	}

	err := checkRateLimitBuckets(ctx, limiter, method)
	if err == nil {
		t.Fatal("second call within the same window: got nil error, want rate_limited")
	}
	if status.Code(err) != codes.ResourceExhausted {
		t.Errorf("code = %v, want %v", status.Code(err), codes.ResourceExhausted)
	}
	detail, ok := contract.FromError(err)
	if !ok {
		t.Fatalf("error %v carries no Failure detail", err)
	}
	if detail.Class != string(contract.FailureRateLimited) {
		t.Errorf("Class = %q, want %q", detail.Class, contract.FailureRateLimited)
	}
	retryAfter, ok := contract.RetryAfterFromError(err)
	if !ok {
		t.Fatal("error carries no RetryInfo detail")
	}
	if retryAfter <= 0 {
		t.Errorf("retryAfter = %v, want > 0", retryAfter)
	}
}

// TestCheckRateLimitBuckets_DistinctPrincipals_Independent covers "two
// distinct principals do not share a bucket" through the actual
// checkRateLimitBuckets call path (principalKey derivation + Limiter), not
// just the Limiter in isolation.
func TestCheckRateLimitBuckets_DistinctPrincipals_Independent(t *testing.T) {
	limiter := ratelimit.NewInMemoryLimiter(map[ratelimit.Bucket]ratelimit.WindowConfig{
		ratelimit.BucketReadDefault: {Limit: 1, Window: time.Minute},
	})
	const method = "/leaflab.api.v1.LeafLabAPI/GetDeviceConfig"

	if err := checkRateLimitBuckets(claimsContext("p1"), limiter, method); err != nil {
		t.Fatalf("p1 first call: unexpected error: %v", err)
	}
	if err := checkRateLimitBuckets(claimsContext("p1"), limiter, method); err == nil {
		t.Fatal("p1 second call: expected a rate_limited error, got nil")
	}
	if err := checkRateLimitBuckets(claimsContext("p2"), limiter, method); err != nil {
		t.Fatalf("p2 first call: unexpected error -- p2 must not share p1's exhausted bucket: %v", err)
	}
}

// TestCheckRateLimitBuckets_OracleSafety_IdenticalAcrossKeys is the
// interceptor-level oracle-safety proof (forward-binding for FR76): a key
// that resolves to nothing and a key that resolves to a real entity must
// produce the same status, body and observed timing once rate-limited.
// checkRateLimitBuckets has no notion of "exists" at all -- it only ever
// derives an opaque ratelimit.Key and asks the Limiter -- so this asserts
// that structural guarantee end to end through the actual error the caller
// receives.
func TestCheckRateLimitBuckets_OracleSafety_IdenticalAcrossKeys(t *testing.T) {
	limiter := ratelimit.NewInMemoryLimiter(map[ratelimit.Bucket]ratelimit.WindowConfig{
		ratelimit.BucketReadDefault: {Limit: 1, Window: time.Minute},
	})
	const method = "/leaflab.api.v1.LeafLabAPI/GetDeviceConfig"

	type observation struct {
		code    codes.Code
		message string
		reason  string
		elapsed time.Duration
	}
	observe := func(subject string) observation {
		ctx := claimsContext(subject)
		if err := checkRateLimitBuckets(ctx, limiter, method); err != nil {
			t.Fatalf("%s: first call: unexpected error: %v", subject, err)
		}
		start := time.Now()
		err := checkRateLimitBuckets(ctx, limiter, method)
		elapsed := time.Since(start)
		if err == nil {
			t.Fatalf("%s: second call: expected a rate_limited error, got nil", subject)
		}
		st, _ := status.FromError(err)
		detail, _ := contract.FromError(err)
		return observation{code: st.Code(), message: st.Message(), reason: detail.GetReason(), elapsed: elapsed}
	}

	// The subject names deliberately suggest "resolves to nothing" vs.
	// "resolves to a real entity" -- checkRateLimitBuckets must treat them
	// identically since it never inspects what a key means.
	nonexistent := observe("nonexistent-device-id")
	real := observe("real-device-id")

	if nonexistent.code != real.code {
		t.Errorf("code differs by key existence: nonexistent=%v real=%v", nonexistent.code, real.code)
	}
	if nonexistent.message != real.message {
		t.Errorf("status message differs by key existence: nonexistent=%q real=%q", nonexistent.message, real.message)
	}
	if nonexistent.reason != real.reason {
		t.Errorf("Failure reason differs by key existence: nonexistent=%q real=%q", nonexistent.reason, real.reason)
	}
	// Both paths run the identical code (a map lookup, then a
	// mutex-guarded counter check) with no key-dependent branch, so
	// observed latency should be comparable. Assert both are well under a
	// generous bound rather than near-equality, which would be flaky under
	// scheduler jitter.
	const maxElapsed = 50 * time.Millisecond
	if nonexistent.elapsed > maxElapsed || real.elapsed > maxElapsed {
		t.Errorf("unexpectedly slow rate-limit check: nonexistent=%v real=%v (want both < %v)", nonexistent.elapsed, real.elapsed, maxElapsed)
	}
}

// TestRateLimitInterceptor_OverTheWire_ExceedsReadDefault_ThenRecoversAfterWindow
// proves NFR10's enforcement over the real production wiring (buildServer,
// via startTestServerWithLimiter): the interceptor is actually reached in
// the chain, a caller sees codes.ResourceExhausted with a retry hint once
// exhausted, and the next request after the window rolls over succeeds
// again -- the Testing section's "the next request after the window
// succeeds" bullet, proven end to end rather than just against
// InMemoryLimiter in isolation.
func TestRateLimitInterceptor_OverTheWire_ExceedsReadDefault_ThenRecoversAfterWindow(t *testing.T) {
	const window = 80 * time.Millisecond
	limiter := ratelimit.NewInMemoryLimiter(map[ratelimit.Bucket]ratelimit.WindowConfig{
		ratelimit.BucketReadDefault: {Limit: 1, Window: window},
	})
	conn := startTestServerWithLimiter(t, discardTestLogger(), limiter)
	client := pb.NewLeafLabAPIClient(conn)
	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+testValidToken)

	if _, err := client.GetDeviceConfig(ctx, &pb.GetDeviceConfigRequest{DeviceId: "board-1"}); err != nil {
		t.Fatalf("first call: expected success, got %v", err)
	}

	_, err := client.GetDeviceConfig(ctx, &pb.GetDeviceConfigRequest{DeviceId: "board-1"})
	if err == nil {
		t.Fatal("second call within the window: expected a rate_limited error, got nil")
	}
	if status.Code(err) != codes.ResourceExhausted {
		t.Errorf("code = %v, want %v", status.Code(err), codes.ResourceExhausted)
	}
	if _, ok := contract.RetryAfterFromError(err); !ok {
		t.Error("rate-limited error over the wire carries no RetryInfo detail")
	}

	time.Sleep(window + 40*time.Millisecond)

	if _, err := client.GetDeviceConfig(ctx, &pb.GetDeviceConfigRequest{DeviceId: "board-1"}); err != nil {
		t.Fatalf("call after window rollover: expected success, got %v", err)
	}
}

// TestRateLimitInterceptor_OverTheWire_AnonymousAndAuthenticatedKeys_Independent
// proves principalKey's two key namespaces ("peer:" for the anonymous
// GetHealth path, "principal:" for an authenticated caller) are actually
// independent over the wire: exhausting the anonymous peer-address bucket
// must not throttle an authenticated call made over the very same
// connection. (fakeBearerAuthUnary only recognizes one token -- see
// main_test.go -- so this suite proves principal-vs-principal independence
// at the checkRateLimitBuckets level above with real distinct
// Claims.Subject values, and proves the anonymous/authenticated key-space
// split over the wire here.)
func TestRateLimitInterceptor_OverTheWire_AnonymousAndAuthenticatedKeys_Independent(t *testing.T) {
	limiter := ratelimit.NewInMemoryLimiter(map[ratelimit.Bucket]ratelimit.WindowConfig{
		ratelimit.BucketReadDefault: {Limit: 1, Window: time.Minute},
	})
	conn := startTestServerWithLimiter(t, discardTestLogger(), limiter)
	client := pb.NewLeafLabAPIClient(conn)

	// Exhaust the anonymous (peer-address-keyed) bucket via GetHealth.
	if _, err := client.GetHealth(context.Background(), &pb.GetHealthRequest{}); err != nil {
		t.Fatalf("first anonymous call: expected success, got %v", err)
	}
	if _, err := client.GetHealth(context.Background(), &pb.GetHealthRequest{}); err == nil {
		t.Fatal("second anonymous call: expected a rate_limited error (bucket exhausted), got nil")
	}

	// An authenticated call over the same connection is keyed on the
	// principal, not the peer address -- it must not share the exhausted
	// anonymous bucket.
	authCtx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+testValidToken)
	if _, err := client.GetDeviceConfig(authCtx, &pb.GetDeviceConfigRequest{DeviceId: "board-1"}); err != nil {
		t.Fatalf("authenticated call: expected success (distinct key from the exhausted anonymous peer bucket), got %v", err)
	}
}

// TestCheckRateLimitBuckets_SupportReferenceResolve_ExceedsBucket_PerAdminPrincipal
// is FR80/NFR10's named "rate limiting kicks in per admin principal after
// the configured number of resolve attempts" criterion: ResolveToHousehold
// is wired against ratelimit.BucketSupportReferenceResolve
// (rateLimitBucketByMethod, ratelimit_interceptor.go) -- checked here
// through checkRateLimitBuckets directly, with resolveToHouseholdFullMethod
// as the method under test, mirroring
// TestCheckRateLimitBuckets_ExceedsReadDefault_ReturnsRateLimitedWithRetryHint's
// shape for BucketReadDefault. BucketReadDefault is configured generously so
// only the support_reference_resolve bucket is what trips. Independence
// across two distinct admin principals proves "per admin principal", not a
// single shared bucket for every admin.
func TestCheckRateLimitBuckets_SupportReferenceResolve_ExceedsBucket_PerAdminPrincipal(t *testing.T) {
	limiter := ratelimit.NewInMemoryLimiter(map[ratelimit.Bucket]ratelimit.WindowConfig{
		ratelimit.BucketReadDefault:             {Limit: 1000, Window: time.Minute},
		ratelimit.BucketSupportReferenceResolve: {Limit: 3, Window: time.Minute},
	})

	for i := 0; i < 3; i++ {
		if err := checkRateLimitBuckets(claimsContext("admin1"), limiter, resolveToHouseholdFullMethod); err != nil {
			t.Fatalf("admin1 call %d (within the configured limit): unexpected error: %v", i+1, err)
		}
	}
	err := checkRateLimitBuckets(claimsContext("admin1"), limiter, resolveToHouseholdFullMethod)
	if err == nil {
		t.Fatal("admin1 call past the configured limit: got nil error, want rate_limited")
	}
	if status.Code(err) != codes.ResourceExhausted {
		t.Errorf("code = %v, want %v", status.Code(err), codes.ResourceExhausted)
	}
	if _, ok := contract.RetryAfterFromError(err); !ok {
		t.Error("rate-limited error carries no RetryInfo detail")
	}

	// A distinct admin principal has its own budget -- exhausting admin1's
	// bucket must not throttle admin2 (NFR10: "rate-limited per admin
	// principal").
	if err := checkRateLimitBuckets(claimsContext("admin2"), limiter, resolveToHouseholdFullMethod); err != nil {
		t.Fatalf("admin2 first call: expected success -- admin1's exhausted bucket must not be shared, got %v", err)
	}
}
