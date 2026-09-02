package sync

// Pure-Go coverage of SyncSchedule (video_sync.go, issue #1576) against
// in-memory fakes -- no Docker required, runs as part of `bazel test
// //...`. fakeSyncStore reimplements UpsertVideos' ON CONFLICT
// (channel_id, youtube_video_id) DO UPDATE natural-key upsert semantics in
// memory (store/sync.go's real SQL is the authority; this mirrors it
// closely enough to prove SyncSchedule itself drives that contract
// correctly -- id stability across re-syncs, in-place field updates,
// disappeared-video retention), so this file can exercise the exact
// scenarios video_sync_test.go's "integration" (real Postgres, requires
// Docker -- not runnable in this sandbox, see that file and this task's
// Testing-phase comment) target also covers, just without a real
// database. See tokens/store_test.go for the analogous pure-Go/
// integration split on that package's own Save/TokenSource/
// MarkNeedsReauth.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
	"golang.org/x/oauth2"

	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/tokens"
	"github.com/whale-net/everything/audience_score_system/youtube"
	"github.com/whale-net/everything/audience_score_system/youtube/fake"
)

// ── fake store.SyncStore ─────────────────────────────────────────────────

type syncedVideoKey struct {
	channelID uuid.UUID
	videoID   string
}

// fakeSyncStore is an in-memory store.SyncStore double, keyed by
// (channel_id, youtube_video_id) exactly like the real synced_video
// table's UNIQUE constraint -- see this file's doc comment.
type fakeSyncStore struct {
	rows map[syncedVideoKey]store.SyncedVideo

	upsertCalls int
	upsertErr   error
}

func newFakeSyncStore() *fakeSyncStore {
	return &fakeSyncStore{rows: map[syncedVideoKey]store.SyncedVideo{}}
}

var _ store.SyncStore = (*fakeSyncStore)(nil)

func (f *fakeSyncStore) UpsertVideos(_ context.Context, channelID uuid.UUID, vids []store.SyncedVideo) error {
	f.upsertCalls++
	if f.upsertErr != nil {
		return f.upsertErr
	}
	for _, v := range vids {
		key := syncedVideoKey{channelID: channelID, videoID: v.YouTubeVideoID}
		if existing, ok := f.rows[key]; ok {
			v.ID = existing.ID
		} else {
			v.ID = uuid.New()
		}
		v.ChannelID = channelID
		f.rows[key] = v
	}
	return nil
}

func (f *fakeSyncStore) UpsertMetrics(context.Context, []store.VideoMetrics) error {
	return errors.New("fakeSyncStore.UpsertMetrics is not used by these tests")
}

func (f *fakeSyncStore) ListSchedule(_ context.Context, channelID uuid.UUID) ([]store.SyncedVideo, error) {
	var out []store.SyncedVideo
	for k, v := range f.rows {
		if k.channelID == channelID {
			out = append(out, v)
		}
	}
	return out, nil
}

func (f *fakeSyncStore) byVideoID(channelID uuid.UUID, videoID string) (store.SyncedVideo, bool) {
	v, ok := f.rows[syncedVideoKey{channelID: channelID, videoID: videoID}]
	return v, ok
}

// GetByID/LatestMetricsFor are not exercised by SyncSchedule's tests (this
// file) -- see outcomes_test.go (issue #1581) for SyncOutcomes' coverage of
// these.
func (f *fakeSyncStore) GetByID(context.Context, uuid.UUID) (store.SyncedVideo, error) {
	return store.SyncedVideo{}, errors.New("fakeSyncStore.GetByID is not used by these tests")
}

func (f *fakeSyncStore) LatestMetricsFor(context.Context, uuid.UUID) (*store.VideoMetrics, error) {
	return nil, errors.New("fakeSyncStore.LatestMetricsFor is not used by these tests")
}

// ── fake tokens.Store ────────────────────────────────────────────────────

type markNeedsReauthCall struct {
	channelID uuid.UUID
	reason    string
}

