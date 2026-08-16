// Package writeback implements AR-4b's writeback contract: the
// WritebackWorkflow that carries one promotion's state out of the registry,
// and the Writeback activity interface it drives -- see
// ../../ARCHITECTURE.md "Writeback: outbox -> Temporal" and PLAN.md's AR-4b
// scope.
//
// # Determinism
//
// WritebackWorkflow is replayed by the Temporal SDK, so its code must be
// deterministic: no direct network/disk I/O, no time.Now(), no map
// iteration whose order matters, nothing that could produce a different
// result on replay than it did the first time. Every side effect (calling
// the AppRegistry API, writing a file, a future gitops commit) lives behind
// the Writeback activity interface below and is invoked only via
// workflow.ExecuteActivity. See AGENTS.md/PLAN.md's "Workflow determinism"
// hazard.
//
// # Swapping the stub for a real implementation
//
// AR-4b ships StubActivities (stub.go): RenderEnvironmentState reads state
// via the AppRegistry API's GetEnvironmentState RPC, and Publish writes the
// rendered document to a local path, skipping a no-op write when
// state_hash already matches what was last published. Neither commits to
// the gitops repo nor publishes to S3 -- both are explicitly out of scope
// for AR-4b (see PLAN.md).
//
// A later change plugs in a real gitops-committer implementation of the
// same Writeback interface (e.g. one whose Publish clones/commits/pushes a
// git repo) and registers it with the worker instead of StubActivities.
// WritebackWorkflow, WritebackInput, RenderedState, and PublishResult all
// stay exactly as they are -- no schema, proto, or workflow change is
// needed. That is the whole point of drawing the activity boundary here.
package writeback

