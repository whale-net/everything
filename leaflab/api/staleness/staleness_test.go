package staleness

import (
	"testing"
	"time"
)

func TestThreshold_FloorsAt15Minutes(t *testing.T) {
	// A23: a board with a 1-minute poll interval floors at 15 minutes
	// (3 * 1 minute = 3 minutes, below the 15-minute floor).
	c := NewConfig()
	got := c.Threshold(1 * time.Minute)
	if got != 15*time.Minute {
		t.Fatalf("Threshold(1m) = %v, want 15m", got)
	}
}

func TestThreshold_ThreeTimesLongestPollInterval(t *testing.T) {
	// A23: a board with a 30-minute poll interval is not-reporting at 90
	// minutes (3 * 30 minutes), which is above the 15-minute floor.
	c := NewConfig()
	got := c.Threshold(30 * time.Minute)
	if got != 90*time.Minute {
		t.Fatalf("Threshold(30m) = %v, want 90m", got)
	}
}

func TestIsStale_BoundaryBehavior(t *testing.T) {
	c := NewConfig()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	// 30-minute poll interval: not stale at 89 minutes, stale at 91 minutes.
	notStale := now.Add(-89 * time.Minute)
	if c.IsStale(now, notStale, 30*time.Minute) {
		t.Errorf("IsStale at 89m with 30m interval = true, want false")
	}
	stale := now.Add(-91 * time.Minute)
	if !c.IsStale(now, stale, 30*time.Minute) {
		t.Errorf("IsStale at 91m with 30m interval = false, want true")
	}

	// 1-minute poll interval: not stale at 14 minutes, stale at 16 minutes
	// (floored at 15 minutes, not 3 minutes).
	notStaleFloored := now.Add(-14 * time.Minute)
	if c.IsStale(now, notStaleFloored, 1*time.Minute) {
		t.Errorf("IsStale at 14m with 1m interval = true, want false (floor)")
	}
	staleFloored := now.Add(-16 * time.Minute)
	if !c.IsStale(now, staleFloored, 1*time.Minute) {
		t.Errorf("IsStale at 16m with 1m interval = false, want true (floor)")
	}
}

func TestConfig_ZeroValueDefaultsAreApplied(t *testing.T) {
	var c Config // zero value: Multiplier=0, Floor=0
	got := c.Threshold(30 * time.Minute)
	want := NewConfig().Threshold(30 * time.Minute)
	if got != want {
		t.Fatalf("zero-value Config.Threshold(30m) = %v, want %v (A23 defaults)", got, want)
	}
}

func TestConfig_CustomMultiplierAndFloor(t *testing.T) {
	c := Config{Multiplier: 2, Floor: 5 * time.Minute}
	if got := c.Threshold(10 * time.Minute); got != 20*time.Minute {
		t.Fatalf("Threshold(10m) with multiplier=2 = %v, want 20m", got)
	}
	if got := c.Threshold(1 * time.Minute); got != 5*time.Minute {
		t.Fatalf("Threshold(1m) with floor=5m = %v, want 5m", got)
	}
}