// fakeTokenStore is a minimal tokens.Store double: TokenSource never needs
// to produce a usable oauth2.TokenSource in these tests because
// NewYouTubeClient (below) is always stubbed to ignore whatever
// TokenSource returns and hand back a fixed youtube/fake.Client instead --
// only MarkNeedsReauth's call log matters here.
type fakeTokenStore struct {
	markNeedsReauthCalls []markNeedsReauthCall
	markNeedsReauthErr   error
}

var _ tokens.Store = (*fakeTokenStore)(nil)

func (f *fakeTokenStore) TokenSource(context.Context, uuid.UUID) (oauth2.TokenSource, error) {
	return nil, nil
}

func (f *fakeTokenStore) Save(context.Context, uuid.UUID, uuid.UUID, *oauth2.Token, []string) error {
	return errors.New("fakeTokenStore.Save is not used by these tests")
}

func (f *fakeTokenStore) MarkNeedsReauth(_ context.Context, channelID uuid.UUID, reason string) error {
	f.markNeedsReauthCalls = append(f.markNeedsReauthCalls, markNeedsReauthCall{channelID: channelID, reason: reason})
	if f.markNeedsReauthErr != nil {
		return f.markNeedsReauthErr
	}
	return nil
}

// ── fixtures ─────────────────────────────────────────────────────────────

func unitPublicVideo(id, title string, publishedAt time.Time) youtube.Video {
	return youtube.Video{
		YouTubeVideoID: id,
		Title:          title,
		PrivacyStatus:  store.PrivacyStatusPublic,
		PublishedAt:    &publishedAt,
	}
}

func unitScheduledDraft(id, title string, publishAt time.Time) youtube.Video {
	return youtube.Video{
		YouTubeVideoID:   id,
		Title:            title,
		PrivacyStatus:    store.PrivacyStatusPrivate,
		PublishAt:        &publishAt,
		IsScheduledDraft: true,
	}
}

// newUnitActivities builds an *Activities backed entirely by in-memory
// fakes for channelID -- ready for SyncSchedule(ctx, channelID).
func newUnitActivities(channelID uuid.UUID, yt youtube.Client, syncStore *fakeSyncStore, tokenStore *fakeTokenStore) *Activities {
	return &Activities{
		Channels: &fakeChannelStore{getByIDChannel: store.Channel{
			ID:               channelID,
			YouTubeChannelID: "yt-channel-1",
		}},
		Tokens:           tokenStore,
		Sync:             syncStore,
		NewYouTubeClient: func(oauth2.TokenSource) youtube.Client { return yt },
	}
}

// ── mixed fixture: mapping onto store.SyncedVideo ───────────────────────

func TestSyncSchedule_Unit_MixedFixture_MapsFieldsCorrectly(t *testing.T) {
	ctx := context.Background()
	channelID := uuid.New()

	now := time.Now()
	draftPublishAt := now.Add(48 * time.Hour)
	yt := &fake.Client{Schedule: []youtube.Video{
		unitPublicVideo("vid-public", "A public upload", now.Add(-time.Hour)),
		unitScheduledDraft("vid-draft", "A scheduled draft", draftPublishAt),
	}}
	syncStore := newFakeSyncStore()
	a := newUnitActivities(channelID, yt, syncStore, &fakeTokenStore{})

	require.NoError(t, a.SyncSchedule(ctx, channelID))

	rows, err := syncStore.ListSchedule(ctx, channelID)
	require.NoError(t, err)
	require.Len(t, rows, 2)

	pub, ok := syncStore.byVideoID(channelID, "vid-public")
	require.True(t, ok)
	assert.Equal(t, store.PrivacyStatusPublic, pub.PrivacyStatus)
	assert.False(t, pub.IsScheduledDraft)
	assert.NotNil(t, pub.PublishedAt)
	assert.Nil(t, pub.PublishAt)

	draft, ok := syncStore.byVideoID(channelID, "vid-draft")
	require.True(t, ok)
	assert.Equal(t, store.PrivacyStatusPrivate, draft.PrivacyStatus)
	assert.True(t, draft.IsScheduledDraft)
	require.NotNil(t, draft.PublishAt)
	assert.Equal(t, draftPublishAt, *draft.PublishAt)
}

// ── double-run: same row count, same ids; only last_synced_at moves ────────

