// argosync.go implements TriggerArgoRefresh and PollArgoSyncStatus, the two
// activities WritebackWorkflow drives (after Publish/RecordWritebackResult)
// to observe ArgoCD's sync/health state for a promotion's target
// Application and persist every transition to promotion_sync_event -- FR1-
// FR5, NFR3, NFR6, issue #1030. Uses libs/go/argocd's minimal REST client
// (#1027) and repository.Registry.Promotions().RecordSyncEvent (#1028) --
// same "worker talks to Postgres directly for its own write-path
// activities" pattern as record.go's Recorder and worker/release/record.go.
//
// Wiring into WritebackWorkflow's call sequence and worker/main.go's
// activity registration lands in a later phase of issue #1030 -- these two
// methods are fully implemented and independently callable/testable here,
// but not yet invoked by anything.
package writeback

import (
	"context"
	"fmt"
	"time"

	"go.temporal.io/sdk/activity"

	"github.com/whale-net/everything/libs/go/argocd"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// pollArgoSyncMaxAttempts/pollArgoSyncInterval bound PollArgoSyncStatus's
// internal loop at ~6 minutes total (NFR3): 3 attempts, 2 minutes apart.
// This is a loop bound enforced by the activity itself, not merely an
// external StartToCloseTimeout -- see PollArgoSyncStatus's doc comment.
const (
	pollArgoSyncMaxAttempts = 3
	pollArgoSyncInterval    = 2 * time.Minute
)

// ArgoSyncInput carries everything TriggerArgoRefresh/PollArgoSyncStatus
// need to call ArgoCD's Application API and persist the observation,
// without a second registry read (FR1). ApplicationName is
// "<ChartName>-<EnvironmentKey>", derived by the caller (WritebackWorkflow)
// from the SAME RenderedState.ChartName/EnvironmentKey fields the gitops
// publish path already resolves -- never re-derived a second way here (see
// issue #1030's Summary).
type ArgoSyncInput struct {
	PromotionID string
	// Domain is passed through to the argocd.Client as ArgoCD's "project"
	// scoping parameter (see libs/go/argocd's NFR7 doc comment) --
	// WritebackInput.Domain/RenderedState.Domain, not re-derived.
	Domain string
	// ApplicationName is the ArgoCD Application name: "<ChartName>-<EnvironmentKey>".
	ApplicationName string
}

// ArgoSyncResult is PollArgoSyncStatus's outcome: the last-observed
// sync/health pair, whether or not it was terminal. Never an error on its
// own -- see PollArgoSyncStatus's doc comment for FR5's "never fail the
// workflow" contract.
type ArgoSyncResult struct {
	SyncStatus   string
	HealthStatus string
	// Terminal is true when SyncStatus/HealthStatus reached a stop-early
	// state (FR4) before pollArgoSyncMaxAttempts was exhausted.
	Terminal bool
}

// ArgoSyncActivities implements TriggerArgoRefresh/PollArgoSyncStatus.
// Constructed and registered by ../main.go, mirroring writeback.Recorder's
// shape (a distinct type, not a method on GitOpsActivities/StubActivities,
// since both Writeback implementations share these same two activities).
type ArgoSyncActivities struct {
	// Client talks to ArgoCD's REST API (libs/go/argocd, #1027). Required;
	// both methods below fail fast with a clear "not configured" error if
	// nil, matching writeback.Recorder.Registry's convention -- worker/
	// main.go only constructs an ArgoSyncActivities (and registers these
	// activities) when ARGOCD_SERVER is set (see issue #1030 scope item 4),
	// so a nil Client here would only ever be a wiring bug, not an expected
	// runtime state.
	Client *argocd.Client
	// Registry is the direct-Postgres repository.Registry both activities
	// write promotion_sync_event rows through -- same rationale as
	// writeback.Recorder.Registry and worker/release.Activities.Registry:
	// RecordSyncEvent has no mutating gRPC RPC equivalent.
	Registry repository.Registry
}

// TriggerArgoRefresh implements FR1: calls Client.Refresh for
// in.Domain/in.ApplicationName, then records exactly one
// promotion_sync_event row (Source: refresh_triggered) via
// Registry.Promotions().RecordSyncEvent. Either call failing returns an
// error (this activity's own RetryPolicy, set by whichever
// workflow.ActivityOptions WritebackWorkflow wraps it in, covers retries);
// WritebackWorkflow's own best-effort wrapping around this activity (issue
// #1030 scope item 3) is what keeps a persistent failure here from failing
// the overall workflow result, not anything in this method.
func (a *ArgoSyncActivities) TriggerArgoRefresh(ctx context.Context, in ArgoSyncInput) error {
	if a.Client == nil {
		return fmt.Errorf("trigger argo refresh for promotion %s: ArgoSyncActivities.Client not configured", in.PromotionID)
	}
	if err := a.Client.Refresh(ctx, in.Domain, in.ApplicationName); err != nil {
		return fmt.Errorf("trigger argo refresh for promotion %s: %w", in.PromotionID, err)
	}
	if _, err := a.recordSyncEvent(ctx, in.PromotionID, repository.PromotionSyncEventSourceRefreshTriggered, "", ""); err != nil {
		return fmt.Errorf("trigger argo refresh for promotion %s: %w", in.PromotionID, err)
	}
	return nil
}

// PollArgoSyncStatus implements FR3-FR5, NFR3: ONE activity execution that
// loops internally up to pollArgoSyncMaxAttempts times, pollArgoSyncInterval
// apart (a plain select/time.After inside the activity body, not repeated
// workflow-level workflow.ExecuteActivity calls -- workflow.Sleep is not
// available inside an activity). Each attempt calls Client.GetStatus,
// records one promotion_sync_event row (Source: poll_observed) with the
// observed sync_status/health_status, and stops early (FR4) once that pair
// reaches a terminal state: Synced+Healthy, or Degraded health. If every
// attempt completes without reaching terminal (FR5), PollArgoSyncStatus
// returns normally (nil error) with the last-observed pair -- it must NEVER
// return an error for "still pending", only for a genuine call/write
// failure (ArgoCD unreachable, RecordSyncEvent failing), which -- like
// TriggerArgoRefresh -- WritebackWorkflow's best-effort wrapping (scope
// item 3) tolerates without failing the overall workflow.
//
// activity.RecordHeartbeat is called every attempt so a stuck poll is
// visible/cancelable in the Temporal UI; ctx.Err() is checked between
// sleeps so workflow cancellation is honored promptly instead of waiting
// out the full pollArgoSyncInterval.
func (a *ArgoSyncActivities) PollArgoSyncStatus(ctx context.Context, in ArgoSyncInput) (ArgoSyncResult, error) {
	if a.Client == nil {
		return ArgoSyncResult{}, fmt.Errorf("poll argo sync status for promotion %s: ArgoSyncActivities.Client not configured", in.PromotionID)
	}

	var last ArgoSyncResult
	for attempt := 1; attempt <= pollArgoSyncMaxAttempts; attempt++ {
		activity.RecordHeartbeat(ctx, fmt.Sprintf("attempt %d/%d", attempt, pollArgoSyncMaxAttempts))

		syncStatus, healthStatus, err := a.Client.GetStatus(ctx, in.Domain, in.ApplicationName)
		if err != nil {
			return ArgoSyncResult{}, fmt.Errorf("poll argo sync status for promotion %s (attempt %d/%d): %w", in.PromotionID, attempt, pollArgoSyncMaxAttempts, err)
		}
		if _, err := a.recordSyncEvent(ctx, in.PromotionID, repository.PromotionSyncEventSourcePollObserved, syncStatus, healthStatus); err != nil {
			return ArgoSyncResult{}, fmt.Errorf("poll argo sync status for promotion %s (attempt %d/%d): %w", in.PromotionID, attempt, pollArgoSyncMaxAttempts, err)
		}

		last = ArgoSyncResult{SyncStatus: syncStatus, HealthStatus: healthStatus}
		if isTerminalArgoSyncState(syncStatus, healthStatus) {
			last.Terminal = true
			return last, nil
		}

		if attempt == pollArgoSyncMaxAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return ArgoSyncResult{}, ctx.Err()
		case <-time.After(pollArgoSyncInterval):
		}
	}
	// FR5: exhausted every attempt without reaching terminal -- the
	// last-observed pair stands as "still pending". Must never fail the
	// workflow (see doc comment above).
	return last, nil
}

