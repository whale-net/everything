//go:build integration

package main

import (
	"context"
	"testing"
	"time"

	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/libs/go/dbtest"
)

// tierQueryTestSchema is a self-contained schema covering only what
// GetSeriesAtTier reads: sensor_reading (raw) plus the two aggregate
// tables it queries directly, sensor_reading_5m and sensor_reading_1h.
// These are plain tables here (not TimescaleDB continuous aggregates) --
// GetSeriesAtTier issues a plain SELECT against each by name, so a plain
// table with the same columns exercises the same query path without
// depending on the TimescaleDB extension or migration 025's continuous
// aggregate machinery. Column shapes mirror migration 025 exactly.
const tierQueryTestSchema = `
CREATE TABLE sensor_reading (
	reading_id  BIGSERIAL,
	sensor_id   BIGINT NOT NULL,
	region_id   BIGINT,
	value       DOUBLE PRECISION NOT NULL,
	valid       BOOLEAN NOT NULL DEFAULT TRUE,
	uptime_ms   INTEGER NOT NULL DEFAULT 0,
	recorded_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	PRIMARY KEY (reading_id, recorded_at)
);

CREATE TABLE sensor_reading_5m (
	sensor_id     BIGINT NOT NULL,
	region_id     BIGINT NOT NULL,
	bucket_start  TIMESTAMPTZ NOT NULL,
	min_value     DOUBLE PRECISION NOT NULL,
	max_value     DOUBLE PRECISION NOT NULL,
	sum_value     DOUBLE PRECISION NOT NULL,
	reading_count BIGINT NOT NULL
);

CREATE TABLE sensor_reading_1h (
	sensor_id     BIGINT NOT NULL,
	region_id     BIGINT NOT NULL,
	bucket_start  TIMESTAMPTZ NOT NULL,
	min_value     DOUBLE PRECISION NOT NULL,
	max_value     DOUBLE PRECISION NOT NULL,
	sum_value     DOUBLE PRECISION NOT NULL,
	reading_count BIGINT NOT NULL
);
`

// TestGetSeriesAtTier_Raw_FiltersValidAndWindow verifies the raw tier's
// query path: it excludes invalid readings, excludes readings outside
// [windowStart, windowEnd), orders ascending by recorded_at, and reports
// each raw row as a bucket-of-one (min == max == sum == value, count == 1,
// BucketStart == the reading's own recorded_at).
func TestGetSeriesAtTier_Raw_FiltersValidAndWindow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: tierQueryTestSchema})
	repo := NewRepository(db.Pool)

	const sensorID int64 = 1
	const regionID int64 = 10
	windowStart := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	windowEnd := windowStart.Add(1 * time.Hour)

	insertRaw := func(recordedAt time.Time, value float64, valid bool) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, `
			INSERT INTO sensor_reading (sensor_id, region_id, value, valid, uptime_ms, recorded_at)
			VALUES ($1, $2, $3, $4, 0, $5)
		`, sensorID, regionID, value, valid, recordedAt); err != nil {
			t.Fatalf("insert sensor_reading: %v", err)
		}
	}

	// In window, valid: should be returned.
	insertRaw(windowStart.Add(10*time.Minute), 21.5, true)
	// In window, invalid: must be excluded.
	insertRaw(windowStart.Add(20*time.Minute), 999.0, false)
	// Before window: must be excluded.
	insertRaw(windowStart.Add(-10*time.Minute), 5.0, true)
	// At or after windowEnd: must be excluded (half-open window).
	insertRaw(windowEnd, 6.0, true)
	// A second valid, in-window reading, inserted out of chronological order
	// to prove the ORDER BY, not insertion order, determines result order.
	insertRaw(windowStart.Add(5*time.Minute), 20.0, true)

	buckets, err := repo.GetSeriesAtTier(ctx, pb.GranularityTier_GRANULARITY_TIER_RAW, sensorID, windowStart, windowEnd)
	if err != nil {
		t.Fatalf("GetSeriesAtTier: %v", err)
	}

	if len(buckets) != 2 {
		t.Fatalf("expected 2 valid in-window raw readings, got %d: %+v", len(buckets), buckets)
	}

	// Ascending order by recorded_at: the 5-minute-in reading first.
	if !buckets[0].BucketStart.Equal(windowStart.Add(5 * time.Minute)) {
		t.Errorf("expected first bucket at +5m, got %v", buckets[0].BucketStart)
	}
	if !buckets[1].BucketStart.Equal(windowStart.Add(10 * time.Minute)) {
		t.Errorf("expected second bucket at +10m, got %v", buckets[1].BucketStart)
	}

	first := buckets[0]
	if first.SensorID != sensorID {
		t.Errorf("expected sensor_id %d, got %d", sensorID, first.SensorID)
	}
	if first.RegionID != regionID {
		t.Errorf("expected region_id %d, got %d", regionID, first.RegionID)
	}
	if first.MinValue != 20.0 || first.MaxValue != 20.0 || first.SumValue != 20.0 {
		t.Errorf("expected a raw row to report a bucket-of-one (min==max==sum==value==20.0), got min=%v max=%v sum=%v",
			first.MinValue, first.MaxValue, first.SumValue)
	}
	if first.ReadingCount != 1 {
		t.Errorf("expected raw ReadingCount == 1, got %d", first.ReadingCount)
	}
}

