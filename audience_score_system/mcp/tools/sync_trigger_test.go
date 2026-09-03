package tools

// Pure-Go coverage of trigger_channel_sync's mutate step (sync_trigger.go,
// issue #1650): channel_id parsing/validation, propagating a
// nonexistent-channel error from store.ChannelStore.GetByID, and
// propagating a ScheduleTrigger.TriggerNow failure -- driven directly
// against in-memory fakes, entirely bypassing the MCP session/HTTP/auth
// plumbing and Temporal itself. No Docker required, runs as part of
// `bazel test //...`. Channel-scoping (store.CanWrite via
// server.RegisterWrite) is real-server integration coverage, not
// attempted here -- see this package's other *_integration_test.go files
// for that pattern.

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/store"
)

// fakeChannelStore is a minimal store.ChannelStore stand-in scoped to
// exactly what triggerChannelSyncMutate needs (GetByID).
type fakeChannelStore struct {
	channel store.Channel
	err     error
}

var _ store.ChannelStore = fakeChannelStore{}

func (f fakeChannelStore) Create(context.Context, string, string, uuid.UUID) (store.Channel, error) {
	return store.Channel{}, errors.New("fakeChannelStore.Create is not used by these tests")
}

func (f fakeChannelStore) GetByID(context.Context, uuid.UUID) (store.Channel, error) {
	return f.channel, f.err
}

func (f fakeChannelStore) GetByYouTubeChannelID(context.Context, string) (store.Channel, error) {
	return store.Channel{}, errors.New("fakeChannelStore.GetByYouTubeChannelID is not used by these tests")
}

func (f fakeChannelStore) SetConnectionState(context.Context, uuid.UUID, store.ConnectionState) error {
	return errors.New("fakeChannelStore.SetConnectionState is not used by these tests")
}

func (f fakeChannelStore) ListConnected(context.Context) ([]store.Channel, error) {
	return nil, errors.New("fakeChannelStore.ListConnected is not used by these tests")
}

// fakeScheduleTrigger is a minimal ScheduleTrigger stand-in.
type fakeScheduleTrigger struct {
	calls []uuid.UUID
	err   error
}

var _ ScheduleTrigger = &fakeScheduleTrigger{}

func (f *fakeScheduleTrigger) TriggerNow(_ context.Context, channelID uuid.UUID) error {
	f.calls = append(f.calls, channelID)
	return f.err
}

// ── triggerChannelSyncMutate ─────────────────────────────────────────────

func TestTriggerChannelSyncMutate_InvalidChannelID_Rejected(t *testing.T) {
	trigger := &fakeScheduleTrigger{}
	mutate := triggerChannelSyncMutate(fakeChannelStore{}, trigger)

	_, err := mutate(context.Background(), TriggerChannelSyncInput{ChannelID: "not-a-uuid"})
	require.Error(t, err)
	assert.Empty(t, trigger.calls, "TriggerNow must not be called for an unparseable channel_id")
}

func TestTriggerChannelSyncMutate_NonexistentChannel_Rejected(t *testing.T) {
	channelID := uuid.New()
	notFound := errors.New("channel not found")
	trigger := &fakeScheduleTrigger{}
	mutate := triggerChannelSyncMutate(fakeChannelStore{err: notFound}, trigger)

	_, err := mutate(context.Background(), TriggerChannelSyncInput{ChannelID: channelID.String()})
	require.Error(t, err)
	assert.ErrorIs(t, err, notFound)
	assert.Empty(t, trigger.calls, "TriggerNow must not be called when the channel lookup fails")
}

func TestTriggerChannelSyncMutate_TriggerError_Propagates(t *testing.T) {
	channelID := uuid.New()
	channels := fakeChannelStore{channel: store.Channel{ID: channelID}}
	triggerErr := errors.New("schedule not found")
	trigger := &fakeScheduleTrigger{err: triggerErr}
	mutate := triggerChannelSyncMutate(channels, trigger)

	_, err := mutate(context.Background(), TriggerChannelSyncInput{ChannelID: channelID.String()})
	require.Error(t, err)
	assert.ErrorIs(t, err, triggerErr)
}

func TestTriggerChannelSyncMutate_Success_ReturnsChannelIDAndCallsTrigger(t *testing.T) {
	channelID := uuid.New()
	channels := fakeChannelStore{channel: store.Channel{ID: channelID}}
	trigger := &fakeScheduleTrigger{}
	mutate := triggerChannelSyncMutate(channels, trigger)

	ref, err := mutate(context.Background(), TriggerChannelSyncInput{ChannelID: channelID.String()})
	require.NoError(t, err)
	assert.Equal(t, channelID, ref)
	assert.Equal(t, []uuid.UUID{channelID}, trigger.calls)
}

// ── triggerChannelSyncRender ─────────────────────────────────────────────

func TestTriggerChannelSyncRender_EchoesRefAsTriggered(t *testing.T) {
	channelID := uuid.New()
	render := triggerChannelSyncRender()

	_, out, err := render(context.Background(), channelID)
	require.NoError(t, err)
	assert.Equal(t, channelID.String(), out.ChannelID)
	assert.True(t, out.Triggered)
}

// ── ChannelScopeID ───────────────────────────────────────────────────────

func TestTriggerChannelSyncInput_ChannelScopeID(t *testing.T) {
	id := uuid.New()
	in := TriggerChannelSyncInput{ChannelID: id.String()}
	assert.Equal(t, id, in.ChannelScopeID())

	invalid := TriggerChannelSyncInput{ChannelID: "not-a-uuid"}
	assert.Equal(t, uuid.Nil, invalid.ChannelScopeID(), "an unparseable ChannelID must resolve to uuid.Nil, not panic")
}
