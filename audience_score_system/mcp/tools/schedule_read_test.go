package tools

// Pure-Go coverage of get_channel_schedule's filtering/mapping logic
// (schedule_read.go, issue #1576) -- withinWindow's from/to window
// semantics and getChannelScheduleHandler's include_drafts filtering,
// driven directly against an in-memory store.SyncStore fake, entirely
// bypassing the MCP session/HTTP/auth plumbing. No Docker required, runs
// as part of `bazel test //...`. See schedule_read_integration_test.go
// ("integration" gotag, requires Docker -- not runnable in this sandbox,
// per this task's Testing-phase comment) for the real end-to-end
// Channel-scoping (store.CanRead via server.RegisterRead) coverage this
// file deliberately does not attempt to fake.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/store"
)

// fakeSyncStore is a minimal store.SyncStore stand-in scoped to exactly
// what getChannelScheduleHandler needs (ListSchedule).
type fakeSyncStore struct {
	videos []store.SyncedVideo
	err    error
}

var _ store.SyncStore = fakeSyncStore{}

func (f fakeSyncStore) UpsertVideos(context.Context, uuid.UUID, []store.SyncedVideo) error {
	return errors.New("fakeSyncStore.UpsertVideos is not used by these tests")
}

func (f fakeSyncStore) UpsertMetrics(context.Context, []store.VideoMetrics) error {
	return errors.New("fakeSyncStore.UpsertMetrics is not used by these tests")
}

func (f fakeSyncStore) ListSchedule(context.Context, uuid.UUID) ([]store.SyncedVideo, error) {
	return f.videos, f.err
}

func (f fakeSyncStore) GetByID(context.Context, uuid.UUID) (store.SyncedVideo, error) {
	return store.SyncedVideo{}, errors.New("fakeSyncStore.GetByID is not used by these tests")
}

func (f fakeSyncStore) LatestMetricsFor(context.Context, uuid.UUID) (*store.VideoMetrics, error) {
	return nil, errors.New("fakeSyncStore.LatestMetricsFor is not used by these tests")
}

func ptrTime(t time.Time) *time.Time { return &t }

// ── withinWindow ─────────────────────────────────────────────────────────

func TestWithinWindow(t *testing.T) {
	now := time.Now()

	cases := []struct {
		name string
		v    store.SyncedVideo
		from *time.Time
		to   *time.Time
		want bool
	}{
		{
			name: "no bounds always passes",
			v:    store.SyncedVideo{PublishedAt: ptrTime(now)},
			want: true,
		},
		{
			name: "neither PublishAt nor PublishedAt set always passes",
			v:    store.SyncedVideo{},
			from: ptrTime(now.Add(-time.Hour)),
			to:   ptrTime(now.Add(time.Hour)),
			want: true,
		},
		{
			name: "published video within [from, to]",
			v:    store.SyncedVideo{PublishedAt: ptrTime(now)},
			from: ptrTime(now.Add(-time.Hour)),
			to:   ptrTime(now.Add(time.Hour)),
			want: true,
		},
		{
			name: "published video before from is excluded",
			v:    store.SyncedVideo{PublishedAt: ptrTime(now.Add(-2 * time.Hour))},
			from: ptrTime(now.Add(-time.Hour)),
			want: false,
		},
		{
			name: "published video after to is excluded",
			v:    store.SyncedVideo{PublishedAt: ptrTime(now.Add(2 * time.Hour))},
			to:   ptrTime(now.Add(time.Hour)),
			want: false,
		},
		{
			name: "draft uses PublishAt as its effective timestamp, not PublishedAt",
			v:    store.SyncedVideo{PublishAt: ptrTime(now.Add(48 * time.Hour)), IsScheduledDraft: true},
			from: ptrTime(now.Add(-time.Hour)),
			to:   ptrTime(now.Add(time.Hour)),
			want: false,
		},
		{
			name: "draft's PublishAt within window passes",
			v:    store.SyncedVideo{PublishAt: ptrTime(now.Add(48 * time.Hour)), IsScheduledDraft: true},
			from: ptrTime(now.Add(24 * time.Hour)),
			to:   ptrTime(now.Add(72 * time.Hour)),
			want: true,
		},
		{
			name: "bound exactly equal to the timestamp is inclusive (from)",
			v:    store.SyncedVideo{PublishedAt: ptrTime(now)},
			from: ptrTime(now),
			want: true,
		},
		{
			name: "bound exactly equal to the timestamp is inclusive (to)",
			v:    store.SyncedVideo{PublishedAt: ptrTime(now)},
			to:   ptrTime(now),
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, withinWindow(tc.v, tc.from, tc.to))
		})
	}
}

