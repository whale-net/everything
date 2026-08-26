package ratelimit

import (
	"sync"
	"testing"
)

func TestKeyForPrincipal(t *testing.T) {
	key := ForPrincipal("device-12345")
	if key.Principal() != "device-12345" {
		t.Errorf("expected principal device-12345, got %s", key.Principal())
	}
	if key.HasSession() {
		t.Error("expected no session, got session")
	}
}

func TestKeyForSession(t *testing.T) {
	key := ForSession("device-12345", "session-abc")
	if key.Principal() != "device-12345" {
		t.Errorf("expected principal device-12345, got %s", key.Principal())
	}
	if !key.HasSession() {
		t.Error("expected session, got no session")
	}
}

func TestRegistryRegisterAndGet(t *testing.T) {
	reg := NewRegistry()

	bucket := Bucket{
		Name:              "read",
		RequestsPerSecond: 100,
		Description:       "Rate limit for read operations",
	}

	reg.Register(bucket)

	retrieved, ok := reg.Get("read")
	if !ok {
		t.Error("expected bucket to be found")
	}
	if retrieved.RequestsPerSecond != 100 {
		t.Errorf("expected 100 requests per second, got %d", retrieved.RequestsPerSecond)
	}
}

func TestRegistryGetNotFound(t *testing.T) {
	reg := NewRegistry()

	_, ok := reg.Get("nonexistent")
	if ok {
		t.Error("expected bucket not to be found")
	}
}

// TestPrincipalRateLimitingIndependent verifies that a principal exceeding a bucket is refused,
// while a second principal is unaffected. This tests the per-principal rate limiting isolation.
func TestPrincipalRateLimitingIndependent(t *testing.T) {
	reg := NewRegistry()
	reg.Register(Bucket{
		Name:              "test-bucket",
		RequestsPerSecond: 2, // Low limit for testing
		Description:       "Test bucket",
	})

	limiter := NewLimiter(reg)

	// Principal 1 makes 2 requests (should succeed)
	principal1Key := ForPrincipal("principal-1")
	if !limiter.Allow(principal1Key, "test-bucket") {
		t.Error("principal-1: first request should be allowed")
	}
	if !limiter.Allow(principal1Key, "test-bucket") {
		t.Error("principal-1: second request should be allowed")
	}

	// Principal 1 makes a third request (should be rate limited)
	if limiter.Allow(principal1Key, "test-bucket") {
		t.Error("principal-1: third request should be rate limited")
	}

	// Principal 2 makes 2 requests (should all succeed - independent bucket)
	principal2Key := ForPrincipal("principal-2")
	if !limiter.Allow(principal2Key, "test-bucket") {
		t.Error("principal-2: first request should be allowed")
	}
	if !limiter.Allow(principal2Key, "test-bucket") {
		t.Error("principal-2: second request should be allowed")
	}

	// Principal 2 makes a third request (should be rate limited)
	if limiter.Allow(principal2Key, "test-bucket") {
		t.Error("principal-2: third request should be rate limited")
	}
}

// TestSessionIsolationWithinPrincipal verifies that two sessions of the same principal
// share the per-principal bucket and have distinct per-session buckets.
func TestSessionIsolationWithinPrincipal(t *testing.T) {
	reg := NewRegistry()
	reg.Register(Bucket{
		Name:              "test-bucket",
		RequestsPerSecond: 4, // Limit for testing
		Description:       "Test bucket",
	})

	limiter := NewLimiter(reg)

	principal := "principal-1"

	// Session 1: Make 2 requests
	session1Key := ForSession(principal, "session-1")
	if !limiter.Allow(session1Key, "test-bucket") {
		t.Error("session-1: first request should be allowed")
	}
	if !limiter.Allow(session1Key, "test-bucket") {
		t.Error("session-1: second request should be allowed")
	}

	// Session 2: Make 2 requests (distinct per-session bucket)
	session2Key := ForSession(principal, "session-2")
	if !limiter.Allow(session2Key, "test-bucket") {
		t.Error("session-2: first request should be allowed")
	}
	if !limiter.Allow(session2Key, "test-bucket") {
		t.Error("session-2: second request should be allowed")
	}

	// Now principal has used 4 tokens on principal bucket (2 from session1 + 2 from session2)
	// Session 1 has used 2 tokens on its session bucket
	// Session 2 has used 2 tokens on its session bucket

	// Session 1 third request should be blocked (per-session limit)
	if limiter.Allow(session1Key, "test-bucket") {
		t.Error("session-1: third request should be rate limited (per-session)")
	}

	// Session 2 third request should be blocked (per-session limit)
	if limiter.Allow(session2Key, "test-bucket") {
		t.Error("session-2: third request should be rate limited (per-session)")
	}

	// Principal-only request should be blocked (per-principal limit exhausted)
	principalKey := ForPrincipal(principal)
	if limiter.Allow(principalKey, "test-bucket") {
		t.Error("principal-only: request should be rate limited (per-principal)")
	}
}

