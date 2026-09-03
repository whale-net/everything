package sync

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/store"
)

// fakeChannelStore is a minimal store.ChannelStore stand-in -- mirrors
// ../../web/channel/channel_test.go's fakeChannelStore pattern -- scoped to
// exactly what LoadChannelState (GetByID) needs.
type fakeChannelStore struct {
	getByIDChannel store.Channel
	getByIDErr     error

	listConnectedChannels []store.Channel
	listConnectedErr      error
}

var _ store.ChannelStore = (*fakeChannelStore)(nil)

func (f *fakeChannelStore) Create(_ context.Context, _, _ string, _ uuid.UUID) (store.Channel, error) {
	return store.Channel{}, errors.New("not implemented")
}

func (f *fakeChannelStore) GetByID(_ context.Context, _ uuid.UUID) (store.Channel, error) {
	return f.getByIDChannel, f.getByIDErr
}

func (f *fakeChannelStore) GetByYouTubeChannelID(_ context.Context, _ string) (store.Channel, error) {
	return store.Channel{}, errors.New("not implemented")
}

func (f *fakeChannelStore) SetConnectionState(_ context.Context, _ uuid.UUID, _ store.ConnectionState) error {
	return errors.New("not implemented")
}

func (f *fakeChannelStore) ListConnected(_ context.Context) ([]store.Channel, error) {
	return f.listConnectedChannels, f.listConnectedErr
}

// TestActivities_LoadChannelState_MapsConnectionState proves
// LoadChannelState maps store.Channel.ConnectionState onto
// ChannelState.ConnectionState for both known values -- the field
// ChannelSyncWorkflow's skip-on-disconnected gate reads.
func TestActivities_LoadChannelState_MapsConnectionState(t *testing.T) {
	cases := []struct {
		name  string
		state store.ConnectionState
	}{
		{name: "connected", state: store.ConnectionStateConnected},
		{name: "needs_reauth", state: store.ConnectionStateNeedsReauth},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			channelID := uuid.New()
			channels := &fakeChannelStore{getByIDChannel: store.Channel{
				ID:              channelID,
				ConnectionState: tc.state,
			}}
			a := &Activities{Channels: channels}

			got, err := a.LoadChannelState(context.Background(), channelID)
			require.NoError(t, err)
			require.Equal(t, ChannelState{ConnectionState: tc.state}, got)
		})
	}
}

// TestActivities_LoadChannelState_StoreError proves a store.ChannelStore
// lookup failure surfaces as an error (wrapped, mentioning the activity
// name and channel id) rather than being swallowed into a zero-value
// ChannelState -- ChannelSyncWorkflow treats a LoadChannelState error as a
// workflow failure (see workflow.go), so a swallowed error here would
// silently misroute an unrelated store outage into "skip cleanly".
func TestActivities_LoadChannelState_StoreError(t *testing.T) {
	channelID := uuid.New()
	storeErr := errors.New("connection refused")
	channels := &fakeChannelStore{getByIDErr: storeErr}
	a := &Activities{Channels: channels}

	_, err := a.LoadChannelState(context.Background(), channelID)
	require.Error(t, err)
	require.ErrorIs(t, err, storeErr)
	require.Contains(t, err.Error(), channelID.String())
}

// TestActivities_SyncOutcomes_AlwaysSucceeds proves SyncOutcomes is a
// genuine permanent no-op stub (issue #1574's Scaffold status, pending
// #1581): it never errors, regardless of input, so ChannelSyncWorkflow's
// connected-Channel path is independently testable before #1581 lands.
// SyncSchedule is real as of #1576 -- see video_sync_test.go for its
// coverage.
func TestActivities_SyncOutcomes_AlwaysSucceeds(t *testing.T) {
	a := &Activities{}
	channelID := uuid.New()

	require.NoError(t, a.SyncOutcomes(context.Background(), channelID))
}
