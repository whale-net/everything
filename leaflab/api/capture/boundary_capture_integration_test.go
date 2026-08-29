//go:build integration

// Real-Postgres (TimescaleDB, for time_bucket) integration coverage for
// FR20's two-phase boundary capture: Recorder (phase one) and Completer
// (phase two) exercised together against a hermetic schema mirroring
// migrations 001 (sensor_reading), 022 (sensor_reading_5m) and 033
// (boundary_capture, boundary_partial). testSchema is hand-written, not an
// embed of leaflab/migrate's migrations -- see //libs/go/dbtest's README
// and leaflab/api/placement/move_integration_test.go's doc comment on why
// integration tests in this repo stay hermetic. Deliberately does not
// define sensor_reading_1h at all (see
// TestHourlyCompletionNeverQueriesFullHourlyBucketTable's doc comment) --
// migration 022's continuous-aggregate refresh/retention behavior itself is
// covered by leaflab/migrate/tiers_migration_integration_test.go, not here;
// this file only proves leaflab/api/capture's own logic against tables
// shaped like migration 022's and 033's, populated directly rather than via
// a real continuous aggregate refresh (which this package's code does not
// depend on -- it reads sensor_reading_5m as a plain relation).
//
// Named tests below map to FR20.4's four verifiable clauses exactly as this
// task's Testing section requests:
//   - TestMoveMidBucketChangesNoEarlierBucket
//   - TestStraddlingBucketEqualsRawRestricted
//   - TestExactAfterRawRetention
//   - TestTwoBoundariesInOneBucketYieldThreePartials
//
// See //libs/go/dbtest's README for how to run tests like this one:
//
//	bazel test //leaflab/api/capture:boundary_capture_integration_test --test_output=all
package capture

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/leaflab/api/tiers"
	"github.com/whale-net/everything/libs/go/dbtest"
)

// captureTimescaleImage matches tiersTimescaleImage in
// leaflab/migrate/tiers_migration_integration_test.go -- this package's
// Recorder/Completer call time_bucket (pgTimeBucket in recorder.go), a
// TimescaleDB function not present in plain postgres. Declared again here
// rather than reused, for the same "Bazel test targets do not always share
// compilation" reason given in
// plant_region_history_migration_integration_test.go.
const captureTimescaleImage = "timescale/timescaledb:latest-pg16"

// testSchema is a hermetic, hand-written mirror of the columns
// leaflab/api/capture's SQL actually touches: sensor_reading (migration
// 001, trimmed to sensor_id/value/recorded_at -- everything aggregate.go's
// rawRestrictedAggregate reads), sensor_reading_5m (migration 022, trimmed
// to the four aggregate columns aggregate.go composes from) and
// boundary_capture/boundary_partial (migration 033, verbatim). No
// sensor_reading_1h table exists here at all -- see
// TestHourlyCompletionNeverQueriesFullHourlyBucketTable.
const testSchema = `
	CREATE EXTENSION IF NOT EXISTS timescaledb;

	CREATE TABLE sensor (
		sensor_id BIGSERIAL PRIMARY KEY
	);

	CREATE TABLE sensor_reading (
		sensor_id   BIGINT NOT NULL REFERENCES sensor(sensor_id),
		value       DOUBLE PRECISION NOT NULL,
		recorded_at TIMESTAMPTZ NOT NULL
	);
	CREATE INDEX idx_sensor_reading_sensor_recorded ON sensor_reading(sensor_id, recorded_at);

	CREATE TABLE sensor_reading_5m (
		sensor_id     BIGINT NOT NULL REFERENCES sensor(sensor_id),
		bucket        TIMESTAMPTZ NOT NULL,
		reading_count BIGINT NOT NULL,
		value_sum     DOUBLE PRECISION NOT NULL,
		value_min     DOUBLE PRECISION NOT NULL,
		value_max     DOUBLE PRECISION NOT NULL,
		PRIMARY KEY (sensor_id, bucket)
	);

	CREATE TABLE boundary_capture (
		capture_id    BIGSERIAL PRIMARY KEY,
		sensor_id     BIGINT NOT NULL REFERENCES sensor(sensor_id) ON DELETE RESTRICT,
		boundary_at   TIMESTAMPTZ NOT NULL,
		tier          TEXT NOT NULL,
		bucket_start  TIMESTAMPTZ NOT NULL,
		state         TEXT NOT NULL DEFAULT 'pending',
		completed_at  TIMESTAMPTZ,
		CONSTRAINT boundary_capture_tier_check
			CHECK (tier IN ('five_minute', 'hourly')),
		CONSTRAINT boundary_capture_state_check
			CHECK (state IN ('pending', 'completed')),
		CONSTRAINT boundary_capture_completed_at_check
			CHECK ((state = 'completed') = (completed_at IS NOT NULL))
	);
	CREATE INDEX idx_boundary_capture_pending
		ON boundary_capture(tier, bucket_start)
		WHERE state = 'pending';
	CREATE INDEX idx_boundary_capture_sensor_id
		ON boundary_capture(sensor_id, boundary_at);

	CREATE TABLE boundary_partial (
		partial_id    BIGSERIAL PRIMARY KEY,
		capture_id    BIGINT NOT NULL REFERENCES boundary_capture(capture_id) ON DELETE RESTRICT,
		tier          TEXT NOT NULL,
		bucket_start  TIMESTAMPTZ NOT NULL,
		partial_from  TIMESTAMPTZ NOT NULL,
		partial_to    TIMESTAMPTZ NOT NULL,
		reading_count BIGINT NOT NULL,
		value_sum     DOUBLE PRECISION NOT NULL,
		value_min     DOUBLE PRECISION NOT NULL,
		value_max     DOUBLE PRECISION NOT NULL,
		CONSTRAINT boundary_partial_tier_check
			CHECK (tier IN ('five_minute', 'hourly')),
		CONSTRAINT boundary_partial_interval_check
			CHECK (partial_from < partial_to)
	);
	CREATE INDEX idx_boundary_partial_capture_id ON boundary_partial(capture_id);
	CREATE INDEX idx_boundary_partial_bucket
		ON boundary_partial(tier, bucket_start, partial_from);
`

