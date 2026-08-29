package readings

import (
	"context"
	"fmt"
	"time"

	"github.com/whale-net/everything/leaflab/api/suspect"
	"github.com/whale-net/everything/leaflab/api/tiers"
)

// This file wires FR26.3's enumerable checks (leaflab/api/suspect) into
// the bounded read path's queries. Every check is computed against the
// raw reading(s) a point summarizes -- never against a value this package
// invents -- and every check is bounded (NFR3.2): CheckOutOfRange and
// CheckPersistedInvalidFlag never require a scan proportional to the
// query window (the former reuses the tier's own min/max composition
// property, the latter rides idx_sensor_reading_invalid's partial index,
// which is small because invalid readings are rare); CheckStaleAttribution
// and CheckMigrationSnapWindow are derived from sensor_region_history and
// plant_region_history, both of which are bounded by the number of
// placement changes that have ever happened, not by reading volume.
//
// FR26.2's compensating control is load-bearing here too: nothing in this
// file writes back to sensor_reading.region_id. A disagreement is
// reported, never corrected.

// tierBucketWidth returns the duration a single bucket of tier spans, or
// zero for TierRaw (a raw point is not a bucket at all -- see this
// package's Point doc comment).
func tierBucketWidth(tier tiers.Tier) time.Duration {
	switch tier {
	case tiers.TierFiveMinute:
		return 5 * time.Minute
	case tiers.TierHourly:
		return time.Hour
	default:
		return 0
	}
}

// regionInterval2 is a half-open [From, To) time range with To's zero
// value meaning "open/unbounded" -- distinct from readings.regionInterval
// (plant-placement-specific, keyed to a single region already) since this
// shape is used for arbitrary interval-overlap arithmetic below.
type regionInterval2 struct {
	From, To time.Time
}

// overlaps reports whether a and b share any instant, treating a zero To
// as +infinity.
func (a regionInterval2) overlaps(b regionInterval2) bool {
	aTo := a.To
	bTo := b.To
	if !aTo.IsZero() && !b.From.Before(aTo) {
		return false
	}
	if !bTo.IsZero() && !a.From.Before(bTo) {
		return false
	}
	return true
}

// intersect returns the overlapping sub-range of a and b. Callers must
// only call this after overlaps(a, b) is true.
func (a regionInterval2) intersect(b regionInterval2) regionInterval2 {
	from := a.From
	if b.From.After(from) {
		from = b.From
	}
	to := a.To
	if to.IsZero() || (!b.To.IsZero() && b.To.Before(to)) {
		to = b.To
	}
	return regionInterval2{From: from, To: to}
}

// containsInstant reports whether t falls in [iv.From, iv.To), treating a
// zero iv.To as +infinity.
func (iv regionInterval2) containsInstant(t time.Time) bool {
	if t.Before(iv.From) {
		return false
	}
	return iv.To.IsZero() || t.Before(iv.To)
}

