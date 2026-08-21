package release

import "context"

// CheckApproval implements FR14's approval-gate extension point: as of this
// package's introduction it is a true no-op that always approves
// immediately, mirroring the schema fields architecture/18-future-approval-
// gate.md already reserves for a real gate (environment.requires_approval,
// PromotionState.PENDING_APPROVAL) without this package depending on them --
// no new schema is needed for a release-level gate, and none is added here.
//
// A future real implementation (e.g. one that inspects the release run's
// targets against an environment/domain policy and returns false, leaving
// the run PENDING_APPROVAL for a later Approve action) replaces only this
// method's body. ReleaseWorkflow, ReleaseWorkflowInput, and every other
// activity stay exactly as they are -- ReleaseWorkflow already calls
// CheckApproval once, before ResolvePlan (see workflow.go), and already
// handles a false result without a build being dispatched. That is the
// whole point of drawing the activity boundary here, matching
// worker/writeback/workflow.go's "Swapping the stub for a real
// implementation" precedent.
func (a *Activities) CheckApproval(ctx context.Context, releaseRunID string) (bool, error) {
	return true, nil
}