// newCaptureFixture starts a real TimescaleDB container with testSchema
// applied and seeds one sensor, returning the pool and that sensor's id.
func newCaptureFixture(t *testing.T) (*pgxpool.Pool, int64) {
	t.Helper()
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Image: captureTimescaleImage, Schema: testSchema})

	var sensorID int64
	if err := db.Pool.QueryRow(ctx, `INSERT INTO sensor DEFAULT VALUES RETURNING sensor_id`).Scan(&sensorID); err != nil {
		t.Fatalf("seed sensor: %v", err)
	}
	return db.Pool, sensorID
}

// insertRawReading writes one raw sensor_reading row.
func insertRawReading(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sensorID int64, value float64, at time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO sensor_reading (sensor_id, value, recorded_at) VALUES ($1, $2, $3)
	`, sensorID, value, at); err != nil {
		t.Fatalf("insert raw reading at %s: %v", at, err)
	}
}

// populateFiveMinuteFullBucket computes bucketStart's five-minute aggregate
// directly from raw and writes it into sensor_reading_5m -- standing in for
// a real continuous aggregate refresh (migration 022), which this package's
// code never triggers or depends on itself. Mirrors a real continuous
// aggregate's behavior of materializing no row for an empty group: a bucket
// with zero raw readings is left unwritten.
func populateFiveMinuteFullBucket(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sensorID int64, bucketStart time.Time) {
	t.Helper()
	bucketEnd := bucketStart.Add(5 * time.Minute)
	count, sum, min, max := directRawAggregate(t, ctx, pool, sensorID, bucketStart, bucketEnd)
	if count == 0 {
		return
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO sensor_reading_5m (sensor_id, bucket, reading_count, value_sum, value_min, value_max)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (sensor_id, bucket) DO UPDATE SET
			reading_count = EXCLUDED.reading_count,
			value_sum     = EXCLUDED.value_sum,
			value_min     = EXCLUDED.value_min,
			value_max     = EXCLUDED.value_max
	`, sensorID, bucketStart, count, sum, min, max); err != nil {
		t.Fatalf("insert five-minute full bucket %s: %v", bucketStart, err)
	}
}

// populateFiveMinuteFullBucketsForHour calls populateFiveMinuteFullBucket
// for every five-minute-aligned bucket in [hourStart, hourStart+1h).
func populateFiveMinuteFullBucketsForHour(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sensorID int64, hourStart time.Time) {
	t.Helper()
	for offset := 0; offset < 60; offset += 5 {
		populateFiveMinuteFullBucket(t, ctx, pool, sensorID, hourStart.Add(time.Duration(offset)*time.Minute))
	}
}

// directRawAggregate computes count/sum/min/max over [from, to) straight
// from sensor_reading, independently of anything in this package -- the
// test's own oracle for "equals the raw-restricted computation" (FR20.4),
// deliberately not calling this package's own rawRestrictedAggregate so a
// bug shared between production code and the test's expectation cannot
// cancel out.
func directRawAggregate(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sensorID int64, from, to time.Time) (count int64, sum, min, max float64) {
	t.Helper()
	var minN, maxN sql.NullFloat64
	if err := pool.QueryRow(ctx, `
		SELECT count(*), coalesce(sum(value), 0), min(value), max(value)
		FROM sensor_reading
		WHERE sensor_id = $1 AND recorded_at >= $2 AND recorded_at < $3
	`, sensorID, from, to).Scan(&count, &sum, &minN, &maxN); err != nil {
		t.Fatalf("direct raw aggregate [%s, %s): %v", from, to, err)
	}
	if minN.Valid {
		min = minN.Float64
	}
	if maxN.Valid {
		max = maxN.Float64
	}
	return count, sum, min, max
}

// recordBoundary opens its own transaction (mirroring the placement
// writer's real usage -- see recorder.go's doc comment on Record always
// running inside the caller's transaction) and commits it, inserting one
// boundary_capture row per captureTiers for sensorID at boundaryAt.
func recordBoundary(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sensorID int64, boundaryAt time.Time) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin recordBoundary transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := NewRecorder().Record(ctx, tx, []int64{sensorID}, boundaryAt); err != nil {
		t.Fatalf("Record(%s): %v", boundaryAt, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit recordBoundary transaction: %v", err)
	}
}

// runCompleter runs Completer.RunPending once and fails the test on error.
func runCompleter(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if err := NewCompleter(pool).RunPending(ctx); err != nil {
		t.Fatalf("RunPending: %v", err)
	}
}

// partialRow is one boundary_partial row, fetched for assertions.
type partialRow struct {
	From, To      time.Time
	Count         int64
	Sum, Min, Max float64
}

