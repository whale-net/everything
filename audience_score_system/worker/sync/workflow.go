// Package sync is the per-Channel Temporal scheduled-sync machinery worker
// runs (issue #1574, FR14/FR21, NFR4): ChannelSyncWorkflow, the
// SyncActivities interface both C6 schedule sync (#1576) and C9 outcome
// sync (#1581) plug their real activities into, and ScheduleManager
// (schedule.go), the Go wiring on top of the Temporal SDK's
// client.ScheduleClient that creates/reconciles one schedule per connected
// Channel.
//
// # Determinism
//
// ChannelSyncWorkflow is replayed by the Temporal SDK, so its code must be
// deterministic: no direct network/disk I/O, no time.Now(), nothing that
// could produce a different result on replay than it did the first time.
// Every side effect (reading connection_state, calling the YouTube Data/
// Analytics APIs) lives behind the SyncActivities interface below and is
// invoked only via workflow.ExecuteActivity -- mirrors
// tools/app_registry/worker/release/workflow.go's identical discipline and
// AGENTS.md/PLAN.md's "Workflow determinism" hazard.
//
// # Scaffold status
//
// ChannelSyncWorkflow's control flow below is NOT a stub: its skip-on-
// disconnected branch (FR14) and non-retryable-revoked clean-end branch
// (FR4) are the machinery this task's Scaffold phase settles, so
// Testing-phase work (workflow_test.go) asserts against it as-is.
// activities.go's LoadChannelState is fully implemented (real
// store.ChannelStore-backed lookup) as of #1574's Implementation phase.
// SyncSchedule is now real too (video_sync.go, issue #1576). SyncOutcomes
// remains a genuine, permanent no-op stub (returns nil, not an error) --
// issue #1574's Scaffold section calls these "no-op stub implementations"
// specifically so this package is independently buildable and testable
// before #1576/#1581 land with their real implementations, which plug
// into these same methods rather than editing this package.
package sync

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/whale-net/everything/audience_score_system/store"
)

// TaskQueue is the Temporal task queue ChannelSyncWorkflow and its
// activities are registered/dispatched on (issue #1574's Scaffold
// section).
const TaskQueue = "audience-score-system-sync"

// Activity name constants. ChannelSyncWorkflow dispatches activities by
// these string names (not by Go method value) so the workflow depends on
// the SyncActivities interface, not a concrete implementation -- mirrors
// tools/app_registry/worker/release's ActivityCheckApproval convention.
// The worker registers whatever SyncActivities implementation it is built
// with under these same names (see ../main.go).
const (
	ActivityLoadChannelState = "LoadChannelState"
	ActivitySyncSchedule     = "SyncSchedule"
	ActivitySyncOutcomes     = "SyncOutcomes"
)

// RevokedErrorType is the temporal.ApplicationError.Type() an activity
// reports when youtube.ErrRevoked surfaces (FR4/FR14): the activity has
// already called tokens.Store.MarkNeedsReauth itself before returning it,
// so ChannelSyncWorkflow's only remaining job is to end the run cleanly
// (isRevoked below) rather than retry or fail. Activities MUST construct
// this via temporal.NewNonRetryableApplicationError(msg, RevokedErrorType,
// cause) -- a retryable error of this type would defeat the "no infinite
// retry, no quota burn on a revoked credential" guarantee FR4 requires.
const RevokedErrorType = "Revoked"

// ChannelSyncInput is ChannelSyncWorkflow's single argument -- one per
// scheduled run, carrying the Channel this run syncs.
type ChannelSyncInput struct {
	ChannelID uuid.UUID
}

// ChannelState is LoadChannelState's result: just enough of `channel` for
// ChannelSyncWorkflow's skip-on-disconnected gate (FR14).
type ChannelState struct {
	ConnectionState store.ConnectionState
}

// SyncActivities is the activity interface ChannelSyncWorkflow drives.
// Every method is a Temporal activity: it may perform I/O and must never
// be invoked directly from workflow code -- only via
// workflow.ExecuteActivity (see the package doc comment's "Determinism"
// section). See activities.go for the concrete Activities implementation
// worker/main.go registers.
type SyncActivities interface {
	// LoadChannelState resolves channelID's current connection_state
	// (`channel.connection_state`, FR4) -- ChannelSyncWorkflow's
	// skip-on-disconnected gate reads this before doing anything else, and
	// re-reads it fresh on every scheduled run so a reconnect resumes
	// syncing automatically on the next cycle with no manual step (FR14).
	LoadChannelState(ctx context.Context, channelID uuid.UUID) (ChannelState, error)

	// SyncSchedule is C6's schedule-sync activity (#1576): syncs the
	// Channel's YouTube upload schedule. A youtube.ErrRevoked failure must
	// surface as a temporal.NewNonRetryableApplicationError with
	// RevokedErrorType, after calling tokens.Store.MarkNeedsReauth --
	// ChannelSyncWorkflow's isRevoked branch below relies on both.
	SyncSchedule(ctx context.Context, channelID uuid.UUID) error

	// SyncOutcomes is C9's outcome-sync activity (#1581): syncs published-
	// video metrics. Same RevokedErrorType/MarkNeedsReauth contract as
	// SyncSchedule.
	SyncOutcomes(ctx context.Context, channelID uuid.UUID) error
}

