// Package contract defines the response contract shared by every LeafLab
// API RPC: structured failure classes (FR59), keyset pagination (FR61) and
// absolute-instant time fields (FR64). These are established once here, on
// the first three RPCs, because every later response inherits them --
// cheaper to define once than to retrofit across every requirement that
// adds an RPC later.
package contract

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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
// and field (FR59.1).
func FromError(err error) (*pb.Failure, bool) {
	st, ok := status.FromError(err)
	if !ok {
		return nil, false
	}
	for _, d := range st.Details() {
		if f, ok := d.(*pb.Failure); ok {
			return f, true
		}
	}
	return nil, false
}
