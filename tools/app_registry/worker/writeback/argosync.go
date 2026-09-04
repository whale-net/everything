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
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/whale-net/everything/libs/go/argocd"
	"github.com/whale-net/everything/tools/app_registry/events"
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
// without a second registry read (FR1). ApplicationName is read by the
// caller (WritebackWorkflow) directly from RenderedState.ArgoApplicationName
// -- resolved by RenderEnvironmentState from the SAME AppRegistry.ListCharts
// lookup the gitops publish path already used for RenderedState.ChartName,
// as either the "<ChartName>-<EnvironmentKey>" convention or an admin-set
// per-chart override for ad-hoc/legacy deployments (see
// repository.Chart.ResolveArgoApplicationName) -- never re-derived a second
// way here (see issue #1030's Summary).
type ArgoSyncInput struct {
	PromotionID string
	// Domain is passed through to the argocd.Client as ArgoCD's "project"
	// scoping parameter (see libs/go/argocd's NFR7 doc comment) --
	// WritebackInput.Domain/RenderedState.Domain, not re-derived.
	Domain string
	// ApplicationName is the ArgoCD Application name -- RenderedState.ArgoApplicationName, verbatim.
	ApplicationName string
	// IsRetry selects the retry_triggered/retry_observed promotion_sync_event
	// Source pair instead of the default refresh_triggered/poll_observed
	// pair (issue #1033's manual retry, FR12) -- TriggerArgoRefresh/
	// PollArgoSyncStatus are reused verbatim by both WritebackWorkflow and
	// RetryArgoSyncWorkflow; this is the one field that lets the resulting
	// audit trail (#1028's promotion_sync_event, surfaced on the Promotion
	// Details page's sync history, #1031/#1032) tell a manual retry's
	// observations apart from the original writeback's own. Zero value
	// (false) is every existing WritebackWorkflow call site -- adding this
	// field changes no existing behavior.
	IsRetry bool
}

// ArgoSyncResult is PollArgoSyncStatus's outcome: the last-observed
// sync/health pair, whether or not it was terminal. Never an error on its
// own -- see PollArgoSyncStatus's doc comment for FR5's "never fail the
// workflow" contract.
type ArgoSyncResult struct {
	SyncStatus   string
	HealthStatus string
	// OperationPhase is ArgoCD's status.operationState.phase (e.g.
	// "Running"/"Succeeded"/"Failed"/"Error"), "" if no operation has run.
	// It is the only signal that reflects hook resources (PreSync/Sync/
	// PostSync, e.g. a migration Job) -- see isTerminalArgoSyncState's doc
	// comment for why SyncStatus/HealthStatus alone are not enough to know
	// every resource has finished rolling out.
	OperationPhase string
	// Terminal is true when SyncStatus/HealthStatus/OperationPhase reached
	// a stop-early state (FR4) before pollArgoSyncMaxAttempts was
	// exhausted.
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
	// PollInterval overrides pollArgoSyncInterval (2 minutes) between
	// PollArgoSyncStatus attempts when non-zero. Exists so tests can shrink
	// the interval to something practical instead of waiting out the full
	// production cadence -- mirrors GitOpsActivities.HTTPClient/
	// GitHubAPIBaseURL's "overridable in tests" convention (gitops.go).
	// worker/main.go never sets this, so production always gets the real
	// 2-minute cadence (NFR3).
	PollInterval time.Duration
	// Publisher enqueues sync-state transition events for subscribers;
	// see #1130 (FR7b). Nil in tests that do not verify publishing behavior.
	Publisher events.PublisherInterface
}

// ArgoSync is the interface both *ArgoSyncActivities and
// NoopArgoSyncActivities satisfy -- the boundary SelectArgoSyncActivities
// (below) returns, and what ../main.go registers WritebackWorkflow's
// ActivityTriggerArgoRefresh/ActivityPollArgoSyncStatus against, mirroring
// how the Writeback interface (workflow.go) lets WritebackWorkflow depend
// on an interface rather than StubActivities/GitOpsActivities directly.
type ArgoSync interface {
	TriggerArgoRefresh(ctx context.Context, in ArgoSyncInput) error
	PollArgoSyncStatus(ctx context.Context, in ArgoSyncInput) (ArgoSyncResult, error)
}

