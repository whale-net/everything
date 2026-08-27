package capture

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// aggregateResult is the four-column shape both boundary_partial and
// migration 022's continuous aggregates share (reading_count, value_sum,
// value_min, value_max) -- value_avg is deliberately never carried: it is
// always derived as Sum / Count by a caller, never averaged (migration
// 022's comment, mirrored by FR20.3's "avg is derived from sum/count, never
// averaged").
//
// Count == 0 means the interval held no readings; Sum/Min/Max are the
// arithmetic identity (0) in that case, not a meaningful value -- a caller
// must check Count before trusting Min/Max, same as it must before dividing
// Sum by Count for an average. boundary_partial's columns are NOT NULL
// (migration 033), so an empty partial is written as zeros rather than
// left unrepresented; the row's existence (not a sentinel value) means
// "this interval was captured, and had no readings."
type aggregateResult struct {
	Count int64
	Sum   float64
	Min   float64
	Max   float64
}

// merge combines two aggregateResults computed over disjoint intervals into
// the aggregate over their union -- count and sum simply add; min/max are
// only combined when both sides actually held data, since min(0, actualMin)
// would corrupt a real minimum with the empty side's zero placeholder.
func (a aggregateResult) merge(b aggregateResult) aggregateResult {
	switch {
	case a.Count == 0:
		return b
	case b.Count == 0:
		return a
	}
	merged := aggregateResult{
		Count: a.Count + b.Count,
		Sum:   a.Sum + b.Sum,
		Min:   a.Min,
		Max:   a.Max,
	}
	if b.Min < merged.Min {
		merged.Min = b.Min
	}
	if b.Max > merged.Max {
		merged.Max = b.Max
	}
	return merged
}

// rawRestrictedAggregate computes count/sum/min/max directly from
// sensor_reading over [from, to) for sensorID -- phase two's "from raw,
// both sides, independently" (A17) for the five-minute tier, the finest
// tier a placement boundary is ever captured against. It never filters on
// sensor_reading.valid, matching migration 022's continuous aggregate
// definitions (which also group raw rows with no valid filter) -- a
// boundary_partial and the sensor_reading_5m bucket it can stand in for
// must be computed the same way, or FR20.4's "the same bucket, split or
// whole" equivalence breaks.
func rawRestrictedAggregate(ctx context.Context, q querier, sensorID int64, from, to time.Time) (aggregateResult, error) {
	var res aggregateResult
	var min, max sql.NullFloat64
	err := q.QueryRow(ctx, `
		SELECT count(*), coalesce(sum(value), 0), min(value), max(value)
		FROM sensor_reading
		WHERE sensor_id = $1 AND recorded_at >= $2 AND recorded_at < $3
	`, sensorID, from, to).Scan(&res.Count, &res.Sum, &min, &max)
	if err != nil {
		return aggregateResult{}, fmt.Errorf("capture: raw-restricted aggregate for sensor %d [%s, %s): %w", sensorID, from, to, err)
	}
	if min.Valid {
		res.Min = min.Float64
	}
	if max.Valid {
		res.Max = max.Float64
	}
	return res, nil
}

// fiveMinuteFullBucketsAggregate sums whole sensor_reading_5m buckets in
// [from, to) for sensorID, excluding any bucket that has itself been split
// by a five-minute boundary_capture (that bucket's contribution comes from
// its boundary_partial rows instead, via fiveMinutePartialsAggregate --
// never both, which would double-count it).
func fiveMinuteFullBucketsAggregate(ctx context.Context, q querier, sensorID int64, from, to time.Time) (aggregateResult, error) {
	var res aggregateResult
	var min, max sql.NullFloat64
	err := q.QueryRow(ctx, `
		SELECT
			coalesce(sum(m.reading_count), 0)::bigint,
			coalesce(sum(m.value_sum), 0),
			min(m.value_min),
			max(m.value_max)
		FROM sensor_reading_5m m
		WHERE m.sensor_id = $1
		  AND m.bucket >= $2 AND m.bucket < $3
		  AND NOT EXISTS (
		      SELECT 1
		      FROM boundary_partial bp
		      JOIN boundary_capture bc ON bc.capture_id = bp.capture_id
		      WHERE bc.sensor_id = m.sensor_id
		        AND bp.tier = 'five_minute'
		        AND bp.bucket_start = m.bucket
		  )
	`, sensorID, from, to).Scan(&res.Count, &res.Sum, &min, &max)
	if err != nil {
		return aggregateResult{}, fmt.Errorf("capture: five-minute full-bucket aggregate for sensor %d [%s, %s): %w", sensorID, from, to, err)
	}
	if min.Valid {
		res.Min = min.Float64
	}
	if max.Valid {
		res.Max = max.Float64
	}
	return res, nil
}

// fiveMinutePartialsAggregate sums every five-minute boundary_partial for
// sensorID whose [partial_from, partial_to) lies within [from, to) -- the
// contribution of any five-minute bucket that a placement boundary split,
// standing in for the whole-bucket row fiveMinuteFullBucketsAggregate
// deliberately excludes.
func fiveMinutePartialsAggregate(ctx context.Context, q querier, sensorID int64, from, to time.Time) (aggregateResult, error) {
	var res aggregateResult
	var min, max sql.NullFloat64
	err := q.QueryRow(ctx, `
		SELECT
			coalesce(sum(bp.reading_count), 0)::bigint,
			coalesce(sum(bp.value_sum), 0),
			min(bp.value_min),
			max(bp.value_max)
		FROM boundary_partial bp
		JOIN boundary_capture bc ON bc.capture_id = bp.capture_id
		WHERE bc.sensor_id = $1
		  AND bp.tier = 'five_minute'
		  AND bp.partial_from >= $2 AND bp.partial_to <= $3
	`, sensorID, from, to).Scan(&res.Count, &res.Sum, &min, &max)
	if err != nil {
		return aggregateResult{}, fmt.Errorf("capture: five-minute partials aggregate for sensor %d [%s, %s): %w", sensorID, from, to, err)
	}
	if min.Valid {
		res.Min = min.Float64
	}
	if max.Valid {
		res.Max = max.Float64
	}
	return res, nil
}

// fiveMinuteComposedAggregate computes the hourly tier's contribution over
// [from, to) by composing the five-minute tier -- whole buckets plus any
// five-minute partials -- rather than a second raw scan (FR20.3: "a coarser
// tier's partials are composed from the finer tier's rather than from a
// second raw scan").
func fiveMinuteComposedAggregate(ctx context.Context, q querier, sensorID int64, from, to time.Time) (aggregateResult, error) {
	full, err := fiveMinuteFullBucketsAggregate(ctx, q, sensorID, from, to)
	if err != nil {
		return aggregateResult{}, err
	}
	partial, err := fiveMinutePartialsAggregate(ctx, q, sensorID, from, to)
	if err != nil {
		return aggregateResult{}, err
	}
	return full.merge(partial), nil
}