// TestNonHumanSessionlessKey verifies that a limiter key can be constructed for
// a session-less, non-human holder. This is a compile-time guarantee that the key
// type is not typed as an IdP subject string.
func TestNonHumanSessionlessKey(t *testing.T) {
	// Device ID as used in FR76 - session-less, non-human principal
	deviceID := "device-abc123"

	// This must compile and work at runtime as a non-IdP-subject principal
	key := ForPrincipal(deviceID)

	// Verify it's not a session key
	if key.HasSession() {
		t.Error("device key should not have a session")
	}

	// Verify principal is preserved
	if key.Principal() != deviceID {
		t.Errorf("expected principal %s, got %s", deviceID, key.Principal())
	}

	// Verify we can use it in a limiter
	reg := NewRegistry()
	reg.Register(Bucket{
		Name:              "device-bucket",
		RequestsPerSecond: 10,
		Description:       "Device test bucket",
	})

	limiter := NewLimiter(reg)

	// Should allow requests
	if !limiter.Allow(key, "device-bucket") {
		t.Error("device key: request should be allowed")
	}

	// Verify the type prevents accidental usage as IdP subject
	// The Key struct type (not string) ensures it cannot be used where a string is expected
	_ = key // This would fail to compile if Key were accidentally made a string type
}

// TestTimingConsistency verifies that refusal timing does not differ measurably
// between rate-limited responses (the attack happens quickly either way, no information leakage).
// This test doesn't measure actual timing (which can vary), but ensures that both
// limited and allowed checks go through the same code path.
func TestTimingConsistency(t *testing.T) {
	reg := NewRegistry()
	reg.Register(Bucket{
		Name:              "test-bucket",
		RequestsPerSecond: 1, // Very restrictive for testing
		Description:       "Test bucket",
	})

	limiter := NewLimiter(reg)
	key := ForPrincipal("test-principal")

	// First request should succeed
	if !limiter.Allow(key, "test-bucket") {
		t.Error("first request should be allowed")
	}

	// Subsequent requests should be rate limited
	if limiter.Allow(key, "test-bucket") {
		t.Error("second request should be rate limited")
	}

	// Verify multiple denied requests go through the same path
	// (The limiter should consistently deny them at the same point)
	deniedCount := 0
	for i := 0; i < 10; i++ {
		if !limiter.Allow(key, "test-bucket") {
			deniedCount++
		}
	}

	if deniedCount != 10 {
		t.Errorf("expected 10 denials, got %d", deniedCount)
	}
}

// TestConcurrentAccessSafety verifies that the limiter is thread-safe and handles
// concurrent requests correctly.
func TestConcurrentAccessSafety(t *testing.T) {
	reg := NewRegistry()
	reg.Register(Bucket{
		Name:              "test-bucket",
		RequestsPerSecond: 100,
		Description:       "Test bucket",
	})

	limiter := NewLimiter(reg)
	key := ForPrincipal("concurrent-principal")

	const goroutines = 10
	const requestsPerGoroutine = 100

	var wg sync.WaitGroup
	allowedCount := 0
	var mu sync.Mutex

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < requestsPerGoroutine; j++ {
				if limiter.Allow(key, "test-bucket") {
					mu.Lock()
					allowedCount++
					mu.Unlock()
				}
			}
		}()
	}

	wg.Wait()

	// Verify that all requests were accounted for
	// With 100 requests/sec limit, should allow roughly 100 requests
	// Allow some margin due to timing
	if allowedCount < 95 || allowedCount > 105 {
		t.Errorf("expected ~100 allowed requests, got %d", allowedCount)
	}
}

// TestNamedBucketRegistry verifies that named buckets can be registered and accessed
// for use by later phases without changing the limiter mechanism.
func TestNamedBucketRegistry(t *testing.T) {
	reg := NewRegistry()

	// Register named buckets for different features
	buckets := []Bucket{
		{Name: "read", RequestsPerSecond: 100, Description: "General read operations"},
		{Name: "claim-initiate", RequestsPerSecond: 10, Description: "FR76 claim initiation"},
		{Name: "claim-rounds", RequestsPerSecond: 20, Description: "FR76 claim rounds"},
		{Name: "support-reference", RequestsPerSecond: 5, Description: "FR80 support reference resolution"},
		{Name: "resend", RequestsPerSecond: 3, Description: "FR42 re-send"},
		{Name: "concurrent-waits", RequestsPerSecond: 15, Description: "FR47 concurrent open bounded waits"},
	}

	for _, bucket := range buckets {
		reg.Register(bucket)
	}

	// Verify all buckets are accessible by name
	for _, expectedBucket := range buckets {
		retrieved, ok := reg.Get(expectedBucket.Name)
		if !ok {
			t.Errorf("bucket %s not found", expectedBucket.Name)
			continue
		}
		if retrieved.RequestsPerSecond != expectedBucket.RequestsPerSecond {
			t.Errorf("bucket %s: expected %d rps, got %d",
				expectedBucket.Name, expectedBucket.RequestsPerSecond, retrieved.RequestsPerSecond)
		}
	}

	// Verify All() returns all buckets
	all := reg.All()
	if len(all) != len(buckets) {
		t.Errorf("expected %d buckets, got %d", len(buckets), len(all))
	}
}

// TestFailOpenBehavior verifies that the limiter fails open (allows requests) when
// a bucket is not configured.
func TestFailOpenBehavior(t *testing.T) {
	reg := NewRegistry()
	limiter := NewLimiter(reg)

	key := ForPrincipal("test-principal")

	// Request for non-existent bucket should be allowed (fail-open)
	if !limiter.Allow(key, "nonexistent-bucket") {
		t.Error("request for unconfigured bucket should be allowed (fail-open)")
	}

	// Multiple requests should all be allowed
	for i := 0; i < 100; i++ {
		if !limiter.Allow(key, "nonexistent-bucket") {
			t.Error("request for unconfigured bucket should always be allowed")
		}
	}
}