// SelectArgoSyncActivities implements the ARGOCD_SERVER opt-in gate (issue
// #1030 scope item 4): a real *ArgoSyncActivities when server is non-empty
// (constructing an argocd.Client from server/authToken), or
// NoopArgoSyncActivities otherwise -- see that type's doc comment for why
// this is a runtime-configurability gate, not an environment exemption.
// Pulled out of ../main.go as its own pure, testable function since
// main.go's run() itself connects to a real database/Temporal server and
// cannot be unit tested directly.
func SelectArgoSyncActivities(server, authToken string, registry repository.Registry) (ArgoSync, error) {
	if server == "" {
		return NoopArgoSyncActivities{}, nil
	}
	client, err := argocd.NewClient(argocd.Config{ServerURL: server, AuthToken: authToken}, nil)
	if err != nil {
		return nil, fmt.Errorf("configure argocd client: %w", err)
	}
	return &ArgoSyncActivities{Client: client, Registry: registry}, nil
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
	source := repository.PromotionSyncEventSourceRefreshTriggered
	if in.IsRetry {
		source = repository.PromotionSyncEventSourceRetryTriggered
	}
	if _, err := a.recordSyncEvent(ctx, in.PromotionID, source, "", "", ""); err != nil {
		return fmt.Errorf("trigger argo refresh for promotion %s: %w", in.PromotionID, err)
	}
	workerLog.Info("argo refresh triggered",
		"promotion_id", in.PromotionID, "domain", in.Domain, "application_name", in.ApplicationName, "is_retry", in.IsRetry)
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
	interval := a.PollInterval
	if interval <= 0 {
		interval = pollArgoSyncInterval
	}

	pollSource := repository.PromotionSyncEventSourcePollObserved
	if in.IsRetry {
		pollSource = repository.PromotionSyncEventSourceRetryObserved
	}

	var last ArgoSyncResult
	for attempt := 1; attempt <= pollArgoSyncMaxAttempts; attempt++ {
		activity.RecordHeartbeat(ctx, fmt.Sprintf("attempt %d/%d", attempt, pollArgoSyncMaxAttempts))

		syncStatus, healthStatus, operationPhase, err := a.Client.GetStatus(ctx, in.Domain, in.ApplicationName)
		if err != nil {
			return ArgoSyncResult{}, fmt.Errorf("poll argo sync status for promotion %s (attempt %d/%d): %w", in.PromotionID, attempt, pollArgoSyncMaxAttempts, err)
		}
		if _, err := a.recordSyncEvent(ctx, in.PromotionID, pollSource, syncStatus, healthStatus, operationPhase); err != nil {
			return ArgoSyncResult{}, fmt.Errorf("poll argo sync status for promotion %s (attempt %d/%d): %w", in.PromotionID, attempt, pollArgoSyncMaxAttempts, err)
		}

		last = ArgoSyncResult{SyncStatus: syncStatus, HealthStatus: healthStatus, OperationPhase: operationPhase}
		if isTerminalArgoSyncState(syncStatus, healthStatus, operationPhase) {
			last.Terminal = true
			workerLog.Info("argo sync reached terminal state",
				"promotion_id", in.PromotionID, "attempt", attempt, "sync_status", syncStatus, "health_status", healthStatus, "operation_phase", operationPhase)
			return last, nil
		}

		if attempt == pollArgoSyncMaxAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return ArgoSyncResult{}, ctx.Err()
		case <-time.After(interval):
		}
	}
	// FR5: exhausted every attempt without reaching terminal -- the
	// last-observed pair stands as "still pending". Must never fail the
	// workflow (see doc comment above), but it's a real signal an operator
	// should be able to find without digging through Temporal's own event
	// history: ArgoCD taking longer than ~6 minutes to settle usually means
	// something downstream (the workload itself, not this activity) needs
	// attention.
	workerLog.Warn("argo sync did not reach terminal state within poll budget",
		"promotion_id", in.PromotionID, "attempts", pollArgoSyncMaxAttempts, "sync_status", last.SyncStatus, "health_status", last.HealthStatus)
	return last, nil
}

