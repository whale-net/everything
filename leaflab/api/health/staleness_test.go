package health

import (
	"testing"
	"time"
)

// TestThreshold_FloorAppliesBelowMultiplierResult proves A23's floor: a
// board with a 1-minute configured poll interval goes stale after
// StalenessFloor (15 minutes), not 3 * 1 minute = 3 minutes -- the case
// this task's Testing section calls out explicitly ("floored at 15
// minutes ... wrong at both ends of the configured range").
func TestThreshold_FloorAppliesBelowMultiplierResult(t *testing.T) {
	got := Threshold(1 * time.Minute)
	if got != StalenessFloor {
		t.Errorf("Threshold(1m) = %v, want the 15-minute floor (StalenessFloor), not 3 * 1m", got)
	}
}

// TestThreshold_MultiplierAppliesAboveFloor proves the 3x multiplier is
// used once it exceeds the floor: a 10-minute configured poll interval
// goes stale after 30 minutes, not 15 -- the floor must never substitute
// for the multiplier once 3x already clears it.
func TestThreshold_MultiplierAppliesAboveFloor(t *testing.T) {
	got := Threshold(10 * time.Minute)
	want := 30 * time.Minute
	if got != want {
		t.Errorf("Threshold(10m) = %v, want %v (3x, not the 15-minute floor)", got, want)
	}
}

// TestThreshold_ExactlyAtFloorBoundary proves the boundary itself: a
// 5-minute configured interval multiplies to exactly 15 minutes, so the
// floor and the multiplier agree -- this must not be mistaken for the
// floor "winning" by an off-by-one comparison.
func TestThreshold_ExactlyAtFloorBoundary(t *testing.T) {
	got := Threshold(5 * time.Minute)
	want := 15 * time.Minute
	if got != want {
		t.Errorf("Threshold(5m) = %v, want %v", got, want)
	}
}

// TestThreshold_ZeroInterval_UsesFloor proves an unconfigured (0) longest
// poll interval -- e.g. a board with no accepted config -- still floors to
// StalenessFloor rather than treating a board as instantly stale.
func TestThreshold_ZeroInterval_UsesFloor(t *testing.T) {
	got := Threshold(0)
	if got != StalenessFloor {
		t.Errorf("Threshold(0) = %v, want the 15-minute floor", got)
	}
}

// TestEffectivePollInterval_ZeroMapsToDeviceDefault proves the 0 sentinel
// ("use device default", per SensorConfig.poll_interval_ms's proto doc
// comment) maps to DefaultPollInterval, not to a literal 0-duration
// interval that would floor every such sensor's board to the minimum.
func TestEffectivePollInterval_ZeroMapsToDeviceDefault(t *testing.T) {
	if got := EffectivePollInterval(0); got != DefaultPollInterval {
		t.Errorf("EffectivePollInterval(0) = %v, want DefaultPollInterval %v", got, DefaultPollInterval)
	}
}

// TestEffectivePollInterval_NonZeroPassesThrough proves a real configured
// value is used as-is, not remapped.
func TestEffectivePollInterval_NonZeroPassesThrough(t *testing.T) {
	const pollMs = 45000
	want := 45 * time.Second
	if got := EffectivePollInterval(pollMs); got != want {
		t.Errorf("EffectivePollInterval(%d) = %v, want %v", pollMs, got, want)
	}
}

// TestIsStale_OneMinuteInterval_GoesStaleAtFloorNotMultiplier is this
// task's exact Testing-section fixture: a board with a 1-minute configured
// poll interval must not yet be stale at 10 minutes (below the 15-minute
// floor) but must be stale at 16 minutes.
func TestIsStale_OneMinuteInterval_GoesStaleAtFloorNotMultiplier(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	interval := 1 * time.Minute

	notYetStale := now.Add(-10 * time.Minute) // > 3x1m=3m, still < the 15m floor
	if IsStale(notYetStale, now, interval) {
		t.Error("board with a 1-minute configured interval reported stale at 10 minutes -- want the 15-minute floor to still apply")
	}

	stale := now.Add(-16 * time.Minute)
	if !IsStale(stale, now, interval) {
		t.Error("board with a 1-minute configured interval not reported stale at 16 minutes (past the 15-minute floor)")
	}
}

// TestIsStale_TenMinuteInterval_GoesStaleAtMultiplierNotFifteen is this
// task's second exact Testing-section fixture: a board with a 10-minute
// configured poll interval must not yet be stale at 20 minutes (below 3x =
// 30 minutes) even though 20 minutes is past the flat 15-minute floor, and
// must be stale at 31 minutes.
func TestIsStale_TenMinuteInterval_GoesStaleAtMultiplierNotFifteen(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	interval := 10 * time.Minute

	notYetStale := now.Add(-20 * time.Minute) // past the 15m floor, still < 3x10m=30m
	if IsStale(notYetStale, now, interval) {
		t.Error("board with a 10-minute configured interval reported stale at 20 minutes -- want the 3x multiplier (30 minutes), not the flat 15-minute floor")
	}

	stale := now.Add(-31 * time.Minute)
	if !IsStale(stale, now, interval) {
		t.Error("board with a 10-minute configured interval not reported stale at 31 minutes (past 3x = 30 minutes)")
	}
}
