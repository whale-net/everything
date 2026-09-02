package sync

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/client"

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
//
// EnsureSchedule/RemoveSchedule/Reconcile are all unimplemented at
// Scaffold time -- issue #1574's own Implementation phase adds the real
// ScheduleSpec (interval + per-Channel jitter/offset so N Channels don't
// stampede Google at the same instant), overlap-skip policy, and
// idempotent create-or-update logic described there.
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

// EnsureSchedule is unimplemented at scaffold time. See ScheduleManager's
// doc comment and issue #1574's Implementation scope: the real
// implementation calls Schedules.Create with a ScheduleWorkflowAction
// targeting ChannelSyncWorkflow on TaskQueue, an interval ScheduleSpec
// derived from m.Interval plus a per-Channel jitter/offset, and
// SCHEDULE_OVERLAP_POLICY_SKIP -- tolerating (as success) the
// already-exists error Temporal returns when ScheduleID(channelID) is
// already in use.
func (m *scheduleManager) EnsureSchedule(ctx context.Context, channelID uuid.UUID) error {
	return unimplemented("EnsureSchedule")
}

// RemoveSchedule is unimplemented at scaffold time. See ScheduleManager's
// doc comment and issue #1574's Implementation scope: the real
// implementation calls Schedules.GetHandle(ScheduleID(channelID)).Delete.
func (m *scheduleManager) RemoveSchedule(ctx context.Context, channelID uuid.UUID) error {
	return unimplemented("RemoveSchedule")
}

// Reconcile is unimplemented at scaffold time. See ScheduleManager's doc
// comment and issue #1574's Implementation scope: the real implementation
// lists m.Channels.ListConnected and calls m.EnsureSchedule for each.
func (m *scheduleManager) Reconcile(ctx context.Context) error {
	return unimplemented("Reconcile")
}
