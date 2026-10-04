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
// It takes a string for the reason MilestoneStatusStyle does: an
// unrecognised kind falls through to the neutral fallback rather than
// failing to compile when a third kind is added.
func NonGoalKindStyle(kind string) StatusStyle {
	switch kind {
	case "permanent":
		return StatusStyle{htmxui.BadgeNeutral, htmxui.BadgeSizeSM, false}
	case "deferred":
		return StatusStyle{htmxui.BadgeNeutral, htmxui.BadgeSizeSM, false}
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
