package sync

import (
	"context"
	"errors"
	"testing"
	"time"

	enumspb "go.temporal.io/api/enums/v1"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"

	"github.com/whale-net/everything/audience_score_system/store"
)

// channelsWithIDs builds the []store.Channel slice fakeChannelStore's
// listConnectedChannels field expects, one bare Channel per id -- Reconcile
// only reads ch.ID (see schedule.go), so no other field needs populating.
func channelsWithIDs(ids ...uuid.UUID) []store.Channel {
	channels := make([]store.Channel, len(ids))
	for i, id := range ids {
		channels[i] = store.Channel{ID: id}
	}
	return channels
}

// ── fake client.ScheduleClient ──────────────────────────────────────────
//
// Embeds the real client.ScheduleClient interface (nil) so only the two
// methods ScheduleManager actually calls (Create, GetHandle) need
// implementations -- List is never exercised by schedule.go and would
// nil-pointer panic if called, which is fine: a test that reaches it is
// itself broken.

type fakeScheduleClient struct {
	client.ScheduleClient

	createCalls []client.ScheduleOptions
	// createErrByID lets a test fail Create for specific schedule IDs only
	// (e.g. Reconcile's join-not-abort test), leaving others to succeed.
	createErrByID map[string]error
	// createErr, if set, is returned for every Create call regardless of ID.
	createErr error

	deleteCalls []string
	deleteErr   error
}

var _ client.ScheduleClient = (*fakeScheduleClient)(nil)

func (f *fakeScheduleClient) Create(_ context.Context, options client.ScheduleOptions) (client.ScheduleHandle, error) {
	f.createCalls = append(f.createCalls, options)
	if f.createErr != nil {
		return nil, f.createErr
	}
	if err, ok := f.createErrByID[options.ID]; ok && err != nil {
		return nil, err
	}
	return &fakeScheduleHandle{id: options.ID, owner: f}, nil
}

func (f *fakeScheduleClient) GetHandle(_ context.Context, scheduleID string) client.ScheduleHandle {
	return &fakeScheduleHandle{id: scheduleID, owner: f}
}

type fakeScheduleHandle struct {
	client.ScheduleHandle

	id    string
	owner *fakeScheduleClient
}

func (h *fakeScheduleHandle) GetID() string { return h.id }

func (h *fakeScheduleHandle) Delete(_ context.Context) error {
	h.owner.deleteCalls = append(h.owner.deleteCalls, h.id)
	return h.owner.deleteErr
}

// ── ScheduleID ───────────────────────────────────────────────────────────

// TestScheduleID_Deterministic proves ScheduleID follows the documented
// "ass-channel-sync-{channel_id}" scheme and is stable across repeated
// calls for the same Channel -- what makes EnsureSchedule safe to call
// repeatedly.
func TestScheduleID_Deterministic(t *testing.T) {
	channelID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	want := "ass-channel-sync-22222222-2222-2222-2222-222222222222"

	require.Equal(t, want, ScheduleID(channelID))
	require.Equal(t, ScheduleID(channelID), ScheduleID(channelID), "must be stable across repeated calls")
}

// ── channelScheduleOffset ────────────────────────────────────────────────

// TestChannelScheduleOffset_Deterministic proves the jitter offset is a
// pure function of (channelID, interval): identical inputs yield identical
// output -- required for EnsureSchedule's idempotency (repeated calls must
// build the same ScheduleSpec).
func TestChannelScheduleOffset_Deterministic(t *testing.T) {
	channelID := uuid.New()
	interval := 20 * time.Minute

	got1 := channelScheduleOffset(channelID, interval)
	got2 := channelScheduleOffset(channelID, interval)
	require.Equal(t, got1, got2)
}

// TestChannelScheduleOffset_WithinInterval proves the offset always falls
// in [0, interval) -- the ScheduleIntervalSpec.Offset contract -- across a
// range of Channel ids and interval bands.
func TestChannelScheduleOffset_WithinInterval(t *testing.T) {
	intervals := []time.Duration{MinSyncInterval, 20 * time.Minute, MaxSyncInterval}
	for _, interval := range intervals {
		for i := 0; i < 25; i++ {
			channelID := uuid.New()
			offset := channelScheduleOffset(channelID, interval)
			require.GreaterOrEqualf(t, offset, time.Duration(0), "interval=%s channelID=%s", interval, channelID)
			require.Lessf(t, offset, interval, "interval=%s channelID=%s", interval, channelID)
		}
	}
}

