// Package ratelimit defines the per-principal and per-session rate limiting
// primitives (NFR10) shared by every read RPC in leaflab/api. Established
// once, in Phase 1, because later requirements bind to this limiter by
// reference and must not each invent their own: FR42's re-send, FR47's
// concurrent open waits, FR76's claim initiation and rounds (FR76.2), and
// FR80's support-reference resolution.
//
// This package declares the Key/Bucket vocabulary, the Limiter interface, a
// process-local InMemoryLimiter implementation (see its doc comment for why
// in-process is NFR10-compliant at leaflab-api's replicas = 1), and an
// env-var-driven config loader (see env.go, leaflab/api/ENV.md). The six
// Phase 1 buckets are registered here; only BucketReadDefault is actually
// wired into the interceptor chain in Phase 1 (see leaflab/api's
// ratelimit_interceptor.go) -- the rest exist so a later task adds an
// enforcement point by referencing an existing bucket instead of inventing
// a new limiter.
package ratelimit

import (
	"context"
	"sync"
	"time"
)

// Key is an opaque rate-limit dimension: a principal id, a session id, or
// an arbitrary caller-supplied value (e.g. a device id). Deliberately not
// an IdP subject type -- NFR10 requires the limiter to remain expressible
// against a non-human, session-less holder (e.g. FR76.2's device claim
// flow, which limits on the submitted device_id itself, not on any
// authenticated party). Callers construct a Key from whatever dimension is
// appropriate for the bucket in question; this package does not prescribe
// how a Key is derived from a request.
type Key string

// Bucket names a single protected operation. Each bucket carries its own
// limit configuration (see leaflab/api/ENV.md) so a later implementation
// task can register enforcement points without changing the Limiter
// interface or the interceptor chain shape.
type Bucket string

// Buckets registered in Phase 1. Registering the name now -- even before
// any RPC enforces it -- lets later tasks (FR42, FR47, FR76, FR80) add
// enforcement by referencing an existing bucket instead of inventing a new
// limiter each time. See NFR10's requirement text for the mapping from
// bucket to requirement.
const (
	// BucketReadDefault is the default limit applied to every read RPC
	// that has no more specific named bucket.
	BucketReadDefault Bucket = "read_default"
	// BucketResend covers FR42's re-send operation.
	BucketResend Bucket = "resend"
	// BucketAckWaitConcurrent covers FR47's concurrent open waits.
	BucketAckWaitConcurrent Bucket = "ack_wait_concurrent"
	// BucketClaimOpen covers FR76's claim initiation. Keyed on both the
	// submitted device_id and the calling principal (FR76.2) -- deliberately
	// not per-board, since a per-board cap would itself be an existence
	// oracle (NFR10).
	BucketClaimOpen Bucket = "claim_open"
	// BucketClaimRound covers FR76.2's claim rounds.
	BucketClaimRound Bucket = "claim_round"
	// BucketSupportReferenceResolve covers FR80's support-reference
	// resolution.
	BucketSupportReferenceResolve Bucket = "support_reference_resolve"
)

// Buckets lists every Bucket registered in Phase 1, in declaration order.
// A later validation task asserts against this list rather than
// re-deriving it from interceptor wiring, so "the six named buckets exist"
// is checkable without standing up a server.
var Buckets = []Bucket{
	BucketReadDefault,
	BucketResend,
	BucketAckWaitConcurrent,
	BucketClaimOpen,
	BucketClaimRound,
	BucketSupportReferenceResolve,
}

// Limiter enforces a rate limit for a Key within a Bucket. Allow reports
// whether the caller may proceed; when it returns false, retryAfter is the
// minimum duration the caller should wait before the next attempt is
// expected to succeed. See leaflab/api's rate-limit interceptor for the
// grpc.UnaryServerInterceptor that calls this after auth in the chain (see
// leaflab/api/main.go's buildServer for chain ordering).
//
// Allow must behave identically for a Key that resolves to a real entity
// and one that does not (NFR10's oracle-safety constraint, forward-binding
// for FR76): the limiter itself has no notion of whether a Key "exists" --
// it only ever sees the opaque string.
type Limiter interface {
	Allow(ctx context.Context, key Key, bucket Bucket) (allowed bool, retryAfter time.Duration)
}

// WindowConfig is a single Bucket's fixed-window limit: at most Limit calls
// for a given Key within Window, after which Allow returns false until the
// window rolls over.
type WindowConfig struct {
	// Limit is the number of calls permitted per Key within Window. A
	// Limit of zero or less disables enforcement for the bucket (Allow
	// always returns true) -- see NewInMemoryLimiter.
	Limit int
	// Window is the fixed duration a Limit applies over.
	Window time.Duration
}

// window is one Key/Bucket pair's live counter within the current fixed
// window. Not exported -- InMemoryLimiter's internal state.
type window struct {
	count   int
	resetAt time.Time
}

type keyBucket struct {
	key    Key
	bucket Bucket
}

// InMemoryLimiter is a process-local, fixed-window Limiter. NFR10's storage
// note permits an in-process store for V1 only where behaviour is identical
// across replicas; leaflab-api runs with replicas = 1 (see
// leaflab/api/BUILD.bazel's release_app), so there is exactly one process
// and in-process state is trivially replica-identical -- no shared store is
// needed for Phase 1. If leaflab-api is ever scaled beyond one replica,
// this must move to a shared store (e.g. Redis) before that happens, or
// per-replica windows silently multiply the effective limit.
//
// Safe for concurrent use.
type InMemoryLimiter struct {
	mu      sync.Mutex
	configs map[Bucket]WindowConfig
	state   map[keyBucket]*window
	now     func() time.Time
}

// NewInMemoryLimiter builds an InMemoryLimiter from configs, one
// WindowConfig per Bucket. A Bucket with no entry in configs -- or an entry
// with Limit <= 0 -- is never throttled: Allow returns true unconditionally
// for it. This lets a bucket be registered (ratelimit.Buckets) before an
// environment supplies a limit for it, per the scaffold's "later tasks add
// enforcement points without touching the interceptor" goal.
func NewInMemoryLimiter(configs map[Bucket]WindowConfig) *InMemoryLimiter {
	return &InMemoryLimiter{
		configs: configs,
		state:   make(map[keyBucket]*window),
		now:     time.Now,
	}
}

// Allow implements Limiter. It never inspects key or bucket beyond using
// them as a map lookup -- no branch here depends on whether key resolves to
// a real entity, which is what makes Allow's behaviour identical for a
// synthetic, non-existent key and a real one (NFR10's oracle-safety
// constraint).
func (l *InMemoryLimiter) Allow(_ context.Context, key Key, bucket Bucket) (bool, time.Duration) {
	cfg, ok := l.configs[bucket]
	if !ok || cfg.Limit <= 0 {
		return true, 0
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	kb := keyBucket{key: key, bucket: bucket}
	w, exists := l.state[kb]
	if !exists || !now.Before(w.resetAt) {
		w = &window{count: 0, resetAt: now.Add(cfg.Window)}
		l.state[kb] = w
	}

	w.count++
	if w.count > cfg.Limit {
		return false, w.resetAt.Sub(now)
	}
	return true, 0
}