// fetchPartials returns every boundary_partial row for (sensorID, tier,
// bucketStart), ordered by partial_from -- the order FR20.3's induction
// guarantees is contiguous and gap-free across the whole bucket.
func fetchPartials(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sensorID int64, tier string, bucketStart time.Time) []partialRow {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT bp.partial_from, bp.partial_to, bp.reading_count, bp.value_sum, bp.value_min, bp.value_max
		FROM boundary_partial bp
		JOIN boundary_capture bc ON bc.capture_id = bp.capture_id
		WHERE bc.sensor_id = $1 AND bp.tier = $2 AND bp.bucket_start = $3
		ORDER BY bp.partial_from
	`, sensorID, tier, bucketStart)
	if err != nil {
		t.Fatalf("fetch partials: %v", err)
	}
	defer rows.Close()

	var out []partialRow
	for rows.Next() {
		var p partialRow
		if err := rows.Scan(&p.From, &p.To, &p.Count, &p.Sum, &p.Min, &p.Max); err != nil {
			t.Fatalf("scan partial row: %v", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate partial rows: %v", err)
	}
	return out
}

// assertPartialMatchesRaw fails the test unless p's aggregate (count, sum,
// min, max and derived avg) equals directRawAggregate over [p.From, p.To) --
// FR20.4's "the straddling bucket equals the raw-restricted computation."
func assertPartialMatchesRaw(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sensorID int64, p partialRow) {
	t.Helper()
	wantCount, wantSum, wantMin, wantMax := directRawAggregate(t, ctx, pool, sensorID, p.From, p.To)
	if p.Count != wantCount {
		t.Errorf("partial [%s, %s) count = %d, want raw-restricted %d", p.From, p.To, p.Count, wantCount)
	}
	if p.Sum != wantSum {
		t.Errorf("partial [%s, %s) sum = %v, want raw-restricted %v", p.From, p.To, p.Sum, wantSum)
	}
	if p.Min != wantMin {
		t.Errorf("partial [%s, %s) min = %v, want raw-restricted %v", p.From, p.To, p.Min, wantMin)
	}
	if p.Max != wantMax {
		t.Errorf("partial [%s, %s) max = %v, want raw-restricted %v", p.From, p.To, p.Max, wantMax)
	}
	if wantCount > 0 {
		wantAvg := wantSum / float64(wantCount)
		gotAvg := p.Sum / float64(p.Count)
		if gotAvg != wantAvg {
			t.Errorf("partial [%s, %s) derived avg = %v, want %v", p.From, p.To, gotAvg, wantAvg)
		}
	}
}

// ── TestMoveMidBucketChangesNoEarlierBucket ────────────────────────────

// TestMoveMidBucketChangesNoEarlierBucket proves FR20.4's first verifiable
// clause: moving a plant mid-bucket changes no bucket value before the
// move. A boundary recorded at 10:37 must leave the prior five-minute
// bucket's already-materialized sensor_reading_5m row byte-identical, must
// leave the raw readings inside it untouched, and Recorder/Completer must
// never create a boundary_capture or boundary_partial row for any bucket
// other than the one the boundary actually falls inside.
func TestMoveMidBucketChangesNoEarlierBucket(t *testing.T) {
	pool, sensorID := newCaptureFixture(t)
	ctx := context.Background()

	earlierBucket := time.Date(2024, 6, 1, 10, 25, 0, 0, time.UTC) // [10:25, 10:30)
	boundaryAt := time.Date(2024, 6, 1, 10, 37, 0, 0, time.UTC)    // inside [10:35, 10:40)

	insertRawReading(t, ctx, pool, sensorID, 11.0, earlierBucket.Add(1*time.Minute))
	insertRawReading(t, ctx, pool, sensorID, 22.0, earlierBucket.Add(3*time.Minute))
	insertRawReading(t, ctx, pool, sensorID, 7.5, boundaryAt.Add(-1*time.Minute))
	insertRawReading(t, ctx, pool, sensorID, 9.5, boundaryAt.Add(1*time.Minute))

	populateFiveMinuteFullBucket(t, ctx, pool, sensorID, earlierBucket)

	beforeCount, beforeSum, beforeMin, beforeMax := directRawAggregate(t, ctx, pool, sensorID, earlierBucket, earlierBucket.Add(5*time.Minute))
	var beforeMatViewCount int64
	var beforeMatViewSum, beforeMatViewMin, beforeMatViewMax float64
	if err := pool.QueryRow(ctx, `
		SELECT reading_count, value_sum, value_min, value_max FROM sensor_reading_5m WHERE sensor_id = $1 AND bucket = $2
	`, sensorID, earlierBucket).Scan(&beforeMatViewCount, &beforeMatViewSum, &beforeMatViewMin, &beforeMatViewMax); err != nil {
		t.Fatalf("read sensor_reading_5m row before boundary: %v", err)
	}

	recordBoundary(t, ctx, pool, sensorID, boundaryAt)
	runCompleter(t, ctx, pool)

	afterCount, afterSum, afterMin, afterMax := directRawAggregate(t, ctx, pool, sensorID, earlierBucket, earlierBucket.Add(5*time.Minute))
	if afterCount != beforeCount || afterSum != beforeSum || afterMin != beforeMin || afterMax != beforeMax {
		t.Errorf("earlier bucket raw aggregate changed: before (%d, %v, %v, %v), after (%d, %v, %v, %v)",
			beforeCount, beforeSum, beforeMin, beforeMax, afterCount, afterSum, afterMin, afterMax)
	}

	var afterMatViewCount int64
	var afterMatViewSum, afterMatViewMin, afterMatViewMax float64
	if err := pool.QueryRow(ctx, `
		SELECT reading_count, value_sum, value_min, value_max FROM sensor_reading_5m WHERE sensor_id = $1 AND bucket = $2
	`, sensorID, earlierBucket).Scan(&afterMatViewCount, &afterMatViewSum, &afterMatViewMin, &afterMatViewMax); err != nil {
		t.Fatalf("read sensor_reading_5m row after boundary: %v", err)
	}
	if afterMatViewCount != beforeMatViewCount || afterMatViewSum != beforeMatViewSum || afterMatViewMin != beforeMatViewMin || afterMatViewMax != beforeMatViewMax {
		t.Error("sensor_reading_5m row for the earlier bucket is not byte-identical before/after the mid-bucket boundary")
	}

	// Recorder inserts one row per captureTiers (five_minute AND hourly),
	// so the only rows that should exist at all are the exact five-minute
	// bucket [10:35, 10:40) and the exact hourly bucket [10:00, 11:00) the
	// boundary itself falls into -- scoped per tier, since the hourly
	// bucket_start (10:00) is itself earlier than the five-minute
	// earlierBucket (10:25) used above and would otherwise false-positive
	// an unscoped "bucket_start < earlierBucket" check.
	var earlierFiveMinuteCaptures int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM boundary_capture WHERE sensor_id = $1 AND tier = 'five_minute' AND bucket_start != $2
	`, sensorID, time.Date(2024, 6, 1, 10, 35, 0, 0, time.UTC)).Scan(&earlierFiveMinuteCaptures); err != nil {
		t.Fatalf("count unexpected five-minute captures: %v", err)
	}
	if earlierFiveMinuteCaptures != 0 {
		t.Errorf("boundary_capture rows exist for a five-minute bucket other than the one the boundary fell into: %d, want 0", earlierFiveMinuteCaptures)
	}

	var earlierHourlyCaptures int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM boundary_capture WHERE sensor_id = $1 AND tier = 'hourly' AND bucket_start != $2
	`, sensorID, time.Date(2024, 6, 1, 10, 0, 0, 0, time.UTC)).Scan(&earlierHourlyCaptures); err != nil {
		t.Fatalf("count unexpected hourly captures: %v", err)
	}
	if earlierHourlyCaptures != 0 {
		t.Errorf("boundary_capture rows exist for an hourly bucket other than the one the boundary fell into: %d, want 0", earlierHourlyCaptures)
	}

	var earlierBucketPartials int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM boundary_partial WHERE bucket_start = $1`, earlierBucket).Scan(&earlierBucketPartials); err != nil {
		t.Fatalf("count partials for earlier bucket: %v", err)
	}
	if earlierBucketPartials != 0 {
		t.Errorf("boundary_partial rows exist for the earlier bucket: %d, want 0", earlierBucketPartials)
	}
}

