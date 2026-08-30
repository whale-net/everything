package main

import (
	"testing"
	"time"

	pb "github.com/whale-net/everything/leaflab/api/proto"
)

// TestReportingState is a table-driven test of reportingState as a pure
// function of (last-reading timestamp, now) — no database involved (see
// #1497's Testing section). It covers the boundary exactly at the
// threshold, just inside it, just outside it, and the no-reading-at-all
// case.
func TestReportingState(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name          string
		lastReadingAt *time.Time
		want          pb.ReportingState
	}{
		{
			name:          "no reading at all is never reported",
			lastReadingAt: nil,
			want:          pb.ReportingState_REPORTING_STATE_NEVER_REPORTED,
		},
		{
			name:          "just inside the threshold (9m59s ago) is reporting",
			lastReadingAt: timePtr(now.Add(-(reportingThreshold - time.Second))),
			want:          pb.ReportingState_REPORTING_STATE_REPORTING,
		},
		{
			name:          "exactly at the threshold (10m ago) is still reporting (inclusive boundary)",
			lastReadingAt: timePtr(now.Add(-reportingThreshold)),
			want:          pb.ReportingState_REPORTING_STATE_REPORTING,
		},
		{
			name:          "just outside the threshold (10m1s ago) is stale",
			lastReadingAt: timePtr(now.Add(-(reportingThreshold + time.Second))),
			want:          pb.ReportingState_REPORTING_STATE_STALE,
		},
		{
			name:          "long stale (30m ago) is stale",
			lastReadingAt: timePtr(now.Add(-30 * time.Minute)),
			want:          pb.ReportingState_REPORTING_STATE_STALE,
		},
		{
			name:          "a reading in the future (clock skew) is reporting",
			lastReadingAt: timePtr(now.Add(time.Minute)),
			want:          pb.ReportingState_REPORTING_STATE_REPORTING,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reportingState(tt.lastReadingAt, now)
			if got != tt.want {
				t.Errorf("reportingState(%v, %v) = %v, want %v", tt.lastReadingAt, now, got, tt.want)
			}
		})
	}
}

func timePtr(t time.Time) *time.Time {
	return &t
}
