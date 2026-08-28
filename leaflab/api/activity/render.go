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
// Registry is populated below with one Template per audited action this
// service currently writes (leaflab/api/audit_registry.go's
// auditRegistrations), plus support_reference.go's SupportReferenceResolve
// action (a real audited action that is not itself a declaredWriteMethods
// entry -- see its doc comment) and this task's ClaimAttempt/board
// synthetic entry (FR76.7 -- sourced from claim_challenge, never
// audit_log). leaflab/api/activity.go (the ListHouseholdActivity handler)
// resolves every RenderInput field -- ActorLabel, EntityLabel, Outcome --
// from the underlying row before calling Render; this package never does
// its own I/O or its own "is this the caller" comparison. The
// exhaustiveness test over every registered (action, entity_kind) pair
// (this task's Testing section) is separate work layered on top of this
// Registry, not part of it.
package activity

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/whale-net/everything/leaflab/api/audit"
)

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

// Claim-attempt outcomes (FR76.7) -- the three-valued Outcome
// ClaimAttempt/board's Template below switches on. Sourced from
// claim_challenge.state plus whether the household's board_ownership row
// covering the attempt has since been superseded (leaflab/api/activity.go
// computes this from the joined query, never this package):
//   - ClaimAttemptNotDischarged: the challenge never reached 'discharged'
//     (requirement 5's bounded lifetime/attempts ran out) -- the attempt
//     failed outright.
//   - ClaimAttemptDischargedRetained: the challenge discharged (r rounds
//     satisfied) but the board is still this household's -- CompleteClaim
//     refuses to move a real household's board (requirement 6), so
//     discharging a challenge against one confers nothing.
//   - ClaimAttemptDischargedDeparted: the challenge discharged and the
//     board_ownership interval it fell within has since closed -- the
//     board is no longer this household's.
const (
	ClaimAttemptNotDischarged      = "not_discharged"
	ClaimAttemptDischargedRetained = "discharged_retained"
	ClaimAttemptDischargedDeparted = "discharged_departed"
)

// ClaimAttemptAction/ClaimAttemptEntityKind name FR76.7's synthetic
// Registry entry -- exported so leaflab/api/activity.go (which never
// touches audit_log for this entry) can build the same Key Render expects
// without a string literal duplicated at each call site.
const (
	ClaimAttemptAction     = "ClaimAttempt"
	ClaimAttemptEntityKind = "board"
)

// SupportReferenceResolveAction mirrors support_reference.go's
// supportReferenceResolveAction -- duplicated as a constant here (rather
// than importing leaflab/api, which would be a cycle: leaflab/api imports
// this package) so Registry's key for it can't drift from a typo. Kept in
// sync with the leaflab/api constant by registration_test.go's
// (Testing-phase) exhaustiveness check.
const SupportReferenceResolveAction = "SupportReferenceResolve"

