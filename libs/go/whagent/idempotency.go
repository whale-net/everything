package whagent

import "fmt"

// IdempotencyKeyArgument is the contract's top-level tool argument name
// (LB4, FR11) every mutating tool a domain server exposes must accept:
// `idempotency_key` (string). This matches what audience_score_system
// already ships (see audience_score_system/mcp/server/idempotency.go's
// IdempotencyKeyed).
const IdempotencyKeyArgument = "idempotency_key"

// IdempotencyKeyed is implemented by a write tool's input type when its
// schema includes the IdempotencyKeyArgument argument. A domain server's
// tool registry type-asserts each call's decoded input against this
// interface to decide whether to route the call's mutation through its
// idempotency guard (see the guard-scope doc below) -- mirroring
// audience_score_system/mcp/server/idempotency.go's own IdempotencyKeyed,
// which this contract formalizes.
type IdempotencyKeyed interface {
	// IdempotencyKey returns the caller-supplied idempotency key for this
	// call, or "" if none was supplied.
	IdempotencyKey() string
}

// DeriveIdempotencyKey deterministically derives the idempotency_key
// (LB4) a session's tool call carries from (sessionID, turn, callIndex): a
// colon-joined "sessionID:turn:callIndex". By construction this is stable
// across retries of the same call -- a retry of a failed call re-sends
// the same turn and callIndex within the same session, so it re-derives
// to the exact same key -- callers must never regenerate this key on
// retry, and must never derive it any other way. This is a pure function
// of its three arguments with no hidden state (no randomness, no
// wall-clock reads), so it is safe to call repeatedly for the same call.
func DeriveIdempotencyKey(sessionID string, turn, callIndex int) string {
	return fmt.Sprintf("%s:%d:%d", sessionID, turn, callIndex)
}

// IdempotencyGuardScope documents the guard scope domain servers must use
// (LB4): (tool, resolved identity, key). "Resolved identity" is the
// domain's own user record (e.g. audience_score_system's person_id), not
// which of the domain's parallel authentication paths (this package's
// Claim-verifying path, or any pre-existing web-session path) produced
// it -- so a domain keeps one idempotency store across all of its
// authentication paths, not one per path (FR11).
const IdempotencyGuardScope = "(tool, resolved identity, key)"