// TestGetSeriesAtTier_FiveMinute_QueriesAggregateTableDirectly verifies the
// 5-minute tier's query path: it reads sensor_reading_5m directly, filters
// by sensor_id and [windowStart, windowEnd) on bucket_start, and returns
// the aggregate columns as stored (no recomputation).
func TestGetSeriesAtTier_FiveMinute_QueriesAggregateTableDirectly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: tierQueryTestSchema})
	repo := NewRepository(db.Pool)

	const sensorID int64 = 1
	const regionID int64 = 10
	windowStart := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	windowEnd := windowStart.Add(1 * time.Hour)

	insertBucket := func(table string, bucketStart time.Time, min, max, sum float64, count int64, sensor, region int64) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, `
			INSERT INTO `+table+` (sensor_id, region_id, bucket_start, min_value, max_value, sum_value, reading_count)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
		`, sensor, region, bucketStart, min, max, sum, count); err != nil {
			t.Fatalf("insert %s: %v", table, err)
		}
	}

	// In window: should be returned.
	insertBucket("sensor_reading_5m", windowStart.Add(15*time.Minute), 18.0, 22.0, 200.0, 10, sensorID, regionID)
	// Before window: excluded.
	insertBucket("sensor_reading_5m", windowStart.Add(-15*time.Minute), 1.0, 2.0, 3.0, 4, sensorID, regionID)
	// Different sensor, in window: excluded.
	insertBucket("sensor_reading_5m", windowStart.Add(20*time.Minute), 1.0, 2.0, 3.0, 4, sensorID+1, regionID)

	buckets, err := repo.GetSeriesAtTier(ctx, pb.GranularityTier_GRANULARITY_TIER_5_MINUTE, sensorID, windowStart, windowEnd)
	if err != nil {
		t.Fatalf("GetSeriesAtTier: %v", err)
	}

	if len(buckets) != 1 {
		t.Fatalf("expected 1 bucket for sensor %d in window, got %d: %+v", sensorID, len(buckets), buckets)
	}
	b := buckets[0]
	if b.MinValue != 18.0 || b.MaxValue != 22.0 || b.SumValue != 200.0 || b.ReadingCount != 10 {
		t.Errorf("expected stored aggregate values returned as-is, got %+v", b)
	}
	if b.RegionID != regionID {
		t.Errorf("expected region_id %d, got %d", regionID, b.RegionID)
	}
}

