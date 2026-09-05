// calibration.go covers the per-Channel outcome-bar calibration trend
// (C14 / FR3 / FR4 / FR5 / FR7, issue #1884): one calendar-month row of
// candidate / calibrated / miscalibrated counts and rate, classified
// against a Channel's outcome_bar (migration 014, issue #1882). Built on
// top of browse.go's predictionOutcomeJoin (issue #1582/#1808) -- reused
// as-is, not modified -- with two additional predicates layered on to
// narrow it to FR3's calibration-candidate definition: a viable verdict
// (FR3a) and an actually-published video (FR3b); "synced metrics exist"
// (FR3c) is already enforced by that join's inner LATERAL join to
// video_metrics.
//
// CalibrationStore performs NO authorization itself and does NOT read
// outcome_bar itself -- store.CanRead is applied by callers (NFR5), and
// the caller supplies the OutcomeBar to classify against (FR4 always
// classifies against the Channel's CURRENT bar; there is no historical
// snapshot, see outcome_bar.go's doc comment / AGENTS.md § SCD2).
package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrCalibrationNotImplemented is returned by every CalibrationStore
// method until Implementation wires in the real SQL -- same scaffold/feat
// split other store methods in this package have followed (e.g.
// OutcomeBarStore, store/outcome_bar.go).
var ErrCalibrationNotImplemented = errors.New("store: calibration not implemented")

// CalibrationBucket is one calendar-month row of the calibration trend
// (FR5). Rate is calibrated/candidate as a float in [0,1]; it is never
// computed for an empty bucket (Candidates == 0 rows are not emitted at
// all, since GROUP BY produces no row for a month with no candidates).
type CalibrationBucket struct {
	BucketStart   time.Time // month start, UTC (date_trunc('month', published_at))
	Candidates    int
	Calibrated    int
	Miscalibrated int
	Rate          float64
}

// CalibrationStore covers the calibration trend read (C14 / FR3 / FR4 /
// FR5 / FR7, issue #1884).
type CalibrationStore interface {
	// MonthlyTrend returns one CalibrationBucket per calendar month that
	// has at least one calibration candidate (FR3) on channelID,
	// classified against bar (FR4 -- always the Channel's CURRENT bar;
	// there is no historical snapshot). since/before bound the window by
	// the candidate video's published_at (nil = unbounded on that side),
	// matching PredictionVsOutcome's NULL-safe idiom. limit caps the
	// number of BUCKET ROWS (<=0 = unbounded); truncated reports whether
	// older buckets exist beyond it -- rows are always returned
	// chronologically (oldest -> newest), so truncated means "older
	// buckets exist beyond the window", and a caller pages backward by
	// re-calling with before set to the oldest returned bucket's
	// BucketStart (the same backward-paging idiom PredictionVsOutcome
	// documents for issue #1808). Returns ErrUnsupportedOutcomeBarMetric,
	// and no rows, for a bar whose MetricName is not
	// OutcomeBarMetricViews.
	MonthlyTrend(ctx context.Context, channelID uuid.UUID, bar OutcomeBar, since, before *time.Time, limit int) (rows []CalibrationBucket, truncated bool, err error)
}

// calibrationStore implements CalibrationStore against `idea`,
// `video_script`, `viability_verdict`, `video_schedule_match`,
// `synced_video`, `video_metrics` (migrations 002/010/012) via
// browse.go's predictionOutcomeJoin, classified against a caller-supplied
// `outcome_bar` (migration 014) row. Every method returns
// ErrCalibrationNotImplemented until Implementation wires in the real
// queries.
type calibrationStore struct{ pool *pgxpool.Pool }

var _ CalibrationStore = calibrationStore{}

func (s calibrationStore) MonthlyTrend(ctx context.Context, channelID uuid.UUID, bar OutcomeBar, since, before *time.Time, limit int) ([]CalibrationBucket, bool, error) {
	return nil, false, ErrCalibrationNotImplemented
}
