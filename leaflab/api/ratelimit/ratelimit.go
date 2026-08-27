// Package ratelimit defines the per-principal and per-session rate limiting
// primitives (NFR10) shared by every read RPC in leaflab/api. Established
// once, in Phase 1, because later requirements bind to this limiter by
// reference and must not each invent their own: FR42's re-send, FR47's
// concurrent open waits, FR76's claim initiation and rounds (FR76.2), and
// FR80's support-reference resolution.
//
// This package is scaffold only -- it declares the Key/Bucket vocabulary
// and the Limiter interface, and registers the named buckets a later
// implementation task wires into the interceptor chain. No enforcement
// happens here yet; see leaflab/api's interceptor wiring (a later task)
// for that.
package ratelimit

import (
	"context"
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
// expected to succeed. A future implementation task fills in Allow's
// behaviour (in-process or shared-store backed, per NFR10's storage note)
// and wires a grpc.UnaryServerInterceptor that calls it after auth in the
// chain (see leaflab/api/main.go's buildServer for chain ordering).
//
// Allow must behave identically for a Key that resolves to a real entity
// and one that does not (NFR10's oracle-safety constraint, forward-binding
// for FR76): the limiter itself has no notion of whether a Key "exists" --
// it only ever sees the opaque string.
type Limiter interface {
	Allow(ctx context.Context, key Key, bucket Bucket) (allowed bool, retryAfter time.Duration)
}