func TestSyncSchedule_Unit_DoubleRun_NoChurn_OnlyLastSyncedAtMoves(t *testing.T) {
	ctx := context.Background()
	channelID := uuid.New()

	yt := &fake.Client{Schedule: []youtube.Video{
		unitPublicVideo("vid-a", "Title A", time.Now().Add(-time.Hour)),
		unitScheduledDraft("vid-b", "Title B", time.Now().Add(48*time.Hour)),
	}}
	syncStore := newFakeSyncStore()
	a := newUnitActivities(channelID, yt, syncStore, &fakeTokenStore{})

	require.NoError(t, a.SyncSchedule(ctx, channelID))
	first, err := syncStore.ListSchedule(ctx, channelID)
	require.NoError(t, err)
	require.Len(t, first, 2)
	firstByID := make(map[string]store.SyncedVideo, len(first))
	for _, r := range first {
		firstByID[r.YouTubeVideoID] = r
	}

	time.Sleep(time.Millisecond)

	require.NoError(t, a.SyncSchedule(ctx, channelID))
	second, err := syncStore.ListSchedule(ctx, channelID)
	require.NoError(t, err)
	require.Len(t, second, 2, "re-running over unchanged YouTube data must not add or remove rows")

	for _, r := range second {
		prior, ok := firstByID[r.YouTubeVideoID]
		require.True(t, ok)
		assert.Equal(t, prior.ID, r.ID, "re-syncing unchanged data must update the existing row in place, never insert a new id")
		assert.True(t, r.LastSyncedAt.After(prior.LastSyncedAt), "last_synced_at must move forward on every cycle")
	}
	assert.Equal(t, 2, syncStore.upsertCalls, "SyncSchedule must call UpsertVideos exactly once per cycle")
}

// ── changed title on second run updates in place, same row id ──────────────

func TestSyncSchedule_Unit_ChangedTitleOnSecondRun_UpdatesInPlace(t *testing.T) {
	ctx := context.Background()
	channelID := uuid.New()

	yt := &fake.Client{Schedule: []youtube.Video{
		unitPublicVideo("vid-a", "Original Title", time.Now().Add(-time.Hour)),
	}}
	syncStore := newFakeSyncStore()
	a := newUnitActivities(channelID, yt, syncStore, &fakeTokenStore{})

	require.NoError(t, a.SyncSchedule(ctx, channelID))
	first, ok := syncStore.byVideoID(channelID, "vid-a")
	require.True(t, ok)
	originalID := first.ID

	yt.Schedule[0].Title = "Updated Title"
	require.NoError(t, a.SyncSchedule(ctx, channelID))

	second, ok := syncStore.byVideoID(channelID, "vid-a")
	require.True(t, ok)
	assert.Equal(t, originalID, second.ID)
	assert.Equal(t, "Updated Title", second.Title)

	rows, err := syncStore.ListSchedule(ctx, channelID)
	require.NoError(t, err)
	assert.Len(t, rows, 1, "a title change must update the existing row, never insert a second one")
}

// ── a video present on run 1, absent on run 2, is retained not deleted ─────

func TestSyncSchedule_Unit_VideoDisappearsOnSecondRun_RowRetained(t *testing.T) {
	ctx := context.Background()
	channelID := uuid.New()

	yt := &fake.Client{Schedule: []youtube.Video{
		unitPublicVideo("vid-stays", "Stays around", time.Now().Add(-time.Hour)),
		unitPublicVideo("vid-vanishes", "Disappears next cycle", time.Now().Add(-2*time.Hour)),
	}}
	syncStore := newFakeSyncStore()
	a := newUnitActivities(channelID, yt, syncStore, &fakeTokenStore{})

	require.NoError(t, a.SyncSchedule(ctx, channelID))
	before, ok := syncStore.byVideoID(channelID, "vid-vanishes")
	require.True(t, ok)

	yt.Schedule = []youtube.Video{
		unitPublicVideo("vid-stays", "Stays around", time.Now().Add(-time.Hour)),
	}
	require.NoError(t, a.SyncSchedule(ctx, channelID))

	after, ok := syncStore.byVideoID(channelID, "vid-vanishes")
	require.True(t, ok, "vid-vanishes' row must still exist after it drops out of the YouTube response")
	assert.Equal(t, before.ID, after.ID)
	assert.Equal(t, before.LastSyncedAt, after.LastSyncedAt, "a video not seen this cycle must keep its stale last_synced_at")

	rows, err := syncStore.ListSchedule(ctx, channelID)
	require.NoError(t, err)
	assert.Len(t, rows, 2, "a disappeared video's row must be retained, not deleted")
}

