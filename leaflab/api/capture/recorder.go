package capture

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/whale-net/everything/leaflab/api/tiers"
)

// captureTiers lists the tiers a placement boundary is recorded against --
// five-minute and hourly, migration 022's two continuous-aggregate tiers.
// Raw is never captured against: raw already resolves any window exactly
// on its own, with no aggregate bucket to straddle.
var captureTiers = []tiers.Tier{tiers.TierFiveMinute, tiers.TierHourly}

// tierBucketWidth maps each captured tier to the literal interval width
// migration 022 defined its continuous aggregate with -- shared by Recorder
// (to find the bucket boundaryAt falls in) and Completer (to find when that
// bucket closes and, for the five-minute tier, its bucket's end instant).
var tierBucketWidth = map[tiers.Tier]string{
	tiers.TierFiveMinute: "5 minutes",
	tiers.TierHourly:     "1 hour",
}

// pgTimeBucket asks Postgres itself for time_bucket(width, at) rather than
// reimplementing bucket alignment in Go -- this is what guarantees a
// boundary_capture row's bucket_start agrees, byte for byte, with the
// bucket migration 022's sensor_reading_5m / sensor_reading_1h continuous
// aggregates group recorded_at into (FR20.4's "the straddling bucket equals
// the raw-restricted computation" verifiable clause depends on the two
// never silently drifting apart).
func pgTimeBucket(ctx context.Context, q querier, width string, at time.Time) (time.Time, error) {
	var bucketStart time.Time
	if err := q.QueryRow(ctx, `SELECT time_bucket($1::interval, $2::timestamptz)`, width, at).Scan(&bucketStart); err != nil {
		return time.Time{}, fmt.Errorf("capture: compute time_bucket(%s, %s): %w", width, at, err)
	}
	return bucketStart, nil
}

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
// Record never de-duplicates against a capture already pending for the same
// (sensor, tier, bucket_start): a bucket that straddles N placement
// boundaries is expected to accumulate N boundary_capture rows, one per
// boundary event, each later splitting the partial it falls inside
// (FR20.3, Completer.RunPending) -- collapsing them at insert time would
// break that induction.
func (r *Recorder) Record(ctx context.Context, tx pgx.Tx, affectedSensorIDs []int64, boundaryAt time.Time) error {
	if len(affectedSensorIDs) == 0 {
		return nil
	}

	for _, tier := range captureTiers {
		width, ok := tierBucketWidth[tier]
		if !ok {
			return fmt.Errorf("capture: no bucket width configured for tier %q", tier)
		}

		bucketStart, err := pgTimeBucket(ctx, tx, width, boundaryAt)
		if err != nil {
			return err
		}

		for _, sensorID := range affectedSensorIDs {
			if _, err := tx.Exec(ctx, `
				INSERT INTO boundary_capture (sensor_id, boundary_at, tier, bucket_start)
				VALUES ($1, $2, $3, $4)
			`, sensorID, boundaryAt, string(tier), bucketStart); err != nil {
				return fmt.Errorf("capture: insert boundary_capture for sensor %d, tier %s: %w", sensorID, tier, err)
			}
		}
	}

	return nil
}