// ── TestStraddlingBucketEqualsRawRestricted ────────────────────────────

// TestStraddlingBucketEqualsRawRestricted proves FR20.4's second verifiable
// clause: the straddling bucket's two partials each equal a direct
// raw-restricted computation over the same sub-interval, for count, sum,
// min, max and derived avg. The fixture never populates sensor_reading_5m
// for the split bucket itself, so this also demonstrates the "no
// subtraction" property (A17) at the five-minute tier: there is no full
// bucket row available to subtract from, and the split still succeeds and
// is exact.
func TestStraddlingBucketEqualsRawRestricted(t *testing.T) {
	pool, sensorID := newCaptureFixture(t)
	ctx := context.Background()

	bucketStart := time.Date(2024, 6, 1, 10, 35, 0, 0, time.UTC) // [10:35, 10:40)
	boundaryAt := bucketStart.Add(2*time.Minute + 30*time.Second)

	insertRawReading(t, ctx, pool, sensorID, 5.0, bucketStart.Add(10*time.Second))
	insertRawReading(t, ctx, pool, sensorID, 8.0, bucketStart.Add(1*time.Minute))
	insertRawReading(t, ctx, pool, sensorID, 20.0, bucketStart.Add(3*time.Minute))
	insertRawReading(t, ctx, pool, sensorID, 2.0, bucketStart.Add(4*time.Minute+30*time.Second))

	recordBoundary(t, ctx, pool, sensorID, boundaryAt)
	runCompleter(t, ctx, pool)

	partials := fetchPartials(t, ctx, pool, sensorID, "five_minute", bucketStart)
	if len(partials) != 2 {
		t.Fatalf("got %d five-minute partials for the straddled bucket, want 2", len(partials))
	}
	if !partials[0].From.Equal(bucketStart) || !partials[0].To.Equal(boundaryAt) {
		t.Errorf("left partial = [%s, %s), want [%s, %s)", partials[0].From, partials[0].To, bucketStart, boundaryAt)
	}
	if !partials[1].From.Equal(boundaryAt) || !partials[1].To.Equal(bucketStart.Add(5*time.Minute)) {
		t.Errorf("right partial = [%s, %s), want [%s, %s)", partials[1].From, partials[1].To, boundaryAt, bucketStart.Add(5*time.Minute))
	}

	for _, p := range partials {
		assertPartialMatchesRaw(t, ctx, pool, sensorID, p)
	}
}

// ── TestExactAfterRawRetention ─────────────────────────────────────────

