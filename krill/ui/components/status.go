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