// ── getChannelScheduleHandler ────────────────────────────────────────────

func TestGetChannelScheduleHandler_IncludeDrafts_DefaultTrue_FalseOmits(t *testing.T) {
	ctx := context.Background()
	channelID := uuid.New()

	publishAt := time.Now().Add(48 * time.Hour)
	publishedAt := time.Now().Add(-time.Hour)
	fake := fakeSyncStore{videos: []store.SyncedVideo{
		{YouTubeVideoID: "vid-draft", PrivacyStatus: store.PrivacyStatusPrivate, PublishAt: &publishAt, IsScheduledDraft: true},
		{YouTubeVideoID: "vid-public", PrivacyStatus: store.PrivacyStatusPublic, PublishedAt: &publishedAt},
	}}
	h := getChannelScheduleHandler(fake)

	t.Run("default (nil) includes drafts", func(t *testing.T) {
		_, out, err := h(ctx, nil, GetChannelScheduleInput{ChannelID: channelID.String()})
		require.NoError(t, err)
		assert.Len(t, out.Videos, 2)
	})

	t.Run("include_drafts=false omits drafts", func(t *testing.T) {
		f := false
		_, out, err := h(ctx, nil, GetChannelScheduleInput{ChannelID: channelID.String(), IncludeDrafts: &f})
		require.NoError(t, err)
		require.Len(t, out.Videos, 1)
		assert.Equal(t, "vid-public", out.Videos[0].YouTubeVideoID)
	})

	t.Run("include_drafts=true explicitly still includes drafts", func(t *testing.T) {
		tr := true
		_, out, err := h(ctx, nil, GetChannelScheduleInput{ChannelID: channelID.String(), IncludeDrafts: &tr})
		require.NoError(t, err)
		assert.Len(t, out.Videos, 2)
	})
}

func TestGetChannelScheduleHandler_FromToWindow_FiltersVideos(t *testing.T) {
	ctx := context.Background()
	channelID := uuid.New()
	now := time.Now()

	fake := fakeSyncStore{videos: []store.SyncedVideo{
		{YouTubeVideoID: "vid-early", PublishedAt: ptrTime(now.Add(-72 * time.Hour))},
		{YouTubeVideoID: "vid-mid", PublishedAt: ptrTime(now.Add(-24 * time.Hour))},
		{YouTubeVideoID: "vid-late", PublishAt: ptrTime(now.Add(72 * time.Hour)), IsScheduledDraft: true},
	}}
	h := getChannelScheduleHandler(fake)

	from := now.Add(-48 * time.Hour)
	to := now.Add(48 * time.Hour)
	_, out, err := h(ctx, nil, GetChannelScheduleInput{ChannelID: channelID.String(), From: &from, To: &to})
	require.NoError(t, err)
	require.Len(t, out.Videos, 1)
	assert.Equal(t, "vid-mid", out.Videos[0].YouTubeVideoID)
}

func TestGetChannelScheduleHandler_EmptyChannel_ReturnsEmptyListNotError(t *testing.T) {
	ctx := context.Background()
	h := getChannelScheduleHandler(fakeSyncStore{videos: nil})

	_, out, err := h(ctx, nil, GetChannelScheduleInput{ChannelID: uuid.New().String()})
	require.NoError(t, err)
	assert.Empty(t, out.Videos)
}

func TestGetChannelScheduleHandler_StoreError_Propagates(t *testing.T) {
	ctx := context.Background()
	storeErr := errors.New("connection refused")
	h := getChannelScheduleHandler(fakeSyncStore{err: storeErr})

	_, _, err := h(ctx, nil, GetChannelScheduleInput{ChannelID: uuid.New().String()})
	require.Error(t, err)
	assert.ErrorIs(t, err, storeErr)
}

// ── ChannelScopeID ───────────────────────────────────────────────────────

func TestGetChannelScheduleInput_ChannelScopeID(t *testing.T) {
	id := uuid.New()
	in := GetChannelScheduleInput{ChannelID: id.String()}
	assert.Equal(t, id, in.ChannelScopeID())

	invalid := GetChannelScheduleInput{ChannelID: "not-a-uuid"}
	assert.Equal(t, uuid.Nil, invalid.ChannelScopeID(), "an unparseable ChannelID must resolve to uuid.Nil, not panic")
}
