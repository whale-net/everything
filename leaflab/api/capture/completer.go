package capture

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Completer is phase two of FR20's two-phase boundary capture: at bucket
// close, it computes each pending capture's partials from raw -- both
// sides, independently -- and durably writes them as boundary_partial rows
// (migration 033) before marking the capture completed. It never derives
// one side from `full_bucket - other_side`, for any of count, sum, min or
// max (A17): min and max are not invertible, and keeping every aggregate
// on one code path avoids a special case that would only be safe for
// sum/count.
type Completer struct {
	db *pgxpool.Pool
}

// NewCompleter constructs a Completer over db. Unlike Recorder, Completer
// owns its own transactions -- it runs independently of any caller's
// placement-write transaction, on its own schedule (see this package's doc
// comment for where that schedule lives and why).
func NewCompleter(db *pgxpool.Pool) *Completer {
	return &Completer{db: db}
}

// RunPending finds every boundary_capture row whose bucket has closed and
// is still state = 'pending' (idx_boundary_capture_pending, migration 033),
// computes its partials from raw, and marks the capture completed -- each
// capture as one unit of work, so a partially-completed capture is never
// left half-written.
//
// For a bucket capturing its first boundary, computing partials means one
// raw-restricted scan on each side of boundary_at. For a bucket that
// already holds partials from an earlier boundary in the same bucket
// (FR20.3: "N boundaries in one bucket compose to N+1 partials"), it means
// finding the existing partial whose [partial_from, partial_to) contains
// the new boundary and replacing it with two -- never recomputing partials
// that a prior boundary in the same bucket already settled.
//
// The hourly tier's partials are composed from the five-minute tier's
// partials and full buckets (FR20.3: "a coarser tier's partials are
// composed from the finer tier's rather than from a second raw scan"), not
// recomputed from raw independently -- RunPending's Implementation-phase
// body must preserve that: it is the difference between "coarser tiers
// stay honest at scale" and a second raw scan per tier.
//
// NFR5: a capture still pending as its raw chunk approaches retention must
// fail loudly, not be silently dropped -- wiring that alert/assertion
// against the tiers package's derived retention constants is also part of
// this task's Implementation phase.
//
// Scaffold only (this task's Scaffold phase, #1360): the actual partial
// computation described above is filled in during this task's
// Implementation phase.
func (c *Completer) RunPending(ctx context.Context) error {
	return ErrNotImplemented
}
