// Package release implements the Temporal ReleaseWorkflow saga described in
// issue #889 (FR6-9, FR11, FR14, NFR1-3): resolve one release plan for a
// batch, dispatch the existing GHA build job with that locked plan, poll it
// to completion, and verify/record outcomes. It is a new package inside the
// existing app-registry-worker binary -- see
// ../../architecture/12-writeback-outbox-temporal.md for the sibling
// WritebackWorkflow this package's structure mirrors, and PLAN.md's AR-5
// scope for the root plan this closes.
//
// # Determinism
//
// ReleaseWorkflow is replayed by the Temporal SDK, so its code must be
// deterministic: no direct network/disk I/O, no time.Now(), no map
// iteration whose order matters, nothing that could produce a different
// result on replay than it did the first time. Every side effect (resolving
// a version plan, calling the GitHub Actions API, calling the AppRegistry
// API, writing release_run_target rows) lives behind the ReleaseActivities
// interface below and is invoked only via workflow.ExecuteActivity. See
// worker/writeback/workflow.go's identical discipline and AGENTS.md/PLAN.md's
// "Workflow determinism" hazard.
//
// # Scaffold status
//
// As of this package's introduction, CheckApproval (approval.go) is the
// only activity with a real implementation -- FR14's true no-op, "always
// approve" -- see architecture/18-future-approval-gate.md. ResolvePlan,
// DispatchBuild, PollBuild, VerifyPublished, and RecordTargetState
// (activities.go) return unimplemented errors; a later change (issue #889's
// own Implementation phase) replaces each with the real logic described in
// that issue. The workflow control flow itself (ReleaseWorkflow below) is
// NOT a stub: its dispatch order and failure-propagation behavior are the
// part of FR6/FR11 this scaffold settles, so Testing-phase work can assert
// against it once real activities land.
package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
	"github.com/whale-net/everything/tools/app_registry/worker/writeback"
)

// TaskQueue is the Temporal task queue ReleaseWorkflow and its activities
// are registered/dispatched on. Same value as writeback.TaskQueue -- the
// root plan's settled design decision is "no new binary, same default task
// queue as WritebackWorkflow initially" (see issue #889's Summary); do not
// invent a second queue name at this phase.
const TaskQueue = writeback.TaskQueue

// Activity name constants. ReleaseWorkflow dispatches activities by these
// string names (not by Go function/method value) so the ReleaseActivities
// interface -- not a concrete implementation -- is the boundary the
// workflow depends on, mirroring writeback.ActivityRenderEnvironmentState's
// doc comment. The worker registers whatever ReleaseActivities
// implementation it is built with under these same names (see
// ../main.go).
const (
	ActivityCheckApproval     = "CheckApproval"
	ActivityResolvePlan       = "ResolvePlan"
	ActivityDispatchBuild     = "DispatchBuild"
	ActivityPollBuild         = "PollBuild"
	ActivityVerifyPublished   = "VerifyPublished"
	ActivityRecordTargetState = "RecordTargetState"
)

// ReleaseTarget is one target (app image or chart) in a release batch, as
// carried on ReleaseWorkflowInput. Deliberately does not carry the
// release_run_target row's own id (repository.ReleaseRunTarget has one) --
// activities that need to mutate a specific row (RecordTargetState) resolve
// it from ReleaseWorkflowInput.ReleaseRunID + OwnerFullName + Kind via
// GetReleaseRun, keeping this input shape independent of storage-layer
// primary keys.
type ReleaseTarget struct {
	OwnerFullName string
	Kind          repository.ArtifactKind
	// Digest, when non-empty, is FR2's "release exactly this
	// already-built artifact" input (hotfix / re-release flow). Threaded
	// through the input/activity signatures at this phase, but consuming
	// it is out of scope for issue #889's implementation -- see
	// DispatchBuild's doc comment.
	Digest string
}

// key returns the same "<kind>:<owner>" identity repository.TargetKey uses
// for promotion targets, reused here rather than inventing a second format.
func (t ReleaseTarget) key() string {
	return repository.TargetKey(t.Kind, t.OwnerFullName)
}

// ReleaseWorkflowInput is ReleaseWorkflow's single argument.
type ReleaseWorkflowInput struct {
	ReleaseRunID string
	Targets      []ReleaseTarget
}

// ReleaseTargetResult is one target's outcome, as reported in
// ReleaseWorkflowResult.
type ReleaseTargetResult struct {
	OwnerFullName string
	Kind          repository.ArtifactKind
	State         repository.ReleaseRunTargetState
	ErrorDetail   string
}

