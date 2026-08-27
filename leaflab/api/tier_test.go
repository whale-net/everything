package main

import (
	"strings"
	"testing"
	"time"

	pb "github.com/whale-net/everything/leaflab/api/proto"
)

// TestResolveTier_RawWithinCapStaysRaw verifies that a raw request for a
// short, recent window is honored as raw (FR71: no coarsening when the
// window fits within the 48-hour raw serving cap).
func TestResolveTier_RawWithinCapStaysRaw(t *testing.T) {
	now := time.Now()
	windowStart := now.Add(-1 * time.Hour)
	windowEnd := now

	res, err := ResolveTier(pb.GranularityTier_GRANULARITY_TIER_RAW, windowStart, windowEnd, now)
	if err != nil {
		t.Fatalf("ResolveTier: %v", err)
	}
	if res.Tier != pb.GranularityTier_GRANULARITY_TIER_RAW {
		t.Errorf("expected raw tier for a 1-hour recent window, got %v (reason: %s)", res.Tier, res.Reason)
	}
	if res.Reason == "" {
		t.Error("FR71: reason must be populated even when the requested tier was honored")
	}
}

// TestResolveTier_WindowLongerThan48HoursNeverReturnsRaw verifies the
// acceptance criterion directly: "A window longer than 48 hours never
// returns raw," independent of how recent the window is.
func TestResolveTier_WindowLongerThan48HoursNeverReturnsRaw(t *testing.T) {
	now := time.Now()
	windowStart := now.Add(-72 * time.Hour) // 72-hour window, recent (ends now)
	windowEnd := now

	res, err := ResolveTier(pb.GranularityTier_GRANULARITY_TIER_RAW, windowStart, windowEnd, now)
	if err != nil {
		t.Fatalf("ResolveTier: %v", err)
	}
	if res.Tier == pb.GranularityTier_GRANULARITY_TIER_RAW {
		t.Errorf("NFR3.2: a 72-hour window must never resolve to raw, got raw (reason: %s)", res.Reason)
	}
	if res.Tier != pb.GranularityTier_GRANULARITY_TIER_5_MINUTE {
		t.Errorf("expected coarsening to 5-minute for a 72-hour window within 90-day retention, got %v", res.Tier)
	}
	if !strings.Contains(res.Reason, "48-hour") {
		t.Errorf("expected reason to disclose the 48-hour raw cap as the coarsening trigger, got %q", res.Reason)
	}
}

// TestResolveTier_RawExactlyAt48HourBoundaryStaysRaw verifies the boundary is
// inclusive: a window of exactly 48 hours is still servable as raw.
func TestResolveTier_RawExactlyAt48HourBoundaryStaysRaw(t *testing.T) {
	now := time.Now()
	windowStart := now.Add(-RawServingWindowCap)
	windowEnd := now

	res, err := ResolveTier(pb.GranularityTier_GRANULARITY_TIER_RAW, windowStart, windowEnd, now)
	if err != nil {
		t.Fatalf("ResolveTier: %v", err)
	}
	if res.Tier != pb.GranularityTier_GRANULARITY_TIER_RAW {
		t.Errorf("expected raw at exactly the 48-hour cap (inclusive boundary), got %v", res.Tier)
	}
}

// TestResolveTier_JustOver48HourBoundaryCoarsens verifies the boundary is
// strict: one duration-unit over 48 hours must coarsen.
func TestResolveTier_JustOver48HourBoundaryCoarsens(t *testing.T) {
	now := time.Now()
	windowStart := now.Add(-RawServingWindowCap - time.Second)
	windowEnd := now

	res, err := ResolveTier(pb.GranularityTier_GRANULARITY_TIER_RAW, windowStart, windowEnd, now)
	if err != nil {
		t.Fatalf("ResolveTier: %v", err)
	}
	if res.Tier == pb.GranularityTier_GRANULARITY_TIER_RAW {
		t.Errorf("expected coarsening for a window one second over the 48-hour cap, got raw")
	}
}

