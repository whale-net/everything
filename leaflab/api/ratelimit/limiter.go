package ratelimit

import (
	"sync"
	"time"
)

// tokenBucket implements a simple token bucket rate limiter.
type tokenBucket struct {
	mu                sync.Mutex
	requestsPerSecond int
	tokens            float64
	lastRefillTime    time.Time
}

// newTokenBucket creates a new token bucket with the given rate.
func newTokenBucket(requestsPerSecond int) *tokenBucket {
	return &tokenBucket{
		requestsPerSecond: requestsPerSecond,
		tokens:            float64(requestsPerSecond),
		lastRefillTime:    time.Now(),
	}
}

// allow attempts to consume one token from the bucket.
// Returns true if a token was available, false otherwise.
func (tb *tokenBucket) allow() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	// Refill tokens based on time elapsed
	now := time.Now()
	elapsed := now.Sub(tb.lastRefillTime).Seconds()
	tokensToAdd := elapsed * float64(tb.requestsPerSecond)
	tb.tokens += tokensToAdd
	tb.lastRefillTime = now

	// Cap tokens at the request rate (max burst = rate)
	if tb.tokens > float64(tb.requestsPerSecond) {
		tb.tokens = float64(tb.requestsPerSecond)
	}

	// Try to consume a token
	if tb.tokens >= 1.0 {
		tb.tokens -= 1.0
		return true
	}

	return false
}

// Limiter applies rate limits to keys using token buckets.
// It maintains separate buckets for per-principal and per-session limits.
type Limiter struct {
	mu            sync.RWMutex
	registry      *Registry
	principalLims map[string]*tokenBucket // keyed by principal
	sessionLims   map[string]*tokenBucket  // keyed by principal:session
}

// NewLimiter creates a new rate limiter with the given registry.
func NewLimiter(registry *Registry) *Limiter {
	return &Limiter{
		registry:      registry,
		principalLims: make(map[string]*tokenBucket),
		sessionLims:   make(map[string]*tokenBucket),
	}
}

// Allow checks if a request should be allowed for the given key and bucket name.
// Returns true if the request is allowed, false if it exceeds the rate limit.
// If the bucket is not found, the request is allowed (fail-open).
func (l *Limiter) Allow(key Key, bucketName string) bool {
	bucket, ok := l.registry.Get(bucketName)
	if !ok {
		// Bucket not found, allow the request
		return true
	}

	// Get or create per-principal limiter
	principalKey := key.Principal()
	if !l.checkPrincipalLimit(principalKey, bucket) {
		return false
	}

	// If the key has a session, also check the per-session limit
	if key.HasSession() {
		sessionKey := key.principal + ":" + key.session
		if !l.checkSessionLimit(sessionKey, bucket) {
			return false
		}
	}

	return true
}

// checkPrincipalLimit checks the per-principal rate limit.
func (l *Limiter) checkPrincipalLimit(principalKey string, bucket Bucket) bool {
	l.mu.Lock()
	limiter, ok := l.principalLims[principalKey]
	if !ok {
		// Create new limiter for this principal
		limiter = newTokenBucket(bucket.RequestsPerSecond)
		l.principalLims[principalKey] = limiter
	}
	l.mu.Unlock()

	return limiter.allow()
}

// checkSessionLimit checks the per-session rate limit.
func (l *Limiter) checkSessionLimit(sessionKey string, bucket Bucket) bool {
	l.mu.Lock()
	limiter, ok := l.sessionLims[sessionKey]
	if !ok {
		// Create new limiter for this session
		limiter = newTokenBucket(bucket.RequestsPerSecond)
		l.sessionLims[sessionKey] = limiter
	}
	l.mu.Unlock()

	return limiter.allow()
}
