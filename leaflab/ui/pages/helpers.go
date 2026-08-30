package pages

import (
	"fmt"
	"time"

	leaflabapipb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/libs/go/htmxui"
)

// boardStateLabel and boardStateVariant translate the API's ReportingState
// enum into the label/colour the boards list shows (FR5) -- exactly these
// three concrete states are ever rendered as text. Both switches fall
// through to the "never reported" case on their default arm, so
// REPORTING_STATE_UNSPECIFIED (which api.proto documents as something that
// "should never arrive") gets the same neutral, non-crashing treatment as
// a genuinely never-reported board instead of a fourth visual state, a
// blank label, or a panic.
func boardStateLabel(state leaflabapipb.ReportingState) string {
	switch state {
	case leaflabapipb.ReportingState_REPORTING_STATE_REPORTING:
		return "Reporting"
	case leaflabapipb.ReportingState_REPORTING_STATE_STALE:
		return "Stale"
	default:
		return "Never reported"
	}
}

func boardStateVariant(state leaflabapipb.ReportingState) htmxui.BadgeVariant {
	switch state {
	case leaflabapipb.ReportingState_REPORTING_STATE_REPORTING:
		return htmxui.BadgeSuccess
	case leaflabapipb.ReportingState_REPORTING_STATE_STALE:
		return htmxui.BadgeWarning
	default:
		return htmxui.BadgeNeutral
	}
}

// formatReadingTime renders a sensor's latest reading timestamp (FR6) as an
// absolute UTC RFC3339 string -- unlike lastReadingAge's relative "N ago"
// caption for the boards list, the board detail screen shows the exact
// timestamp of the reading itself. Mirrors
// tools/app_registry/ui/pages/dashboard.templ's
// `time.Unix(...).UTC().Format(time.RFC3339)` convention.
func formatReadingTime(ts time.Time) string {
	return ts.UTC().Format(time.RFC3339)
}

// lastReadingAge renders FR5's "last reading 42 minutes ago" caption for a
// stale board, computed straight off the API's last_reading_at timestamp --
// no independently-derived threshold or state here, per FR5's "the UI does
// not recompute the state" rule; this only formats an age the API already
// implies is stale by returning REPORTING_STATE_STALE alongside it. Callers
// (boards.templ) only invoke this for the stale case -- last_reading_at is
// unset for REPORTING_STATE_NEVER_REPORTED (api.proto), so ts == nil is a
// defensive fallback, not an expected path.
func lastReadingAge(ts time.Time) string {
	return fmt.Sprintf("last reading %s ago", roughDuration(time.Since(ts)))
}

// roughDuration renders a coarse, human-scale age ("42 minutes", "3
// hours", "2 days") -- deliberately not to the second, since the exact
// duration since the last reading is not itself meaningful past the unit
// a person would say out loud.
func roughDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "less than a minute"
	case d < time.Hour:
		minutes := int(d.Minutes())
		if minutes == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", minutes)
	case d < 24*time.Hour:
		hours := int(d.Hours())
		if hours == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", hours)
	default:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1 day"
		}
		return fmt.Sprintf("%d days", days)
	}
}
