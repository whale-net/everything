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

	"github.com/whale-net/everything/audience_score_system/store"
	temporallib "github.com/whale-net/everything/libs/go/temporal"
)

// MinSyncInterval and MaxSyncInterval bound ASS_SYNC_INTERVAL to NFR4's
// ~1-24 hour cadence (widened again from the ~1-6 hour band: even 3h still
// spent the schedule-discovery search.list(forMine=true) call -- 100 quota
// units -- often enough per Channel to threaten YouTube's default daily
// project quota at M1's Channel count, so the default moved to 24h. On-demand
// freshness is available anytime via mcp's trigger_channel_sync tool
// (issue #1650), which is unaffected by this band -- see defaultSyncInterval
// in ../main.go and ../../web/main.go). ../main.go's config loader fails
// fast at startup if the configured interval falls outside this band -- see
// ValidateSyncInterval.
const (
	MinSyncInterval = 1 * time.Hour
	MaxSyncInterval = 24 * time.Hour
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
// to call repeatedly for the same Channel: temporallib.UpsertSchedule
// resolves an already-exists response from Temporal into an update of the
// existing schedule (issue #1742), not an error -- see ScheduleManager's
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
	// EnsureSchedule idempotently creates channelID's schedule, or -- on a
	// later call, e.g. after ASS_SYNC_INTERVAL changes and the process
	// restarts -- patches its Spec/Action/Overlap to match the current
	// m.Interval (temporallib.UpsertSchedule, issue #1742). ScheduleID
	// (channelID) is deterministic, so this is safe to call repeatedly for
	// the same Channel. Called by `web` after a successful Channel connect
	// (#1571) and by this worker's Reconcile at startup.
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

	// TriggerNow forces an immediate, out-of-band run of channelID's
	// ChannelSyncWorkflow via Temporal's schedule-trigger patch, rather
	// than waiting for the schedule's normal interval cadence -- issue
	// #1650: there was no way to verify a schedule/matching change took
	// effect without waiting up to ASS_SYNC_INTERVAL. Called by the MCP
	// trigger_channel_sync tool (mcp/tools/sync_trigger.go). Returns an
	// error if channelID has no schedule (e.g. never connected).
	TriggerNow(ctx context.Context, channelID uuid.UUID) error
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
// desired ScheduleSpec, which matters now that temporallib.UpsertSchedule
// actually reapplies it to an existing schedule (issue #1742) rather than
// only using it once at creation time.
func channelScheduleOffset(channelID uuid.UUID, interval time.Duration) time.Duration {
	if interval <= 0 {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write(channelID[:]) // hash.Hash.Write never errors
	return time.Duration(h.Sum64() % uint64(interval))
}

// EnsureSchedule idempotently creates-or-updates channelID's schedule: a
// ScheduleWorkflowAction targeting ChannelSyncWorkflow on TaskQueue, an
// interval ScheduleSpec of m.Interval with a per-Channel jitter offset
// (channelScheduleOffset), and SCHEDULE_OVERLAP_POLICY_SKIP so a slow run
// never stacks concurrent runs for the same Channel.
// temporallib.UpsertSchedule (issue #1742) reconciles an already-exists
// response from Temporal into an update of the existing schedule's
// Spec/Action/Overlap, rather than treating it as a no-op -- see
// ScheduleManager's doc comment.
func (m *scheduleManager) EnsureSchedule(ctx context.Context, channelID uuid.UUID) error {
	err := temporallib.UpsertSchedule(ctx, m.Schedules, client.ScheduleOptions{
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
		logger.WarnContext(ctx, "failed to ensure channel sync schedule", "channel_id", channelID, "error", err.Error())
		return fmt.Errorf("ensure schedule for channel %s: %w", channelID, err)
	}
	logger.InfoContext(ctx, "ensured channel sync schedule", "channel_id", channelID, "interval", m.Interval)
	return nil
}

// RemoveSchedule deletes channelID's schedule via
// Schedules.GetHandle(ScheduleID(channelID)).Delete -- see ScheduleManager's
// doc comment for why this is NOT called for a needs_reauth Channel.
func (m *scheduleManager) RemoveSchedule(ctx context.Context, channelID uuid.UUID) error {
	if err := m.Schedules.GetHandle(ctx, ScheduleID(channelID)).Delete(ctx); err != nil {
		return fmt.Errorf("remove schedule for channel %s: %w", channelID, err)
	}
	logger.InfoContext(ctx, "removed channel sync schedule", "channel_id", channelID)
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
	logger.InfoContext(ctx, "schedule reconcile complete", "channels_checked", len(channels), "errors", len(errs))
	if len(errs) > 0 {
		return fmt.Errorf("reconcile: %w", errors.Join(errs...))
	}
	return nil
}

// TriggerNow patches channelID's schedule with a TriggerImmediately request
// (client.ScheduleHandle.Trigger) -- see ScheduleManager's doc comment.
// Uses the schedule's own overlap policy (SCHEDULE_OVERLAP_POLICY_SKIP, set
// in EnsureSchedule) rather than overriding it, so a manual trigger racing
// an already-running cycle for the same Channel is skipped, not stacked.
func (m *scheduleManager) TriggerNow(ctx context.Context, channelID uuid.UUID) error {
	if err := m.Schedules.GetHandle(ctx, ScheduleID(channelID)).Trigger(ctx, client.ScheduleTriggerOptions{}); err != nil {
		return fmt.Errorf("trigger sync for channel %s: %w", channelID, err)
	}
	logger.InfoContext(ctx, "triggered out-of-band channel sync", "channel_id", channelID)
	return nil
}
