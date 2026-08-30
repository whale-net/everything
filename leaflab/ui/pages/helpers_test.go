package pages

import (
	"testing"
	"time"

	leaflabapipb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/libs/go/htmxui"
)

// TestBoardStateLabel_ThreeStatesPlusUnspecifiedFallback covers FR5's
// "exactly these three concrete states" rule plus the defensive
// UNSPECIFIED fallback: REPORTING_STATE_UNSPECIFIED (which api.proto
// documents as something that "should never arrive") must land on the same
// label as a genuine never-reported board, not a fourth string or an empty
// one.
func TestBoardStateLabel_ThreeStatesPlusUnspecifiedFallback(t *testing.T) {
	cases := []struct {
		name  string
		state leaflabapipb.ReportingState
		want  string
	}{
		{"reporting", leaflabapipb.ReportingState_REPORTING_STATE_REPORTING, "Reporting"},
		{"stale", leaflabapipb.ReportingState_REPORTING_STATE_STALE, "Stale"},
		{"never reported", leaflabapipb.ReportingState_REPORTING_STATE_NEVER_REPORTED, "Never reported"},
		{"unspecified falls through to never reported", leaflabapipb.ReportingState_REPORTING_STATE_UNSPECIFIED, "Never reported"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := boardStateLabel(tc.state); got != tc.want {
				t.Errorf("boardStateLabel(%v) = %q, want %q", tc.state, got, tc.want)
			}
		})
	}
}

// TestBoardStateVariant_ThreeStatesPlusUnspecifiedFallback mirrors the
// label test above for the badge colour mapping -- a regression that
// remapped e.g. stale onto success/error would be a silent operator-facing
// defect with no compile-time signal.
func TestBoardStateVariant_ThreeStatesPlusUnspecifiedFallback(t *testing.T) {
	cases := []struct {
		name  string
		state leaflabapipb.ReportingState
		want  htmxui.BadgeVariant
	}{
		{"reporting -> success", leaflabapipb.ReportingState_REPORTING_STATE_REPORTING, htmxui.BadgeSuccess},
		{"stale -> warning", leaflabapipb.ReportingState_REPORTING_STATE_STALE, htmxui.BadgeWarning},
		{"never reported -> neutral", leaflabapipb.ReportingState_REPORTING_STATE_NEVER_REPORTED, htmxui.BadgeNeutral},
		{"unspecified -> neutral (defensive fallback, not a 4th colour)", leaflabapipb.ReportingState_REPORTING_STATE_UNSPECIFIED, htmxui.BadgeNeutral},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := boardStateVariant(tc.state); got != tc.want {
				t.Errorf("boardStateVariant(%v) = %q, want %q", tc.state, got, tc.want)
			}
		})
	}
}

// TestLastReadingAge_FormatsCaptionAroundRoughDuration guards the exact
// "last reading N ago" wording FR5 specifies, independent of
// TestRoughDuration's own coverage of the duration formatting itself.
func TestLastReadingAge_FormatsCaptionAroundRoughDuration(t *testing.T) {
	ts := time.Now().Add(-42 * time.Minute)
	got := lastReadingAge(ts)
	want := "last reading 42 minutes ago"
	if got != want {
		t.Errorf("lastReadingAge(now-42m) = %q, want %q", got, want)
	}
}

// TestRoughDuration_CoarseHumanScaleBuckets covers roughDuration's unit
// boundaries (minute/hour/day) and singular/plural wording, plus the
// defensive clamp on a negative duration (a clock skew edge case, not an
// expected path).
func TestRoughDuration_CoarseHumanScaleBuckets(t *testing.T) {
	cases := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"negative clamps to zero", -5 * time.Second, "less than a minute"},
		{"zero", 0, "less than a minute"},
		{"under a minute", 30 * time.Second, "less than a minute"},
		{"exactly one minute singular", 1 * time.Minute, "1 minute"},
		{"several minutes plural", 42 * time.Minute, "42 minutes"},
		{"just under an hour", 59 * time.Minute, "59 minutes"},
		{"exactly one hour singular", 1 * time.Hour, "1 hour"},
		{"several hours plural", 3 * time.Hour, "3 hours"},
		{"just under a day", 23 * time.Hour, "23 hours"},
		{"exactly one day singular", 24 * time.Hour, "1 day"},
		{"several days plural", 48 * time.Hour, "2 days"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := roughDuration(tc.d); got != tc.want {
				t.Errorf("roughDuration(%v) = %q, want %q", tc.d, got, tc.want)
			}
		})
	}
}
