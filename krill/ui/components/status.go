// Package components holds krill's app-owned UI chrome: the shell
// wrapper around htmxui's shared Shell, and the domain vocabulary that
// maps krill's own status strings onto htmxui's generic primitives.
//
// It is deliberately a separate package from krill/ui's pages. htmxui
// §1 scopes itself to chrome and primitives common across apps; the
// things that make a badge say "partially complete" are krill's, and
// keeping them here is what stops them leaking into the shared library.
package components

import "github.com/whale-net/everything/libs/go/htmxui"

// StatusStyle is the full daisyUI presentation for one krill status
// value. The tuple, not the variant alone, is what keeps krill's eight
// milestone states visually distinct: the palette has fewer usable
// values than statuses, so "in progress" and "partially complete" share
// a hue and are separated by the soft treatment instead.
type StatusStyle struct {
	Variant htmxui.BadgeVariant
	Size    htmxui.BadgeSize
	Soft    bool
}

// MilestoneStatusStyle maps krill's milestone status vocabulary onto a
// daisyUI badge.
//
// It takes a string rather than the store type so this chrome package
// carries no //krill/store dependency -- htmxui §1's "no domain type"
// rule, applied to krill's own chrome package one level down -- and so
// an unrecognised value falls through to a neutral badge instead of
// failing to compile when a ninth status is added.
//
// The colours come from manmanv2/ui/DESIGN_SYSTEM.md via htmxui §6:
// warning for in-flight work, success for shipped, error for given-up,
// ghost for not-yet-started.
func MilestoneStatusStyle(status string) StatusStyle {
	switch status {
	case "not started":
		return StatusStyle{htmxui.BadgeGhost, htmxui.BadgeSizeSM, false}
	case "in design":
		return StatusStyle{htmxui.BadgeInfo, htmxui.BadgeSizeSM, true}
	case "designed":
		return StatusStyle{htmxui.BadgeSecondary, htmxui.BadgeSizeSM, false}
	case "planned":
		return StatusStyle{htmxui.BadgePrimary, htmxui.BadgeSizeSM, true}
	case "in progress":
		return StatusStyle{htmxui.BadgeWarning, htmxui.BadgeSizeSM, false}
	case "shipped":
		return StatusStyle{htmxui.BadgeSuccess, htmxui.BadgeSizeSM, false}
	case "partially complete":
		return StatusStyle{htmxui.BadgeWarning, htmxui.BadgeSizeSM, true}
	case "abandoned":
		return StatusStyle{htmxui.BadgeError, htmxui.BadgeSizeSM, true}
	default:
		return StatusStyle{htmxui.BadgeNeutral, htmxui.BadgeSizeSM, false}
	}
}

// MilestoneCountStyle is the badge for a milestone cell that COUNTS rather
// than names -- "N milestones", where several deliver one feature's
// requirements and there is no single status to colour by.
//
// Neutral for that reason, the same value the unknown-status fallback in
// MilestoneStatusStyle returns: a count asserts no state, so it takes no
// state colour. It lives beside the mapper rather than being spelled at the
// call site so a future count badge cannot acquire a second, hand-picked
// tone.
func MilestoneCountStyle() StatusStyle {
	return StatusStyle{htmxui.BadgeNeutral, htmxui.BadgeSizeSM, false}
}