// ReleaseWorkflowResult is ReleaseWorkflow's return value: the terminal
// state recorded for every target in the batch.
type ReleaseWorkflowResult struct {
	ReleaseRunID string
	Targets      []ReleaseTargetResult
}

// ResolvedPlan is ResolvePlan's output: the single version plan for the
// whole batch (FR7/FR8), computed once and threaded unchanged into
// DispatchBuild -- no downstream activity may independently resolve a
// version.
type ResolvedPlan struct {
	ReleaseRunID string
	// Versions maps a target's key() (repository.TargetKey format) to the
	// version resolved for it.
	Versions map[string]string
	// RawJSON is the resolved plan as persisted verbatim onto
	// release_run.ResolvedPlan (see repository.ReleaseRun's doc comment) --
	// opaque to the workflow, whatever shape the plan-resolution logic
	// (tools/release_helper_go/cmd/plan.go et al) produces.
	RawJSON []byte
}

// BuildRef identifies the single GitHub Actions run DispatchBuild started
// for the whole batch -- one build covers every target in ResolvedPlan, not
// one build per target.
type BuildRef struct {
	ReleaseRunID string
	// RunID is the GitHub Actions run id, used by PollBuild to poll it.
	RunID string
	// RunURL is informational only, for operator visibility / linking from
	// the admin UI.
	RunURL string
}

// BuildStatus is PollBuild's outcome once the GitHub Actions run reaches a
// terminal state (success/failure/cancelled).
type BuildStatus struct {
	Succeeded bool
	Detail    string
}

// VerifyResult is VerifyPublished's outcome: whether every target in the
// batch reached published/succeeded (FR12's "same registry-visible end
// state as v1"), and which ones didn't.
type VerifyResult struct {
	AllPublished bool
	// Failed lists the key() of every target that did not reach
	// published/succeeded, so RecordTargetState knows which rows to mark
	// failed vs succeeded.
	Failed map[string]string // key() -> failure detail
}

// ReleaseActivities is the activity interface ReleaseWorkflow drives. Every
// method is a Temporal activity: it may perform I/O, must be idempotent
// under Temporal's at-least-once activity retry (see issue #889's
// Implementation scope "Idempotency (NFR3, FR11)"), and must never be
// invoked directly from workflow code -- only via workflow.ExecuteActivity
// (see the package doc comment's "Determinism" section). Mirrors
// writeback.Writeback's role for WritebackWorkflow.
type ReleaseActivities interface {
	// CheckApproval implements FR14's approval-gate extension point: today
	// a true no-op that always returns true immediately (see approval.go
	// and architecture/18-future-approval-gate.md). Called once per
	// workflow execution, before ResolvePlan.
	CheckApproval(ctx context.Context, releaseRunID string) (bool, error)

	// ResolvePlan resolves versions once for the whole batch (FR7/FR8).
	// Called exactly once per workflow execution; its result is the only
	// version source threaded into DispatchBuild.
	ResolvePlan(ctx context.Context, targets []ReleaseTarget) (ResolvedPlan, error)

	// DispatchBuild invokes the GHA build job with plan as direct input,
	// bypassing that workflow's own plan-release resolution for this
	// trigger. digestOverrides carries FR2's per-target pinned-digest input
	// (ReleaseTarget.Digest, keyed by key()); the empty map means "build
	// fresh" for every target.
	DispatchBuild(ctx context.Context, plan ResolvedPlan, digestOverrides map[string]string) (BuildRef, error)

	// PollBuild polls the GitHub Actions run identified by ref until it
	// reaches a terminal state.
	PollBuild(ctx context.Context, ref BuildRef) (BuildStatus, error)

	// VerifyPublished confirms every target in releaseRunID reached
	// published/succeeded via GetRelease/GetReleaseRun (FR12).
	VerifyPublished(ctx context.Context, releaseRunID string) (VerifyResult, error)

	// RecordTargetState updates the release_run_target row identified by
	// releaseRunID+target (see ReleaseTarget's doc comment on why this
	// resolves the row id itself rather than taking one) to newState,
	// giving FR10's live status visibility. buildID/errorDetail follow
	// repository.ReleaseRunRepository.UpdateTargetState's "empty string
	// leaves the existing value unchanged" contract.
	RecordTargetState(ctx context.Context, releaseRunID string, target ReleaseTarget, newState repository.ReleaseRunTargetState, buildID, errorDetail string) error
}