// TestExactAfterRawRetention proves FR20.4's third verifiable clause: the
// same query answered after raw retention has elapsed returns the same
// value. It captures a straddling bucket's partials, deletes the raw rows
// entirely (simulating retention having dropped the chunk), and asserts the
// already-written boundary_partial rows are unchanged -- durable, not a
// raw scan recomputed on read.
func TestExactAfterRawRetention(t *testing.T) {
	pool, sensorID := newCaptureFixture(t)
	ctx := context.Background()

	bucketStart := time.Date(2024, 6, 1, 10, 35, 0, 0, time.UTC)
	boundaryAt := bucketStart.Add(2 * time.Minute)

	insertRawReading(t, ctx, pool, sensorID, 4.0, bucketStart.Add(30*time.Second))
	insertRawReading(t, ctx, pool, sensorID, 6.0, bucketStart.Add(1*time.Minute))
	insertRawReading(t, ctx, pool, sensorID, 12.0, bucketStart.Add(3*time.Minute))
	insertRawReading(t, ctx, pool, sensorID, 1.0, bucketStart.Add(4*time.Minute))

	recordBoundary(t, ctx, pool, sensorID, boundaryAt)
	runCompleter(t, ctx, pool)

	before := fetchPartials(t, ctx, pool, sensorID, "five_minute", bucketStart)
	if len(before) != 2 {
		t.Fatalf("got %d partials before raw retention, want 2", len(before))
	}

	if _, err := pool.Exec(ctx, `DELETE FROM sensor_reading WHERE sensor_id = $1`, sensorID); err != nil {
		t.Fatalf("simulate raw retention (delete raw rows): %v", err)
	}

	after := fetchPartials(t, ctx, pool, sensorID, "five_minute", bucketStart)
	if len(after) != 2 {
		t.Fatalf("got %d partials after raw retention, want 2", len(after))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Errorf("partial %d changed after raw retention: before %+v, after %+v", i, before[i], after[i])
		}
	}
}

// ── TestTwoBoundariesInOneBucketYieldThreePartials ─────────────────────

// TestTwoBoundariesInOneBucketYieldThreePartials proves FR20.4's fourth
// verifiable clause: two plants leaving one region at different instants
// inside one bucket yield three exact partials. Both boundary events are
// recorded against the same sensor (boundary_capture is keyed by sensor and
// instant, never by plant, per FR20.2 -- two plants attributed to the same
// sensor's region moving at different instants produces exactly this
// shape).
func TestTwoBoundariesInOneBucketYieldThreePartials(t *testing.T) {
	pool, sensorID := newCaptureFixture(t)
	ctx := context.Background()

	bucketStart := time.Date(2024, 6, 1, 10, 35, 0, 0, time.UTC) // [10:35, 10:40)
	firstBoundary := bucketStart.Add(1 * time.Minute)            // 10:36
	secondBoundary := bucketStart.Add(3 * time.Minute)           // 10:38

	insertRawReading(t, ctx, pool, sensorID, 1.0, bucketStart.Add(10*time.Second))
	insertRawReading(t, ctx, pool, sensorID, 2.0, bucketStart.Add(1*time.Minute+30*time.Second))
	insertRawReading(t, ctx, pool, sensorID, 3.0, bucketStart.Add(4*time.Minute))

	recordBoundary(t, ctx, pool, sensorID, firstBoundary)
	recordBoundary(t, ctx, pool, sensorID, secondBoundary)
	runCompleter(t, ctx, pool)

	partials := fetchPartials(t, ctx, pool, sensorID, "five_minute", bucketStart)
	if len(partials) != 3 {
		t.Fatalf("got %d five-minute partials, want 3 (N=2 boundaries -> N+1 partials, FR20.3)", len(partials))
	}

	wantBounds := [][2]time.Time{
		{bucketStart, firstBoundary},
		{firstBoundary, secondBoundary},
		{secondBoundary, bucketStart.Add(5 * time.Minute)},
	}
	for i, want := range wantBounds {
		if !partials[i].From.Equal(want[0]) || !partials[i].To.Equal(want[1]) {
			t.Errorf("partial %d = [%s, %s), want [%s, %s)", i, partials[i].From, partials[i].To, want[0], want[1])
		}
		assertPartialMatchesRaw(t, ctx, pool, sensorID, partials[i])
	}
}

// TestThreeBoundariesInOneBucketYieldFourPartials generalizes the
// N-boundary induction one step further: three boundaries in one bucket
// yield four partials, each splitting the partial it falls inside rather
// than the whole bucket (FR20.3).
func TestThreeBoundariesInOneBucketYieldFourPartials(t *testing.T) {
	pool, sensorID := newCaptureFixture(t)
	ctx := context.Background()

	bucketStart := time.Date(2024, 6, 1, 10, 35, 0, 0, time.UTC) // [10:35, 10:40)
	boundaries := []time.Time{
		bucketStart.Add(30 * time.Second),               // 10:35:30
		bucketStart.Add(2 * time.Minute),                // 10:37:00
		bucketStart.Add(3*time.Minute + 30*time.Second), // 10:38:30
	}

	insertRawReading(t, ctx, pool, sensorID, 1.5, bucketStart.Add(10*time.Second))
	insertRawReading(t, ctx, pool, sensorID, 4.5, bucketStart.Add(1*time.Minute))
	insertRawReading(t, ctx, pool, sensorID, 9.0, bucketStart.Add(2*time.Minute+45*time.Second))
	insertRawReading(t, ctx, pool, sensorID, 3.0, bucketStart.Add(3*time.Minute+45*time.Second))
	insertRawReading(t, ctx, pool, sensorID, 6.0, bucketStart.Add(4*time.Minute+50*time.Second))

	for _, b := range boundaries {
		recordBoundary(t, ctx, pool, sensorID, b)
	}
	runCompleter(t, ctx, pool)

	partials := fetchPartials(t, ctx, pool, sensorID, "five_minute", bucketStart)
	if len(partials) != 4 {
		t.Fatalf("got %d five-minute partials, want 4 (N=3 boundaries -> N+1 partials, FR20.3)", len(partials))
	}

	edges := append([]time.Time{bucketStart}, boundaries...)
	edges = append(edges, bucketStart.Add(5*time.Minute))
	for i := 0; i < 4; i++ {
		if !partials[i].From.Equal(edges[i]) || !partials[i].To.Equal(edges[i+1]) {
			t.Errorf("partial %d = [%s, %s), want [%s, %s)", i, partials[i].From, partials[i].To, edges[i], edges[i+1])
		}
		assertPartialMatchesRaw(t, ctx, pool, sensorID, partials[i])
	}
}