// regionSnapWindows returns, for each region in regionIDs, every sub-range
// where two or more distinct plants' plant_region_history intervals are
// concurrently open for that region -- FR21's disclosed migration-snap
// cost (migration 017's backfill comment: "a plant removed mid-hour and
// whatever plant next occupies its region share that hour's bucket").
// Derived purely from plant_region_history's own (few) intervals per
// region -- never a sensor_reading scan -- so this is bounded by how many
// plants have ever occupied these regions, independent of window length.
func (r *Reader) regionSnapWindows(ctx context.Context, regionIDs []int64) (map[int64][]regionInterval2, error) {
	if len(regionIDs) == 0 {
		return nil, nil
	}
	rows, err := r.db.Query(ctx, `
		SELECT region_id, plant_id, valid_from, valid_to
		FROM plant_region_history
		WHERE region_id = ANY($1)
		ORDER BY region_id, valid_from
	`, regionIDs)
	if err != nil {
		return nil, fmt.Errorf("readings: load plant_region_history for migration-snap check: %w", err)
	}
	defer rows.Close()

	type interval struct {
		plantID  int64
		from, to time.Time // zero to = open
	}
	byRegion := make(map[int64][]interval)
	for rows.Next() {
		var regionID, plantID int64
		var validFrom time.Time
		var validTo *time.Time
		if err := rows.Scan(&regionID, &plantID, &validFrom, &validTo); err != nil {
			return nil, fmt.Errorf("readings: scan plant_region_history row: %w", err)
		}
		iv := interval{plantID: plantID, from: validFrom}
		if validTo != nil {
			iv.to = *validTo
		}
		byRegion[regionID] = append(byRegion[regionID], iv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("readings: iterate plant_region_history rows: %w", err)
	}

	windows := make(map[int64][]regionInterval2)
	for regionID, intervals := range byRegion {
		for i := 0; i < len(intervals); i++ {
			for j := i + 1; j < len(intervals); j++ {
				if intervals[i].plantID == intervals[j].plantID {
					continue
				}
				a := regionInterval2{From: intervals[i].from, To: intervals[i].to}
				b := regionInterval2{From: intervals[j].from, To: intervals[j].to}
				if a.overlaps(b) {
					windows[regionID] = append(windows[regionID], a.intersect(b))
				}
			}
		}
	}
	return windows, nil
}

// inSnapWindow reports whether the hour bucket containing recordedAt
// overlaps any of regionID's migration-snap windows.
func inSnapWindow(windows map[int64][]regionInterval2, regionID int64, recordedAt time.Time) bool {
	hourStart := recordedAt.Truncate(time.Hour)
	hourEnd := hourStart.Add(time.Hour)
	hour := regionInterval2{From: hourStart, To: hourEnd}
	for _, w := range windows[regionID] {
		if w.overlaps(hour) {
			return true
		}
	}
	return false
}

// sensorRegionAt resolves, from a preloaded slice of sensor_region_history
// rows (already filtered to the relevant sensors), the region a sensor was
// actually assigned to at instant t. Ok is false when no interval covers
// t (should not happen for any sensor with at least one history row
// covering its whole lifetime, but handled defensively rather than
// assumed).
type sensorRegionHistoryRow struct {
	sensorID  int64
	regionID  int64
	validFrom time.Time
	validTo   *time.Time
}

func sensorRegionAt(rows []sensorRegionHistoryRow, sensorID int64, t time.Time) (int64, bool) {
	for _, row := range rows {
		if row.sensorID != sensorID {
			continue
		}
		if t.Before(row.validFrom) {
			continue
		}
		if row.validTo != nil && !t.Before(*row.validTo) {
			continue
		}
		return row.regionID, true
	}
	return 0, false
}

// loadSensorRegionHistory fetches every sensor_region_history row for
// sensorIDs -- bounded by the number of placement changes those sensors
// have ever had, never by reading volume.
func (r *Reader) loadSensorRegionHistory(ctx context.Context, sensorIDs []int64) ([]sensorRegionHistoryRow, error) {
	if len(sensorIDs) == 0 {
		return nil, nil
	}
	rows, err := r.db.Query(ctx, `
		SELECT sensor_id, region_id, valid_from, valid_to
		FROM sensor_region_history
		WHERE sensor_id = ANY($1)
	`, sensorIDs)
	if err != nil {
		return nil, fmt.Errorf("readings: load sensor_region_history for stale-attribution check: %w", err)
	}
	defer rows.Close()

	var out []sensorRegionHistoryRow
	for rows.Next() {
		var row sensorRegionHistoryRow
		if err := rows.Scan(&row.sensorID, &row.regionID, &row.validFrom, &row.validTo); err != nil {
			return nil, fmt.Errorf("readings: scan sensor_region_history row: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("readings: iterate sensor_region_history rows: %w", err)
	}
	return out, nil
}

// invalidReadingBuckets returns the set of tier-bucket start instants
// (truncated to bucketWidth; bucketWidth == 0 means "raw," one entry per
// reading) that contain at least one sensor_reading row with valid =
// FALSE, matching whereFragment/whereArgs (already-parameterized SQL
// referencing sensor_reading's own columns, starting at $1) within
// window. Rides idx_sensor_reading_invalid (migration 001) -- the
// Postgres planner can use this partial index because the query's own
// WHERE clause repeats the index's predicate (valid = FALSE) verbatim, so
// this is cheap regardless of how wide window is (invalid readings are
// rare -- that's the whole reason the index is partial).
func (r *Reader) invalidReadingBuckets(ctx context.Context, whereFragment string, whereArgs []any, window Window, bucketWidth time.Duration) (map[time.Time]bool, error) {
	next := len(whereArgs) + 1
	args := append([]any{}, whereArgs...)
	query := fmt.Sprintf(`
		SELECT recorded_at
		FROM sensor_reading
		WHERE valid = FALSE AND %s AND recorded_at >= $%d AND recorded_at < $%d
	`, whereFragment, next, next+1)
	args = append(args, window.Start, window.End)

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("readings: query invalid readings: %w", err)
	}
	defer rows.Close()

	buckets := make(map[time.Time]bool)
	for rows.Next() {
		var recordedAt time.Time
		if err := rows.Scan(&recordedAt); err != nil {
			return nil, fmt.Errorf("readings: scan invalid reading row: %w", err)
		}
		buckets[bucketKey(recordedAt, bucketWidth)] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("readings: iterate invalid reading rows: %w", err)
	}
	return buckets, nil
}

// bucketKey truncates t to bucketWidth (UTC), or returns t unchanged when
// bucketWidth is zero (the raw-tier convention -- one point per reading,
// no bucketing).
func bucketKey(t time.Time, bucketWidth time.Duration) time.Time {
	if bucketWidth == 0 {
		return t
	}
	return t.UTC().Truncate(bucketWidth)
}

// pointMarkers projects points' SuspectChecks into suspect.Marker values,
// in the same order, for suspect.CountMarkers -- the one place every
// readings response's top-level marked_count/returned_count is tallied
// (FR26.3).
func pointMarkers(points []Point) []suspect.Marker {
	out := make([]suspect.Marker, len(points))
	for i, p := range points {
		out[i] = suspect.Marker{Checks: p.SuspectChecks}
	}
	return out
}

// markAggregatedPoints computes FR26.3's SuspectChecks for every point in
// points in place, without ever scanning raw sensor_reading rows
// proportional to window's length (NFR3.2): CheckOutOfRange is derived
// from each point's own Min/Max (exact via composition, migration 022's
// comment), and CheckPersistedInvalidFlag rides idx_sensor_reading_invalid
// (invalidWhereFragment/invalidWhereArgs -- already-parameterized SQL
// against sensor_reading's own unaliased columns, starting at $1, mirroring
// the caller's own raw-tier WHERE fragment so the two never disagree on
// which sensors/region this series includes).
//
// CheckOutOfRange is only evaluated when measurementTypeID names exactly
// one type: an unfiltered aggregated series merges every sensor type's
// values into one bucket (this package's seriesForSensorSet/seriesForRegion
// doc comments), so no single range applies -- same documented limitation
// as PeriodSummary's own per-type framing.
//
// CheckStaleAttribution and CheckMigrationSnapWindow additionally require
// one fixed region to compare against (sensor_region_history and
// plant_region_history are both keyed by region); fixedRegionID == 0 skips
// both (the sensor/board entity kind's aggregated series, merged across
// sensors and potentially regions -- see seriesForSensorSet's call site).
func (r *Reader) markAggregatedPoints(ctx context.Context, points []Point, tier tiers.Tier, invalidWhereFragment string, invalidWhereArgs []any, window Window, ranges map[int64]measurementRange, measurementTypeID int64, fixedRegionID int64) error {
	if len(points) == 0 {
		return nil
	}
	bucketWidth := tierBucketWidth(tier)

	index := make(map[time.Time]int, len(points))
	for i := range points {
		index[points[i].RecordedAt] = i
		if measurementTypeID != 0 {
			if outOfRange(ranges, measurementTypeID, points[i].Min) || outOfRange(ranges, measurementTypeID, points[i].Max) {
				points[i].SuspectChecks = append(points[i].SuspectChecks, suspect.CheckOutOfRange)
			}
		}
	}

	invalidBuckets, err := r.invalidReadingBuckets(ctx, invalidWhereFragment, invalidWhereArgs, window, bucketWidth)
	if err != nil {
		return err
	}
	for bucket := range invalidBuckets {
		if i, ok := index[bucket]; ok {
			points[i].SuspectChecks = append(points[i].SuspectChecks, suspect.CheckPersistedInvalidFlag)
		}
	}

	if fixedRegionID == 0 {
		return nil
	}
	return r.markRegionFixedAggregatedChecks(ctx, points, index, tier, fixedRegionID, window)
}

// markRegionFixedAggregatedChecks computes CheckStaleAttribution and
// CheckMigrationSnapWindow for points, all of which are known to belong to
// regionID (the region/plant entity kinds' aggregated series, both of
// which query a single fixed region -- see this file's markAggregatedPoints
// doc comment).
//
// Neither check scans sensor_reading: the (bucket, sensor_id) pairs come
// from the tier table itself (already bounded the same way the caller's
// own aggregate query is), and sensor_region_history/plant_region_history
// are each bounded by the number of placement changes that have ever
// happened, not by reading volume. Both checks are evaluated at each
// bucket's own start instant, not per underlying raw reading -- a
// documented bucket-granularity approximation, consistent with this
// package's other aggregated-tier approximations (e.g. Point's own
// Min/Max/Avg/Count being exact only at the bucket, not sub-bucket, level).
func (r *Reader) markRegionFixedAggregatedChecks(ctx context.Context, points []Point, index map[time.Time]int, tier tiers.Tier, regionID int64, window Window) error {
	table, timeCol := tierTable(tier)
	rows, err := r.db.Query(ctx, fmt.Sprintf(`
		SELECT DISTINCT %s, sensor_id
		FROM %s
		WHERE region_id = $1 AND %s >= $2 AND %s < $3
	`, timeCol, table, timeCol, timeCol), regionID, window.Start, window.End)
	if err != nil {
		return fmt.Errorf("readings: query tier (bucket, sensor_id) pairs for region %d: %w", regionID, err)
	}

	type pair struct {
		bucket   time.Time
		sensorID int64
	}
	var pairs []pair
	sensorSet := make(map[int64]bool)
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.bucket, &p.sensorID); err != nil {
			rows.Close()
			return fmt.Errorf("readings: scan tier (bucket, sensor_id) row for region %d: %w", regionID, err)
		}
		pairs = append(pairs, p)
		sensorSet[p.sensorID] = true
	}
	rowsErr := rows.Err()
	rows.Close()
	if rowsErr != nil {
		return fmt.Errorf("readings: iterate tier (bucket, sensor_id) rows for region %d: %w", regionID, rowsErr)
	}
	if len(pairs) == 0 {
		return nil
	}

	sensorIDs := make([]int64, 0, len(sensorSet))
	for id := range sensorSet {
		sensorIDs = append(sensorIDs, id)
	}
	history, err := r.loadSensorRegionHistory(ctx, sensorIDs)
	if err != nil {
		return err
	}
	snapWindows, err := r.regionSnapWindows(ctx, []int64{regionID})
	if err != nil {
		return err
	}

	for _, p := range pairs {
		i, ok := index[p.bucket]
		if !ok {
			continue
		}
		if assigned, found := sensorRegionAt(history, p.sensorID, p.bucket); found && assigned != regionID {
			points[i].SuspectChecks = addCheckOnce(points[i].SuspectChecks, suspect.CheckStaleAttribution)
		}
		if inSnapWindow(snapWindows, regionID, p.bucket) {
			points[i].SuspectChecks = addCheckOnce(points[i].SuspectChecks, suspect.CheckMigrationSnapWindow)
		}
	}
	return nil
}