// DesignSessionStageStyle maps a design session's derived Stage (store's
// Stage, spelled out as strings so this package keeps no //krill/store
// dependency) onto a daisyUI badge. It is the only place that vocabulary
// acquires a colour, so the sessions table and the session detail's own
// header show one stage the one way.
//
// The seven values read as progress towards an approved session: ghost for
// a session nothing has happened in yet, the two info values for the two
// rounds the operator is waiting on a reply to, secondary/primary for the
// settled ones, error-soft for the one that sent the session back, and
// success for the terminal approval. Distinctness is over the whole
// (variant, soft) tuple, as everywhere else here, and the default arm
// keeps an eighth stage from rendering as a blank badge.
func DesignSessionStageStyle(stage string) StatusStyle {
	switch stage {
	case "opened":
		return StatusStyle{htmxui.BadgeGhost, htmxui.BadgeSizeSM, false}
	case "approved":
		return StatusStyle{htmxui.BadgeSuccess, htmxui.BadgeSizeSM, false}
	case "changes_requested":
		return StatusStyle{htmxui.BadgeError, htmxui.BadgeSizeSM, true}
	case "architect_review":
		return StatusStyle{htmxui.BadgeInfo, htmxui.BadgeSizeSM, true}
	case "in_draft":
		return StatusStyle{htmxui.BadgeInfo, htmxui.BadgeSizeSM, false}
	case "answered":
		return StatusStyle{htmxui.BadgeSecondary, htmxui.BadgeSizeSM, false}
	case "ruled":
		return StatusStyle{htmxui.BadgePrimary, htmxui.BadgeSizeSM, false}
	default:
		return StatusStyle{htmxui.BadgeNeutral, htmxui.BadgeSizeSM, false}
	}
}

// DesignSessionStageLabel is the human wording on a stage badge. The
// store's values are snake_cased wire strings and a badge an operator
// reads is not one, so the two live apart here rather than being spelled
// apart at each call site. An unrecognised stage shows the wire value
// itself rather than an empty badge.
func DesignSessionStageLabel(stage string) string {
	switch stage {
	case "opened":
		return "opened"
	case "approved":
		return "approved"
	case "changes_requested":
		return "changes requested"
	case "architect_review":
		return "architect review"
	case "in_draft":
		return "in draft"
	case "answered":
		return "answered"
	case "ruled":
		return "ruled"
	case "":
		return "unknown"
	default:
		return stage
	}
}

// DesignSessionEventTypeStyle maps a revision_event's round kind
// (store's EventType, spelled out as strings so this package keeps no
// //krill/store dependency) onto a daisyUI badge.
//
// It is the only place the five-round vocabulary acquires a colour, so the
// session detail's timeline and any future surface showing the same rounds
// read one history the one way (FR dcecb049: one mapper per vocabulary).
//
// The rounds read as kinds rather than as severities -- a draft is not a
// warning and a ruling is not a success -- so the only severity tone in the
// set is the one that is: reconciliation, the round where an architect has
// found work to do, wears warning-soft. Distinctness is over the whole
// (variant, soft) tuple, and the default arm keeps a sixth round from
// rendering as a blank badge.
func DesignSessionEventTypeStyle(eventType string) StatusStyle {
	switch eventType {
	case "draft":
		return StatusStyle{htmxui.BadgeGhost, htmxui.BadgeSizeSM, false}
	case "reconciliation":
		return StatusStyle{htmxui.BadgeWarning, htmxui.BadgeSizeSM, true}
	case "answer":
		return StatusStyle{htmxui.BadgeInfo, htmxui.BadgeSizeSM, false}
	case "signoff":
		return StatusStyle{htmxui.BadgeSuccess, htmxui.BadgeSizeSM, false}
	case "ruling":
		return StatusStyle{htmxui.BadgePrimary, htmxui.BadgeSizeSM, false}
	default:
		return StatusStyle{htmxui.BadgeNeutral, htmxui.BadgeSizeSM, false}
	}
}

// DesignSessionEventTypeLabel is the human wording on a round badge. Unlike
// the stage vocabulary these wire values are already words an operator reads
// ("draft", "reconciliation", "answer", "signoff", "ruling"), so the mapper
// earns its place as the single owner of the wording rather than as a
// translation: a newly-added round shows its own value, and an absent one
// reads as "unknown" rather than as a blank badge.
func DesignSessionEventTypeLabel(eventType string) string {
	switch eventType {
	case "":
		return "unknown"
	default:
		return eventType
	}
}

