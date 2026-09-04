//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. See //libs/go/dbtest's README and
// //audience_score_system/store/store_integration_test.go for the pattern
// this file follows: spin up a throwaway Postgres via dbtest, apply the
// real embedded migrations, then exercise SyncSchedule (video_sync.go)
// against it, with a youtube/fake.Client standing in for the real YouTube
// API (this task's #1576 Testing section) so no test here ever makes a
// live network call.
//
// tokens.Store is used for real too (tokens.NewStore, backed by the same
// Postgres pool) rather than faked: SyncSchedule never calls
// TokenSource(...).Token() itself (only a.NewYouTubeClient consumes the
// resulting oauth2.TokenSource, and that factory is swapped for a fake
// client here), so no channel_credential row or encryption key setup is
// needed for TokenSource to succeed -- only MarkNeedsReauth's real
// channel.connection_state flip actually matters to these tests.
package sync

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
	"golang.org/x/oauth2"

	"github.com/whale-net/everything/audience_score_system/migrate/schema"
	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/tokens"
	"github.com/whale-net/everything/audience_score_system/youtube"
	"github.com/whale-net/everything/audience_score_system/youtube/fake"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

// newSyncTestStack provisions an isolated Postgres database via dbtest,
// applies every migration in the real embedded schema, and returns a
// ready *store.Store plus the underlying dbtest.Postgres -- mirrors
// store_integration_test.go's newStore and tokens/store_integration_test.go's
// newTokensTestStack.
func newSyncTestStack(t *testing.T) (*store.Store, *dbtest.Postgres) {
	t.Helper()
	ctx := context.Background()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "apply every migration from the real embedded schema")

	return store.New(db.Pool), db
}

// setupSyncChannel creates a Person and a connected Channel it is the
// creator of -- mirrors store_integration_test.go's own setupChannel
// fixture, duplicated here since it is unexported in that package.
func setupSyncChannel(t *testing.T, ctx context.Context, st *store.Store) store.Channel {
	t.Helper()

	creator, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "creator@example.com", "Creator")
	require.NoError(t, err)

	ch, err := st.Channels().Create(ctx, "yt-"+uuid.NewString(), "Channel", creator.ID)
	require.NoError(t, err)

	return ch
}

// newSyncActivities builds an *Activities wired against st (real Channels/
// Sync/Matches/Tokens, all backed by db's Postgres) with NewYouTubeClient
// fixed to always return ytClient, regardless of the oauth2.TokenSource it
// is handed -- so every test below (SyncSchedule here, SyncOutcomes in
// outcomes_test.go) drives real upsert/matching/error-classification logic
// without ever making a live YouTube API call. Matches is wired here too
// (not just in an outcomes-specific helper) so both files share one
// fixture builder.
func newSyncActivities(st *store.Store, ytClient youtube.Client) *Activities {
	return &Activities{
		Channels:         st.Channels(),
		Tokens:           tokens.NewStore(nil, st.Channels(), [32]byte{}, tokens.Config{}), // pool arg unused: TokenSource is never exercised, see file doc comment
		Sync:             st.Sync(),
		Matches:          st.Matches(),
		NewYouTubeClient: func(oauth2.TokenSource) youtube.Client { return ytClient },
	}
}

func publicVideo(id, title string, publishedAt time.Time) youtube.Video {
	return youtube.Video{
		YouTubeVideoID: id,
		Title:          title,
		PrivacyStatus:  store.PrivacyStatusPublic,
		PublishedAt:    &publishedAt,
	}
}

func scheduledDraft(id, title string, publishAt time.Time) youtube.Video {
	return youtube.Video{
		YouTubeVideoID:   id,
		Title:            title,
		PrivacyStatus:    store.PrivacyStatusPrivate,
		PublishAt:        &publishAt,
		IsScheduledDraft: true,
	}
}

// ── mixed fixture: public upload, future draft, unlisted -> one row each ────

