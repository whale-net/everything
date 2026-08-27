// Package audit implements FR8's append-only audit trail: every write, and
// every read performed under an elevated or granted (non-member) identity,
// produces an audit record via Auditor.Record.
//
// This is the Scaffold: the Entry shape and the Auditor seam. The
// append-only enforcement (NFR6.2's BEFORE UPDATE OR DELETE trigger plus a
// REVOKE on the application role -- see
// leaflab/migrate/migrations/016_audit_log.up.sql), the gRPC
// interceptor-level "every registered write method has an audit
// registration" hook, transactional participation (a rolled-back write
// leaves no audit row), and the reason-required-for-some-actions type
// tightening are all Implementation-phase work.
package audit

import "context"

// ActorKind distinguishes a human principal from a non-human one. FR8.3:
// "actor" is not defined as something only a human can be -- a scheduled
// job or an automated re-send must be representable as the acting
// principal, not just a human subject. Stored verbatim in
// audit_log.actor_kind.
type ActorKind string

const (
	// ActorKindHuman is a human principal, identified by their subject.
	ActorKindHuman ActorKind = "human"
	// ActorKindSystem is a non-human actor, e.g. a scheduled job or an
	// automated re-send acting with no human principal behind it (FR8.2
	// names "re-sends that write no config row" as a case that must still
	// be audited).
	ActorKindSystem ActorKind = "system"
)

// Entry carries FR8.1's required fields for a single audit record: actor
// subject, target household, action, entity, timestamp (occurred_at is
// stamped by the database default -- Entry does not set it), and reason
// where a reason is required.
//
// TargetHouseholdID and EntityID are nullable: not every audited action
// resolves to a single household or produces an identifiable entity (e.g.
// FR8.2's re-send that writes no device_config row still needs an audit
// row, but has no new entity_id to record).
//
// Reason is *string rather than string: FR8's reason is required only for
// specific actions (FR10 elevation, FR48 multi-board push, FR77 transfer).
// Tightening reason to non-optional for exactly those actions -- rather
// than leaving every caller free to omit it -- is Implementation-phase
// work; this Scaffold's Entry is the field set every audited action shares.
type Entry struct {
	// ActorSubject is the acting principal's subject. Recorded on every
	// write, including config pushes, re-sends that write no config row,
	// claims, adoptions, transfers, relocations and membership changes
	// (FR8.2).
	ActorSubject string
	// ActorKind is ActorKindHuman or ActorKindSystem (FR8.3).
	ActorKind ActorKind
	// TargetHouseholdID is the household the action targets, or nil when
	// the action does not resolve to a single household.
	TargetHouseholdID *int64
	// Action names the operation performed, e.g. "RetireBoard" or
	// "PushConfig". Matched against the gRPC interceptor's per-method audit
	// registration in the Implementation phase.
	Action string
	// EntityKind names the kind of entity Action acted on, e.g. "board" or
	// "device_config".
	EntityKind string
	// EntityID identifies the specific entity Action acted on, or nil when
	// the action produced or targeted no identifiable entity.
	EntityID *string
	// Reason records why the action was taken. Required for FR10
	// elevation, FR48 multi-board push and FR77 transfer; optional
	// otherwise. See the Entry doc comment.
	Reason *string
	// CorrelationID is carried from Phase 1's NFR12 correlation-id
	// interceptor (leaflab/api/logging_interceptor.go) so an operational
	// log line and an audit row are joinable.
	CorrelationID string
}

// Auditor writes a single append-only audit record (FR8, NFR6.2, NFR6.3).
//
// Implementations must:
//   - participate in the same transaction as the write they record, so a
//     rolled-back write leaves no audit row and a committed write always
//     has exactly one (Implementation phase);
//   - never update or delete an existing row -- enforced close to the data
//     by a database trigger and role grants, not only by this interface
//     (NFR6.2; see 016_audit_log.up.sql).
type Auditor interface {
	Record(ctx context.Context, entry Entry) error
}