// isTerminalArgoSyncState reports whether the observed sync/health/
// operation-phase triple is one PollArgoSyncStatus should stop early on
// (FR4).
//
// Synced+Healthy alone is NOT sufficient to declare success: ArgoCD
// excludes hook resources (PreSync/Sync/PostSync, e.g. a migration Job run
// via the argocd.argoproj.io/hook annotation) from the Health rollup
// entirely, so an Application can report Sync.Status=Synced,
// Health.Status=Healthy while a PostSync hook is still Running -- exactly
// the "readiness banner said ready before the migration job was even
// scheduled" race this field exists to close. operationPhase=="Succeeded"
// is ArgoCD's own confirmation that the most recent sync operation --
// including every hook it ran -- has fully completed, so success requires
// all three: Synced, Healthy, AND Succeeded.
//
// Degraded health is terminal-failure regardless of operationPhase (a
// workload came up unhealthy after sync). A Failed/Error operationPhase is
// ALSO terminal-failure even if health still reads Healthy, since that is
// exactly the case of a hook (e.g. the migration job) failing while the
// non-hook resources it gates remain healthy.
func isTerminalArgoSyncState(syncStatus, healthStatus, operationPhase string) bool {
	if healthStatus == "Degraded" {
		return true
	}
	if operationPhase == "Failed" || operationPhase == "Error" {
		return true
	}
	return syncStatus == "Synced" && healthStatus == "Healthy" && operationPhase == "Succeeded"
}

// NoopArgoSyncActivities is the zero-config fallback registered instead of
// a real *ArgoSyncActivities when ARGOCD_SERVER is unset (see
// ../main.go's opt-in gate, issue #1030 scope item 4) -- so `bazel
// test`/local dev/Tilt keep working with zero ArgoCD configuration. Both
// methods skip the ArgoCD call entirely, report success, and write no
// promotion_sync_event rows. This is a runtime-configurability opt-in, not
// an environment exemption: it does not touch FR2/NFR6, which govern
// promotion TARGET environments (dev/staging/prod), not whether a given
// deployment has ArgoCD configured at all.
type NoopArgoSyncActivities struct{}

// TriggerArgoRefresh implements the same signature as
// (*ArgoSyncActivities).TriggerArgoRefresh but is a pure no-op. See
// NoopArgoSyncActivities's doc comment.
func (NoopArgoSyncActivities) TriggerArgoRefresh(ctx context.Context, in ArgoSyncInput) error {
	return nil
}

// PollArgoSyncStatus implements the same signature as
// (*ArgoSyncActivities).PollArgoSyncStatus but is a pure no-op, returning a
// zero-value ArgoSyncResult. See NoopArgoSyncActivities's doc comment.
func (NoopArgoSyncActivities) PollArgoSyncStatus(ctx context.Context, in ArgoSyncInput) (ArgoSyncResult, error) {
	return ArgoSyncResult{}, nil
}

// RetryArgoSyncWorkflowID builds a fresh Temporal workflow id for one
// RetryArgoSync RPC call (issue #1033, FR12): "retry-<promotion_id>-
// <suffix>". Deliberately NOT release.WorkflowID's fully-deterministic
// "same input always hashes to the same id" scheme (issue #889's own "no
// time component" design, where a second identical trigger is meant to
// collide on purpose) -- promotion_id alone is also not used bare as the
// workflow id, because that id is permanently owned by the promotion's
// original, by-then-terminal WritebackWorkflow (see workflow.go's
// WritebackWorkflow doc comment on that reuse). FR12 wants the opposite
// property here: an admin must be able to click retry more than once for
// the same promotion, each one starting its OWN execution rather than
// colliding with WorkflowExecutionAlreadyStarted against a prior retry.
// suffix (typically a Unix-nanosecond timestamp) is generated by the
// caller (server/handlers/promotion.go's RetryArgoSync, at request time),
// not read from a clock in here -- this function is a pure string builder
// with no side effect of its own, kept trivially unit-testable.
func RetryArgoSyncWorkflowID(promotionID string, suffix int64) string {
	return fmt.Sprintf("retry-%s-%d", promotionID, suffix)
}

