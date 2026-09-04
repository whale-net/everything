package tools

// Pure-Go coverage of get_channel_schedule's thin handler (schedule_read.go,
// issue #1576), driven directly against an in-memory store.SyncStore fake,
// entirely bypassing the MCP session/HTTP/auth plumbing. No Docker
// required, runs as part of `bazel test //...`.
//
// The from/to window, include_drafts, and limit/truncated filtering this
// handler used to implement in Go moved into store.SyncStore.ListSchedule's
// real SQL (issue #1808/#1812's follow-up: filtering belongs against
// Postgres, not re-implemented over an unbounded Go-side fetch) -- that
// behavior is now covered by schedule_read_integration_test.go
// ("integration" gotag, requires Docker) against the real embedded schema,
// not fakeable here without duplicating the SQL logic in Go.

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
// what getChannelScheduleHandler needs (ListSchedule) -- it does not
// replicate the real store's from/to/include_drafts/limit filtering, since
// that now lives in real SQL (see this file's package doc comment).
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

func (f fakeSyncStore) ListSchedule(context.Context, uuid.UUID, *time.Time, *time.Time, bool, int) ([]store.SyncedVideo, bool, error) {
	return f.videos, false, f.err
}

func (f fakeSyncStore) GetByID(context.Context, uuid.UUID) (store.SyncedVideo, error) {
	return store.SyncedVideo{}, errors.New("fakeSyncStore.GetByID is not used by these tests")
}

func (f fakeSyncStore) LatestMetricsFor(context.Context, uuid.UUID) (*store.VideoMetrics, error) {
	return nil, errors.New("fakeSyncStore.LatestMetricsFor is not used by these tests")
}

// ── getChannelScheduleHandler ────────────────────────────────────────────

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
