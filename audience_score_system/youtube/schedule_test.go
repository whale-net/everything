package youtube

// Table-driven mapping tests over testdata/ fixtures (this task's Testing
// section) plus a pagination test proving ListSchedule's search.list ->
// videos.list flow yields the de-duplicated union across pages.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	youtubev3 "google.golang.org/api/youtube/v3"

	"github.com/whale-net/everything/audience_score_system/store"
)

// loadVideoFixture unmarshals testdata/name into a *youtubev3.Video, the
// same type youtube/v3's videos.list response items decode into.
func loadVideoFixture(t *testing.T, name string) *youtubev3.Video {
	t.Helper()
	var v youtubev3.Video
	require.NoError(t, json.Unmarshal(mustLoadFixture(t, name), &v))
	return &v
}

// fixedNow is the reference time every mapVideo test below evaluates
// IsScheduledDraft's "future publishAt" test against -- mapVideo takes now
// as an explicit parameter precisely so tests control it (schedule.go's
// mapVideo doc comment) instead of racing time.Now().
var fixedNow = time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

func TestMapVideo_Table(t *testing.T) {
	cases := []struct {
		name     string
		fixture  string
		want     Video
		wantErrs bool
	}{
		{
			name:    "public upload",
			fixture: "video_public.json",
			want: Video{
				YouTubeVideoID:   "vid-public",
				Title:            "Public Upload",
				PrivacyStatus:    store.PrivacyStatusPublic,
				PublishedAt:      ptrTime(mustParseRFC3339(t, "2024-01-01T00:00:00Z")),
				IsScheduledDraft: false,
			},
		},
		{
			name:    "scheduled private draft with future publishAt",
			fixture: "video_scheduled_draft.json",
			want: Video{
				YouTubeVideoID:   "vid-draft",
				Title:            "Scheduled Draft",
				PrivacyStatus:    store.PrivacyStatusPrivate,
				PublishAt:        ptrTime(mustParseRFC3339(t, "2099-06-01T12:00:00Z")),
				IsScheduledDraft: true,
			},
		},
		{
			name:    "unlisted video",
			fixture: "video_unlisted.json",
			want: Video{
				YouTubeVideoID:   "vid-unlisted",
				Title:            "Unlisted Video",
				PrivacyStatus:    store.PrivacyStatusUnlisted,
				PublishedAt:      ptrTime(mustParseRFC3339(t, "2024-02-15T08:30:00Z")),
				IsScheduledDraft: false,
			},
		},
		{
			// A private video that is already published (no publishAt, or a
			// past one) must NOT be flagged as a scheduled draft -- guards
			// against IsScheduledDraft keying off privacyStatus=="private"
			// alone, which would misfire for every already-published
			// private video (FR18 collision detection depends on this being
			// exact, per schedule.go's Video.IsScheduledDraft doc comment).
			name:    "private but already published is NOT a scheduled draft",
			fixture: "video_private_published.json",
			want: Video{
				YouTubeVideoID:   "vid-private-published",
				Title:            "Private, Already Published",
				PrivacyStatus:    store.PrivacyStatusPrivate,
				PublishedAt:      ptrTime(mustParseRFC3339(t, "2023-05-01T00:00:00Z")),
				IsScheduledDraft: false,
			},
		},
		{
			name:     "missing snippet/status parts is an error, not a zero-valued Video",
			fixture:  "video_missing_parts.json",
			wantErrs: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := loadVideoFixture(t, tc.fixture)
			got, err := mapVideo(v, fixedNow)
			if tc.wantErrs {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

func mustParseRFC3339(t *testing.T, s string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, s)
	require.NoError(t, err)
	return parsed
}

// TestListSchedule_Pagination_YieldsUnionWithNoDuplicates drives a two-page
// search.list fixture (search_page1.json/search_page2.json, sharing
// vid-public across both pages) followed by one videos.list fixture
// (videos_list.json), asserting the final result is the de-duplicated
// union: exactly the 3 distinct ids, each mapped correctly, with
// IsScheduledDraft true only for the scheduled draft.
func TestListSchedule_Pagination_YieldsUnionWithNoDuplicates(t *testing.T) {
	c := newTestServer(t, map[string]http.HandlerFunc{
		"/youtube/v3/search": func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "true", r.URL.Query().Get("forMine"))
			if r.URL.Query().Get("pageToken") == "page2" {
				writeJSONFixture(t, w, "search_page2.json")
				return
			}
			writeJSONFixture(t, w, "search_page1.json")
		},
		"/youtube/v3/videos": func(w http.ResponseWriter, r *http.Request) {
			writeJSONFixture(t, w, "videos_list.json")
		},
	})

	got, err := c.ListSchedule(context.Background(), "chan-123")
	require.NoError(t, err)
	require.Len(t, got, 3, "search_page1+search_page2 share vid-public; the union must de-duplicate it")

	byID := make(map[string]Video, len(got))
	for _, v := range got {
		_, dup := byID[v.YouTubeVideoID]
		require.False(t, dup, "duplicate video id %s in ListSchedule result", v.YouTubeVideoID)
		byID[v.YouTubeVideoID] = v
	}

	require.Contains(t, byID, "vid-public")
	require.Contains(t, byID, "vid-draft")
	require.Contains(t, byID, "vid-unlisted")

	assert.False(t, byID["vid-public"].IsScheduledDraft)
	assert.True(t, byID["vid-draft"].IsScheduledDraft, "the future-publishAt private video must be flagged as a scheduled draft")
	assert.False(t, byID["vid-unlisted"].IsScheduledDraft)
}

// TestListSchedule_NoOwnVideos_ReturnsNilNotError covers the empty-channel
// edge case: an empty search.list response must short-circuit before ever
// calling videos.list, per schedule.go's "if len(ids) == 0" guard.
func TestListSchedule_NoOwnVideos_ReturnsNilNotError(t *testing.T) {
	c := newTestServer(t, map[string]http.HandlerFunc{
		"/youtube/v3/search": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"items": []}`))
		},
		"/youtube/v3/videos": func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("videos.list must not be called when search.list returned no ids")
		},
	})

	got, err := c.ListSchedule(context.Background(), "chan-123")
	require.NoError(t, err)
	assert.Nil(t, got)
}

// TestListSchedule_SearchError_ClassifiesThroughRealRequestPath proves a
// search.list failure is classified the same way ChannelInfo's failures are
// (client_test.go) -- the classify call in schedule.go's own error path,
// not just errors.go's unit-level coverage.
func TestListSchedule_SearchError_ClassifiesThroughRealRequestPath(t *testing.T) {
	c := newTestServer(t, map[string]http.HandlerFunc{
		"/youtube/v3/search": func(w http.ResponseWriter, r *http.Request) {
			writeAPIError(w, http.StatusForbidden, "quotaExceeded", "Quota exceeded")
		},
	})

	_, err := c.ListSchedule(context.Background(), "chan-123")
	assert.ErrorIs(t, err, ErrQuotaExceeded)
}