// QuestionBlockingStyle maps a question's blocking tag -- "blocking" or
// "non-blocking", the wire form store's bool takes in a question row -- onto
// a daisyUI badge.
//
// It exists as its own mapper because "blocking" is a question's severity,
// not a session's: the sessions table's "N blocking" cell wears the same
// ERROR register (DesignSessionBlockingCountStyle) because both answer the
// one question an operator scans a design session for -- what is it waiting
// on. Non-blocking is ghost, the absence treatment rather than a second
// severity, so a question nobody is held up by never wears a colour that
// says otherwise.
func QuestionBlockingStyle(blocking string) StatusStyle {
	switch blocking {
	case "blocking":
		return StatusStyle{htmxui.BadgeError, htmxui.BadgeSizeSM, true}
	case "non-blocking":
		return StatusStyle{htmxui.BadgeGhost, htmxui.BadgeSizeSM, false}
	default:
		return StatusStyle{htmxui.BadgeNeutral, htmxui.BadgeSizeSM, false}
	}
}

// QuestionBlockingLabel is the human wording on a question's blocking badge.
// The wire values are already the words the rail reads, so -- as with
// DesignSessionEventTypeLabel -- this is the single owner of the wording
// rather than a translation.
func QuestionBlockingLabel(blocking string) string {
	switch blocking {
	case "":
		return "unknown"
	default:
		return blocking
	}
}

// DesignSessionBlockingCountStyle is the badge for a session's open
// BLOCKING question count -- the "N blocking" cell that names a session
// needing an answer before it can move.
//
// Error, unlike MilestoneCountStyle's neutral: a count that asserts a
// state (something is owed a reply) may take a state colour, and this is
// the same error tone the blocking questions of every other surface wear.
// It lives beside the mapper so that cell's colour has exactly one owner
// and a future count badge cannot acquire a second, hand-picked tone.
func DesignSessionBlockingCountStyle() StatusStyle {
	return StatusStyle{htmxui.BadgeError, htmxui.BadgeSizeSM, false}
}

// ShipmentStyle maps an item's shipment state onto a daisyUI badge. It is
// the only place that vocabulary acquires a colour, so every table that
// shows a Shipment column shows it the same way.
//
// "shipped" is deliberately the SAME tuple MilestoneStatusStyle gives the
// milestone status of the same name: an item with a shipment record is the
// item that is done, and a second, near-identical green for the two words
// would let one page disagree with itself about what shipped looks like.
// "unshipped" is ghost -- the absence of a record, not a failure -- the same
// treatment "not started" gets. The unknown-state fallback is neutral, as
// everywhere else here.
func ShipmentStyle(state string) StatusStyle {
	switch state {
	case "shipped":
		return StatusStyle{htmxui.BadgeSuccess, htmxui.BadgeSizeSM, false}
	case "unshipped":
		return StatusStyle{htmxui.BadgeGhost, htmxui.BadgeSizeSM, false}
	default:
		return StatusStyle{htmxui.BadgeNeutral, htmxui.BadgeSizeSM, false}
	}
}

// TaskLaneStyle maps krill's five canonical task lanes (store's
// CanonicalLaneOrder, spelled out as strings so this package keeps no
// //krill/store dependency) onto a daisyUI badge. It is the only place a
// task's lane acquires a colour, so the list, the board and the detail
// all show the same lane the same way.
//
// The lanes read as progress towards Done: ghost for the lane nothing
// has entered, warning for work in flight, and success once the task has
// shipped. An unrecognised lane falls through to a neutral badge rather
// than failing to compile when a sixth lane is added.
func TaskLaneStyle(lane string) StatusStyle {
	switch lane {
	case "Scaffold":
		return StatusStyle{htmxui.BadgeGhost, htmxui.BadgeSizeSM, false}
	case "Implementation":
		return StatusStyle{htmxui.BadgePrimary, htmxui.BadgeSizeSM, true}
	case "Testing":
		return StatusStyle{htmxui.BadgeInfo, htmxui.BadgeSizeSM, false}
	case "Validation":
		return StatusStyle{htmxui.BadgeWarning, htmxui.BadgeSizeSM, false}
	case "Done":
		return StatusStyle{htmxui.BadgeSuccess, htmxui.BadgeSizeSM, false}
	default:
		return StatusStyle{htmxui.BadgeNeutral, htmxui.BadgeSizeSM, false}
	}
}

