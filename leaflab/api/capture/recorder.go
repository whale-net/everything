package capture

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/whale-net/everything/leaflab/api/tiers"
)

// captureTiers lists the tiers a placement boundary is recorded against --
// five-minute and hourly, migration 022's two continuous-aggregate tiers.
// Raw is never captured against: raw already resolves any window exactly
// on its own, with no aggregate bucket to straddle.
var captureTiers = []tiers.Tier{tiers.TierFiveMinute, tiers.TierHourly}

// Recorder is phase one of FR20's two-phase boundary capture: at the
// instant a placement boundary is recorded, it inserts one boundary_capture
// row (migration 033) per affected sensor and tier, for the bucket
// boundaryAt falls into at that tier.
type Recorder struct{}

// NewRecorder constructs a Recorder. Recorder carries no state of its own
// -- every call to Record is scoped to the caller-supplied transaction, so
// nothing needs to be threaded through the constructor yet.
func NewRecorder() *Recorder {
	return &Recorder{}
}

// Record inserts a boundary_capture row for each of affectedSensorIDs, at
// each tier in captureTiers, for the bucket boundaryAt falls into -- in the
// same transaction as tx.
//
// tx is always the caller's own transaction, never one Record opens itself
// (FR20's Implementation section: "Insert boundary_capture rows in the same
// transaction as the placement write") -- the two intended callers are
// leaflab/api/placement's SCD2 close-and-open writer (FR19) and Phase 5's
// FR51/FR74 writers, both of which already hold an open transaction for
// the placement write itself.
//
// affectedSensorIDs is the caller's responsibility to compute -- the
// sensors in the region subtree the plant left or entered (FR20's
// Implementation section) -- Record itself does not walk the region tree;
// that keeps this package agnostic to how a caller determines "affected,"
// which differs between a plain move (FR19) and a region-lifecycle move
// (FR51/FR74).
//
// Scaffold only (this task's Scaffold phase, #1360): this task's
// Implementation phase fills in the actual INSERT and the per-tier
// bucket-boundary arithmetic (which bucket_start boundaryAt falls into at
// each tier in captureTiers).
func (r *Recorder) Record(ctx context.Context, tx pgx.Tx, affectedSensorIDs []int64, boundaryAt time.Time) error {
	return ErrNotImplemented
}
