// Coverage for fake.Client itself -- as of this task (#1573), no external
// consumer package exists yet to write the "used by at least one consumer
// test" case this task's Testing section calls for (worker's #1574, mcp's
// #1576/#1581 land later and depend on this package). This file is the
// closest available proxy: it drives fake.Client exactly as a future
// consumer test would (populate the fields, call the youtube.Client
// interface methods, assert on the result), and asserts the interface
// satisfaction fake.go's var _ youtube.Client = (*Client)(nil) line only
// checks at compile time.
package fake

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/youtube"
)

func TestClient_ListSchedule_ReturnsScheduleAsIs(t *testing.T) {
	want := []youtube.Video{{YouTubeVideoID: "vid-1"}, {YouTubeVideoID: "vid-2"}}
	f := &Client{Schedule: want}

	got, err := f.ListSchedule(context.Background(), "chan-1")
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestClient_Metrics_FallsBackToZeroValuedEntryForUnknownID(t *testing.T) {
	since := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	f := &Client{
		MetricsByVideoID: map[string]youtube.VideoMetrics{
			"vid-known": {YouTubeVideoID: "vid-known", Views: ptrI64(10)},
		},
	}

	got, err := f.Metrics(context.Background(), "chan-1", []string{"vid-known", "vid-unknown"}, since)
	require.NoError(t, err)
	require.Len(t, got, 2)

	byID := make(map[string]youtube.VideoMetrics, len(got))
	for _, m := range got {
		byID[m.YouTubeVideoID] = m
	}

	require.Contains(t, byID, "vid-known")
	assert.Equal(t, ptrI64(10), byID["vid-known"].Views)

	require.Contains(t, byID, "vid-unknown")
	unknown := byID["vid-unknown"]
	assert.Nil(t, unknown.Views, "an id with no MetricsByVideoID entry must fall back to a zero-valued VideoMetrics, mirroring the real Client's contract")
	assert.Equal(t, since, unknown.MeasuredAt)
}

func TestClient_ChannelInfo_ReturnsChanAsIs(t *testing.T) {
	want := youtube.Channel{YouTubeChannelID: "chan-1", Title: "My Channel"}
	f := &Client{Chan: want}

	got, err := f.ChannelInfo(context.Background())
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

// TestClient_Err_PropagatesFromEveryMethod exercises the "set Err to one of
// youtube.ErrRevoked/ErrQuotaExceeded/ErrTransient/ErrPermanent" contract
// fake.go's doc comment describes, across all three Client methods.
func TestClient_Err_PropagatesFromEveryMethod(t *testing.T) {
	f := &Client{Err: youtube.ErrQuotaExceeded}

	_, err := f.ListSchedule(context.Background(), "chan-1")
	assert.ErrorIs(t, err, youtube.ErrQuotaExceeded)

	_, err = f.Metrics(context.Background(), "chan-1", []string{"vid-1"}, time.Now())
	assert.ErrorIs(t, err, youtube.ErrQuotaExceeded)

	_, err = f.ChannelInfo(context.Background())
	assert.ErrorIs(t, err, youtube.ErrQuotaExceeded)
}

func ptrI64(i int64) *int64 { return &i }