// TestChannelScheduleOffset_DiffersAcrossChannels proves distinct Channels
// don't all collapse onto the same offset (the whole point of the jitter:
// N Channels sharing an interval must not stampede Google at the same
// instant). Uses fixed ids rather than random ones so the test itself is
// deterministic.
func TestChannelScheduleOffset_DiffersAcrossChannels(t *testing.T) {
	interval := 20 * time.Minute
	a := channelScheduleOffset(uuid.MustParse("11111111-1111-1111-1111-111111111111"), interval)
	b := channelScheduleOffset(uuid.MustParse("22222222-2222-2222-2222-222222222222"), interval)
	require.NotEqual(t, a, b)
}

// TestChannelScheduleOffset_ZeroInterval proves the degenerate interval<=0
// case returns 0 rather than dividing by zero (defensive: ScheduleManager
// callers are expected to have already validated the interval via
// ValidateSyncInterval, but this function must not panic if that
// invariant is ever violated).
func TestChannelScheduleOffset_ZeroInterval(t *testing.T) {
	require.Equal(t, time.Duration(0), channelScheduleOffset(uuid.New(), 0))
}

// ── ValidateSyncInterval (NFR4) ──────────────────────────────────────────

func TestValidateSyncInterval(t *testing.T) {
	cases := []struct {
		name    string
		d       time.Duration
		wantErr bool
	}{
		{name: "below band", d: 14*time.Minute + 59*time.Second, wantErr: true},
		{name: "lower bound inclusive", d: 15 * time.Minute, wantErr: false},
		{name: "mid band", d: 20 * time.Minute, wantErr: false},
		{name: "upper bound inclusive", d: 30 * time.Minute, wantErr: false},
		{name: "above band", d: 30*time.Minute + time.Second, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSyncInterval(tc.d)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// ── EnsureSchedule ───────────────────────────────────────────────────────

// TestEnsureSchedule_BuildsExpectedScheduleOptions proves EnsureSchedule
// wires the deterministic id, interval + per-Channel jitter offset,
// ChannelSyncWorkflow target on TaskQueue, and overlap-skip policy issue
// #1574's Implementation section specifies.
func TestEnsureSchedule_BuildsExpectedScheduleOptions(t *testing.T) {
	channels := &fakeChannelStore{}
	schedules := &fakeScheduleClient{}
	interval := 20 * time.Minute
	m := NewScheduleManager(schedules, channels, interval)

	channelID := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	err := m.EnsureSchedule(context.Background(), channelID)
	require.NoError(t, err)

	require.Len(t, schedules.createCalls, 1)
	got := schedules.createCalls[0]
	require.Equal(t, ScheduleID(channelID), got.ID)
	require.Equal(t, enumspb.SCHEDULE_OVERLAP_POLICY_SKIP, got.Overlap)
	require.Len(t, got.Spec.Intervals, 1)
	require.Equal(t, interval, got.Spec.Intervals[0].Every)
	require.Equal(t, channelScheduleOffset(channelID, interval), got.Spec.Intervals[0].Offset)

	action, ok := got.Action.(*client.ScheduleWorkflowAction)
	require.True(t, ok, "Action must be a *client.ScheduleWorkflowAction")
	require.Equal(t, TaskQueue, action.TaskQueue)
	require.Equal(t, []interface{}{ChannelSyncInput{ChannelID: channelID}}, action.Args)
}

// TestEnsureSchedule_IdempotentAcrossRepeatedCalls proves calling
// EnsureSchedule twice for the same Channel is safe: Temporal's
// already-exists response (temporal.ErrScheduleAlreadyRunning) is treated
// as success, not surfaced as an error.
func TestEnsureSchedule_IdempotentAcrossRepeatedCalls(t *testing.T) {
	channels := &fakeChannelStore{}
	channelID := uuid.New()
	schedules := &fakeScheduleClient{
		createErrByID: map[string]error{ScheduleID(channelID): temporal.ErrScheduleAlreadyRunning},
	}
	m := NewScheduleManager(schedules, channels, 20*time.Minute)

	// Simulates a second EnsureSchedule call for a Channel whose schedule
	// already exists: the fake is pre-loaded to return
	// ErrScheduleAlreadyRunning for this schedule id, exactly what the real
	// Temporal server returns on a repeat Create.
	err := m.EnsureSchedule(context.Background(), channelID)
	require.NoError(t, err, "an already-exists response must be tolerated, not surfaced as an error")
}

// TestEnsureSchedule_OtherErrorPropagates proves EnsureSchedule does not
// swallow every error -- only the specific already-exists case -- so a
// real failure (e.g. Temporal unreachable) is not silently treated as
// success.
func TestEnsureSchedule_OtherErrorPropagates(t *testing.T) {
	channels := &fakeChannelStore{}
	channelID := uuid.New()
	schedules := &fakeScheduleClient{createErr: errors.New("temporal unreachable")}
	m := NewScheduleManager(schedules, channels, 20*time.Minute)

	err := m.EnsureSchedule(context.Background(), channelID)
	require.Error(t, err)
	require.Contains(t, err.Error(), channelID.String())
}

// ── RemoveSchedule ───────────────────────────────────────────────────────

func TestRemoveSchedule_DeletesByDeterministicID(t *testing.T) {
	channels := &fakeChannelStore{}
	schedules := &fakeScheduleClient{}
	m := NewScheduleManager(schedules, channels, 20*time.Minute)

	channelID := uuid.New()
	require.NoError(t, m.RemoveSchedule(context.Background(), channelID))
	require.Equal(t, []string{ScheduleID(channelID)}, schedules.deleteCalls)
}

func TestRemoveSchedule_PropagatesDeleteError(t *testing.T) {
	channels := &fakeChannelStore{}
	schedules := &fakeScheduleClient{deleteErr: errors.New("not found")}
	m := NewScheduleManager(schedules, channels, 20*time.Minute)

	channelID := uuid.New()
	err := m.RemoveSchedule(context.Background(), channelID)
	require.Error(t, err)
	require.Contains(t, err.Error(), channelID.String())
}

// ── Reconcile ────────────────────────────────────────────────────────────

// TestReconcile_EnsuresScheduleForEveryConnectedChannel proves Reconcile
// calls EnsureSchedule once per store.ChannelStore.ListConnected Channel.
func TestReconcile_EnsuresScheduleForEveryConnectedChannel(t *testing.T) {
	ch1, ch2 := uuid.New(), uuid.New()
	channels := &fakeChannelStore{listConnectedChannels: channelsWithIDs(ch1, ch2)}
	schedules := &fakeScheduleClient{}
	m := NewScheduleManager(schedules, channels, 20*time.Minute)

	require.NoError(t, m.Reconcile(context.Background()))

	require.Len(t, schedules.createCalls, 2)
	gotIDs := []string{schedules.createCalls[0].ID, schedules.createCalls[1].ID}
	require.ElementsMatch(t, []string{ScheduleID(ch1), ScheduleID(ch2)}, gotIDs)
}

// TestReconcile_JoinsPerChannelErrors_DoesNotAbort proves Reconcile does
// not stop at the first failing Channel: every Channel is attempted, and
// the failures are joined into one returned error rather than the first
// error masking the rest (schedule.go's Reconcile doc comment).
func TestReconcile_JoinsPerChannelErrors_DoesNotAbort(t *testing.T) {
	ok1, bad, ok2 := uuid.New(), uuid.New(), uuid.New()
	channels := &fakeChannelStore{listConnectedChannels: channelsWithIDs(ok1, bad, ok2)}
	schedules := &fakeScheduleClient{
		createErrByID: map[string]error{ScheduleID(bad): errors.New("quota exceeded creating schedule")},
	}
	m := NewScheduleManager(schedules, channels, 20*time.Minute)

	err := m.Reconcile(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), bad.String(), "the failing Channel's error must be present in the joined error")

	require.Len(t, schedules.createCalls, 3, "every Channel must be attempted even though one failed")
	gotIDs := []string{schedules.createCalls[0].ID, schedules.createCalls[1].ID, schedules.createCalls[2].ID}
	require.ElementsMatch(t, []string{ScheduleID(ok1), ScheduleID(bad), ScheduleID(ok2)}, gotIDs)
}

// TestReconcile_ListConnectedError_PropagatesWithoutCreatingSchedules
// proves a ListConnected failure short-circuits Reconcile before any
// EnsureSchedule call -- there is no Channel list to iterate.
func TestReconcile_ListConnectedError_PropagatesWithoutCreatingSchedules(t *testing.T) {
	channels := &fakeChannelStore{listConnectedErr: errors.New("db unavailable")}
	schedules := &fakeScheduleClient{}
	m := NewScheduleManager(schedules, channels, 20*time.Minute)

	err := m.Reconcile(context.Background())
	require.Error(t, err)
	require.Empty(t, schedules.createCalls)
}
