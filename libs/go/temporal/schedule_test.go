package temporal

import (
	"context"
	"errors"
	"testing"
	"time"

	enumspb "go.temporal.io/api/enums/v1"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"
	sdktemporal "go.temporal.io/sdk/temporal"
)

// ── fake client.ScheduleClient ──────────────────────────────────────────
//
// Embeds the real client.ScheduleClient interface (nil) so only the two
// methods UpsertSchedule actually calls (Create, GetHandle) need
// implementations -- List is never exercised and would nil-pointer panic
// if called, which is fine: a test that reaches it is itself broken.

type fakeScheduleClient struct {
	client.ScheduleClient

	createCalls []client.ScheduleOptions
	// createErr, if set, is returned by every Create call.
	createErr error

	// handle, if set, is what GetHandle returns regardless of id -- tests
	// only ever exercise one schedule id at a time.
	handle *fakeScheduleHandle
}

var _ client.ScheduleClient = (*fakeScheduleClient)(nil)

func (f *fakeScheduleClient) Create(_ context.Context, options client.ScheduleOptions) (client.ScheduleHandle, error) {
	f.createCalls = append(f.createCalls, options)
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &fakeScheduleHandle{id: options.ID}, nil
}

func (f *fakeScheduleClient) GetHandle(_ context.Context, scheduleID string) client.ScheduleHandle {
	if f.handle == nil {
		f.handle = &fakeScheduleHandle{id: scheduleID}
	}
	return f.handle
}

// fakeScheduleHandle simulates the server-side Update contract closely
// enough to test UpsertSchedule's DoUpdate callback: it actually invokes
// the callback against `current` and records the resulting *client.Schedule,
// rather than just recording that Update was called.
type fakeScheduleHandle struct {
	client.ScheduleHandle

	id string

	current   client.ScheduleDescription
	updateErr error

	updateCalls     int
	appliedSchedule *client.Schedule
}

func (h *fakeScheduleHandle) Update(_ context.Context, options client.ScheduleUpdateOptions) error {
	h.updateCalls++
	if h.updateErr != nil {
		return h.updateErr
	}
	result, err := options.DoUpdate(client.ScheduleUpdateInput{Description: h.current})
	if err != nil {
		return err
	}
	h.appliedSchedule = result.Schedule
	return nil
}

// ── UpsertSchedule ───────────────────────────────────────────────────────

// TestUpsertSchedule_CreatesWhenAbsent proves the common case -- no
// existing schedule -- goes through Create alone, never touching
// GetHandle/Update.
func TestUpsertSchedule_CreatesWhenAbsent(t *testing.T) {
	schedules := &fakeScheduleClient{}
	opts := client.ScheduleOptions{
		ID: "sched-1",
		Spec: client.ScheduleSpec{
			Intervals: []client.ScheduleIntervalSpec{{Every: 20 * time.Minute}},
		},
		Action:  &client.ScheduleWorkflowAction{Workflow: "MyWorkflow"},
		Overlap: enumspb.SCHEDULE_OVERLAP_POLICY_SKIP,
	}

	err := UpsertSchedule(context.Background(), schedules, opts)
	require.NoError(t, err)

	require.Len(t, schedules.createCalls, 1)
	require.Equal(t, opts, schedules.createCalls[0])
	require.Nil(t, schedules.handle, "must not fall through to GetHandle/Update when Create succeeds")
}

// TestUpsertSchedule_UpdatesWhenAlreadyExists proves the fix for issue
// #1742: when Create reports the schedule already exists, UpsertSchedule
// patches its Spec/Action/Overlap to the newly-requested opts rather than
// leaving the schedule's original parameters (e.g. its interval) in place
// forever.
func TestUpsertSchedule_UpdatesWhenAlreadyExists(t *testing.T) {
	schedules := &fakeScheduleClient{createErr: sdktemporal.ErrScheduleAlreadyRunning}
	schedules.handle = &fakeScheduleHandle{
		id: "sched-1",
		current: client.ScheduleDescription{
			Schedule: client.Schedule{
				Spec: &client.ScheduleSpec{
					Intervals: []client.ScheduleIntervalSpec{{Every: 20 * time.Minute}},
				},
				Action: &client.ScheduleWorkflowAction{Workflow: "OldWorkflow"},
				Policy: &client.SchedulePolicies{Overlap: enumspb.SCHEDULE_OVERLAP_POLICY_BUFFER_ONE},
				State:  &client.ScheduleState{Paused: true, Note: "manually paused"},
			},
		},
	}

	newSpec := client.ScheduleSpec{
		Intervals: []client.ScheduleIntervalSpec{{Every: 24 * time.Hour}},
	}
	opts := client.ScheduleOptions{
		ID:      "sched-1",
		Spec:    newSpec,
		Action:  &client.ScheduleWorkflowAction{Workflow: "NewWorkflow"},
		Overlap: enumspb.SCHEDULE_OVERLAP_POLICY_SKIP,
	}

	err := UpsertSchedule(context.Background(), schedules, opts)
	require.NoError(t, err)

	require.Equal(t, 1, schedules.handle.updateCalls)
	got := schedules.handle.appliedSchedule
	require.NotNil(t, got)
	require.Equal(t, &newSpec, got.Spec, "the interval must be updated to the newly-requested spec")
	require.Equal(t, opts.Action, got.Action)
	require.Equal(t, enumspb.SCHEDULE_OVERLAP_POLICY_SKIP, got.Policy.Overlap)
	require.True(t, got.State.Paused, "an existing pause must be preserved, not reset by an unrelated Upsert")
	require.Equal(t, "manually paused", got.State.Note)
}

// TestUpsertSchedule_OtherCreateErrorPropagatesWithoutUpdate proves
// UpsertSchedule does not swallow every Create error -- only the specific
// already-exists case -- and does not fall through to GetHandle/Update for
// an unrelated failure (e.g. Temporal unreachable).
func TestUpsertSchedule_OtherCreateErrorPropagatesWithoutUpdate(t *testing.T) {
	schedules := &fakeScheduleClient{createErr: errors.New("temporal unreachable")}

	err := UpsertSchedule(context.Background(), schedules, client.ScheduleOptions{ID: "sched-1"})
	require.Error(t, err)
	require.Nil(t, schedules.handle, "an unrelated Create error must not fall through to GetHandle/Update")
}

// TestUpsertSchedule_UpdateErrorPropagates proves an Update failure on the
// already-exists path is surfaced, not silently ignored.
func TestUpsertSchedule_UpdateErrorPropagates(t *testing.T) {
	schedules := &fakeScheduleClient{createErr: sdktemporal.ErrScheduleAlreadyRunning}
	schedules.handle = &fakeScheduleHandle{id: "sched-1", updateErr: errors.New("update conflict")}

	err := UpsertSchedule(context.Background(), schedules, client.ScheduleOptions{ID: "sched-1"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "sched-1")
}
