package ratelimit

import (
	"context"
	"testing"
	"time"
)

// TestInMemoryLimiter_WindowExceeded_DeniesThenAllowsAfterRollover covers
// the Testing section's fixed-window rollover bullet: exceeding a bucket
// denies with a positive retryAfter, and the next call after the window
// rolls over succeeds again. Uses the whitebox now field (package-internal;
// this file is `package ratelimit`, not `package ratelimit_test`) instead of
// a real sleep so the window boundary is exact and the test is instant.
func TestInMemoryLimiter_WindowExceeded_DeniesThenAllowsAfterRollover(t *testing.T) {
	limiter := NewInMemoryLimiter(map[Bucket]WindowConfig{
		BucketReadDefault: {Limit: 2, Window: time.Minute},
	})
	fakeNow := time.Now()
	limiter.now = func() time.Time { return fakeNow }

	ctx := context.Background()
	key := Key("principal:p1")

	for i := 0; i < 2; i++ {
		allowed, retryAfter := limiter.Allow(ctx, key, BucketReadDefault)
		if !allowed {
			t.Fatalf("call %d: Allow = false, want true within the limit (retryAfter=%v)", i+1, retryAfter)
		}
	}

	allowed, retryAfter := limiter.Allow(ctx, key, BucketReadDefault)
	if allowed {
		t.Fatal("3rd call within the same window: Allow = true, want false (limit exceeded)")
	}
	if retryAfter <= 0 {
		t.Errorf("retryAfter = %v, want > 0 when denied", retryAfter)
	}
	if retryAfter > time.Minute {
		t.Errorf("retryAfter = %v, want <= the window (%v)", retryAfter, time.Minute)
	}

	// Still within the window: must still be denied.
	if allowed, _ := limiter.Allow(ctx, key, BucketReadDefault); allowed {
		t.Fatal("4th call still within the same window: Allow = true, want false")
	}

	// Roll the fake clock past the window boundary: the counter resets.
	fakeNow = fakeNow.Add(time.Minute + time.Second)
	if allowed, retryAfter := limiter.Allow(ctx, key, BucketReadDefault); !allowed {
		t.Fatalf("call after window rollover: Allow = false, want true (retryAfter=%v)", retryAfter)
	}
}

// TestInMemoryLimiter_DistinctPrincipalKeys_Independent covers "two distinct
// principals do not share a bucket": exhausting one principal's budget must
// not affect a different principal's budget for the same bucket.
func TestInMemoryLimiter_DistinctPrincipalKeys_Independent(t *testing.T) {
	limiter := NewInMemoryLimiter(map[Bucket]WindowConfig{
		BucketReadDefault: {Limit: 1, Window: time.Minute},
	})
	ctx := context.Background()

	if allowed, _ := limiter.Allow(ctx, Key("principal:p1"), BucketReadDefault); !allowed {
		t.Fatal("p1 first call: Allow = false, want true")
	}
	if allowed, _ := limiter.Allow(ctx, Key("principal:p1"), BucketReadDefault); allowed {
		t.Fatal("p1 second call: Allow = true, want false (limit exhausted)")
	}
	if allowed, _ := limiter.Allow(ctx, Key("principal:p2"), BucketReadDefault); !allowed {
		t.Fatal("p2 first call: Allow = false, want true -- a distinct principal must not share p1's exhausted bucket")
	}
}

// TestInMemoryLimiter_SessionKeyedIndependentOfSamePrincipal covers "two
// sessions of the same principal are independently limited where the
// bucket is session-keyed." Key is deliberately opaque (see its doc
// comment): a caller that wants a session-scoped bucket embeds the session
// id in the Key itself. This is a property of Key opacity, not something
// InMemoryLimiter special-cases for "session" vs. "principal" -- the same
// mechanism proven independent-by-key in the principal test above must also
// hold when the two keys share a principal but differ only by session.
func TestInMemoryLimiter_SessionKeyedIndependentOfSamePrincipal(t *testing.T) {
	limiter := NewInMemoryLimiter(map[Bucket]WindowConfig{
		BucketAckWaitConcurrent: {Limit: 1, Window: time.Minute},
	})
	ctx := context.Background()
	sessionA := Key("principal:p1:session:A")
	sessionB := Key("principal:p1:session:B")

	if allowed, _ := limiter.Allow(ctx, sessionA, BucketAckWaitConcurrent); !allowed {
		t.Fatal("sessionA first call: Allow = false, want true")
	}
	if allowed, _ := limiter.Allow(ctx, sessionA, BucketAckWaitConcurrent); allowed {
		t.Fatal("sessionA second call: Allow = true, want false (limit exhausted)")
	}
	if allowed, _ := limiter.Allow(ctx, sessionB, BucketAckWaitConcurrent); !allowed {
		t.Fatal("sessionB first call: Allow = false, want true -- session B must not share session A's exhausted bucket despite sharing principal p1")
	}
}