// TaskStateStyle maps one derived task state -- the keys
// taskStateBadges hands it -- onto a daisyUI badge. It is the only place
// a task's state acquires a colour, so a claimed task looks the same on
// the list, the board and the detail.
//
// The colours come from manmanv2/ui/DESIGN_SYSTEM.md via htmxui §6:
// info for a live claim, warning for a claim that needs reclaiming or a
// task that has run out of attempts, error for a task that needs a
// human, and ghost for one nobody has claimed. As with
// MilestoneStatusStyle, distinctness is over the whole (variant, soft)
// tuple -- error and warning each carry two states that share a hue.
func TaskStateStyle(state string) StatusStyle {
	switch state {
	case "escalated":
		return StatusStyle{htmxui.BadgeError, htmxui.BadgeSizeSM, false}
	case "cancelled":
		return StatusStyle{htmxui.BadgeError, htmxui.BadgeSizeSM, true}
	case "lease-expired":
		return StatusStyle{htmxui.BadgeWarning, htmxui.BadgeSizeSM, false}
	case "capped":
		return StatusStyle{htmxui.BadgeWarning, htmxui.BadgeSizeSM, true}
	case "claimed":
		return StatusStyle{htmxui.BadgeInfo, htmxui.BadgeSizeSM, false}
	case "ready":
		return StatusStyle{htmxui.BadgeGhost, htmxui.BadgeSizeSM, false}
	default:
		return StatusStyle{htmxui.BadgeNeutral, htmxui.BadgeSizeSM, false}
	}
}

// NoteKindStyle maps a note's kind -- what a note IS (store.NoteKind,
// spelled out as strings so this package keeps no //krill/store
// dependency) -- onto a daisyUI badge.
//
// A kind is a category rather than a state, so these colours separate the
// three categories and carry no severity: a scope note is a discovery
// someone surfaced, a comment is the general-purpose case, and a
// cheap/expensive-later note is a statement about a capability. The
// unknown-kind fallback is neutral, the same as everywhere else here.
func NoteKindStyle(kind string) StatusStyle {
	switch kind {
	case "scope-note":
		return StatusStyle{htmxui.BadgeInfo, htmxui.BadgeSizeSM, false}
	case "comment":
		return StatusStyle{htmxui.BadgeNeutral, htmxui.BadgeSizeSM, false}
	case "cheap-expensive-later":
		return StatusStyle{htmxui.BadgeSecondary, htmxui.BadgeSizeSM, false}
	default:
		return StatusStyle{htmxui.BadgeNeutral, htmxui.BadgeSizeSM, false}
	}
}

// NoteLifecycleStyle maps a note's lifecycle status -- where it sits in
// the noted -> carried-over / deferred / closed progression (store's
// NoteLifecycleStatus) -- onto a daisyUI badge.
//
// This vocabulary does carry severity, unlike the kind's: "noted" is a
// note still waiting on a decision, which is the state an operator scans
// for, so it is warning; "carried-over" moved to another unit of work and
// is live somewhere else, which is info; "deferred" was deliberately put
// aside, which is ghost; and "closed" is finished, so success. As with
// the other mappers, the default arm keeps a fifth status from rendering
// as a blank badge.
func NoteLifecycleStyle(status string) StatusStyle {
	switch status {
	case "noted":
		return StatusStyle{htmxui.BadgeWarning, htmxui.BadgeSizeSM, false}
	case "carried-over":
		return StatusStyle{htmxui.BadgeInfo, htmxui.BadgeSizeSM, false}
	case "deferred":
		return StatusStyle{htmxui.BadgeGhost, htmxui.BadgeSizeSM, false}
	case "closed":
		return StatusStyle{htmxui.BadgeSuccess, htmxui.BadgeSizeSM, false}
	default:
		return StatusStyle{htmxui.BadgeNeutral, htmxui.BadgeSizeSM, false}
	}
}