import (
	"context"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// TaskQueue is the Temporal task queue the worker and the outbox drain loop
// agree on for WritebackWorkflow. A constant (not env-configurable) because
// nothing outside this binary needs to override it -- unlike
// TEMPORAL_TASK_QUEUE in libs/go/temporal, which is a generic default for
// callers with no opinion.
const TaskQueue = "app-registry-writeback"

// Activity name constants. WritebackWorkflow dispatches activities by these
// string names (not by Go function/method value) so the Writeback
// interface -- not a concrete implementation -- is the boundary the
// workflow depends on; see the package doc comment's "Swapping the stub"
// section. The worker registers whatever Writeback implementation it is
// built with under these same names (see ../main.go).
const (
	ActivityRenderEnvironmentState = "RenderEnvironmentState"
	ActivityPublish                = "Publish"
)

// WritebackInput is WritebackWorkflow's single argument -- everything an
// activity needs to render and publish state for one promotion, without
// reaching back into Postgres itself. PromotionID doubles as the workflow
// id (see ../outbox/drain.go), so Temporal's own dedup on workflow id makes
// a redelivered outbox row harmless -- see ARCHITECTURE.md.
type WritebackInput struct {
	PromotionID    string
	EnvironmentKey string
	// Domain is the promotion's owning app's/chart's domain (see
	// server/repository/models.go's WritebackOutbox.Domain and
	// 014_writeback_outbox_domain.up.sql). RenderEnvironmentState passes it
	// to GetEnvironmentState as the domain filter, so a real (non-stub)
	// Writeback implementation renders one small per-domain document
	// (<domain>/versions/<env>.yaml) instead of the whole environment --
	// see worker/writeback/gitops.go and whale-net/argok8s#68.
	Domain string
	// StateHash is the outbox row's state_hash, computed by the server
	// inside the promotion transaction (see
	// server/handlers/promotion.go's stateHash). Passed through so a
	// future activity can compare against a hash it already knows about
	// without a second registry read, and so RenderEnvironmentState's own
	// read is independently checkable against it.
	StateHash string
}

// RenderedState is RenderEnvironmentState's output: environment state
// pre-rendered into the document Publish writes. Its own type (not the raw
// GetEnvironmentStateResponse proto) so a future change to how rendering
// works doesn't change the activity boundary between the two steps.
type RenderedState struct {
	EnvironmentKey string
	// Domain identifies the target gitops path
	// (<domain>/versions/<EnvironmentKey>.yaml, see issue #798's "Contract
	// v1") for a real Publish implementation. Carried forward from
	// WritebackInput.Domain rather than re-derived, so Publish never has to
	// re-resolve it.
	Domain string
	// StateHash is read back from the GetEnvironmentState response itself
	// (not copied from WritebackInput), so Publish's no-op check reflects
	// what was actually rendered just now, not what the outbox row
	// believed at enqueue time -- the two agree unless something else
	// promoted concurrently, in which case rendering the freshest state is
	// correct.
	StateHash string
	// RenderedAt records generation time, informational only. Must never
	// affect StateHash or Document -- workflow code reads RenderedState's
	// fields only via activity results, but the activity implementation
	// itself must keep this side-effect-free of the hash for the no-op
	// check to mean anything.
	RenderedAt time.Time
	// Document is the fully rendered payload -- JSON today (protojson of
	// GetEnvironmentStateResponse), whatever format a real gitops
	// committer wants tomorrow (e.g. a values.yaml). The workflow treats
	// it as opaque bytes.
	Document []byte
}

// PublishResult is Publish's outcome.
type PublishResult struct {
	// Location is a stub-defined string identifying where the document
	// went -- a local file path today; an object-store key or gitops
	// commit SHA once a real implementation replaces StubActivities.
	// Informational only.
	Location string
	// Skipped is true when Publish detected the document is a no-op
	// against the target's last-published state and wrote nothing -- the
	// state_hash no-op detection ARCHITECTURE.md commits the real
	// implementation to inheriting.
	Skipped bool
}

// Writeback is the activity interface WritebackWorkflow drives. Every
// method is a Temporal activity: it may perform I/O, must be idempotent
// under Temporal's at-least-once activity retry, and must never be invoked
// directly from workflow code -- only via workflow.ExecuteActivity (see the
// package doc comment's "Determinism" section).
type Writeback interface {
	// RenderEnvironmentState reads current promotion state for
	// in.EnvironmentKey via the AppRegistry API's GetEnvironmentState RPC
	// (see ARCHITECTURE.md "The API is the write path; git is the
	// delivery path" -- the worker never reads Postgres directly for
	// this) and renders it into a RenderedState. Pure read-then-transform;
	// must not publish anything. Idempotent: calling it twice for the
	// same promotion produces the same Document as long as environment
	// state hasn't changed since.
	RenderEnvironmentState(ctx context.Context, in WritebackInput) (RenderedState, error)

	// Publish writes state to the target sink and reports whether it did.
	// Must be a no-op (Skipped = true) when state.StateHash already
	// matches what was last published for state.EnvironmentKey -- see
	// RenderedState.StateHash and PublishResult.Skipped. This is what
	// makes Publish safe to call more than once for the same rendered
	// state, which happens whenever a workflow is retried or its
	// enclosing outbox row is reclaimed after a worker is killed mid-run.
	Publish(ctx context.Context, state RenderedState) (PublishResult, error)
}

// WritebackWorkflow carries in.PromotionID's promotion state out of the
// registry: render, then publish. Its workflow id is always in.PromotionID
// (set by the caller of ExecuteWorkflow -- see ../outbox/drain.go), so
// Temporal's own workflow-id collision handling makes starting the same
// promotion's workflow twice (e.g. after a worker is killed mid-run and the
// outbox row is reclaimed) either return
// serviceerror.WorkflowExecutionAlreadyStarted while the first run is still
// open, or -- if it already finished -- start a fresh run that redoes the
// same idempotent work. Either way nothing is lost and nothing double-
// publishes, because Publish's no-op check makes a redundant run of this
// workflow inexpensive rather than merely harmless.
//
// See the package doc comment for the determinism rules this function must
// follow.
func WritebackWorkflow(ctx workflow.Context, in WritebackInput) (PublishResult, error) {
	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 5,
		},
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	var rendered RenderedState
	if err := workflow.ExecuteActivity(ctx, ActivityRenderEnvironmentState, in).Get(ctx, &rendered); err != nil {
		return PublishResult{}, err
	}

	var result PublishResult
	if err := workflow.ExecuteActivity(ctx, ActivityPublish, rendered).Get(ctx, &result); err != nil {
		return PublishResult{}, err
	}
	return result, nil
}
