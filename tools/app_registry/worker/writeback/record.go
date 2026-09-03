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

	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/tools/app_registry/events"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// workerLog is this package's logger, shared by every activity file here.
// Deliberately a package-level slog logger via logging.Get, not
// activity.GetLogger(ctx): several of these methods are called directly by
// unit tests with a plain context.Background() (not through Temporal's
// activity execution machinery), and activity.GetLogger panics outside a
// real activity context -- see worker/reaper and worker/outbox for the same
// "background component gets its own slog logger" precedent.
var workerLog = logging.Get("app-registry-worker-writeback")

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
	// Publisher enqueues writeback-result transition events for subscribers;
	// see #1130 (FR7c). Nil in tests that do not verify publishing behavior.
	Publisher events.PublisherInterface
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
	workerLog.Info("writeback result recorded", "promotion_id", promotionID, "location", location, "commit_sha", commitSHA)

	// FR7a/FR7c: publish after write commits, but only if publisher is configured.
	// Publish errors are discarded and logged by the publisher; see #1130 for details.
	if r.Publisher != nil {
		r.Publisher.Publish(promotionID, "writeback_completed", "success")
	}
	return nil
}