// TestGetSeriesAtTier_Hourly_QueriesAggregateTableDirectly mirrors the
// 5-minute test for the hourly tier, verifying GetSeriesAtTier reads
// sensor_reading_1h -- a distinct table, not a coarsening of the 5-minute
// results -- when the hourly tier is requested.
func TestGetSeriesAtTier_Hourly_QueriesAggregateTableDirectly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: tierQueryTestSchema})
	repo := NewRepository(db.Pool)

	const sensorID int64 = 1
	const regionID int64 = 10
	windowStart := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	windowEnd := windowStart.Add(3 * time.Hour)

	// Seed the same sensor/window into sensor_reading_5m with different
	// values, to prove GetSeriesAtTier(HOURLY, ...) does not accidentally
	// fall through to the 5-minute table.
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO sensor_reading_5m (sensor_id, region_id, bucket_start, min_value, max_value, sum_value, reading_count)
		VALUES ($1, $2, $3, 999, 999, 999, 999)
	`, sensorID, regionID, windowStart.Add(30*time.Minute)); err != nil {
		t.Fatalf("insert decoy 5m row: %v", err)
	}

	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO sensor_reading_1h (sensor_id, region_id, bucket_start, min_value, max_value, sum_value, reading_count)
		VALUES ($1, $2, $3, 15.0, 25.0, 1200.0, 60)
	`, sensorID, regionID, windowStart.Add(1*time.Hour)); err != nil {
		t.Fatalf("insert sensor_reading_1h: %v", err)
	}

	buckets, err := repo.GetSeriesAtTier(ctx, pb.GranularityTier_GRANULARITY_TIER_HOURLY, sensorID, windowStart, windowEnd)
	if err != nil {
		t.Fatalf("GetSeriesAtTier: %v", err)
	}

	if len(buckets) != 1 {
		t.Fatalf("expected exactly 1 hourly bucket (the 5-minute decoy row must not appear), got %d: %+v", len(buckets), buckets)
	}
	b := buckets[0]
	if b.MinValue != 15.0 || b.MaxValue != 25.0 || b.SumValue != 1200.0 || b.ReadingCount != 60 {
		t.Errorf("expected the sensor_reading_1h row's values, got %+v (decoy 5m values would be 999)", b)
	}
}

// TestGetSeriesAtTier_FR20Straddle_ReturnsBothRegionRowsForOneBucket
// verifies the central FR20 straddle case: a sensor that changed region
// mid-bucket produces more than one (sensor_id, region_id) row for the
// same bucket_start, and GetSeriesAtTier must return all of them rather
// than collapsing, deduplicating, or picking one region arbitrarily.
// Losing this would silently hide exactly the boundary partial FR20's
// capture exists to make visible.
func TestGetSeriesAtTier_FR20Straddle_ReturnsBothRegionRowsForOneBucket(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: tierQueryTestSchema})
	repo := NewRepository(db.Pool)

	const sensorID int64 = 1
	const regionBeforeMove int64 = 10
	const regionAfterMove int64 = 20
	windowStart := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	bucketStart := windowStart.Add(5 * time.Minute)
	windowEnd := windowStart.Add(1 * time.Hour)

	// Same sensor_id, same bucket_start, two different region_id rows --
	// exactly what a mid-bucket region move produces in the continuous
	// aggregate (grouped by sensor_id, region_id, bucket_start).
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO sensor_reading_5m (sensor_id, region_id, bucket_start, min_value, max_value, sum_value, reading_count)
		VALUES
			($1, $2, $3, 18.0, 19.0, 55.5, 3),
			($1, $4, $3, 20.0, 21.0, 41.0, 2)
	`, sensorID, regionBeforeMove, bucketStart, regionAfterMove); err != nil {
		t.Fatalf("insert straddling 5m rows: %v", err)
	}

	buckets, err := repo.GetSeriesAtTier(ctx, pb.GranularityTier_GRANULARITY_TIER_5_MINUTE, sensorID, windowStart, windowEnd)
	if err != nil {
		t.Fatalf("GetSeriesAtTier: %v", err)
	}

	if len(buckets) != 2 {
		t.Fatalf("FR20: expected both straddling region rows for the one bucket to be returned, got %d: %+v", len(buckets), buckets)
	}

	seenRegions := map[int64]bool{}
	for _, b := range buckets {
		if !b.BucketStart.Equal(bucketStart) {
			t.Errorf("expected both straddling rows to share bucket_start %v, got %v", bucketStart, b.BucketStart)
		}
		seenRegions[b.RegionID] = true
	}
	if !seenRegions[regionBeforeMove] || !seenRegions[regionAfterMove] {
		t.Errorf("expected both region_id %d and %d present (straddle disclosed, not collapsed), got regions %v",
			regionBeforeMove, regionAfterMove, seenRegions)
	}
}

// TestGetSeriesAtTier_UnspecifiedTierRefused verifies GRANULARITY_TIER_UNSPECIFIED
// has no query path: ResolveTier never returns it (A14), so a caller
// passing it here is a programming error and must be refused, not
// silently guessed at.
func TestGetSeriesAtTier_UnspecifiedTierRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: tierQueryTestSchema})
	repo := NewRepository(db.Pool)

	now := time.Now()
	_, err := repo.GetSeriesAtTier(ctx, pb.GranularityTier_GRANULARITY_TIER_UNSPECIFIED, 1, now.Add(-time.Hour), now)
	if err == nil {
		t.Fatal("expected an error for GRANULARITY_TIER_UNSPECIFIED, got nil")
	}
}