func TestSyncSchedule_MixedFixture_ProducesOneSyncedVideoRowEach(t *testing.T) {
	ctx := context.Background()
	st, _ := newSyncTestStack(t)
	ch := setupSyncChannel(t, ctx, st)

	now := time.Now()
	unlistedPublishedAt := now.Add(-24 * time.Hour)
	draftPublishAt := now.Add(48 * time.Hour)

	yt := &fake.Client{Schedule: []youtube.Video{
		publicVideo("vid-public", "A public upload", now.Add(-time.Hour)),
		scheduledDraft("vid-draft", "A scheduled draft", draftPublishAt),
		{
			YouTubeVideoID: "vid-unlisted",
			Title:          "An unlisted video",
			PrivacyStatus:  store.PrivacyStatusUnlisted,
			PublishedAt:    &unlistedPublishedAt,
		},
	}}
	a := newSyncActivities(st, yt)

	require.NoError(t, a.SyncSchedule(ctx, ch.ID))

	rows, _, err := st.Sync().ListSchedule(ctx, ch.ID, nil, nil, true, 0)
	require.NoError(t, err)
	require.Len(t, rows, 3, "one synced_video row per fixture video")

	byID := make(map[string]store.SyncedVideo, len(rows))
	for _, r := range rows {
		byID[r.YouTubeVideoID] = r
	}

	pub, ok := byID["vid-public"]
	require.True(t, ok)
	assert.Equal(t, store.PrivacyStatusPublic, pub.PrivacyStatus)
	assert.False(t, pub.IsScheduledDraft)
	assert.NotNil(t, pub.PublishedAt)

	draft, ok := byID["vid-draft"]
	require.True(t, ok)
	assert.Equal(t, store.PrivacyStatusPrivate, draft.PrivacyStatus)
	assert.True(t, draft.IsScheduledDraft, "a future-dated private video must be flagged is_scheduled_draft")
	require.NotNil(t, draft.PublishAt)
	assert.WithinDuration(t, draftPublishAt, *draft.PublishAt, time.Second)

	unlisted, ok := byID["vid-unlisted"]
	require.True(t, ok)
	assert.Equal(t, store.PrivacyStatusUnlisted, unlisted.PrivacyStatus)
	assert.False(t, unlisted.IsScheduledDraft)
}

// ── double-run: same row count, same ids; only last_synced_at moves ────────

func TestSyncSchedule_DoubleRun_NoChurn_OnlyLastSyncedAtMoves(t *testing.T) {
	ctx := context.Background()
	st, _ := newSyncTestStack(t)
	ch := setupSyncChannel(t, ctx, st)

	yt := &fake.Client{Schedule: []youtube.Video{
		publicVideo("vid-a", "Title A", time.Now().Add(-time.Hour)),
		scheduledDraft("vid-b", "Title B", time.Now().Add(48*time.Hour)),
	}}
	a := newSyncActivities(st, yt)

	require.NoError(t, a.SyncSchedule(ctx, ch.ID))
	first, _, err := st.Sync().ListSchedule(ctx, ch.ID, nil, nil, true, 0)
	require.NoError(t, err)
	require.Len(t, first, 2)

	firstByID := make(map[string]store.SyncedVideo, len(first))
	for _, r := range first {
		firstByID[r.YouTubeVideoID] = r
	}

	// Ensure the second cycle's last_synced_at is measurably later than the
	// first, so "only last_synced_at moves" is a meaningful assertion rather
	// than two timestamps that happen to collide.
	time.Sleep(10 * time.Millisecond)

	require.NoError(t, a.SyncSchedule(ctx, ch.ID))
	second, _, err := st.Sync().ListSchedule(ctx, ch.ID, nil, nil, true, 0)
	require.NoError(t, err)
	require.Len(t, second, 2, "re-running over unchanged YouTube data must not add or remove rows")

	for _, r := range second {
		prior, ok := firstByID[r.YouTubeVideoID]
		require.True(t, ok, "video %s must keep the same id across runs (video_schedule_match FK)", r.YouTubeVideoID)
		assert.Equal(t, prior.ID, r.ID, "re-syncing unchanged data must update the existing row in place, never insert a new id")
		assert.Equal(t, prior.Title, r.Title)
		assert.Equal(t, prior.PrivacyStatus, r.PrivacyStatus)
		assert.True(t, r.LastSyncedAt.After(prior.LastSyncedAt), "last_synced_at must move forward on every cycle, even with unchanged data")
	}
}