// Registry is the table-driven Key -> Template map this package's doc
// comment describes.
var Registry = map[Key]Template{
	// -- Households and membership (FR75, FR7) ------------------------------
	{Action: "CreateHousehold", EntityKind: "household_membership", ActorKind: audit.ActorKindHuman}: func(in RenderInput) string {
		return fmt.Sprintf("%s created this household.", capitalize(in.ActorLabel))
	},
	{Action: "InviteMember", EntityKind: "household_membership", ActorKind: audit.ActorKindHuman}: func(in RenderInput) string {
		return fmt.Sprintf("%s invited %s to the household.", capitalize(in.ActorLabel), in.EntityLabel)
	},
	{Action: "RemoveMember", EntityKind: "household_membership", ActorKind: audit.ActorKindHuman}: func(in RenderInput) string {
		return fmt.Sprintf("%s removed %s from the household.", capitalize(in.ActorLabel), in.EntityLabel)
	},
	{Action: "RenameHousehold", EntityKind: "household", ActorKind: audit.ActorKindHuman}: func(in RenderInput) string {
		return fmt.Sprintf("%s renamed the household.", capitalize(in.ActorLabel))
	},

	// -- Board claim (FR76) --------------------------------------------------
	{Action: "ClaimBoard", EntityKind: "board", ActorKind: audit.ActorKindHuman}: func(in RenderInput) string {
		return fmt.Sprintf("%s claimed %s.", capitalize(in.ActorLabel), in.EntityLabel)
	},

	// -- Device configuration (FR48 activation surface -- PushConfig) -------
	//
	// PushDeviceConfig's audit row does not yet carry a target_household_id
	// (server.go's PushDeviceConfig doc comment -- deferred to the board's
	// household-scoping task); this entry can never actually be reached
	// through ListHouseholdActivity's WHERE target_household_id = $1
	// filter today. Registered anyway, per this task's Implementation
	// section ("a Template per audited action"): a write RPC whose audit
	// registration exists but whose renderer doesn't would fail the
	// exhaustiveness test the moment target_household_id starts being
	// populated, at which point silently rendering nothing would be a
	// worse regression than carrying an unreachable Template now.
	{Action: "PushConfig", EntityKind: "device_config", ActorKind: audit.ActorKindHuman}: func(in RenderInput) string {
		return fmt.Sprintf("%s updated a board's configuration.", capitalize(in.ActorLabel))
	},

	// -- Admin (FR10, FR12 activation) ---------------------------------------
	//
	// One voice: these render exactly like any other entry in the list --
	// same Template shape, same field names, no separate "admin" or
	// "system" section (FR9's "one list, one voice"). ActorLabel is always
	// "an administrator" for these three (leaflab/api/activity.go decides
	// that, never this package) since the acting subject is never a
	// household member.
	{Action: audit.ActionElevate, EntityKind: "household", ActorKind: audit.ActorKindHuman}: func(in RenderInput) string {
		return fmt.Sprintf("%s began a temporary review of this household.", capitalize(in.ActorLabel))
	},
	{Action: "RenewElevation", EntityKind: "household", ActorKind: audit.ActorKindHuman}: func(in RenderInput) string {
		return fmt.Sprintf("%s extended their temporary review of this household.", capitalize(in.ActorLabel))
	},
	{Action: "EndElevation", EntityKind: "household", ActorKind: audit.ActorKindHuman}: func(in RenderInput) string {
		return fmt.Sprintf("%s ended their temporary review of this household.", capitalize(in.ActorLabel))
	},
	// ResolveToHousehold's own call-granularity audit row (FR10.4) never
	// carries a target_household_id either (it may resolve to several
	// households, or none) -- unreachable through this RPC's filter today,
	// same reasoning as PushConfig above. Registered for the same
	// forward-looking exhaustiveness reason.
	{Action: "ResolveToHousehold", EntityKind: "admin_resolution", ActorKind: audit.ActorKindHuman}: func(in RenderInput) string {
		return fmt.Sprintf("%s looked up this household.", capitalize(in.ActorLabel))
	},

	// -- Support reference (FR80) --------------------------------------------
	{Action: "CreateSupportReference", EntityKind: "support_reference", ActorKind: audit.ActorKindHuman}: func(in RenderInput) string {
		return fmt.Sprintf("%s created a support reference for this household.", capitalize(in.ActorLabel))
	},
	{Action: "RevokeSupportReference", EntityKind: "support_reference", ActorKind: audit.ActorKindHuman}: func(in RenderInput) string {
		return fmt.Sprintf("%s revoked a support reference.", capitalize(in.ActorLabel))
	},
	// SupportReferenceResolve is the reference's *use* (FR80: "existence
	// and use are visible to the owner in FR9's activity list") -- always
	// an administrator's standing-lane resolve (FR10.2), never a household
	// member's own action.
	{Action: SupportReferenceResolveAction, EntityKind: "support_reference", ActorKind: audit.ActorKindHuman}: func(in RenderInput) string {
		return fmt.Sprintf("%s used a support reference to look up this household.", capitalize(in.ActorLabel))
	},

	// -- FR76.7: claim-attempt detection (sourced from claim_challenge, not
	//    audit_log) ------------------------------------------------------
	//
	// Never references ActorLabel (A29: the attempting principal belongs to
	// another household and is never identified) -- see RenderInput's doc
	// comment on ActorLabel.
	{Action: ClaimAttemptAction, EntityKind: ClaimAttemptEntityKind, ActorKind: audit.ActorKindHuman}: func(in RenderInput) string {
		switch in.Outcome {
		case ClaimAttemptNotDischarged:
			return fmt.Sprintf("Someone tried to prove they were at %s. They couldn't.", in.EntityLabel)
		case ClaimAttemptDischargedDeparted:
			return fmt.Sprintf("Someone tried to prove they were at %s. They did, and the board left your household.", in.EntityLabel)
		case ClaimAttemptDischargedRetained:
			return fmt.Sprintf("Someone tried to prove they were at %s. They did, but the board is still yours.", in.EntityLabel)
		default:
			// Defensive fallback for a call with no (or an unrecognized)
			// Outcome -- e.g. a future exhaustiveness test calling this
			// Template with a zero-value RenderInput. Carries no claim about
			// what happened, only that something was attempted.
			return fmt.Sprintf("Someone tried to prove they were at %s.", in.EntityLabel)
		}
	},
}

