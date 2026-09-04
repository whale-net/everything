package youtube

// Table-driven mapping tests over testdata/ fixtures for a video with a
// full Analytics row and one with missing/partial columns (this task's
// Testing section), plus an httptest-backed Metrics test proving a video
// with NO row at all in the Analytics response still comes back as a
// zero-valued entry rather than an error for the whole request.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	youtubeanalytics "google.golang.org/api/youtubeanalytics/v2"
)

func loadQueryResponseFixture(t *testing.T, name string) *youtubeanalytics.QueryResponse {
	t.Helper()
	var resp youtubeanalytics.QueryResponse
	require.NoError(t, json.Unmarshal(mustLoadFixture(t, name), &resp))
	return &resp
}

func f64(f float64) *float64 { return &f }
func i64(i int64) *int64     { return &i }

func TestMapMetricsRows_Table(t *testing.T) {
	measuredAt := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

	t.Run("full row: views, retention, and CTR/impressions all present", func(t *testing.T) {
		resp := loadQueryResponseFixture(t, "metrics_full.json")
		got, err := mapMetricsRows(resp, measuredAt)
		require.NoError(t, err)

		require.Contains(t, got, "vid-full")
		m := got["vid-full"]
		assert.Equal(t, "vid-full", m.YouTubeVideoID)
		assert.Equal(t, i64(1000), m.Views)
		assert.Equal(t, f64(245), m.AverageViewDurationSeconds)
		assert.Equal(t, f64(62.3), m.AverageViewPercentage)
		assert.Equal(t, i64(5000), m.Impressions)
		assert.Equal(t, f64(8.5), m.ImpressionCTR)
		assert.Equal(t, measuredAt, m.MeasuredAt)
	})

	t.Run("partial row: only video+views columns present, other fields stay nil not zero-errored", func(t *testing.T) {
		resp := loadQueryResponseFixture(t, "metrics_partial.json")
		got, err := mapMetricsRows(resp, measuredAt)
		require.NoError(t, err)

		require.Contains(t, got, "vid-partial")
		m := got["vid-partial"]
		assert.Equal(t, "vid-partial", m.YouTubeVideoID)
		assert.Equal(t, i64(42), m.Views)
		assert.Nil(t, m.AverageViewDurationSeconds)
		assert.Nil(t, m.AverageViewPercentage)
		assert.Nil(t, m.Impressions)
		assert.Nil(t, m.ImpressionCTR)
	})
}

// TestMapMetricsRows_MissingDimensionColumn_ReturnsError guards the
// defensive "video dimension column resolved by name, not assumed
// position" contract in mapMetricsRows' doc comment.
func TestMapMetricsRows_MissingDimensionColumn_ReturnsError(t *testing.T) {
	resp := &youtubeanalytics.QueryResponse{
		ColumnHeaders: []*youtubeanalytics.ResultTableColumnHeader{
			{Name: "views"},
		},
		Rows: [][]interface{}{{float64(10)}},
	}
	_, err := mapMetricsRows(resp, time.Now())
	assert.Error(t, err)
}

// TestMetrics_MissingVideoGetsZeroValuedRow_NotAnError is the M1 Testing
// task's explicit acceptance case: "Metrics request for a video with no
// data returns a zero-valued row rather than an error." The Analytics
// fixture only has a row for vid-full; vid-has-no-analytics-yet is
// requested alongside it and must still come back with exactly one entry,
// zero-valued except YouTubeVideoID/MeasuredAt.
func TestMetrics_MissingVideoGetsZeroValuedRow_NotAnError(t *testing.T) {
	c := newTestServer(t, map[string]http.HandlerFunc{
		"/v2/reports": func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "channel==chan-123", r.URL.Query().Get("ids"))
			writeJSONFixture(t, w, "metrics_full.json")
		},
	})

	since := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	got, err := c.Metrics(context.Background(), "chan-123", []string{"vid-full", "vid-has-no-analytics-yet"}, since)
	require.NoError(t, err)
	require.Len(t, got, 2, "one result entry per requested id, present or not")

	byID := make(map[string]VideoMetrics, len(got))
	for _, m := range got {
		byID[m.YouTubeVideoID] = m
	}

	require.Contains(t, byID, "vid-full")
	assert.NotNil(t, byID["vid-full"].Views)

	require.Contains(t, byID, "vid-has-no-analytics-yet")
	zero := byID["vid-has-no-analytics-yet"]
	assert.Nil(t, zero.Views)
	assert.Nil(t, zero.AverageViewDurationSeconds)
	assert.Nil(t, zero.AverageViewPercentage)
	assert.Nil(t, zero.Impressions)
	assert.Nil(t, zero.ImpressionCTR)
}

// TestMetrics_EmptyVideoIDs_ReturnsNilWithoutCallingAnalytics guards the
// "len(videoIDs) == 0" short-circuit in Metrics -- no request should ever
// reach the Analytics endpoint for an empty id list.
func TestMetrics_EmptyVideoIDs_ReturnsNilWithoutCallingAnalytics(t *testing.T) {
	c := newTestServer(t, map[string]http.HandlerFunc{
		"/v2/reports": func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("Reports.Query must not be called for an empty videoIDs slice")
		},
	})

	got, err := c.Metrics(context.Background(), "chan-123", nil, time.Now())
	require.NoError(t, err)
	assert.Nil(t, got)
}

// TestMetrics_TransientError_ClassifiesThroughRealRequestPath proves a
// Reports.Query 500 is classified the same way ChannelInfo's/ListSchedule's
// failures are (client_test.go, schedule_test.go).
func TestMetrics_TransientError_ClassifiesThroughRealRequestPath(t *testing.T) {
	c := newTestServer(t, map[string]http.HandlerFunc{
		"/v2/reports": func(w http.ResponseWriter, r *http.Request) {
			writeAPIError(w, http.StatusInternalServerError, "", "backend error")
		},
	})

	_, err := c.Metrics(context.Background(), "chan-123", []string{"vid-1"}, time.Now())
	assert.ErrorIs(t, err, ErrTransient)
}
