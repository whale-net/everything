// Package components holds krill's app-owned UI chrome: the shell wrapper and
// the mappers from krill's status vocabularies onto htmxui primitives. Domain
// vocabulary lives here so it stays out of the shared library.
package components

import "github.com/whale-net/everything/libs/go/htmxui"

// StatusStyle is the full daisyUI presentation for one status value. The
// (variant, soft) tuple, not the variant alone, keeps statuses distinct since
// the palette has fewer hues than states.
type StatusStyle struct {
	Variant htmxui.BadgeVariant
	Size    htmxui.BadgeSize
	Soft    bool
}

// MilestoneStatusStyle maps a milestone status onto a badge. It takes a
// string to avoid a krill/store dependency; unknown values get a neutral badge.
// Warning is in-flight, success shipped, error given-up, ghost not started.
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

// MilestoneCountStyle is the neutral badge for an "N milestones" cell: a
// count asserts no state.
func MilestoneCountStyle() StatusStyle {
	return StatusStyle{htmxui.BadgeNeutral, htmxui.BadgeSizeSM, false}
}

// DesignSessionStageStyle maps a design session's Stage onto a badge, read
// as progress toward approval; unknown stages fall back to neutral.
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

// DesignSessionStageLabel is the human wording on a stage badge; an
// unrecognised stage shows its wire value.
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

// DesignSessionEventTypeStyle maps a revision round kind onto a badge. Rounds
// are kinds, not severities; only reconciliation (work found) is warning.
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

// DesignSessionEventTypeLabel is the human wording on a round badge; unknown
// rounds show their value, empty ones read "unknown".
func DesignSessionEventTypeLabel(eventType string) string {
	switch eventType {
	case "":
		return "unknown"
	default:
		return eventType
	}
}

// QuestionBlockingStyle maps "blocking"/"non-blocking" onto a badge: blocking
// is error (matching DesignSessionBlockingCountStyle), non-blocking ghost.
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
func QuestionBlockingLabel(blocking string) string {
	switch blocking {
	case "":
		return "unknown"
	default:
		return blocking
	}
}

// DesignSessionBlockingCountStyle is the error badge for a session's "N
// blocking" cell: unlike a milestone count, it asserts a reply is owed.
func DesignSessionBlockingCountStyle() StatusStyle {
	return StatusStyle{htmxui.BadgeError, htmxui.BadgeSizeSM, false}
}

// ShipmentStyle maps a shipment state onto a badge. "shipped" matches
// MilestoneStatusStyle's shipped tuple so one page has one green; "unshipped"
// is ghost, an absence rather than a failure.
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

// TaskLaneStyle maps the five canonical task lanes onto a badge: ghost for
// not entered, warning in flight, success once shipped; unknown is neutral.
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

// TaskStateStyle maps a derived task state onto a badge: info for a live
// claim, warning for reclaim or out of attempts, error for needs a human,
// ghost for unclaimed.
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

// NoteKindStyle maps a note kind onto a badge. Kinds are categories, so the
// colours carry no severity.
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

// NoteLifecycleStyle maps a note's lifecycle status onto a badge: noted
// (awaiting decision) is warning, carried-over info, deferred ghost, closed
// success.
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

// NonGoalKindStyle maps a non-goal kind onto a badge without severity colours,
// since the Spec page must not signal attention. Permanent is secondary
// (settled); deferred is info (open, not absent).
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

// NonGoalKindLabel is the human wording on a non-goal kind badge; an
// unrecognised kind shows its wire value.
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

// EscalationReasonLabel is the human wording for an escalation reason.
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

// EscalationReasonStyle maps an escalation reason onto a badge: automatic
// counter-driven reasons are error-soft, a manual escalation is attention.
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