// ── changed title on second run updates in place, same row id ──────────────

func TestSyncSchedule_ChangedTitleOnSecondRun_UpdatesInPlace(t *testing.T) {
	ctx := context.Background()
	st, _ := newSyncTestStack(t)
	ch := setupSyncChannel(t, ctx, st)

	yt := &fake.Client{Schedule: []youtube.Video{
		publicVideo("vid-a", "Original Title", time.Now().Add(-time.Hour)),
	}}
	a := newSyncActivities(st, yt)

	require.NoError(t, a.SyncSchedule(ctx, ch.ID))
	first, _, err := st.Sync().ListSchedule(ctx, ch.ID, nil, nil, true, 0)
	require.NoError(t, err)
	require.Len(t, first, 1)
	originalID := first[0].ID

	yt.Schedule[0].Title = "Updated Title"
	require.NoError(t, a.SyncSchedule(ctx, ch.ID))

	second, _, err := st.Sync().ListSchedule(ctx, ch.ID, nil, nil, true, 0)
	require.NoError(t, err)
	require.Len(t, second, 1, "a title change must update the existing row, never insert a second one")
	assert.Equal(t, originalID, second[0].ID)
	assert.Equal(t, "Updated Title", second[0].Title)
}

// ── a video present on run 1, absent on run 2, is retained not deleted ─────

func TestSyncSchedule_VideoDisappearsOnSecondRun_RowRetained(t *testing.T) {
	ctx := context.Background()
	st, _ := newSyncTestStack(t)
	ch := setupSyncChannel(t, ctx, st)

	yt := &fake.Client{Schedule: []youtube.Video{
		publicVideo("vid-stays", "Stays around", time.Now().Add(-time.Hour)),
		publicVideo("vid-vanishes", "Disappears next cycle", time.Now().Add(-2*time.Hour)),
	}}
	a := newSyncActivities(st, yt)

	require.NoError(t, a.SyncSchedule(ctx, ch.ID))
	first, _, err := st.Sync().ListSchedule(ctx, ch.ID, nil, nil, true, 0)
	require.NoError(t, err)
	require.Len(t, first, 2)

	var vanishedRowBefore store.SyncedVideo
	for _, r := range first {
		if r.YouTubeVideoID == "vid-vanishes" {
			vanishedRowBefore = r
		}
	}
	require.NotZero(t, vanishedRowBefore.ID, "fixture setup sanity check")

	// Second cycle: YouTube no longer reports vid-vanishes.
	yt.Schedule = []youtube.Video{
		publicVideo("vid-stays", "Stays around", time.Now().Add(-time.Hour)),
	}
	require.NoError(t, a.SyncSchedule(ctx, ch.ID))

	second, _, err := st.Sync().ListSchedule(ctx, ch.ID, nil, nil, true, 0)
	require.NoError(t, err)
	require.Len(t, second, 2, "a disappeared video's row must be retained, not deleted")

	var vanishedRowAfter store.SyncedVideo
	found := false
	for _, r := range second {
		if r.YouTubeVideoID == "vid-vanishes" {
			vanishedRowAfter = r
			found = true
		}
	}
	require.True(t, found, "vid-vanishes' synced_video row must still exist after it drops out of the YouTube response")
	assert.Equal(t, vanishedRowBefore.ID, vanishedRowAfter.ID)
	assert.Equal(t, vanishedRowBefore.LastSyncedAt.UTC(), vanishedRowAfter.LastSyncedAt.UTC(), "a video not seen this cycle must keep its stale last_synced_at, not be silently refreshed")
}

// ── ErrRevoked: needs-reauth, data retained, error non-retryable ───────────