// TestResolveTier_WindowOlderThan90DaysNeverResolvesTo5Minute verifies the
// acceptance criterion directly: "A window older than 90 days never resolves
// to the 5-minute tier," even when 5-minute is explicitly requested.
func TestResolveTier_WindowOlderThan90DaysNeverResolvesTo5Minute(t *testing.T) {
	now := time.Now()
	windowStart := now.Add(-100 * 24 * time.Hour) // 100 days back
	windowEnd := windowStart.Add(1 * time.Hour)

	res, err := ResolveTier(pb.GranularityTier_GRANULARITY_TIER_5_MINUTE, windowStart, windowEnd, now)
	if err != nil {
		t.Fatalf("ResolveTier: %v", err)
	}
	if res.Tier == pb.GranularityTier_GRANULARITY_TIER_5_MINUTE {
		t.Errorf("A12: a window older than 90 days must never resolve to 5-minute, got 5-minute (reason: %s)", res.Reason)
	}
	if res.Tier != pb.GranularityTier_GRANULARITY_TIER_HOURLY {
		t.Errorf("expected coarsening to hourly for a 100-day-old window, got %v", res.Tier)
	}
	if !strings.Contains(res.Reason, "90-day") {
		t.Errorf("expected reason to disclose the 90-day 5-minute retention floor, got %q", res.Reason)
	}
}

// TestResolveTier_5MinuteExactlyAt90DayBoundaryStays verifies the 90-day
// retention boundary is inclusive.
func TestResolveTier_5MinuteExactlyAt90DayBoundaryStays(t *testing.T) {
	now := time.Now()
	windowStart := now.Add(-FiveMinuteRetention)
	windowEnd := windowStart.Add(1 * time.Hour)

	res, err := ResolveTier(pb.GranularityTier_GRANULARITY_TIER_5_MINUTE, windowStart, windowEnd, now)
	if err != nil {
		t.Fatalf("ResolveTier: %v", err)
	}
	if res.Tier != pb.GranularityTier_GRANULARITY_TIER_5_MINUTE {
		t.Errorf("expected 5-minute at exactly the 90-day retention floor (inclusive boundary), got %v", res.Tier)
	}
}

// TestResolveTier_RawRequestPastBothFloorsGoesHourly verifies a raw request
// whose window is both longer than the 48-hour raw cap and older than the
// 90-day 5-minute retention floor coarsens all the way to hourly in one
// step (neither raw nor 5-minute can serve it).
func TestResolveTier_RawRequestPastBothFloorsGoesHourly(t *testing.T) {
	now := time.Now()
	windowStart := now.Add(-200 * 24 * time.Hour) // 200 days back: past 90-day floor
	windowEnd := windowStart.Add(72 * time.Hour)  // duration also past the 48-hour raw cap

	res, err := ResolveTier(pb.GranularityTier_GRANULARITY_TIER_RAW, windowStart, windowEnd, now)
	if err != nil {
		t.Fatalf("ResolveTier: %v", err)
	}
	if res.Tier != pb.GranularityTier_GRANULARITY_TIER_HOURLY {
		t.Errorf("expected raw request 200 days back with a 72-hour window to coarsen to hourly, got %v", res.Tier)
	}
}

// TestResolveTier_RawRequestPastRawRetentionFloorGoesHourly verifies that
// once a window's age exceeds the 13-month raw retention floor, a raw
// request coarsens to hourly even when the window's *duration* alone would
// have fit within the 48-hour raw serving cap -- raw data that old is not
// guaranteed to still exist, and it is also well past the 90-day 5-minute
// floor, so hourly is the only tier left that can answer.
func TestResolveTier_RawRequestPastRawRetentionFloorGoesHourly(t *testing.T) {
	now := time.Now()
	windowStart := now.Add(-(RawRetentionFloor + 24*time.Hour)) // just past the 13-month raw floor
	windowEnd := windowStart.Add(1 * time.Hour)                 // short duration, would fit the 48-hour cap on its own

	res, err := ResolveTier(pb.GranularityTier_GRANULARITY_TIER_RAW, windowStart, windowEnd, now)
	if err != nil {
		t.Fatalf("ResolveTier: %v", err)
	}
	if res.Tier != pb.GranularityTier_GRANULARITY_TIER_HOURLY {
		t.Errorf("expected raw request past the 13-month raw retention floor to coarsen to hourly, got %v", res.Tier)
	}
}

// TestResolveTier_HourlyRequestAlwaysHourly verifies no tier coarser than
// hourly exists (A14): an hourly request always resolves to hourly,
// regardless of window shape.
func TestResolveTier_HourlyRequestAlwaysHourly(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name        string
		windowStart time.Time
		windowEnd   time.Time
	}{
		{"one week", now.Add(-7 * 24 * time.Hour), now},
		{"one hour", now.Add(-1 * time.Hour), now},
		{"one year", now.Add(-365 * 24 * time.Hour), now},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := ResolveTier(pb.GranularityTier_GRANULARITY_TIER_HOURLY, c.windowStart, c.windowEnd, now)
			if err != nil {
				t.Fatalf("ResolveTier: %v", err)
			}
			if res.Tier != pb.GranularityTier_GRANULARITY_TIER_HOURLY {
				t.Errorf("A14: hourly request must always resolve to hourly, got %v", res.Tier)
			}
		})
	}
}