// WorkflowID returns the deterministic Temporal workflow id for a batch of
// targets: "release-" followed by the hex SHA-256 digest of the batch's
// target keys (repository.TargetKey format), newline-joined after a
// lexicographic sort so the id is independent of the caller's input order.
//
// This is the id issue #888's TriggerRelease must construct identically
// (replacing its release-<uuid> placeholder) so that Temporal's own
// WorkflowExecutionAlreadyStarted rejection on ExecuteWorkflow -- not a DB
// constraint -- is the authoritative FR5/NFR2 "at most one non-terminal
// release per target" guarantee: two TriggerRelease calls whose batches
// share even one target produce colliding workflow ids only when their
// batches are identical; overlapping-but-not-identical batches are a known
// gap left to the caller-side fast-fail check (server/handlers/release.go's
// rejectIfAlreadyReleasing) -- see that function's doc comment.
//
// Deliberately NOT derived from ReleaseRunID: ReleaseRunID is minted fresh
// per TriggerRelease call (see repository.ReleaseRun's doc comment), so
// keying on it would let two calls for the same batch always start two
// distinct workflow executions -- defeating the dedup guarantee this
// function exists to provide.
func WorkflowID(targets []ReleaseTarget) string {
	keys := make([]string, len(targets))
	for i, t := range targets {
		keys[i] = t.key()
	}
	sort.Strings(keys)
	sum := sha256.Sum256([]byte(strings.Join(keys, "\n")))
	return "release-" + hex.EncodeToString(sum[:])
}

// defaultActivityOptions applies to every activity ReleaseWorkflow executes
// except PollBuild, which needs a much longer StartToCloseTimeout since a
// GitHub Actions run can take many minutes -- see the PollBuild call site
// below.
var defaultActivityOptions = workflow.ActivityOptions{
	StartToCloseTimeout: 30 * time.Second,
	RetryPolicy: &temporal.RetryPolicy{
		MaximumAttempts: 5,
	},
}

// pollBuildActivityOptions applies only to PollBuild.
var pollBuildActivityOptions = workflow.ActivityOptions{
	StartToCloseTimeout: 30 * time.Minute,
	RetryPolicy: &temporal.RetryPolicy{
		MaximumAttempts: 3,
	},
}

// ReleaseWorkflow carries in.ReleaseRunID's batch from trigger through
// build, publish, and record (FR6): CheckApproval, then ResolvePlan, then
// DispatchBuild, then PollBuild, then VerifyPublished, then
// RecordTargetState once per target -- in that exact order, matching the
// dispatch sequence worker/release/workflow_test.go (Testing phase) asserts
// against. A failure at any step before RecordTargetState still causes
// every target to be recorded failed (via the recordFailure branch below)
// rather than left stuck in whatever state ResolvePlan/DispatchBuild left
// them in -- see issue #889's Testing scope: "a DispatchBuild failure
// should mark targets failed, not hang or silently succeed".
//
// Its workflow id is always WorkflowID(in.Targets) (set by the caller of
// ExecuteWorkflow -- see server/handlers/release.go's TriggerRelease), so
// Temporal's own workflow-id collision handling is the authoritative
// FR5/NFR2 dedup guarantee -- see WorkflowID's doc comment.
//
// See the package doc comment for the determinism rules this function must
// follow.
func ReleaseWorkflow(ctx workflow.Context, in ReleaseWorkflowInput) (ReleaseWorkflowResult, error) {
	ctx = workflow.WithActivityOptions(ctx, defaultActivityOptions)

	approved, err := checkApproval(ctx, in.ReleaseRunID)
	if err != nil {
		return recordFailure(ctx, in, fmt.Errorf("check approval: %w", err))
	}
	if !approved {
		// FR14's gate is a true no-op today (CheckApproval always returns
		// true -- see approval.go), so this branch is unreachable in
		// practice. It exists so a future real CheckApproval implementation
		// can return false without a workflow redesign: targets are left in
		// whatever state RecordTargetState hasn't yet touched (still
		// 'queued'), and the workflow completes without dispatching a
		// build.
		return ReleaseWorkflowResult{ReleaseRunID: in.ReleaseRunID}, nil
	}

	plan, err := resolvePlan(ctx, in.Targets)
	if err != nil {
		return recordFailure(ctx, in, fmt.Errorf("resolve plan: %w", err))
	}

	buildRef, err := dispatchBuild(ctx, plan, digestOverrides(in.Targets))
	if err != nil {
		return recordFailure(ctx, in, fmt.Errorf("dispatch build: %w", err))
	}

	buildStatus, err := pollBuild(workflow.WithActivityOptions(ctx, pollBuildActivityOptions), buildRef)
	if err != nil {
		return recordFailure(ctx, in, fmt.Errorf("poll build: %w", err))
	}
	if !buildStatus.Succeeded {
		return recordFailure(ctx, in, fmt.Errorf("build did not succeed: %s", buildStatus.Detail))
	}

	verify, err := verifyPublished(ctx, in.ReleaseRunID)
	if err != nil {
		return recordFailure(ctx, in, fmt.Errorf("verify published: %w", err))
	}

	result := ReleaseWorkflowResult{ReleaseRunID: in.ReleaseRunID}
	for _, t := range in.Targets {
		state := repository.ReleaseRunTargetStateSucceeded
		detail := ""
		if verify.Failed != nil {
			if d, failed := verify.Failed[t.key()]; failed {
				state = repository.ReleaseRunTargetStateFailed
				detail = d
			}
		}
		if recErr := recordTargetState(ctx, in.ReleaseRunID, t, state, buildRef.RunID, detail); recErr != nil {
			return ReleaseWorkflowResult{}, fmt.Errorf("record target state for %s: %w", t.key(), recErr)
		}
		result.Targets = append(result.Targets, ReleaseTargetResult{
			OwnerFullName: t.OwnerFullName,
			Kind:          t.Kind,
			State:         state,
			ErrorDetail:   detail,
		})
	}
	return result, nil
}