// isTerminalArgoSyncState reports whether the observed sync/health pair is
// one PollArgoSyncStatus should stop early on (FR4): fully synced and
// healthy, or degraded (ArgoCD's health status for an application whose
// sync succeeded but whose workload failed to come up, or reports the sync
// itself failing).
func isTerminalArgoSyncState(syncStatus, healthStatus string) bool {
	if syncStatus == "Synced" && healthStatus == "Healthy" {
		return true
	}
	return healthStatus == "Degraded"
}

// recordSyncEvent is TriggerArgoRefresh/PollArgoSyncStatus's shared
// promotion_sync_event write -- see repository.PromotionRepository.
// RecordSyncEvent's doc comment (append-only, NFR4).
func (a *ArgoSyncActivities) recordSyncEvent(ctx context.Context, promotionID, source, syncStatus, healthStatus string) (*repository.PromotionSyncEvent, error) {
	if a.Registry == nil {
		return nil, fmt.Errorf("record promotion sync event for promotion %s: ArgoSyncActivities.Registry not configured", promotionID)
	}
	e, err := a.Registry.Promotions().RecordSyncEvent(ctx, repository.PromotionSyncEvent{
		PromotionID:  promotionID,
		Source:       source,
		SyncStatus:   syncStatus,
		HealthStatus: healthStatus,
	})
	if err != nil {
		return nil, fmt.Errorf("record promotion sync event for promotion %s: %w", promotionID, err)
	}
	return e, nil
}