// RetryArgoSyncWorkflow implements FR12: a standalone workflow execution
// (not a step of WritebackWorkflow) that starts a fresh ArgoCD refresh/poll
// cycle against the SAME Domain/ApplicationName as the original promotion,
// via TriggerArgoRefresh then PollArgoSyncStatus -- the exact same
// activities WritebackWorkflow uses (issue #1030), reused verbatim, with
// in.IsRetry set so the promotion_sync_event rows they write use
// retry_triggered/retry_observed (see ArgoSyncInput.IsRetry) and are
// visually distinguishable in the Promotion Details sync history (#1031/
// #1032) from the original writeback's own refresh_triggered/poll_observed
// rows.
//
// Unlike WritebackWorkflow's best-effort tail-step treatment of these same
// two activities (a Publish there has already succeeded; the sync/health
// observation is bonus data, so a failure there must never fail the whole
// workflow), THIS workflow's entire purpose IS the sync retry -- so a
// TriggerArgoRefresh failure (after its own activity-level RetryPolicy is
// exhausted) fails this workflow's execution outright, giving the admin
// who clicked retry a real signal via Temporal (visible as a failed
// workflow execution) rather than a silent no-op. A genuine
// PollArgoSyncStatus activity error (as opposed to FR5's "exhausted
// without reaching terminal", which is that activity's own defined SUCCESS
// case, never an error) fails this workflow the same way.
func RetryArgoSyncWorkflow(ctx workflow.Context, in ArgoSyncInput) (ArgoSyncResult, error) {
	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 5,
		},
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	if err := workflow.ExecuteActivity(ctx, ActivityTriggerArgoRefresh, in).Get(ctx, nil); err != nil {
		return ArgoSyncResult{}, err
	}

	// PollArgoSyncStatus's own internal loop runs up to ~6 minutes (NFR3),
	// so it gets its own workflow.ActivityOptions with a much longer
	// StartToCloseTimeout than the 30s ao above -- same values
	// WritebackWorkflow uses for the identical activity (workflow.go).
	pollAO := workflow.ActivityOptions{
		StartToCloseTimeout: 7 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 3,
		},
	}
	pollCtx := workflow.WithActivityOptions(ctx, pollAO)
	var result ArgoSyncResult
	if err := workflow.ExecuteActivity(pollCtx, ActivityPollArgoSyncStatus, in).Get(pollCtx, &result); err != nil {
		return ArgoSyncResult{}, err
	}
	return result, nil
}

// recordSyncEvent is TriggerArgoRefresh/PollArgoSyncStatus's shared
// promotion_sync_event write -- see repository.PromotionRepository.
// RecordSyncEvent's doc comment (append-only, NFR4).
func (a *ArgoSyncActivities) recordSyncEvent(ctx context.Context, promotionID, source, syncStatus, healthStatus, operationPhase string) (*repository.PromotionSyncEvent, error) {
	if a.Registry == nil {
		return nil, fmt.Errorf("record promotion sync event for promotion %s: ArgoSyncActivities.Registry not configured", promotionID)
	}
	e, err := a.Registry.Promotions().RecordSyncEvent(ctx, repository.PromotionSyncEvent{
		PromotionID:    promotionID,
		Source:         source,
		SyncStatus:     syncStatus,
		HealthStatus:   healthStatus,
		OperationPhase: operationPhase,
	})
	if err != nil {
		return nil, fmt.Errorf("record promotion sync event for promotion %s: %w", promotionID, err)
	}
	// FR7a/FR7b: publish after write commits, but only if publisher is configured.
	// Publish errors are discarded and logged by the publisher; see #1130 for details.
	if a.Publisher != nil {
		eventKind := source // Use the source as the event kind (e.g., "refresh_triggered", "poll_observed")
		a.Publisher.Publish(promotionID, eventKind, "pending")
	}
	return e, nil
}
