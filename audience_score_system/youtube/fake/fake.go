// Package fake is an in-memory youtube.Client double for consumer tests
// (worker's sync workflow #1574, mcp's schedule/metrics tools
// #1576/#1581) that must never make a live network call -- see
// ../client.go's package doc comment. Unlike the rest of
// //audience_score_system/youtube, this package is NOT stubbed pending the
// Implementation phase: it is fully working from Scaffold on, since its
// entire purpose is to let other packages' tests depend on it today.
package fake

import (
	"context"
	"time"

	"github.com/whale-net/everything/audience_score_system/youtube"
)

// Client is an in-memory youtube.Client double. Populate Schedule,
// MetricsByVideoID, and Chan before use. If Err is set, every method
// returns it as-is -- set it to one of youtube.ErrRevoked/
// ErrQuotaExceeded/ErrTransient/ErrPermanent to exercise a consumer's
// error-branch handling without a real YouTube API failure.
type Client struct {
	Schedule         []youtube.Video
	MetricsByVideoID map[string]youtube.VideoMetrics
	Chan             youtube.Channel
	Err              error
}

var _ youtube.Client = (*Client)(nil)

// ListSchedule returns Schedule as-is (or Err, if set).
func (f *Client) ListSchedule(ctx context.Context, channelID string) ([]youtube.Video, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	return f.Schedule, nil
}

// Metrics returns MetricsByVideoID[id] for each requested id, falling back
// to a zero-valued youtube.VideoMetrics (never an error) for any id with
// no entry -- mirroring the real Client's "missing data for one video is
// not an error for the whole request" contract (metrics.go).
func (f *Client) Metrics(ctx context.Context, channelID string, videoIDs []string, since time.Time) ([]youtube.VideoMetrics, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	out := make([]youtube.VideoMetrics, 0, len(videoIDs))
	for _, id := range videoIDs {
		if m, ok := f.MetricsByVideoID[id]; ok {
			out = append(out, m)
			continue
		}
		out = append(out, youtube.VideoMetrics{YouTubeVideoID: id, MeasuredAt: since})
	}
	return out, nil
}

// ChannelInfo returns Chan as-is (or Err, if set).
func (f *Client) ChannelInfo(ctx context.Context) (youtube.Channel, error) {
	if f.Err != nil {
		return youtube.Channel{}, f.Err
	}
	return f.Chan, nil
}
