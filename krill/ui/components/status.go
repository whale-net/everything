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
