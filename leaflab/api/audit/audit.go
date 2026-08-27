// Package audit implements FR8's append-only audit trail: every write, and
// every read performed under an elevated or granted (non-member) identity,
// produces an audit record via Auditor.Record.
//
// Entry and Auditor are the seam every audited action writes through.
// PostgresAuditor (postgres.go) is the production implementation --
// constructed over a pgx.Tx so the audit INSERT participates in the same
// transaction as the write it records (NFR6.2). NewElevationEntry /
// NewMultiBoardPushEntry / NewTransferEntry (reason.go) are the
// reason-required constructors for FR10/FR48/FR77, which tighten Reason
// from Entry's general *string to a plain string at the call site.
// Registration/ValidateRegistrations (registration.go) back the "a write
// RPC with no audit registration fails at startup" hook each service wires
// up itself (see leaflab/api/audit_registry.go).
//
// Append-only enforcement itself -- the BEFORE UPDATE OR DELETE trigger and
// the REVOKE on the application role -- lives in the database, not here;
// see leaflab/migrate/migrations/016_audit_log.up.sql.
package audit

import "context"

// ActorKind distinguishes what kind of principal performed an audited
// action. Stored verbatim in audit_log.actor_kind.
//
// Two independent questions share this one column, and a given audit row
// answers whichever one its action cares about:
//   - human vs. non-human (FR8.3: "actor" is not defined as something only
//     a human can be -- a scheduled job or an automated re-send must be
//     representable as the acting principal, not just a human subject):
//     ActorKindHuman / ActorKindSystem.
//   - member vs. grantee (FR7's "actor_kind distinguishing grantee from
//     member" on every grant, revocation, and read performed under a
//     grant): ActorKindMember / ActorKindGrantee.
//
// A given write RPC uses whichever pair is relevant to it -- FR7's grant
// RPCs always involve a human, so they use ActorKindMember/ActorKindGrantee
// rather than ActorKindHuman, since member-vs-grantee is the FR8.1-relevant
// distinction there; PushDeviceConfig has no household-grant dimension to
// it at all, so it uses ActorKindHuman. No audit row is expected to need
// both dimensions recorded at once in V1.
type ActorKind string

const (
	// ActorKindHuman is a human principal, identified by their subject.
	ActorKindHuman ActorKind = "human"
	// ActorKindSystem is a non-human actor, e.g. a scheduled job or an
	// automated re-send acting with no human principal behind it (FR8.2
	// names "re-sends that write no config row" as a case that must still
	// be audited).
	ActorKindSystem ActorKind = "system"
	// ActorKindMember is a household member acting on a "member capability"
	// call site (FR7) -- including a member managing grants themselves
	// (GrantHouseholdAccess, RevokeHouseholdAccess).
	ActorKindMember ActorKind = "member"
	// ActorKindGrantee is a non-member acting under an active household
	// grant (FR7) -- e.g. RevokeHouseholdAccess called by a grantee, or a
	// read performed under a granted identity (FR8.1).
	ActorKindGrantee ActorKind = "grantee"
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
// A caller building one of those three uses NewElevationEntry /
// NewMultiBoardPushEntry / NewTransferEntry (reason.go) instead of an Entry
// literal -- those take reason as a plain string, so the requirement is
// enforced at compile time for the three named actions. Every other action
// builds an Entry literal directly, where Reason stays optional.
type Entry struct {
	// ActorSubject is the acting principal's subject. Recorded on every
	// write, including config pushes, re-sends that write no config row,
	// claims, adoptions, transfers, relocations and membership changes
	// (FR8.2).
	ActorSubject string
	// ActorKind is ActorKindHuman/ActorKindSystem (FR8.3) or
	// ActorKindMember/ActorKindGrantee (FR7) -- see ActorKind's doc comment
	// for which pair a given action uses.
	ActorKind ActorKind
	// TargetHouseholdID is the household the action targets, or nil when
	// the action does not resolve to a single household.
	TargetHouseholdID *int64
	// Action names the operation performed, e.g. "RetireBoard" or
	// "PushConfig". Matched against the acting service's per-method audit
	// registration (see leaflab/api/audit_registry.go's
	// auditRegistrations).
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
//     has exactly one -- PostgresAuditor does this when constructed over a
//     pgx.Tx (see postgres.go and leaflab/api/repository.go's
//     auditedWrite);
//   - never update or delete an existing row -- enforced close to the data
//     by a database trigger and role grants, not only by this interface
//     (NFR6.2; see 016_audit_log.up.sql).
type Auditor interface {
	Record(ctx context.Context, entry Entry) error
}