// TestInMemoryLimiter_SessionlessNonHumanKey_Expressible covers "a limit is
// expressible and enforced for a session-less, non-human key." FR76.2's
// device claim flow limits on the submitted device_id itself -- no IdP
// subject, no session -- and NFR10 requires this to be enforced identically
// to a human principal key through the exact same Limiter/Key vocabulary.
func TestInMemoryLimiter_SessionlessNonHumanKey_Expressible(t *testing.T) {
	limiter := NewInMemoryLimiter(map[Bucket]WindowConfig{
		BucketClaimOpen: {Limit: 1, Window: time.Hour},
	})
	ctx := context.Background()
	deviceKey := Key("device:aa:bb:cc:dd:ee:ff")

	if allowed, _ := limiter.Allow(ctx, deviceKey, BucketClaimOpen); !allowed {
		t.Fatal("first call for a synthetic, session-less device key: Allow = false, want true")
	}
	allowed, retryAfter := limiter.Allow(ctx, deviceKey, BucketClaimOpen)
	if allowed {
		t.Fatal("second call: Allow = true, want false -- the device key must be enforced identically to a principal key")
	}
	if retryAfter <= 0 {
		t.Errorf("retryAfter = %v, want > 0", retryAfter)
	}
}

// TestInMemoryLimiter_OracleSafety_AllowNeverBranchesOnKeyExistence is the
// package-level half of the oracle-safety constraint (forward-binding for
// FR76): Allow's signature has no "does this key exist" parameter at all,
// and its behavior for two keys that differ only in what they "mean" to the
// caller (one deliberately named to suggest a real entity, one to suggest a
// nonexistent one) is bit-for-bit identical -- same allowed value, same
// retryAfter -- because Allow only ever sees an opaque string. The
// interceptor-level proof that this holds all the way out to the gRPC
// status the caller observes lives in leaflab/api's
// ratelimit_interceptor_test.go.
func TestInMemoryLimiter_OracleSafety_AllowNeverBranchesOnKeyExistence(t *testing.T) {
	limiter := NewInMemoryLimiter(map[Bucket]WindowConfig{
		BucketSupportReferenceResolve: {Limit: 1, Window: time.Minute},
	})
	ctx := context.Background()
	nonexistent := Key("support-ref:resolves-to-nothing")
	real := Key("support-ref:resolves-to-a-real-claim")

	// Exhaust both keys identically.
	for _, key := range []Key{nonexistent, real} {
		if allowed, _ := limiter.Allow(ctx, key, BucketSupportReferenceResolve); !allowed {
			t.Fatalf("first call for %q: Allow = false, want true", key)
		}
	}

	allowedNonexistent, retryNonexistent := limiter.Allow(ctx, nonexistent, BucketSupportReferenceResolve)
	allowedReal, retryReal := limiter.Allow(ctx, real, BucketSupportReferenceResolve)

	if allowedNonexistent != allowedReal {
		t.Errorf("allowed differs by key existence: nonexistent=%v real=%v", allowedNonexistent, allowedReal)
	}
	if allowedNonexistent {
		t.Fatal("both keys should be denied on their second call within the window")
	}
	// Both keys were exhausted at essentially the same point in the same
	// fixed window (real wall-clock time, not the whitebox fake clock used
	// elsewhere in this file), so their retryAfter must match within a
	// small tolerance -- not just "both positive". A wider gap would mean
	// something about the key itself is influencing the timing.
	const tolerance = 10 * time.Millisecond
	diff := retryNonexistent - retryReal
	if diff < -tolerance || diff > tolerance {
		t.Errorf("retryAfter differs by key existence beyond tolerance: nonexistent=%v real=%v (diff=%v, want within %v)", retryNonexistent, retryReal, diff, tolerance)
	}
}

// TestInMemoryLimiter_UnconfiguredBucket_NeverThrottles documents
// NewInMemoryLimiter's stated contract: a bucket with no config entry, or
// with Limit <= 0, is never throttled. This is what lets ratelimit.Buckets
// register all six Phase 1 buckets before every environment supplies a
// limit for each of them.
func TestInMemoryLimiter_UnconfiguredBucket_NeverThrottles(t *testing.T) {
	limiter := NewInMemoryLimiter(map[Bucket]WindowConfig{
		BucketReadDefault: {Limit: 0, Window: time.Minute},
	})
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if allowed, _ := limiter.Allow(ctx, Key("principal:p1"), BucketReadDefault); !allowed {
			t.Fatalf("call %d: Allow = false with Limit<=0, want always true", i+1)
		}
	}
	// BucketResend has no entry in configs at all.
	if allowed, _ := limiter.Allow(ctx, Key("principal:p1"), BucketResend); !allowed {
		t.Fatal("bucket with no config entry: Allow = false, want true")
	}
}