// TestResolveTier_UnspecifiedResolvesToHourly verifies that an unspecified
// granularity (no tier coarser than hourly exists, A14) resolves to hourly,
// the coarsest available tier, rather than erroring or defaulting to raw.
func TestResolveTier_UnspecifiedResolvesToHourly(t *testing.T) {
	now := time.Now()
	res, err := ResolveTier(pb.GranularityTier_GRANULARITY_TIER_UNSPECIFIED, now.Add(-1*time.Hour), now, now)
	if err != nil {
		t.Fatalf("ResolveTier: %v", err)
	}
	if res.Tier != pb.GranularityTier_GRANULARITY_TIER_HOURLY {
		t.Errorf("expected GRANULARITY_TIER_UNSPECIFIED to resolve to hourly, got %v", res.Tier)
	}
}

// TestResolveTier_OneWeekWindowResolvesAndDisclosesTier verifies the
// acceptance criterion "A one-week window resolves to a tier and the
// response says which" for each requested tier.
func TestResolveTier_OneWeekWindowResolvesAndDisclosesTier(t *testing.T) {
	now := time.Now()
	windowStart := now.Add(-7 * 24 * time.Hour)
	windowEnd := now

	for _, requested := range []pb.GranularityTier{
		pb.GranularityTier_GRANULARITY_TIER_RAW,
		pb.GranularityTier_GRANULARITY_TIER_5_MINUTE,
		pb.GranularityTier_GRANULARITY_TIER_HOURLY,
	} {
		t.Run(requested.String(), func(t *testing.T) {
			res, err := ResolveTier(requested, windowStart, windowEnd, now)
			if err != nil {
				t.Fatalf("ResolveTier: %v", err)
			}
			if res.Tier == pb.GranularityTier_GRANULARITY_TIER_UNSPECIFIED {
				t.Error("a one-week window must resolve to a concrete tier, not UNSPECIFIED")
			}
			if res.Reason == "" {
				t.Error("FR71: coarsening (or the lack of it) must always be disclosed via a non-empty reason")
			}
			// A one-week window exceeds the 48-hour raw cap, so it can never
			// answer at raw regardless of what was requested.
			if res.Tier == pb.GranularityTier_GRANULARITY_TIER_RAW {
				t.Errorf("a one-week window must never resolve to raw (NFR3.2), got raw for requested=%v", requested)
			}
		})
	}
}

// TestResolveTier_NeverServesFinerThanRequested verifies FR71: the server
// never serves a finer tier than requested, even when a finer tier happens
// to be servable for the window. An hourly request over a short, recent
// window must still resolve to hourly, not raw or 5-minute.
func TestResolveTier_NeverServesFinerThanRequested(t *testing.T) {
	now := time.Now()
	windowStart := now.Add(-10 * time.Minute)
	windowEnd := now

	res, err := ResolveTier(pb.GranularityTier_GRANULARITY_TIER_HOURLY, windowStart, windowEnd, now)
	if err != nil {
		t.Fatalf("ResolveTier: %v", err)
	}
	if res.Tier != pb.GranularityTier_GRANULARITY_TIER_HOURLY {
		t.Errorf("FR71: requesting hourly over a servable-as-raw window must still return hourly, got %v", res.Tier)
	}

	res, err = ResolveTier(pb.GranularityTier_GRANULARITY_TIER_5_MINUTE, windowStart, windowEnd, now)
	if err != nil {
		t.Fatalf("ResolveTier: %v", err)
	}
	if res.Tier != pb.GranularityTier_GRANULARITY_TIER_5_MINUTE {
		t.Errorf("FR71: requesting 5-minute over a servable-as-raw window must still return 5-minute, got %v", res.Tier)
	}
}

// TestResolveTier_InvalidWindowErrors verifies that a window whose end
// precedes its start is refused rather than silently resolved.
func TestResolveTier_InvalidWindowErrors(t *testing.T) {
	now := time.Now()
	_, err := ResolveTier(pb.GranularityTier_GRANULARITY_TIER_RAW, now, now.Add(-1*time.Hour), now)
	if err == nil {
		t.Fatal("expected an error for windowEnd before windowStart, got nil")
	}
}