// capitalize upper-cases s's first rune -- every Template above starts its
// sentence with ActorLabel, which PersonLabel and leaflab/api/activity.go's
// other label helpers return lower-case ("you", "an administrator") so they
// read naturally mid-sentence too (e.g. "...invited you to the household").
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// forbiddenSubstrings are internal identifiers a rendered sentence must
// never contain (FR59.2): the string "_id" (catches every *_id column or
// proto field -- household_id, device_id, board_id, support_reference_id,
// audit_id, challenge_id, correlation_id...), a few other snake_case
// proto/column names with no "_id" suffix, and a curated set of clearly
// internal, multi-word snake_case table names. Single-word product
// vocabulary that also happens to be a table name (board, household,
// region, plant, sensor) is deliberately absent -- FR9 does not ask a
// sentence to avoid the words a household actually uses for its own boards
// and plants, only to avoid leaking a raw internal identifier.
var forbiddenSubstrings = []string{
	"_id",
	"actor_subject",
	"principal_subject",
	"occurred_at",
	"entity_kind",
	"household_membership",
	"device_config",
	"audit_log",
	"claim_challenge",
	"claim_cooldown",
	"board_ownership",
	"admin_elevation",
	"board_uptime_watermark",
	"admin_resolution",
}

// statusCodeLike flags a bare three-digit token (e.g. "404", "500") -- the
// shape of an HTTP/gRPC status code and not otherwise expected in any
// Template above (none renders a date or any other numeral).
var statusCodeLike = regexp.MustCompile(`\b\d{3}\b`)

// mustBeRenderSafe panics if sentence violates FR59.2 (forbiddenSubstrings
// or statusCodeLike). Called by Render on every produced sentence: a
// Template that leaks an internal identifier or a status code is a
// programming error to catch the moment it first renders, not something
// every caller has to remember to check for itself -- mirrors
// audit.ValidateRegistrations' startup-panic convention (registration.go).
func mustBeRenderSafe(sentence string) string {
	lower := strings.ToLower(sentence)
	for _, bad := range forbiddenSubstrings {
		if strings.Contains(lower, bad) {
			panic(fmt.Sprintf("activity: rendered sentence leaks internal identifier %q: %q", bad, sentence))
		}
	}
	if statusCodeLike.MatchString(sentence) {
		panic(fmt.Sprintf("activity: rendered sentence looks like it carries a status code: %q", sentence))
	}
	return sentence
}

// Render looks up (action, entityKind, actorKind) in Registry and applies
// its Template to in. ok is false when no entry is registered; callers
// must treat that as a bug to fix by registering a Template, never as an
// "unknown action" sentence to render as-is. Every produced sentence is
// checked by mustBeRenderSafe before being returned.
func Render(action, entityKind string, actorKind audit.ActorKind, in RenderInput) (sentence string, ok bool) {
	tmpl, found := Registry[Key{Action: action, EntityKind: entityKind, ActorKind: actorKind}]
	if !found {
		return "", false
	}
	return mustBeRenderSafe(tmpl(in)), true
}

// PersonLabel is the one place "is this the household member reading their
// own activity list" gets turned into "you" vs. a generic third-person
// label -- leaflab/api/activity.go calls this for both ActorLabel (was the
// caller themselves the actor?) and, for InviteMember/RemoveMember,
// EntityLabel (is the affected member the caller?), so that decision is
// made in exactly one place regardless of which RenderInput field it
// feeds.
func PersonLabel(isCaller bool, other string) string {
	if isCaller {
		return "you"
	}
	return other
}
