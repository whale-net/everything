// Unit coverage for FR71's tier-selection decision (Select, tiers.go). No
// database is needed here -- Select is pure over (requested, windowStart,
// windowEnd, time.Now()) -- see tiers_migration_integration_test.go
// (leaflab/migrate) for the real-TimescaleDB coverage of migration 022's
// aggregates and policy ordering these constants mirror.
package tiers

import (
	"errors"
	"testing"
	"time"
)

// windowAgo builds a [windowStart, windowEnd] pair ending now, with
// windowStart set age in the past. Select reads windowStart relative to its
// own time.Now() call, so age is computed against a wall-clock time.Now()
// taken as close to the Select call as the test structure allows -- every
// case below uses an age well clear of the tier boundaries it's testing
// (hours or days of margin), so ordinary test-execution jitter cannot flip
// the result.
func windowAgo(age time.Duration) (start, end time.Time) {
	end = time.Now()
	start = end.Add(-age)
	return start, end
}

// TestSelect_NeverReturnsFinerThanRequested is FR71/tierOrder's "only ever
// coarsens" invariant: even when a finer tier could serve the window (e.g. a
// 1-hour-old window, well within raw's cap), Select must not return
// something finer than what was requested.
func TestSelect_NeverReturnsFinerThanRequested(t *testing.T) {
	start, end := windowAgo(1 * time.Hour)

	for _, requested := range []Tier{TierRaw, TierFiveMinute, TierHourly} {
		t.Run(string(requested), func(t *testing.T) {
			got, err := Select(requested, start, end)
			if err != nil {
				t.Fatalf("Select(%s, ...): unexpected error: %v", requested, err)
			}
			if indexOf(got.Tier) < indexOf(requested) {
				t.Errorf("Select(%s, 1h-old window) = %s, want a tier no finer than %s", requested, got.Tier, requested)
			}
		})
	}
}

// TestSelect_WithinRawBound_NoCoarsening covers the Testing section's "a
// window within raw's retention bound gets raw (no coarsening)": a 1-hour
// window, far inside both the 48-hour NFR3.2 cap and the 13-month retention
// floor, requested at TierRaw, must come back as raw with Coarsened=false.
func TestSelect_WithinRawBound_NoCoarsening(t *testing.T) {
	start, end := windowAgo(1 * time.Hour)

	got, err := Select(TierRaw, start, end)
	if err != nil {
		t.Fatalf("Select: unexpected error: %v", err)
	}
	if got.Tier != TierRaw {
		t.Errorf("Tier = %s, want %s", got.Tier, TierRaw)
	}
	if got.Coarsened {
		t.Error("Coarsened = true, want false: the window is well within raw's bound")
	}
}

// TestSelect_ExceedsRawCap_CoarsensToFiveMinute covers "a window exceeding
// raw's bound but within 5m's gets 5m (Coarsened=true)": a 7-day-old window
// exceeds NFR3.2's 48-hour raw cap but is far inside the 5-minute tier's
// 90-day retention. This is also the Testing section's explicit acceptance
// case: "A request for raw over a 7-day window coarsens and says so; it
// does not error and does not silently return raw."
func TestSelect_ExceedsRawCap_CoarsensToFiveMinute(t *testing.T) {
	start, end := windowAgo(7 * 24 * time.Hour)

	got, err := Select(TierRaw, start, end)
	if err != nil {
		t.Fatalf("Select: unexpected error (a coarsenable window must not error): %v", err)
	}
	if got.Tier != TierFiveMinute {
		t.Errorf("Tier = %s, want %s", got.Tier, TierFiveMinute)
	}
	if !got.Coarsened {
		t.Error("Coarsened = false, want true: FR71 requires disclosing every coarsening, never silently returning raw")
	}
}

// TestSelect_ExceedsAllBounds_FallsThroughToHourly covers "a window
// exceeding all bounds falls through to hourly": a 14-month-old window
// exceeds both raw's 13-month retention and the 5-minute tier's 90-day
// retention, so Select must walk all the way to hourly (indefinite
// retention, migration 022 adds no retention policy for it) regardless of
// which tier was originally requested.
func TestSelect_ExceedsAllBounds_FallsThroughToHourly(t *testing.T) {
	start, end := windowAgo(14 * 30 * 24 * time.Hour) // ~14 months

	for _, requested := range []Tier{TierRaw, TierFiveMinute, TierHourly} {
		t.Run(string(requested), func(t *testing.T) {
			got, err := Select(requested, start, end)
			if err != nil {
				t.Fatalf("Select(%s, ...): unexpected error: %v", requested, err)
			}
			if got.Tier != TierHourly {
				t.Errorf("Tier = %s, want %s (14-month window exceeds every finer tier's retention)", got.Tier, TierHourly)
			}
			wantCoarsened := requested != TierHourly
			if got.Coarsened != wantCoarsened {
				t.Errorf("Coarsened = %v, want %v", got.Coarsened, wantCoarsened)
			}
		})
	}
}

// TestSelect_ResponseAlwaysNamesTheTier is FR71's disclosure requirement
// made explicit: every successful Select call, coarsened or not, returns a
// non-empty, recognized Tier.
func TestSelect_ResponseAlwaysNamesTheTier(t *testing.T) {
	cases := []struct {
		name string
		age  time.Duration
	}{
		{"1-hour window", 1 * time.Hour},
		{"7-day window", 7 * 24 * time.Hour},
		{"14-month window", 14 * 30 * 24 * time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start, end := windowAgo(tc.age)
			got, err := Select(TierRaw, start, end)
			if err != nil {
				t.Fatalf("Select: unexpected error: %v", err)
			}
			if indexOf(got.Tier) == -1 {
				t.Errorf("Tier = %q, want one of the three recognized tiers", got.Tier)
			}
		})
	}
}

// TestSelect_ErrUnknownTier covers Select's genuine-input-error path for an
// unrecognized requested tier.
func TestSelect_ErrUnknownTier(t *testing.T) {
	start, end := windowAgo(1 * time.Hour)

	_, err := Select(Tier("daily"), start, end)
	if !errors.Is(err, ErrUnknownTier) {
		t.Errorf("err = %v, want ErrUnknownTier", err)
	}
}

// TestSelect_ErrInvalidWindow covers Select's genuine-input-error path for a
// window whose end precedes its start.
func TestSelect_ErrInvalidWindow(t *testing.T) {
	end := time.Now()
	start := end.Add(1 * time.Hour) // after end -- invalid

	_, err := Select(TierRaw, start, end)
	if !errors.Is(err, ErrInvalidWindow) {
		t.Errorf("err = %v, want ErrInvalidWindow", err)
	}
}

// TestSelect_EqualWindowBoundsIsValid guards against ErrInvalidWindow being
// too strict: windowStart == windowEnd (a zero-width window) is not an
// inverted window and must not error.
func TestSelect_EqualWindowBoundsIsValid(t *testing.T) {
	now := time.Now()
	if _, err := Select(TierRaw, now, now); err != nil {
		t.Errorf("Select with windowStart == windowEnd: unexpected error: %v", err)
	}
}
