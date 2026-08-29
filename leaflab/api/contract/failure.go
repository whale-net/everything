// Package contract defines the response contract shared by every LeafLab
// API RPC: structured failure classes (FR59), keyset pagination (FR61) and
// absolute-instant time fields (FR64). These are established once here, on
// the first three RPCs, because every later response inherits them --
// cheaper to define once than to retrofit across every requirement that
// adds an RPC later.
package contract

import (
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/protoadapt"
	"google.golang.org/protobuf/types/known/durationpb"

	pb "github.com/whale-net/everything/leaflab/api/proto"
)

// FailureClass is a machine-readable failure classification, carried as a
// gRPC status detail (pb.Failure) rather than encoded in the status
// message string. A caller branches on this value without parsing prose
// (FR59.1).
type FailureClass string

const (
	// FailureUnauthenticated: caller identity is missing or invalid.
	FailureUnauthenticated FailureClass = "unauthenticated"
	// FailurePermissionDenied: caller is known but not allowed to perform
	// the operation.
	FailurePermissionDenied FailureClass = "permission_denied"
	// FailureNotFound: the named entity does not exist.
	FailureNotFound FailureClass = "not_found"
	// FailureInvalidArgument: the request is malformed independent of who
	// is asking or what exists.
	FailureInvalidArgument FailureClass = "invalid_argument"
	// FailureRateLimited: the caller is being throttled.
	FailureRateLimited FailureClass = "rate_limited"
	// FailureRefusedWithAlternative: the operation is refused before
	// anything is written, per FR59.3's refuse-and-name-the-alternative
	// contract. Distinguishable from an ordinary FailureInvalidArgument by
	// class alone -- a caller never needs to parse the message to tell
	// "your input was wrong" apart from "this can't be done, do X
	// instead".
	FailureRefusedWithAlternative FailureClass = "refused_with_alternative"
	// FailureInternal: an unexpected server-side failure (e.g. a database
	// error) that is not attributable to the caller's input or identity.
	// The reason carried on this class is always a generic,
	// non-technical sentence (FR59.2) -- the underlying error is logged
	// server-side, never placed in the Failure detail or status message.
	FailureInternal FailureClass = "internal"
)

// grpcCode maps each FailureClass to the gRPC status code a transport-level
// caller sees. gRPC requires a code on every non-OK status; the class in
// the Failure detail -- not the code -- is what a script should branch on.
var grpcCode = map[FailureClass]codes.Code{
	FailureUnauthenticated:        codes.Unauthenticated,
	FailurePermissionDenied:       codes.PermissionDenied,
	FailureNotFound:               codes.NotFound,
	FailureInvalidArgument:        codes.InvalidArgument,
	FailureRateLimited:            codes.ResourceExhausted,
	FailureRefusedWithAlternative: codes.FailedPrecondition,
	FailureInternal:               codes.Internal,
}

// New builds a gRPC error for class, carrying a structured pb.Failure
// detail (entity, field, reason) instead of an interpolated message string
// (FR59.1). reason must already be a persona-appropriate sentence (FR59.2):
// no status code, proto field name, table name or stack trace.
func New(class FailureClass, entity, field, reason string) error {
	return build(class, entity, field, reason, "")
}

// Unauthenticated builds a FailureUnauthenticated error. See New.
func Unauthenticated(entity, field, reason string) error {
	return New(FailureUnauthenticated, entity, field, reason)
}

// PermissionDenied builds a FailurePermissionDenied error. See New.
func PermissionDenied(entity, field, reason string) error {
	return New(FailurePermissionDenied, entity, field, reason)
}

// NotFound builds a FailureNotFound error. See New.
func NotFound(entity, field, reason string) error {
	return New(FailureNotFound, entity, field, reason)
}

// InvalidArgument builds a FailureInvalidArgument error. See New.
func InvalidArgument(entity, field, reason string) error {
	return New(FailureInvalidArgument, entity, field, reason)
}

// RateLimited builds a FailureRateLimited error. See New.
func RateLimited(entity, field, reason string) error {
	return New(FailureRateLimited, entity, field, reason)
}

// RateLimitedWithRetry builds a FailureRateLimited error carrying a
// structured retry hint (NFR10): alongside the usual pb.Failure detail, the
// status carries a google.rpc.RetryInfo detail with retryAfter as its
// retry_delay -- the standard gRPC shape a client library already knows how
// to read, so "the caller is being throttled" comes with a machine-readable
// "try again in N" rather than a prose-only error (FR59's "no prose-only
// error" applied to NFR10's retry hint specifically).
func RateLimitedWithRetry(entity, field, reason string, retryAfter time.Duration) error {
	base := build(FailureRateLimited, entity, field, reason, "")
	st, ok := status.FromError(base)
	if !ok {
		// build always returns a status-backed error; unreachable in
		// practice, but fall back to the un-detailed error rather than
		// panic if that ever changes.
		return base
	}
	withRetry, err := st.WithDetails(&errdetails.RetryInfo{
		RetryDelay: durationpb.New(retryAfter),
	})
	if err != nil {
		// WithDetails only errors on a malformed proto, which RetryInfo
		// never is here -- fall back to the Failure-only status rather
		// than panic or silently drop the failure.
		return base
	}
	return withRetry.Err()
}

// Internal builds a FailureInternal error. reason must already be a
// generic, persona-appropriate sentence (FR59.2) -- callers log the actual
// underlying error server-side and pass only that generic sentence here,
// so no stack trace, table name or driver error ever reaches the client.
func Internal(entity, field, reason string) error {
	return New(FailureInternal, entity, field, reason)
}