// ── Coarser-tier composition (FR20.3) ──────────────────────────────────

// TestHourlyComposedFromFiveMinuteEqualsRawAndSurvivesRawDeletion proves
// FR20.3's "a coarser tier's partials are composed from the finer tier's
// rather than from a second raw scan": the hourly partials this test
// produces equal a direct raw computation taken before any raw data is
// touched, AND remain correct after every raw row outside the split
// five-minute bucket is deleted -- if the hourly composition ever fell back
// to scanning raw for those deleted buckets, the counts it produced would
// come up short (or the row-level foreign key on boundary_capture would be
// irrelevant, since fiveMinuteFullBucketsAggregate/fiveMinutePartialsAggregate
// never touch sensor_reading directly), so this specifically exercises
// "never a second raw scan," not merely "produces the same answer."
func TestHourlyComposedFromFiveMinuteEqualsRawAndSurvivesRawDeletion(t *testing.T) {
	pool, sensorID := newCaptureFixture(t)
	ctx := context.Background()

	hourStart := time.Date(2024, 6, 1, 14, 0, 0, 0, time.UTC) // [14:00, 15:00)
	splitBucketStart := time.Date(2024, 6, 1, 14, 35, 0, 0, time.UTC)
	boundaryAt := splitBucketStart.Add(2 * time.Minute) // 14:37:00

	readings := []struct {
		value float64
		at    time.Time
	}{
		{1.0, hourStart.Add(2 * time.Minute)},
		{2.0, hourStart.Add(7 * time.Minute)},
		{3.0, hourStart.Add(12 * time.Minute)},
		{10.0, splitBucketStart.Add(1 * time.Minute)}, // inside split bucket, before boundary
		{20.0, splitBucketStart.Add(3 * time.Minute)}, // inside split bucket, after boundary
		{4.0, hourStart.Add(53 * time.Minute)},
	}
	for _, r := range readings {
		insertRawReading(t, ctx, pool, sensorID, r.value, r.at)
	}

	// Ground truth, computed from raw before anything is deleted.
	wantLeftCount, wantLeftSum, wantLeftMin, wantLeftMax := directRawAggregate(t, ctx, pool, sensorID, hourStart, boundaryAt)
	wantRightCount, wantRightSum, wantRightMin, wantRightMax := directRawAggregate(t, ctx, pool, sensorID, boundaryAt, hourStart.Add(time.Hour))

	// Materialize the five-minute tier for the whole hour, including a
	// full-bucket row for the bucket that is about to be split -- exercises
	// fiveMinuteFullBucketsAggregate's NOT EXISTS exclusion once that
	// bucket's boundary_partial rows exist.
	populateFiveMinuteFullBucketsForHour(t, ctx, pool, sensorID, hourStart)

	// Delete every raw row OUTSIDE the split five-minute bucket -- the
	// hourly composition for those stretches must come entirely from the
	// sensor_reading_5m rows just materialized, never from raw.
	if _, err := pool.Exec(ctx, `
		DELETE FROM sensor_reading
		WHERE sensor_id = $1 AND NOT (recorded_at >= $2 AND recorded_at < $3)
	`, sensorID, splitBucketStart, splitBucketStart.Add(5*time.Minute)); err != nil {
		t.Fatalf("delete raw rows outside the split five-minute bucket: %v", err)
	}

	recordBoundary(t, ctx, pool, sensorID, boundaryAt)
	runCompleter(t, ctx, pool)

	hourlyPartials := fetchPartials(t, ctx, pool, sensorID, "hourly", hourStart)
	if len(hourlyPartials) != 2 {
		t.Fatalf("got %d hourly partials, want 2", len(hourlyPartials))
	}
	left, right := hourlyPartials[0], hourlyPartials[1]

	if left.Count != wantLeftCount || left.Sum != wantLeftSum || left.Min != wantLeftMin || left.Max != wantLeftMax {
		t.Errorf("hourly left partial [%s, %s) = (%d, %v, %v, %v), want raw ground truth (%d, %v, %v, %v)",
			left.From, left.To, left.Count, left.Sum, left.Min, left.Max, wantLeftCount, wantLeftSum, wantLeftMin, wantLeftMax)
	}
	if right.Count != wantRightCount || right.Sum != wantRightSum || right.Min != wantRightMin || right.Max != wantRightMax {
		t.Errorf("hourly right partial [%s, %s) = (%d, %v, %v, %v), want raw ground truth (%d, %v, %v, %v)",
			right.From, right.To, right.Count, right.Sum, right.Min, right.Max, wantRightCount, wantRightSum, wantRightMin, wantRightMax)
	}
}

