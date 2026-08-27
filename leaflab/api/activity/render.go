// Package activity renders FR9's owner-facing activity list: audit_log
// rows (and FR76.7's board-claim detection, which is sourced from
// claim_challenge rather than audit_log at all) turned into plain-language
// sentences, with no proto, table or column names and no status code or
// stack trace (FR59.2).
//
// Render is the single seam every rendering goes through -- a table-driven
// map keyed on (action, entity_kind, actor_kind), not string formatting
// scattered through leaflab/api's RPC handlers (see this task's Scaffold
// section, #1348). "One list, one voice": an admin's elevation and the
// actions taken under it, a grantee's actions and a member's actions all
// look up a Template the same way, through the same Registry -- nothing in
// this package branches on "is this an admin action" to pick a different
// code path.
//
// Registry starts empty in this Scaffold-phase commit. Populating it with
// a Template per audited action, wiring Render into the
// ListHouseholdActivity handler (leaflab/api/server.go has no handler for
// it yet -- see api.proto's doc comment), and the exhaustiveness test over
// every registered (action, entity_kind) pair are Implementation/Testing
// -phase work.
package activity

import "github.com/whale-net/everything/leaflab/api/audit"

// Key identifies one registered rendering: the same (action, entity_kind)
// pair audit.Registration carries (leaflab/api/audit/registration.go),
// plus actor_kind -- the three-part key the issue's Scaffold section names
// ("one renderer mapping (action, entity_kind, actor_kind) to a plain
// sentence"). actor_kind is part of the key (a system actor's phrasing can
// differ from a human's, e.g. FR8.2's automated re-send) but never changes
// *which* sentence structure an admin/grantee/member action gets -- FR9's
// "one list, one voice" is enforced by Render's callers using the same
// Registry for every entry, not by this type.
type Key struct {
	// Action names the operation performed, matching audit.Entry.Action
	// (e.g. "InviteMember", "ClaimBoard", "Elevate").
	Action string
	// EntityKind names the kind of entity Action acted on, matching
	// audit.Entry.EntityKind (e.g. "board", "household", "device_config").
	EntityKind string
	// ActorKind distinguishes a human principal from a system actor
	// (audit.ActorKindHuman / audit.ActorKindSystem, FR8.3).
	ActorKind audit.ActorKind
}

// RenderInput carries the fields a Template needs, already resolved by the
// caller from an audit_log row (or, for FR76.7's claim-attempt entries,
// from claim_challenge) -- Render itself takes no database dependency,
// which is what keeps it pure and unit-testable with no Postgres fixture.
type RenderInput struct {
	// ActorLabel is the persona-appropriate way to name who acted -- "you",
	// a household member's display name, "an administrator". Never a raw
	// principal subject. Left empty for FR76.7's claim-attempt entries: the
	// attempting principal, who belongs to another household, is never
	// identified (A29) -- a Template for a claim-attempt Key must not
	// reference ActorLabel in its output.
	ActorLabel string
	// EntityLabel names the entity acted on in plain language -- a board's
	// display name, a household's name. Never a raw entity_id or table
	// name. Cross-household references render as FR1.4's opaque
	// placeholders -- resolving EntityLabel to one of those, when
	// applicable, is the caller's job, not this package's.
	EntityLabel string
	// Outcome carries the one or two-valued result some actions render
	// differently by -- e.g. FR76.7's claim attempt: discharged (the board
	// left the household) vs not (the attempt failed). Empty for actions
	// whose Template does not vary by outcome.
	Outcome string
}

// Template renders one RenderInput into a plain-language sentence (FR9,
// FR59.2): no proto, table or column names; no status code, stack trace,
// or the string "_id" anywhere in its output.
type Template func(in RenderInput) string

// Registry is the table-driven Key -> Template map this package's doc
// comment describes. Empty in this Scaffold-phase commit -- see the doc
// comment for what lands here next.
var Registry = map[Key]Template{}

// Render looks up (action, entityKind, actorKind) in Registry and applies
// its Template to in. ok is false when no entry is registered; callers
// must treat that as a bug to fix by registering a Template, never as an
// "unknown action" sentence to render as-is -- Implementation/Testing
// -phase work adds the startup/test-time check that makes an audited
// action without a registered rendering fail loudly instead of silently
// falling through here.
func Render(action, entityKind string, actorKind audit.ActorKind, in RenderInput) (sentence string, ok bool) {
	tmpl, found := Registry[Key{Action: action, EntityKind: entityKind, ActorKind: actorKind}]
	if !found {
		return "", false
	}
	return tmpl(in), true
}