// Refuse is the single implementation of FR59.3's refuse-and-name-the-
// alternative contract: an operation that cannot be performed, or that
// would change identity or attribution, is refused before anything is
// written, states reason against entity/field, and names alternative as
// the path the caller should take instead. Every refusal cited by this
// plan (FR17, FR42.2, FR50.5, FR70, FR74, FR82.3) calls this rather than
// building its own status, so the shape is identical everywhere.
func Refuse(entity, field, reason, alternative string) error {
	return build(FailureRefusedWithAlternative, entity, field, reason, alternative)
}

func build(class FailureClass, entity, field, reason, alternative string) error {
	code, ok := grpcCode[class]
	if !ok {
		code = codes.Unknown
	}
	st := status.New(code, reason)
	detail := &pb.Failure{
		Class:       string(class),
		Entity:      entity,
		Field:       field,
		Reason:      reason,
		Alternative: alternative,
	}
	withDetails, err := st.WithDetails(detail)
	if err != nil {
		// WithDetails only errors on a malformed proto, which pb.Failure
		// never is here -- fall back to the un-detailed status rather
		// than panic or silently drop the failure.
		return st.Err()
	}
	return withDetails.Err()
}

// FromError extracts the pb.Failure detail from err, if present. A caller
// uses this -- never message-string parsing -- to branch on class, entity
// and field (FR59.1). When err carries more than one Failure detail (see
// Many/AllFailures below), FromError returns only the first -- a caller
// that must see every failure (FR39's "all failures returned together")
// needs AllFailures instead.
func FromError(err error) (*pb.Failure, bool) {
	failures, ok := AllFailures(err)
	if !ok {
		return nil, false
	}
	return failures[0], true
}

// AllFailures extracts every pb.Failure detail carried by err, in the
// order they were attached. Unlike FromError (which only ever returns the
// first), this is the one accessor a caller must use to see every failure
// on an error built by Many -- FR39's "all failures returned together,
// never just the first" applies to the reading side too: a caller that
// only calls FromError on a Many-built error silently drops every failure
// but the first.
func AllFailures(err error) ([]*pb.Failure, bool) {
	st, ok := status.FromError(err)
	if !ok {
		return nil, false
	}
	var failures []*pb.Failure
	for _, d := range st.Details() {
		if f, ok := d.(*pb.Failure); ok {
			failures = append(failures, f)
		}
	}
	if len(failures) == 0 {
		return nil, false
	}
	return failures, true
}

// FailureDetail is one failure to attach to a multi-failure error built by
// Many (FR39's "every failure found is collected together, never just the
// first -- a single failure must not mask the rest"). Unlike New's flat
// class/entity/field/reason, each FailureDetail carries its own Class and
// (optionally) Alternative, so a caller branches per-detail exactly as it
// would on a single-failure error's Failure.Class via AllFailures --
// entity is shared by every detail in one Many call, matching New's own
// "one entity per error" shape at the single-failure level.
type FailureDetail struct {
	Class  FailureClass
	Field  string
	Reason string
	// Alternative is set only when Class is FailureRefusedWithAlternative
	// (FR59.3), same as New/Refuse's own split.
	Alternative string
}

// Many builds a gRPC error carrying every one of details as a separate
// pb.Failure status detail -- FR39's validation surface, where a payload
// can fail several independent checks at once and every one of them must
// reach the caller, not just whichever was found first. entity is shared
// by every detail. gRPC allows exactly one status code per error, so the
// outer code is taken from details[0].Class alone; that code is a
// transport-level envelope only; it is never what a caller branches on
// (see New's doc comment) -- AllFailures(err) is what recovers every
// detail's own Class. Many panics if details is empty: a caller with
// nothing to report should not call this at all.
func Many(entity string, details []FailureDetail) error {
	if len(details) == 0 {
		panic("contract.Many called with no failure details")
	}
	code, ok := grpcCode[details[0].Class]
	if !ok {
		code = codes.Unknown
	}
	st := status.New(code, details[0].Reason)
	pbDetails := make([]*pb.Failure, len(details))
	for i, d := range details {
		pbDetails[i] = &pb.Failure{
			Class:       string(d.Class),
			Entity:      entity,
			Field:       d.Field,
			Reason:      d.Reason,
			Alternative: d.Alternative,
		}
	}
	withDetails, err := st.WithDetails(protoMessages(pbDetails)...)
	if err != nil {
		// WithDetails only errors on a malformed proto, which pb.Failure
		// never is here -- fall back to the un-detailed status rather than
		// panic or silently drop every failure.
		return st.Err()
	}
	return withDetails.Err()
}

func protoMessages(details []*pb.Failure) []protoadapt.MessageV1 {
	out := make([]protoadapt.MessageV1, len(details))
	for i, d := range details {
		out[i] = d
	}
	return out
}

// RetryAfterFromError extracts the retry-after duration from err's
// google.rpc.RetryInfo detail, if present. See RateLimitedWithRetry -- this
// is its inverse, letting a caller read the structured retry hint without
// parsing the status message.
func RetryAfterFromError(err error) (time.Duration, bool) {
	st, ok := status.FromError(err)
	if !ok {
		return 0, false
	}
	for _, d := range st.Details() {
		if ri, ok := d.(*errdetails.RetryInfo); ok {
			return ri.GetRetryDelay().AsDuration(), true
		}
	}
	return 0, false
}