// recordFailure marks every target in in.Targets failed with cause's
// message and returns a ReleaseWorkflowResult reflecting that, alongside
// the original error -- see ReleaseWorkflow's doc comment on why a failure
// at any step must not leave targets stuck mid-flight.
func recordFailure(ctx workflow.Context, in ReleaseWorkflowInput, cause error) (ReleaseWorkflowResult, error) {
	result := ReleaseWorkflowResult{ReleaseRunID: in.ReleaseRunID}
	for _, t := range in.Targets {
		detail := cause.Error()
		// Best-effort: a RecordTargetState failure here must not mask
		// cause, the actual reason the release failed.
		_ = recordTargetState(ctx, in.ReleaseRunID, t, repository.ReleaseRunTargetStateFailed, "", detail)
		result.Targets = append(result.Targets, ReleaseTargetResult{
			OwnerFullName: t.OwnerFullName,
			Kind:          t.Kind,
			State:         repository.ReleaseRunTargetStateFailed,
			ErrorDetail:   detail,
		})
	}
	return result, cause
}

// digestOverrides builds DispatchBuild's digestOverrides map from
// in.Targets, keyed by ReleaseTarget.key().
func digestOverrides(targets []ReleaseTarget) map[string]string {
	out := map[string]string{}
	for _, t := range targets {
		if t.Digest != "" {
			out[t.key()] = t.Digest
		}
	}
	return out
}

// The checkApproval/resolvePlan/dispatchBuild/pollBuild/verifyPublished/
// recordTargetState helpers below all call workflow.ExecuteActivity by the
// Activity* name constants (see this file's "Activity name constants"
// section) rather than a Go method value, keeping ReleaseWorkflow's
// dependency on the ReleaseActivities interface rather than a concrete
// implementation -- see the package doc comment's "Scaffold status".

func checkApproval(ctx workflow.Context, releaseRunID string) (bool, error) {
	var approved bool
	err := workflow.ExecuteActivity(ctx, ActivityCheckApproval, releaseRunID).Get(ctx, &approved)
	return approved, err
}

func resolvePlan(ctx workflow.Context, targets []ReleaseTarget) (ResolvedPlan, error) {
	var plan ResolvedPlan
	err := workflow.ExecuteActivity(ctx, ActivityResolvePlan, targets).Get(ctx, &plan)
	return plan, err
}

func dispatchBuild(ctx workflow.Context, plan ResolvedPlan, digests map[string]string) (BuildRef, error) {
	var ref BuildRef
	err := workflow.ExecuteActivity(ctx, ActivityDispatchBuild, plan, digests).Get(ctx, &ref)
	return ref, err
}

func pollBuild(ctx workflow.Context, ref BuildRef) (BuildStatus, error) {
	var status BuildStatus
	err := workflow.ExecuteActivity(ctx, ActivityPollBuild, ref).Get(ctx, &status)
	return status, err
}

func verifyPublished(ctx workflow.Context, releaseRunID string) (VerifyResult, error) {
	var result VerifyResult
	err := workflow.ExecuteActivity(ctx, ActivityVerifyPublished, releaseRunID).Get(ctx, &result)
	return result, err
}

func recordTargetState(ctx workflow.Context, releaseRunID string, target ReleaseTarget, state repository.ReleaseRunTargetState, buildID, errorDetail string) error {
	return workflow.ExecuteActivity(ctx, ActivityRecordTargetState, releaseRunID, target, state, buildID, errorDetail).Get(ctx, nil)
}