// NonGoalKindStyle maps a non-goal's kind -- the store's
// NonGoalKind, spelled out as strings so this package keeps no
// //krill/store dependency -- onto a daisyUI badge. It is the only place
// that vocabulary acquires a colour, so the non-goals page and anything
// that later shows a non-goal inline render a kind the same way.
//
// Neither kind is a failure and neither is in flight, so neither takes a
// severity colour: warning would put a spec-page badge into the same
// visual register as a task needing a human, which the Spec page's
// read-only, no-attention invariant (FR df5bffd1) rules out of its body.
// Instead the pair separates SETTLED from STILL OPEN. Permanent is
// secondary (slate -- DESIGN_SYSTEM's "Inactive, Stopped, Secondary"),
// because it is a closed boundary the product will not cross. Deferred is
// info (indigo -- "Info, Deployed, Primary"), because it is explicitly
// not foreclosed: it is a live statement about a door left ajar, and
// ghost -- the absence treatment, and what a note's "deferred" lifecycle
// takes -- would read as "nothing here" rather than "not yet".
//
// It takes a string for the reason MilestoneStatusStyle does: an
// unrecognised kind falls through to the neutral fallback rather than
// failing to compile when a third kind is added.
func NonGoalKindStyle(kind string) StatusStyle {
	switch kind {
	case "permanent":
		return StatusStyle{htmxui.BadgeSecondary, htmxui.BadgeSizeSM, false}
	case "deferred":
		return StatusStyle{htmxui.BadgeInfo, htmxui.BadgeSizeSM, false}
	default:
		return StatusStyle{htmxui.BadgeNeutral, htmxui.BadgeSizeSM, false}
	}
}

// NonGoalKindLabel is the human wording on a non-goal's kind badge. The
// store's values are lowercase wire strings; a badge an operator reads is
// not one, so the two live apart here rather than being spelled apart at
// each call site. An unrecognised kind shows the wire value itself rather
// than an empty badge.
func NonGoalKindLabel(kind string) string {
	switch kind {
	case "permanent":
		return "Permanent"
	case "deferred":
		return "Deferred"
	default:
		return kind
	}
}

// EscalationReasonLabel is the human wording for one escalation reason.
// The store's values are hyphenated wire strings; a badge an operator
// reads is not one, so the two live apart here rather than being spelled
// apart at each call site.
func EscalationReasonLabel(reason string) string {
	switch reason {
	case "thrash-cap":
		return "thrash cap"
	case "attempt-cap":
		return "attempt cap"
	case "manual":
		return "manual"
	default:
		return reason
	}
}

// EscalationReasonStyle maps an escalation reason onto a daisyUI badge,
// the sibling of MilestoneStatusStyle for the reason vocabulary rather
// than the status one.
//
// An escalation is something that stopped working on its own, so the two
// automatic counter-driven reasons are error-soft; a manual escalation is
// a person having decided the task needs a human, which is attention
// rather than failure. The default arm is what keeps a fourth reason from
// rendering as a blank badge.
func EscalationReasonStyle(reason string) StatusStyle {
	switch reason {
	case "thrash-cap", "attempt-cap":
		return StatusStyle{htmxui.BadgeError, htmxui.BadgeSizeSM, true}
	case "manual":
		return StatusStyle{htmxui.BadgeWarning, htmxui.BadgeSizeSM, true}
	default:
		return StatusStyle{htmxui.BadgeNeutral, htmxui.BadgeSizeSM, false}
	}
}