// TestHourlyCompletionNeverQueriesFullHourlyBucketTable is A17's "no
// subtraction" clause made structural at the hourly tier: testSchema
// defines no sensor_reading_1h table whatsoever. If Completer's hourly path
// ever queried a full hourly bucket to derive one side by subtraction, this
// test would fail with a real "relation does not exist" SQL error instead
// of completing successfully.
func TestHourlyCompletionNeverQueriesFullHourlyBucketTable(t *testing.T) {
	pool, sensorID := newCaptureFixture(t)
	ctx := context.Background()

	hourStart := time.Date(2024, 6, 1, 16, 0, 0, 0, time.UTC)
	boundaryAt := hourStart.Add(20*time.Minute + 7*time.Second) // deliberately not bucket-aligned; see TestBoundaryExactlyAtBucketStartFailsToSplit for the aligned case

	insertRawReading(t, ctx, pool, sensorID, 5.0, hourStart.Add(5*time.Minute))
	insertRawReading(t, ctx, pool, sensorID, 15.0, hourStart.Add(45*time.Minute))
	populateFiveMinuteFullBucketsForHour(t, ctx, pool, sensorID, hourStart)

	recordBoundary(t, ctx, pool, sensorID, boundaryAt)
	runCompleter(t, ctx, pool) // fails the test (via t.Fatalf) on any SQL error, including "sensor_reading_1h does not exist"

	partials := fetchPartials(t, ctx, pool, sensorID, "hourly", hourStart)
	if len(partials) != 2 {
		t.Fatalf("got %d hourly partials, want 2", len(partials))
	}
}

// ── NFR5: completion must finish before raw retention elapses ─────────

// TestCompleterRunPending_PendingCaptureNearRawRetentionReturnsError proves
// NFR5's ordering assertion: a boundary_capture row still 'pending' with a
// boundary_at old enough that raw retention could drop its chunk within one
// more completion window must surface loudly via ErrPendingNearRetention,
// never be silently skipped. The capture's bucket_start is set far in the
// future so completeTier never picks it up as closed -- isolating the
// checkPendingNearRetention path from the completion path.
func TestCompleterRunPending_PendingCaptureNearRawRetentionReturnsError(t *testing.T) {
	pool, sensorID := newCaptureFixture(t)
	ctx := context.Background()

	oldBoundary := time.Now().Add(-tiers.RawRetention)
	neverClosedBucket := time.Now().Add(24 * time.Hour)
	if _, err := pool.Exec(ctx, `
		INSERT INTO boundary_capture (sensor_id, boundary_at, tier, bucket_start) VALUES ($1, $2, 'hourly', $3)
	`, sensorID, oldBoundary, neverClosedBucket); err != nil {
		t.Fatalf("seed stale pending capture: %v", err)
	}

	err := NewCompleter(pool).RunPending(ctx)
	if !errors.Is(err, ErrPendingNearRetention) {
		t.Fatalf("RunPending error = %v, want ErrPendingNearRetention", err)
	}
}

// TestCompleterRunPending_NoStaleCaptureReturnsNil is the control case:
// a pending capture with a recent boundary_at (nowhere near raw retention)
// and a bucket that has not yet closed must not trip NFR5's check.
func TestCompleterRunPending_NoStaleCaptureReturnsNil(t *testing.T) {
	pool, sensorID := newCaptureFixture(t)
	ctx := context.Background()

	recentBoundary := time.Now().Add(-1 * time.Minute)
	neverClosedBucket := time.Now().Add(24 * time.Hour)
	if _, err := pool.Exec(ctx, `
		INSERT INTO boundary_capture (sensor_id, boundary_at, tier, bucket_start) VALUES ($1, $2, 'hourly', $3)
	`, sensorID, recentBoundary, neverClosedBucket); err != nil {
		t.Fatalf("seed recent pending capture: %v", err)
	}

	if err := NewCompleter(pool).RunPending(ctx); err != nil {
		t.Fatalf("RunPending = %v, want nil (pending capture is nowhere near raw retention)", err)
	}
}

// ── Edge case discovered while writing the tests above ────────────────

// TestBoundaryExactlyAtBucketStartCompletesWithoutZeroWidthPartial covers a
// defect found while first drafting
// TestHourlyCompletionNeverQueriesFullHourlyBucketTable (that test
// originally, and purely incidentally to what it was proving, picked a
// boundary_at that happened to land exactly on a five-minute bucket
// boundary): when boundaryAt equals bucketStart exactly, findSplitTarget's
// implicit "whole bucket" split target is [bucketStart, bucketEnd), and
// completeOne unconditionally computes and inserts a "left" partial
// [partialFrom, boundaryAt) -- here [bucketStart, bucketStart), a
// zero-width interval -- which violates boundary_partial's own
// partial_from < partial_to CHECK constraint (migration 033). RunPending
// then returns an error instead of completing the capture, for a case that
// should be entirely resolvable: a bucket whose boundary lands exactly on
// its own start has nothing before the boundary in that bucket at all, and
// should complete as a single whole-bucket partial (or with no split
// needed), never crash.
//
// This is reachable in practice: Recorder.Record's boundaryAt is caller
// -supplied (FR19's writer passes the database's own NOW(), effectively
// never bucket-aligned, but FR20's own Implementation section says this
// package is also called from Phase 5's FR51/FR74 writers, whose boundary
// instants are not guaranteed non-aligned the same way).
func TestBoundaryExactlyAtBucketStartCompletesWithoutZeroWidthPartial(t *testing.T) {
	pool, sensorID := newCaptureFixture(t)
	ctx := context.Background()

	// boundaryAt lands exactly on the five-minute bucket boundary
	// [10:35, 10:40) but not on an hourly bucket boundary, isolating the
	// defect to the five-minute tier for a clearer failure signal.
	bucketStart := time.Date(2024, 6, 1, 10, 35, 0, 0, time.UTC)
	boundaryAt := bucketStart
	insertRawReading(t, ctx, pool, sensorID, 3.0, boundaryAt.Add(1*time.Minute))
	insertRawReading(t, ctx, pool, sensorID, 7.0, boundaryAt.Add(3*time.Minute))

	recordBoundary(t, ctx, pool, sensorID, boundaryAt)

	if err := NewCompleter(pool).RunPending(ctx); err != nil {
		t.Fatalf("RunPending = %v, want nil -- a boundary landing exactly on its bucket's start must not crash the completer", err)
	}

	partials := fetchPartials(t, ctx, pool, sensorID, "five_minute", bucketStart)
	for _, p := range partials {
		if !p.From.Before(p.To) {
			t.Errorf("zero-width partial [%s, %s) was written", p.From, p.To)
		}
	}

	// Whatever shape the partial(s) take, their union must exactly cover
	// [bucketStart, bucketEnd) with no gaps and equal the raw-restricted
	// computation over the whole bucket -- FR20.4's exactness clause holds
	// regardless of how many rows the bucket ends up represented by.
	var merged aggregateResult
	cursor := bucketStart
	for _, p := range partials {
		if !p.From.Equal(cursor) {
			t.Fatalf("partial gap: expected next partial to start at %s, got %s", cursor, p.From)
		}
		merged = merged.merge(aggregateResult{Count: p.Count, Sum: p.Sum, Min: p.Min, Max: p.Max})
		cursor = p.To
	}
	bucketEnd := bucketStart.Add(5 * time.Minute)
	if !cursor.Equal(bucketEnd) {
		t.Fatalf("partials do not cover the whole bucket: covered up to %s, want %s", cursor, bucketEnd)
	}

	wantCount, wantSum, wantMin, wantMax := directRawAggregate(t, ctx, pool, sensorID, bucketStart, bucketEnd)
	if merged.Count != wantCount || merged.Sum != wantSum || merged.Min != wantMin || merged.Max != wantMax {
		t.Errorf("merged partials = (%d, %v, %v, %v), want raw-restricted whole-bucket (%d, %v, %v, %v)",
			merged.Count, merged.Sum, merged.Min, merged.Max, wantCount, wantSum, wantMin, wantMax)
	}
}

