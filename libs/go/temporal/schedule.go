package temporal

import (
	"context"
	"errors"
	"fmt"

	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	sdktemporal "go.temporal.io/sdk/temporal"
)

// UpsertSchedule creates the schedule described by opts, or -- if a
// schedule with opts.ID already exists -- patches that schedule's Spec,
// Action, and Overlap policy to match opts instead of leaving it alone.
//
// client.ScheduleClient.Create's own already-exists response
// (sdktemporal.ErrScheduleAlreadyRunning) has to be tolerated as success by
// any idempotent caller, but treating it as a pure no-op means a
// schedule's parameters -- e.g. its interval -- are permanently pinned to
// whichever value its first caller passed at creation time and are never
// updated again, even after the configured value changes and the service
// restarts (issue #1742: audience_score_system's ASS_SYNC_INTERVAL moved
// from 20m to 24h but every already-connected Channel's schedule kept
// firing every 20m, because its original EnsureSchedule call-site treated
// the already-exists response as a no-op rather than reconciling it).
// UpsertSchedule closes that gap for any caller building a Create-once,
// EnsureSchedule-repeatedly pattern like that one.
//
// Describe is checked first rather than calling Create unconditionally:
// every caller of this function runs on every process restart (e.g.
// whagent_net/worker's ensureArchiveSchedule), so an existing schedule
// would otherwise make Create fail with ErrScheduleAlreadyRunning -- and
// the client-side tracing interceptor records that as an errored
// CreateSchedule span -- on every single restart, forever, even though
// the error is fully handled here. Checking existence first means that
// span only appears once, the very first time a given schedule ID is
// ever created; a Create call that still races and loses (opts.ID created
// by a concurrent caller between Describe and Create) is tolerated the
// same way it always was.
//
// The existing schedule's State (paused/note/limited-actions) is left
// untouched -- a caller who paused a schedule by hand should not have it
// silently resumed by an unrelated Upsert.
func UpsertSchedule(ctx context.Context, schedules client.ScheduleClient, opts client.ScheduleOptions) error {
	handle := schedules.GetHandle(ctx, opts.ID)
	_, describeErr := handle.Describe(ctx)
	if describeErr != nil {
		var notFound *serviceerror.NotFound
		if !errors.As(describeErr, &notFound) {
			return fmt.Errorf("describe schedule %q: %w", opts.ID, describeErr)
		}

		_, err := schedules.Create(ctx, opts)
		if err == nil {
			return nil
		}
		if !errors.Is(err, sdktemporal.ErrScheduleAlreadyRunning) {
			return fmt.Errorf("create schedule %q: %w", opts.ID, err)
		}
	}

	spec := opts.Spec
	updateErr := handle.Update(ctx, client.ScheduleUpdateOptions{
		DoUpdate: func(input client.ScheduleUpdateInput) (*client.ScheduleUpdate, error) {
			sched := input.Description.Schedule
			sched.Spec = &spec
			sched.Action = opts.Action
			if sched.Policy == nil {
				sched.Policy = &client.SchedulePolicies{}
			}
			sched.Policy.Overlap = opts.Overlap
			return &client.ScheduleUpdate{Schedule: &sched}, nil
		},
	})
	if updateErr != nil {
		return fmt.Errorf("update schedule %q: %w", opts.ID, updateErr)
	}
	return nil
}
