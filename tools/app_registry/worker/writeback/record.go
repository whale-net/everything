// record.go implements RecordWritebackResult, the activity WritebackWorkflow
// calls immediately after Publish succeeds to durably persist its result
// (Location, CommitSHA) onto the promotion's writeback_outbox row -- FR7a,
// issue #1029. Same "worker talks to Postgres directly for its own
// write-path activities" pattern as worker/release/record.go's
// RecordResolvedPlan/RecordTargetState (not routed through a new gRPC RPC).
//
// A separate type from GitOpsActivities/StubActivities (not a method on
// either) because both Writeback implementations share this same
// result-recording activity -- see ActivityRecordWritebackResult's doc
// comment.
package writeback

import (
	"context"
	"fmt"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// Recorder implements the RecordWritebackResult activity against
// repository.Registry. Constructed and registered by ../main.go alongside
// whichever Writeback (Render/Publish) implementation the worker is built
// with -- see ActivityRecordWritebackResult.
type Recorder struct {
	// Registry is the direct-Postgres repository.Registry
	// RecordWritebackResult writes through -- same rationale as
	// worker/release.Activities.Registry (see that field's doc comment):
	// WritebackRepository.RecordResult has no mutating gRPC RPC equivalent.
	Registry repository.Registry
}

// RecordWritebackResult persists location/commitSHA onto the
// writeback_outbox row for promotionID via
// Registry.Writeback().RecordResult -- see that interface method's doc
// comment. A failure here must not fail WritebackWorkflow's already-
// successful Publish outcome -- see workflow.go's doc comment on this
// activity's best-effort retry/failure semantics.
func (r *Recorder) RecordWritebackResult(ctx context.Context, promotionID, location, commitSHA string) error {
	if r.Registry == nil {
		return fmt.Errorf("record writeback result for promotion %s: Recorder.Registry not configured", promotionID)
	}
	if err := r.Registry.Writeback().RecordResult(ctx, "", promotionID, location, commitSHA); err != nil {
		return fmt.Errorf("record writeback result for promotion %s: %w", promotionID, err)
	}
	return nil
}