// ── boundary_partial differential retention (FR20.2, migration 033) ────

// seedCompletedPartial inserts one already-completed boundary_capture row
// and its single boundary_partial row directly (bypassing Recorder/
// Completer, which always use the real clock) so retention tests can plant
// a partial at an arbitrary bucket_start, however old, without waiting on
// wall time. Returns the new capture_id.
func seedCompletedPartial(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sensorID int64, tier string, bucketStart time.Time) int64 {
	t.Helper()
	var captureID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO boundary_capture (sensor_id, boundary_at, tier, bucket_start, state, completed_at)
		VALUES ($1, $2, $3, $4, 'completed', $2)
		RETURNING capture_id
	`, sensorID, bucketStart, tier, bucketStart).Scan(&captureID); err != nil {
		t.Fatalf("seed completed boundary_capture (%s, %s): %v", tier, bucketStart, err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO boundary_partial (capture_id, tier, bucket_start, partial_from, partial_to, reading_count, value_sum, value_min, value_max)
		VALUES ($1, $2, $3, $3, $4, 1, 1, 1, 1)
	`, captureID, tier, bucketStart, bucketStart.Add(time.Minute)); err != nil {
		t.Fatalf("seed boundary_partial (%s, %s): %v", tier, bucketStart, err)
	}
	return captureID
}

// countPartials returns how many boundary_partial rows exist for captureID.
func countPartials(t *testing.T, ctx context.Context, pool *pgxpool.Pool, captureID int64) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM boundary_partial WHERE capture_id = $1`, captureID).Scan(&n); err != nil {
		t.Fatalf("count boundary_partial for capture %d: %v", captureID, err)
	}
	return n
}

// TestPruneExpiredPartials_HourlySurvives14MonthRetentionPass proves
// migration 033's differential retention promise (FR20.2: "retention on
// boundary_partial follows the coarsest tier the partial splits -- hourly
// partials are never dropped"): a 14-month-old simulated retention pass
// (well past tiers.FiveMinuteRetention's 90 days) drops the five_minute
// partial but leaves the hourly partial -- at the exact same age --
// untouched. A recent five_minute partial, still inside the retention
// window, is also left untouched, so the query is proven to gate on age,
// not merely tier.
func TestPruneExpiredPartials_HourlySurvives14MonthRetentionPass(t *testing.T) {
	pool, sensorID := newCaptureFixture(t)
	ctx := context.Background()

	fourteenMonthsAgo := time.Now().Add(-14 * 30 * 24 * time.Hour)
	oldFiveMinuteCapture := seedCompletedPartial(t, ctx, pool, sensorID, "five_minute", fourteenMonthsAgo)
	oldHourlyCapture := seedCompletedPartial(t, ctx, pool, sensorID, "hourly", fourteenMonthsAgo)
	recentFiveMinuteCapture := seedCompletedPartial(t, ctx, pool, sensorID, "five_minute", time.Now().Add(-24*time.Hour))

	if err := NewCompleter(pool).PruneExpiredPartials(ctx); err != nil {
		t.Fatalf("PruneExpiredPartials: %v", err)
	}

	if n := countPartials(t, ctx, pool, oldFiveMinuteCapture); n != 0 {
		t.Errorf("14-month-old five_minute partial survived retention: %d rows remain, want 0", n)
	}
	if n := countPartials(t, ctx, pool, oldHourlyCapture); n != 1 {
		t.Errorf("14-month-old hourly partial was dropped: %d rows remain, want 1 (hourly must never be dropped)", n)
	}
	if n := countPartials(t, ctx, pool, recentFiveMinuteCapture); n != 1 {
		t.Errorf("recent (within-retention) five_minute partial was dropped: %d rows remain, want 1", n)
	}
}