// isRevoked reports whether err is a temporal.ApplicationError of
// RevokedErrorType -- ChannelSyncWorkflow's signal to end the run cleanly
// (FR4/FR14) rather than propagate a workflow failure. Deliberately does
// NOT also check NonRetryable(): an activity MUST always construct this
// error non-retryable (see RevokedErrorType's doc comment), but the
// workflow's branch here is driven by the error's identity (a revoked
// credential), not by its retry policy, which is a separate concern.
func isRevoked(err error) bool {
	var appErr *temporal.ApplicationError
	if !errors.As(err, &appErr) {
		return false
	}
	return appErr.Type() == RevokedErrorType
}

func loadChannelState(ctx workflow.Context, channelID uuid.UUID) (ChannelState, error) {
	var state ChannelState
	err := workflow.ExecuteActivity(ctx, ActivityLoadChannelState, channelID).Get(ctx, &state)
	return state, err
}

func syncSchedule(ctx workflow.Context, channelID uuid.UUID) error {
	return workflow.ExecuteActivity(ctx, ActivitySyncSchedule, channelID).Get(ctx, nil)
}

func syncOutcomes(ctx workflow.Context, channelID uuid.UUID) error {
	return workflow.ExecuteActivity(ctx, ActivitySyncOutcomes, channelID).Get(ctx, nil)
}

// defaultActivityOptions applies to every activity ChannelSyncWorkflow
// executes. MaximumAttempts is bounded (FR4: "never infinite retry") --
// once exhausted, workflow.ExecuteActivity's Get returns the final error,
// which ChannelSyncWorkflow propagates as a workflow failure unless it is
// a RevokedErrorType (isRevoked), in which case Temporal's retry policy
// never even applies (a non-retryable error fails immediately, on the
// first attempt).
var defaultActivityOptions = workflow.ActivityOptions{
	StartToCloseTimeout: 5 * time.Minute,
	RetryPolicy: &temporal.RetryPolicy{
		MaximumAttempts: 5,
	},
}

// ChannelSyncWorkflow is the per-Channel scheduled sync run (FR14/FR21):
// load connection_state, skip cleanly for a needs_reauth Channel, or run
// SyncSchedule then SyncOutcomes in order for a connected one. Either
// activity returning a RevokedErrorType error (isRevoked) ends the run
// cleanly too, exactly like the needs_reauth skip -- both paths return nil
// so the workflow run succeeds; a disconnected/revoked Channel must never
// fail the workflow, retry the sync, or consume YouTube quota (FR14).
//
// Its workflow id is deterministic per Channel (see ScheduleManager's
// EnsureSchedule, schedule.go) so Temporal's schedule machinery, not this
// function, is what guarantees one run at a time per Channel (overlap
// policy: skip).
func ChannelSyncWorkflow(ctx workflow.Context, in ChannelSyncInput) error {
	ctx = workflow.WithActivityOptions(ctx, defaultActivityOptions)
	logger := workflow.GetLogger(ctx)

	state, err := loadChannelState(ctx, in.ChannelID)
	if err != nil {
		return fmt.Errorf("load channel state: %w", err)
	}

	if state.ConnectionState == store.ConnectionStateNeedsReauth {
		logger.Info("channel needs reauth, skipping sync cycle", "channel_id", in.ChannelID.String())
		return nil
	}

	if err := syncSchedule(ctx, in.ChannelID); err != nil {
		if isRevoked(err) {
			logger.Info("channel credential revoked during schedule sync, ending cycle cleanly", "channel_id", in.ChannelID.String())
			return nil
		}
		return fmt.Errorf("sync schedule: %w", err)
	}

	if err := syncOutcomes(ctx, in.ChannelID); err != nil {
		if isRevoked(err) {
			logger.Info("channel credential revoked during outcome sync, ending cycle cleanly", "channel_id", in.ChannelID.String())
			return nil
		}
		return fmt.Errorf("sync outcomes: %w", err)
	}

	return nil
}
