package grpcauth

import (
	"errors"
	"fmt"
)

// Delegated-grant sentinel errors.
//
// These are exported error *values* (and, for TransientError, an exported
// type). Callers must branch on them with errors.Is / errors.As and must never
// match on error strings — the strings are not part of the contract.
//
// The distinction these sentinels carry is load-bearing for callers:
//
//   - ErrGrantRevoked and ErrGrantNeedsReauth are terminal for automation
//     (FR8): no amount of retrying will produce a token, and a human must
//     re-consent (needs_reauth) or the grant is gone for good (revoked). A
//     caller seeing these should stop and surface the condition, not retry.
//   - TransientError (FR9) is the opposite: Keycloak or storage was
//     momentarily unreachable, the grant itself is fine, and retrying later is
//     the correct response.
//
// FR8's needs_reauth/revoked case and FR9's transient case are REQUIRED to be
// programmatically distinguishable from each other: a transient error must
// never satisfy errors.Is(err, ErrGrantNeedsReauth) or
// errors.Is(err, ErrGrantRevoked), and vice versa. This property is the
// primitive NFR4 is built on; do not collapse these into one error.
var (
	// ErrGrantRevoked is returned when the grant exists but has been revoked.
	ErrGrantRevoked = errors.New("grpcauth: delegated grant revoked")

	// ErrGrantNeedsReauth is returned when the grant exists but its stored
	// refresh token is no longer usable and a human must re-consent.
	ErrGrantNeedsReauth = errors.New("grpcauth: delegated grant needs reauth")

	// ErrGrantNotFound is returned when no row exists for the given
	// (subject, grant) key. It is distinct from a revoked grant: nothing was
	// ever persisted, or the key is simply wrong.
	ErrGrantNotFound = errors.New("grpcauth: delegated grant not found")

	// ErrTransient is the sentinel every TransientError matches under
	// errors.Is. Callers that only need "should I retry?" can test
	// errors.Is(err, ErrTransient) (or call IsTransient) instead of unwrapping
	// to the concrete type.
	ErrTransient = errors.New("grpcauth: transient delegated-grant failure")
)

// TransientError wraps a lower-level failure (network, storage, 5xx from the
// identity provider) that says nothing about the validity of the grant itself
// (FR9). Retrying such an operation later is expected to succeed.
//
// It matches both ErrTransient and its wrapped cause under errors.Is, and is
// retrievable with errors.As(err, &TransientError{}) — but it deliberately
// never matches ErrGrantRevoked or ErrGrantNeedsReauth.
type TransientError struct {
	// Op names the operation that failed, for diagnostics only.
	Op string
	// Err is the underlying cause. It must not contain token plaintext or
	// key material (NFR1).
	Err error
}

// NewTransientError wraps cause as a retryable failure of operation op.
func NewTransientError(op string, cause error) *TransientError {
	return &TransientError{Op: op, Err: cause}
}

func (e *TransientError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err == nil {
		return fmt.Sprintf("grpcauth: transient failure in %s", e.Op)
	}
	return fmt.Sprintf("grpcauth: transient failure in %s: %v", e.Op, e.Err)
}

// Unwrap exposes the wrapped cause to errors.Is / errors.As.
func (e *TransientError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Is makes every TransientError match the ErrTransient sentinel, so callers
// can ask the retry question without knowing the concrete type.
func (e *TransientError) Is(target error) bool {
	return target == ErrTransient
}

// IsTransient reports whether err represents a retryable failure (FR9).
//
// Use it to branch retry-vs-pause: if IsTransient(err) is true, back off and
// try again; if it is false and errors.Is(err, ErrGrantNeedsReauth) or
// errors.Is(err, ErrGrantRevoked) holds, stop and surface the grant state to a
// human instead of retrying.
func IsTransient(err error) bool {
	return errors.Is(err, ErrTransient)
}