// ── ErrRevoked: needs-reauth, data retained, error non-retryable ───────────

func TestSyncSchedule_Unit_ErrRevoked_MarksNeedsReauthRetainsDataNonRetryable(t *testing.T) {
	ctx := context.Background()
	channelID := uuid.New()

	syncStore := newFakeSyncStore()
	require.NoError(t, syncStore.UpsertVideos(ctx, channelID, []store.SyncedVideo{{
		YouTubeVideoID: "vid-existing",
		Title:          "Existing video",
		PrivacyStatus:  store.PrivacyStatusPublic,
		LastSyncedAt:   time.Now(),
	}}))

	tokenStore := &fakeTokenStore{}
	yt := &fake.Client{Err: youtube.ErrRevoked}
	a := newUnitActivities(channelID, yt, syncStore, tokenStore)

	err := a.SyncSchedule(ctx, channelID)
	require.Error(t, err)

	var appErr *temporal.ApplicationError
	require.True(t, errors.As(err, &appErr), "ErrRevoked must surface as a temporal.ApplicationError, got %T: %v", err, err)
	assert.True(t, appErr.NonRetryable())
	assert.Equal(t, RevokedErrorType, appErr.Type())

	require.Len(t, tokenStore.markNeedsReauthCalls, 1)
	assert.Equal(t, channelID, tokenStore.markNeedsReauthCalls[0].channelID)

	rows, listErr := syncStore.ListSchedule(ctx, channelID)
	require.NoError(t, listErr)
	require.Len(t, rows, 1, "existing synced_video rows must be retained, not deleted, on a revoked credential")
	assert.Equal(t, 1, syncStore.upsertCalls, "UpsertVideos must not be called again after ListSchedule fails with ErrRevoked")
}

// ── ErrQuotaExceeded / ErrTransient: retryable, MarkNeedsReauth never called ─

func TestSyncSchedule_Unit_ErrQuotaExceededAndErrTransient_RetryableNoMarkNeedsReauth(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{name: "quota exceeded", err: youtube.ErrQuotaExceeded},
		{name: "transient", err: youtube.ErrTransient},
		{name: "permanent", err: youtube.ErrPermanent},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			channelID := uuid.New()

			tokenStore := &fakeTokenStore{}
			syncStore := newFakeSyncStore()
			yt := &fake.Client{Err: tc.err}
			a := newUnitActivities(channelID, yt, syncStore, tokenStore)

			err := a.SyncSchedule(ctx, channelID)
			require.Error(t, err)

			var appErr *temporal.ApplicationError
			if errors.As(err, &appErr) {
				assert.False(t, appErr.NonRetryable(), "%s must not classify as the non-retryable RevokedErrorType error", tc.name)
			}
			assert.Empty(t, tokenStore.markNeedsReauthCalls, "%s must never call MarkNeedsReauth", tc.name)
		})
	}
}

// ── ChannelStore lookup failure propagates, never calls YouTube ────────────

func TestSyncSchedule_Unit_ChannelLookupError_Propagates(t *testing.T) {
	ctx := context.Background()
	channelID := uuid.New()

	storeErr := errors.New("connection refused")
	a := &Activities{
		Channels:         &fakeChannelStore{getByIDErr: storeErr},
		Tokens:           &fakeTokenStore{},
		Sync:             newFakeSyncStore(),
		NewYouTubeClient: func(oauth2.TokenSource) youtube.Client { t.Fatal("must not build a YouTube client when the channel lookup fails"); return nil },
	}

	err := a.SyncSchedule(ctx, channelID)
	require.Error(t, err)
	assert.ErrorIs(t, err, storeErr)
}
