package youtube

import (
	"context"
	"fmt"
	"strings"
	"time"

	youtubeanalytics "google.golang.org/api/youtubeanalytics/v2"
)

// VideoMetrics is one Metrics result for a single video -- maps 1:1 onto
// store.VideoMetrics' data columns (migration 002, FR21) minus the
// SyncedVideoID foreign key, which only a caller holding the matching
// store.SyncedVideo row can resolve; YouTubeVideoID is the join key a
// caller (worker, #1574) uses to find it. Only views + retention +
// CTR/impressions are in M1 scope (C16 is out of scope), but the field set
// intentionally mirrors store.VideoMetrics so adding a metric later never
// requires changing this shape's meaning, just adding a field to both.
//
// Fields are pointers because YouTube Analytics can omit any of them (e.g.
// a just-published video with no rows yet) -- see Client.Metrics' doc
// comment on the zero-valued-row-not-error contract for that case.
type VideoMetrics struct {
	YouTubeVideoID             string
	Views                      *int64
	AverageViewDurationSeconds *float64
	AverageViewPercentage      *float64
	Impressions                *int64
	ImpressionCTR              *float64
	MeasuredAt                 time.Time
}

// analyticsDimension/analyticsMetrics are the fixed query shape every
// Metrics call issues: one row per video id (dimensions=video), filtered
// to the requested ids, with M1's metric set (views, retention,
// CTR/impressions -- C16 is out of scope, but adding a metric later only
// means adding a name here and a field above, never changing Client's
// signature).
const analyticsDimension = "video"

var analyticsMetrics = []string{
	"views",
	"averageViewDuration",
	"averageViewPercentage",
	"impressions",
	"impressionClickThroughRate",
}

// analyticsFilterBatchSize bounds how many video ids go into a single
// `video==id1,id2,...` filter, keeping well under YouTube Analytics'
// documented per-request filter-value limits.
const analyticsFilterBatchSize = 200

// dateLayout is the YYYY-MM-DD format the YouTube Analytics API requires
// for startDate/endDate.
const dateLayout = "2006-01-02"

// Metrics returns views, retention, and CTR/impressions for each of
// videoIDs measured since since (FR21): retention from
// averageViewDuration/averageViewPercentage, CTR/impressions from
// impressions/impressionClickThroughRate, all via YouTube Analytics.
//
// A single request-level failure (auth/quota/transient/permanent) aborts
// the whole call; a video with no Analytics rows in the queried range
// yields a zero-valued VideoMetrics entry for that id instead, never an
// error for that video alone -- callers can always index the result by
// YouTubeVideoID and expect exactly one entry per requested id, in the
// same order as videoIDs.
func (c *client) Metrics(ctx context.Context, channelID string, videoIDs []string, since time.Time) ([]VideoMetrics, error) {
	if len(videoIDs) == 0 {
		return nil, nil
	}
	if c.err != nil {
		return nil, classify(c.err)
	}

	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	now := time.Now()
	byID := make(map[string]VideoMetrics, len(videoIDs))

	for _, batch := range chunkStrings(videoIDs, analyticsFilterBatchSize) {
		resp, err := c.ya.Reports.Query().
			Ids("channel==" + channelID).
			StartDate(since.Format(dateLayout)).
			EndDate(now.Format(dateLayout)).
			Dimensions(analyticsDimension).
			Metrics(strings.Join(analyticsMetrics, ",")).
			Filters(analyticsDimension + "==" + strings.Join(batch, ",")).
			Context(ctx).
			Do()
		if err != nil {
			return nil, classify(err)
		}

		rows, err := mapMetricsRows(resp, now)
		if err != nil {
			return nil, classify(err)
		}
		for id, m := range rows {
			byID[id] = m
		}
	}

	out := make([]VideoMetrics, 0, len(videoIDs))
	for _, id := range videoIDs {
		if m, ok := byID[id]; ok {
			out = append(out, m)
			continue
		}
		out = append(out, VideoMetrics{YouTubeVideoID: id, MeasuredAt: now})
	}
	return out, nil
}

// mapMetricsRows maps one QueryResponse's rows onto a VideoMetrics per
// video id, keyed by YouTubeVideoID -- column positions are resolved from
// resp.ColumnHeaders rather than assumed fixed, so a reordering on
// Google's side never silently mismatches columns to fields.
func mapMetricsRows(resp *youtubeanalytics.QueryResponse, measuredAt time.Time) (map[string]VideoMetrics, error) {
	col := make(map[string]int, len(resp.ColumnHeaders))
	for i, h := range resp.ColumnHeaders {
		col[h.Name] = i
	}

	videoIdx, ok := col[analyticsDimension]
	if !ok {
		return nil, fmt.Errorf("analytics response missing %q dimension column", analyticsDimension)
	}

	out := make(map[string]VideoMetrics, len(resp.Rows))
	for _, row := range resp.Rows {
		id, ok := rowString(row, videoIdx)
		if !ok {
			continue
		}
		m := VideoMetrics{YouTubeVideoID: id, MeasuredAt: measuredAt}
		if idx, ok := col["views"]; ok {
			m.Views = rowInt64Ptr(row, idx)
		}
		if idx, ok := col["averageViewDuration"]; ok {
			m.AverageViewDurationSeconds = rowFloat64Ptr(row, idx)
		}
		if idx, ok := col["averageViewPercentage"]; ok {
			m.AverageViewPercentage = rowFloat64Ptr(row, idx)
		}
		if idx, ok := col["impressions"]; ok {
			m.Impressions = rowInt64Ptr(row, idx)
		}
		if idx, ok := col["impressionClickThroughRate"]; ok {
			m.ImpressionCTR = rowFloat64Ptr(row, idx)
		}
		out[id] = m
	}
	return out, nil
}

func rowString(row []interface{}, idx int) (string, bool) {
	if idx < 0 || idx >= len(row) {
		return "", false
	}
	s, ok := row[idx].(string)
	return s, ok
}

// rowFloat64Ptr reads row[idx] as a float64 -- the type
// encoding/json decodes every JSON number into for a
// []interface{}, which is what QueryResponse.Rows' entries are.
func rowFloat64Ptr(row []interface{}, idx int) *float64 {
	if idx < 0 || idx >= len(row) {
		return nil
	}
	f, ok := row[idx].(float64)
	if !ok {
		return nil
	}
	return &f
}

func rowInt64Ptr(row []interface{}, idx int) *int64 {
	f := rowFloat64Ptr(row, idx)
	if f == nil {
		return nil
	}
	i := int64(*f)
	return &i
}
