package sync

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"time"

	enumspb "go.temporal.io/api/enums/v1"

	"github.com/google/uuid"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"

	"github.com/whale-net/everything/audience_score_system/store"
)

// MinSyncInterval and MaxSyncInterval bound ASS_SYNC_INTERVAL to NFR4's
// ~15-30 minute cadence. ../main.go's config loader fails fast at startup
// if the configured interval falls outside this band -- see
// ValidateSyncInterval.
const (
	MinSyncInterval = 15 * time.Minute
	MaxSyncInterval = 30 * time.Minute
)

// ValidateSyncInterval reports an error if d falls outside
// [MinSyncInterval, MaxSyncInterval] (NFR4). Exported so ../main.go's
// startup config loader and this package's own tests share one
// definition of "in band" rather than each hand-rolling the comparison.
func ValidateSyncInterval(d time.Duration) error {
	if d < MinSyncInterval || d > MaxSyncInterval {
		return fmt.Errorf("sync interval %s is outside the NFR4 band [%s, %s]", d, MinSyncInterval, MaxSyncInterval)
	}
	return nil
}

// scheduleIDPrefix is ScheduleID's fixed prefix -- see that function's doc
// comment for the full deterministic id scheme.
const scheduleIDPrefix = "ass-channel-sync-"

// ScheduleID returns the deterministic Temporal schedule id for channelID:
// "ass-channel-sync-{channel_id}". Deterministic so EnsureSchedule is safe
// to call repeatedly for the same Channel (an already-exists response from
// Temporal is treated as success, not an error) -- see ScheduleManager's
// doc comment.
func ScheduleID(channelID uuid.UUID) string {
	return scheduleIDPrefix + channelID.String()
}

// ScheduleManager owns the Temporal client.ScheduleClient wiring for one
// ChannelSyncWorkflow schedule per connected Channel (issue #1574's
// Scaffold section: "the only in-repo scheduled-workflow precedent is
// friendly_computing_machine/temporal/base.py's Python
// AbstractScheduleWorkflow, which does not transfer" -- see
// ../../ARCHITECTURE.md for whether this is worth promoting to
// //libs/go/temporal later).
type ScheduleManager interface {
	// EnsureSchedule idempotently creates (or, on a later call, leaves
	// alone) channelID's schedule -- ScheduleID(channelID) is deterministic,
	// so an already-exists response from Temporal is success, not an
	// error. Called by `web` after a successful Channel connect (#1571)
	// and by this worker's Reconcile at startup.
	EnsureSchedule(ctx context.Context, channelID uuid.UUID) error

	// RemoveSchedule deletes channelID's schedule. NOT called for a
	// needs_reauth Channel (FR4's retention requirement: the skip happens
	// inside ChannelSyncWorkflow, which is what makes reconnect resume
	// automatic -- see that function's doc comment) -- reserved for a
	// Channel actually being removed from the system, which is out of M1
	// scope today.
	RemoveSchedule(ctx context.Context, channelID uuid.UUID) error

	// Reconcile ensures a schedule exists for every store.ChannelStore
	// ListConnected Channel -- run once at worker startup so a Channel
	// connected while the worker was down still gets a schedule.
	Reconcile(ctx context.Context) error
}

// scheduleManager is ScheduleManager's real implementation, wrapping a
// Temporal client.ScheduleClient (client.Client.ScheduleClient()) and the
// store.ChannelStore Reconcile enumerates.
type scheduleManager struct {
	Schedules client.ScheduleClient
	Channels  store.ChannelStore
	Interval  time.Duration
}

var _ ScheduleManager = (*scheduleManager)(nil)

// NewScheduleManager returns a ScheduleManager. interval must already
// satisfy ValidateSyncInterval -- NewScheduleManager does not re-validate
// it (the caller, ../main.go, validates once at startup and fails fast per
// NFR4 rather than silently clamping).
func NewScheduleManager(schedules client.ScheduleClient, channels store.ChannelStore, interval time.Duration) ScheduleManager {
	return &scheduleManager{Schedules: schedules, Channels: channels, Interval: interval}
}

// channelScheduleOffset returns a deterministic offset in [0, interval) for
// channelID, derived from an FNV-1a hash of the Channel's UUID bytes. Used
// as ScheduleIntervalSpec.Offset so N Channels sharing the same interval
// don't all fire at the same instant and stampede Google (issue #1574's
// Implementation scope) -- deterministic (not random) so repeated
// EnsureSchedule calls for the same Channel always compute the same
// ScheduleSpec, keeping Update-free idempotency simple.
func channelScheduleOffset(channelID uuid.UUID, interval time.Duration) time.Duration {
	if interval <= 0 {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write(channelID[:]) // hash.Hash.Write never errors
	return time.Duration(h.Sum64() % uint64(interval))
}

// EnsureSchedule idempotently creates channelID's schedule: a
// ScheduleWorkflowAction targeting ChannelSyncWorkflow on TaskQueue, an
// interval ScheduleSpec of m.Interval with a per-Channel jitter offset
// (channelScheduleOffset), and SCHEDULE_OVERLAP_POLICY_SKIP so a slow run
// never stacks concurrent runs for the same Channel. Temporal's
// already-exists response (temporal.ErrScheduleAlreadyRunning) is treated
// as success, not an error -- see ScheduleManager's doc comment.
func (m *scheduleManager) EnsureSchedule(ctx context.Context, channelID uuid.UUID) error {
	_, err := m.Schedules.Create(ctx, client.ScheduleOptions{
		ID: ScheduleID(channelID),
		Spec: client.ScheduleSpec{
			Intervals: []client.ScheduleIntervalSpec{{
				Every:  m.Interval,
				Offset: channelScheduleOffset(channelID, m.Interval),
			}},
		},
		Action: &client.ScheduleWorkflowAction{
			Workflow:  ChannelSyncWorkflow,
			Args:      []interface{}{ChannelSyncInput{ChannelID: channelID}},
			TaskQueue: TaskQueue,
		},
		Overlap: enumspb.SCHEDULE_OVERLAP_POLICY_SKIP,
	})
	if err != nil {
		if errors.Is(err, temporal.ErrScheduleAlreadyRunning) {
			return nil
		}
		return fmt.Errorf("ensure schedule for channel %s: %w", channelID, err)
	}
	return nil
}

// RemoveSchedule deletes channelID's schedule via
// Schedules.GetHandle(ScheduleID(channelID)).Delete -- see ScheduleManager's
// doc comment for why this is NOT called for a needs_reauth Channel.
func (m *scheduleManager) RemoveSchedule(ctx context.Context, channelID uuid.UUID) error {
	if err := m.Schedules.GetHandle(ctx, ScheduleID(channelID)).Delete(ctx); err != nil {
		return fmt.Errorf("remove schedule for channel %s: %w", channelID, err)
	}
	return nil
}

// Reconcile ensures a schedule exists for every m.Channels.ListConnected
// Channel -- run once at worker startup (../main.go) so a Channel
// connected while the worker was down still gets a schedule. Does not
// stop at the first failing Channel: it attempts every Channel and joins
// any errors, so one bad EnsureSchedule call can't mask the rest.
func (m *scheduleManager) Reconcile(ctx context.Context) error {
	channels, err := m.Channels.ListConnected(ctx)
	if err != nil {
		return fmt.Errorf("reconcile: list connected channels: %w", err)
	}

	var errs []error
	for _, ch := range channels {
		if err := m.EnsureSchedule(ctx, ch.ID); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("reconcile: %w", errors.Join(errs...))
	}
	return nil
}