func TestSyncSchedule_ErrRevoked_MarksNeedsReauthRetainsDataNonRetryable(t *testing.T) {
	ctx := context.Background()
	st, _ := newSyncTestStack(t)
	ch := setupSyncChannel(t, ctx, st)

	// Seed one existing synced_video row directly, so this test can prove
	// ErrRevoked leaves already-synced data alone (FR4), not merely that it
	// fails to add new rows.
	require.NoError(t, st.Sync().UpsertVideos(ctx, ch.ID, []store.SyncedVideo{{
		YouTubeVideoID: "vid-existing",
		Title:          "Existing video",
		PrivacyStatus:  store.PrivacyStatusPublic,
		LastSyncedAt:   time.Now(),
	}}))

	yt := &fake.Client{Err: youtube.ErrRevoked}
	a := newSyncActivities(st, yt)

	err := a.SyncSchedule(ctx, ch.ID)
	require.Error(t, err)

	var appErr *temporal.ApplicationError
	require.True(t, errors.As(err, &appErr), "ErrRevoked must surface as a temporal.ApplicationError, got %T: %v", err, err)
	assert.True(t, appErr.NonRetryable(), "a revoked credential must classify as non-retryable so no further YouTube quota is burned retrying")
	assert.Equal(t, RevokedErrorType, appErr.Type())

	got, getErr := st.Channels().GetByID(ctx, ch.ID)
	require.NoError(t, getErr)
	assert.Equal(t, store.ConnectionStateNeedsReauth, got.ConnectionState, "ErrRevoked must mark the Channel needs-reauth (FR4)")

	rows, _, listErr := st.Sync().ListSchedule(ctx, ch.ID, nil, nil, true, 0)
	require.NoError(t, listErr)
	require.Len(t, rows, 1, "existing synced_video rows must be retained, not deleted, on a revoked credential")
	assert.Equal(t, "vid-existing", rows[0].YouTubeVideoID)
}

// ── ErrQuotaExceeded: retryable, connection_state unchanged ────────────────

func TestSyncSchedule_ErrQuotaExceeded_RetryableConnectionStateUnchanged(t *testing.T) {
	ctx := context.Background()
	st, _ := newSyncTestStack(t)
	ch := setupSyncChannel(t, ctx, st)

	yt := &fake.Client{Err: youtube.ErrQuotaExceeded}
	a := newSyncActivities(st, yt)

	err := a.SyncSchedule(ctx, ch.ID)
	require.Error(t, err)

	var appErr *temporal.ApplicationError
	if errors.As(err, &appErr) {
		assert.False(t, appErr.NonRetryable(), "ErrQuotaExceeded must not classify as the non-retryable RevokedErrorType error")
	}
	assert.NotEqual(t, RevokedErrorType, errTypeIfAny(err), "ErrQuotaExceeded must never be classified as revoked")

	got, getErr := st.Channels().GetByID(ctx, ch.ID)
	require.NoError(t, getErr)
	assert.Equal(t, store.ConnectionStateConnected, got.ConnectionState, "a quota error must NOT trip needs-reauth -- connection_state stays connected")
}

// ── ErrTransient: also retryable, connection_state unchanged ───────────────

func TestSyncSchedule_ErrTransient_RetryableConnectionStateUnchanged(t *testing.T) {
	ctx := context.Background()
	st, _ := newSyncTestStack(t)
	ch := setupSyncChannel(t, ctx, st)

	yt := &fake.Client{Err: youtube.ErrTransient}
	a := newSyncActivities(st, yt)

	err := a.SyncSchedule(ctx, ch.ID)
	require.Error(t, err)
	assert.NotEqual(t, RevokedErrorType, errTypeIfAny(err), "ErrTransient must never be classified as revoked")

	got, getErr := st.Channels().GetByID(ctx, ch.ID)
	require.NoError(t, getErr)
	assert.Equal(t, store.ConnectionStateConnected, got.ConnectionState, "a transient error must NOT trip needs-reauth -- connection_state stays connected")
}

// errTypeIfAny returns err's temporal.ApplicationError.Type() if err (or
// something it wraps) is one, else "".
func errTypeIfAny(err error) string {
	var appErr *temporal.ApplicationError
	if errors.As(err, &appErr) {
		return appErr.Type()
	}
	return ""
}