// addCheckOnce appends c to checks unless it is already present -- more
// than one sensor can contribute to the same aggregated bucket
// (markRegionFixedAggregatedChecks iterates one (bucket, sensor_id) pair
// at a time), and a bucket's suspect_checks must name each applicable
// check exactly once, not once per contributing sensor.
func addCheckOnce(checks []suspect.Check, c suspect.Check) []suspect.Check {
	for _, existing := range checks {
		if existing == c {
			return checks
		}
	}
	return append(checks, c)
}

// regionTypeSuspectMarker computes one suspect.Marker for a whole
// PeriodSummary's SummaryStat (FR26.3, FR28): every named check that
// applies to at least one hourly-tier bucket, for sensorTypeID, contributing
// to regionID's summary over period (api.proto's PeriodSummary.suspect_checks
// comment). valueMin/valueMax are the summary's already-computed exact
// extremes (hourlySummaries' own min(value_min)/max(value_max)) -- reused
// here rather than re-queried, since composition keeps them exact
// (migration 022's comment).
func (r *Reader) regionTypeSuspectMarker(ctx context.Context, regionID int64, period Window, sensorTypeID int64, valueMin, valueMax float64, ranges map[int64]measurementRange) (suspect.Marker, error) {
	var checks []suspect.Check
	if outOfRange(ranges, sensorTypeID, valueMin) || outOfRange(ranges, sensorTypeID, valueMax) {
		checks = append(checks, suspect.CheckOutOfRange)
	}

	var anyInvalid bool
	err := r.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM sensor_reading
			WHERE valid = FALSE AND region_id = $1
			  AND recorded_at >= $2 AND recorded_at < $3
			  AND sensor_id IN (SELECT sensor_id FROM sensor WHERE sensor_type_id = $4)
		)
	`, regionID, period.Start, period.End, sensorTypeID).Scan(&anyInvalid)
	if err != nil {
		return suspect.Marker{}, fmt.Errorf("readings: check invalid readings for region %d type %d: %w", regionID, sensorTypeID, err)
	}
	if anyInvalid {
		checks = append(checks, suspect.CheckPersistedInvalidFlag)
	}

	rows, err := r.db.Query(ctx, `
		SELECT DISTINCT h.bucket, h.sensor_id
		FROM sensor_reading_1h h
		JOIN sensor s ON s.sensor_id = h.sensor_id
		WHERE h.region_id = $1 AND s.sensor_type_id = $2 AND h.bucket >= $3 AND h.bucket < $4
	`, regionID, sensorTypeID, period.Start, period.End)
	if err != nil {
		return suspect.Marker{}, fmt.Errorf("readings: query hourly (bucket, sensor_id) pairs for region %d type %d: %w", regionID, sensorTypeID, err)
	}
	type pair struct {
		bucket   time.Time
		sensorID int64
	}
	var pairs []pair
	sensorSet := make(map[int64]bool)
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.bucket, &p.sensorID); err != nil {
			rows.Close()
			return suspect.Marker{}, fmt.Errorf("readings: scan hourly (bucket, sensor_id) row for region %d type %d: %w", regionID, sensorTypeID, err)
		}
		pairs = append(pairs, p)
		sensorSet[p.sensorID] = true
	}
	rowsErr := rows.Err()
	rows.Close()
	if rowsErr != nil {
		return suspect.Marker{}, fmt.Errorf("readings: iterate hourly (bucket, sensor_id) rows for region %d type %d: %w", regionID, sensorTypeID, rowsErr)
	}

	if len(pairs) > 0 {
		sensorIDs := make([]int64, 0, len(sensorSet))
		for id := range sensorSet {
			sensorIDs = append(sensorIDs, id)
		}
		history, err := r.loadSensorRegionHistory(ctx, sensorIDs)
		if err != nil {
			return suspect.Marker{}, err
		}
		snapWindows, err := r.regionSnapWindows(ctx, []int64{regionID})
		if err != nil {
			return suspect.Marker{}, err
		}

		var staleFound, snapFound bool
		for _, p := range pairs {
			if !staleFound {
				if assigned, found := sensorRegionAt(history, p.sensorID, p.bucket); found && assigned != regionID {
					staleFound = true
				}
			}
			if !snapFound && inSnapWindow(snapWindows, regionID, p.bucket) {
				snapFound = true
			}
			if staleFound && snapFound {
				break
			}
		}
		if staleFound {
			checks = append(checks, suspect.CheckStaleAttribution)
		}
		if snapFound {
			checks = append(checks, suspect.CheckMigrationSnapWindow)
		}
	}

	return suspect.Marker{Checks: checks}, nil
}

// bucketSuspectMarker computes one suspect.Marker for a single hourly-tier
// bucket attributed to sensorID/sensorTypeID in regionID -- used by
// PeriodSummary's overnight-low/daytime-high framing (framedSummary), each
// of which names exactly one winning (bucket, sensor_id) pair rather than
// summarizing a whole period.
func (r *Reader) bucketSuspectMarker(ctx context.Context, regionID, sensorID, sensorTypeID int64, bucket time.Time, value float64, ranges map[int64]measurementRange) (suspect.Marker, error) {
	var checks []suspect.Check
	if outOfRange(ranges, sensorTypeID, value) {
		checks = append(checks, suspect.CheckOutOfRange)
	}

	var anyInvalid bool
	err := r.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM sensor_reading
			WHERE valid = FALSE AND sensor_id = $1
			  AND recorded_at >= $2 AND recorded_at < $3
		)
	`, sensorID, bucket, bucket.Add(time.Hour)).Scan(&anyInvalid)
	if err != nil {
		return suspect.Marker{}, fmt.Errorf("readings: check invalid readings for sensor %d bucket %s: %w", sensorID, bucket, err)
	}
	if anyInvalid {
		checks = append(checks, suspect.CheckPersistedInvalidFlag)
	}

	history, err := r.loadSensorRegionHistory(ctx, []int64{sensorID})
	if err != nil {
		return suspect.Marker{}, err
	}
	if assigned, found := sensorRegionAt(history, sensorID, bucket); found && assigned != regionID {
		checks = append(checks, suspect.CheckStaleAttribution)
	}

	snapWindows, err := r.regionSnapWindows(ctx, []int64{regionID})
	if err != nil {
		return suspect.Marker{}, err
	}
	if inSnapWindow(snapWindows, regionID, bucket) {
		checks = append(checks, suspect.CheckMigrationSnapWindow)
	}

	return suspect.Marker{Checks: checks}, nil
}
