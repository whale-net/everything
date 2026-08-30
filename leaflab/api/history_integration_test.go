//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs it.
// See the go_test target's gotags in BUILD.bazel and
// libs/go/dbtest/README.md for how to run it explicitly.
//
// These tests exercise GetSensorReadingHistory (FR9, FR10) against a real
// Postgres: the point cap (ORDER BY recorded_at DESC LIMIT N applied in
// SQL, not sliced in Go) and the invalid-reading count cannot be verified
// against an in-memory fake -- both depend on the query actually running.
//
// Schema here is a self-contained copy of the sensor/sensor_reading column
// set migration 001_initial_schema.up.sql creates (see
// leaflab/migrate/migrations/001_initial_schema.up.sql) -- dbtest's own
// README asks integration tests to keep schema self-contained rather than
// importing another package's migrations. The real migration also creates a
// TimescaleDB hypertable via create_hypertable(); that's a chunk-pruning
// performance property, not something these tests need a real hypertable to
// prove, so a plain table stands in for it here.
package main

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/libs/go/dbtest"
)

const historySchema = `
	CREATE TABLE sensor (
		sensor_id BIGSERIAL PRIMARY KEY
	);

	CREATE TABLE sensor_reading (
		reading_id  BIGSERIAL,
		sensor_id   BIGINT NOT NULL REFERENCES sensor(sensor_id),
		value       DOUBLE PRECISION NOT NULL,
		valid       BOOLEAN NOT NULL DEFAULT TRUE,
		recorded_at TIMESTAMPTZ NOT NULL,
		PRIMARY KEY (reading_id, recorded_at)
	);
`

// newHistoryTestRepo starts a real, throwaway Postgres and returns a
// Repository plus the raw pool for fixture setup.
func newHistoryTestRepo(t *testing.T) (*Repository, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: historySchema})
	return NewRepository(db.Pool), db.Pool
}

// insertSensor inserts a bare sensor row and returns its ID. Board/type/
// region are irrelevant to GetSensorReadingHistory, which queries
// sensor_reading directly, so the schema above omits them entirely.
func insertSensor(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int64 {
	t.Helper()
	var sensorID int64
	err := pool.QueryRow(ctx, `INSERT INTO sensor DEFAULT VALUES RETURNING sensor_id`).Scan(&sensorID)
	require.NoError(t, err, "insert sensor fixture")
	return sensorID
}

// insertReadings bulk-inserts n readings for sensorID, one per second
// starting at start, via a single generate_series INSERT so the over-cap
// tests (15,000+ rows) stay fast instead of paying a round trip per row.
func insertReadings(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sensorID int64, n int, start time.Time, valid bool) {
	t.Helper()
	if n == 0 {
		return
	}
	_, err := pool.Exec(ctx, `
		INSERT INTO sensor_reading (sensor_id, value, valid, recorded_at)
		SELECT $1, i, $2, $3::timestamptz + (i * interval '1 second')
		FROM generate_series(0, $4::int - 1) AS s(i)
	`, sensorID, valid, start, n)
	require.NoError(t, err, "bulk insert %d readings", n)
}

// ── Repository.GetSensorReadingHistory: FR9/FR10 five cases ────────────────

func TestGetSensorReadingHistory_UnderCap_ReturnsAllAscendingUncapped(t *testing.T) {
	ctx := context.Background()
	repo, pool := newHistoryTestRepo(t)
	sensorID := insertSensor(t, ctx, pool)

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	insertReadings(t, ctx, pool, sensorID, 100, start, true)

	from := start.Add(-time.Hour)
	to := start.Add(24 * time.Hour)
	hist, err := repo.GetSensorReadingHistory(ctx, sensorID, from, to)
	require.NoError(t, err)

	require.Len(t, hist.Points, 100)
	assert.False(t, hist.Capped, "100 points is well under the 15,000 cap")
	assert.Equal(t, uint32(0), hist.ExcludedInvalidCount)
	assertAscending(t, hist.Points)
	assert.True(t, hist.Points[0].RecordedAt.Equal(start))
	assert.True(t, hist.Points[99].RecordedAt.Equal(start.Add(99*time.Second)))
}

func TestGetSensorReadingHistory_OverCap_ReturnsMostRecentCappedPoints(t *testing.T) {
	ctx := context.Background()
	repo, pool := newHistoryTestRepo(t)
	sensorID := insertSensor(t, ctx, pool)

	const total = historyPointCap + 5 // 15,005: 5 over the cap
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	insertReadings(t, ctx, pool, sensorID, total, start, true)

	from := start.Add(-time.Hour)
	to := start.Add(time.Duration(total)*time.Second + time.Hour)
	hist, err := repo.GetSensorReadingHistory(ctx, sensorID, from, to)
	require.NoError(t, err)

	require.Len(t, hist.Points, historyPointCap, "must return exactly the cap, not more")
	assert.True(t, hist.Capped)
	assertAscending(t, hist.Points)

	oldestInRange := start
	assert.True(t, hist.Points[0].RecordedAt.After(oldestInRange),
		"the oldest *returned* point must be newer than the range's oldest reading -- "+
			"the cap keeps the most recent points, not the oldest and not a sample")

	// The 5 oldest readings (i=0..4) were dropped; the returned window is
	// exactly the most recent 15,000 (i=5..15004).
	wantOldestReturned := start.Add(5 * time.Second)
	wantNewestReturned := start.Add(time.Duration(total-1) * time.Second)
	assert.True(t, hist.Points[0].RecordedAt.Equal(wantOldestReturned))
	assert.True(t, hist.Points[len(hist.Points)-1].RecordedAt.Equal(wantNewestReturned))

	assert.True(t, hist.CoveredFrom.Equal(hist.Points[0].RecordedAt),
		"covered_from must match the actual span of the returned points")
	assert.True(t, hist.CoveredTo.Equal(hist.Points[len(hist.Points)-1].RecordedAt),
		"covered_to must match the actual span of the returned points")
	assert.Equal(t, uint32(0), hist.ExcludedInvalidCount)
}

func TestGetSensorReadingHistory_AllInvalid_ZeroPointsFullInvalidCount(t *testing.T) {
	ctx := context.Background()
	repo, pool := newHistoryTestRepo(t)
	sensorID := insertSensor(t, ctx, pool)

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	insertReadings(t, ctx, pool, sensorID, 50, start, false)

	from := start.Add(-time.Hour)
	to := start.Add(24 * time.Hour)
	hist, err := repo.GetSensorReadingHistory(ctx, sensorID, from, to)
	require.NoError(t, err)

	assert.Empty(t, hist.Points)
	assert.False(t, hist.Capped)
	assert.Equal(t, uint32(50), hist.ExcludedInvalidCount)
}

func TestGetSensorReadingHistory_NoReadingsAtAll_ZeroPointsZeroInvalidCount(t *testing.T) {
	ctx := context.Background()
	repo, pool := newHistoryTestRepo(t)
	sensorID := insertSensor(t, ctx, pool)
	// No readings inserted at all.

	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	hist, err := repo.GetSensorReadingHistory(ctx, sensorID, from, to)
	require.NoError(t, err)

	assert.Empty(t, hist.Points)
	assert.False(t, hist.Capped)
	assert.Equal(t, uint32(0), hist.ExcludedInvalidCount)
}

func TestGetSensorReadingHistory_CappedAndInvalid_BothIndependentlyReported(t *testing.T) {
	ctx := context.Background()
	repo, pool := newHistoryTestRepo(t)
	sensorID := insertSensor(t, ctx, pool)

	const totalValid = historyPointCap + 5
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	insertReadings(t, ctx, pool, sensorID, totalValid, start, true)

	// 20 invalid readings interleaved in the same range, at times distinct
	// from the valid readings so they can't accidentally land on a valid
	// timestamp and get silently overwritten by a PK conflict.
	invalidStart := start.Add(-time.Minute)
	insertReadings(t, ctx, pool, sensorID, 20, invalidStart, false)

	from := start.Add(-time.Hour)
	to := start.Add(time.Duration(totalValid)*time.Second + time.Hour)
	hist, err := repo.GetSensorReadingHistory(ctx, sensorID, from, to)
	require.NoError(t, err)

	require.Len(t, hist.Points, historyPointCap)
	assert.True(t, hist.Capped, "over-cap valid readings must still trip capped")
	assert.Equal(t, uint32(20), hist.ExcludedInvalidCount,
		"invalid count must keep counting across the whole selected range even though the cap fired")
}

// assertAscending fails the test if points are not strictly ordered
// oldest-to-newest.
func assertAscending(t *testing.T, points []ReadingPoint) {
	t.Helper()
	for i := 1; i < len(points); i++ {
		if !points[i].RecordedAt.After(points[i-1].RecordedAt) {
			t.Fatalf("points not ascending at index %d: %v is not after %v",
				i, points[i].RecordedAt, points[i-1].RecordedAt)
		}
	}
}

// ── LeafLabAPIServer.GetSensorReadingHistory: range validation, NotFound, OK-empty ──

func newHistoryTestServer(t *testing.T) (*LeafLabAPIServer, *pgxpool.Pool) {
	t.Helper()
	repo, pool := newHistoryTestRepo(t)
	return NewLeafLabAPIServer(repo, nil, slog.Default()), pool
}

// TestGetSensorReadingHistory_Server_ToNotAfterFrom_InvalidArgument and
// TestGetSensorReadingHistory_Server_RangeOver30Days_InvalidArgument live in
// history_test.go (no build tag) -- both return before the handler ever
// touches the repository, so they don't need a real database and are part
// of the plain `bazel test //leaflab/api/...` run instead of this
// Docker-gated one.

func TestGetSensorReadingHistory_Server_RangeExactly30Days_Allowed(t *testing.T) {
	ctx := context.Background()
	srv, pool := newHistoryTestServer(t)
	sensorID := insertSensor(t, ctx, pool)

	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(30 * 24 * time.Hour)

	_, err := srv.GetSensorReadingHistory(ctx, &pb.GetSensorReadingHistoryRequest{
		SensorId: sensorID,
		From:     timestamppb.New(from),
		To:       timestamppb.New(to),
	})

	require.NoError(t, err, "exactly 30 days is the longest allowed range, not rejected")
}

func TestGetSensorReadingHistory_Server_UnknownSensor_NotFound(t *testing.T) {
	ctx := context.Background()
	srv, _ := newHistoryTestServer(t)

	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)

	_, err := srv.GetSensorReadingHistory(ctx, &pb.GetSensorReadingHistoryRequest{
		SensorId: 99999,
		From:     timestamppb.New(from),
		To:       timestamppb.New(to),
	})

	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err),
		"an unknown sensor_id must be NotFound, distinct from an empty-but-valid range")
}

func TestGetSensorReadingHistory_Server_EmptyRange_OKNotError(t *testing.T) {
	ctx := context.Background()
	srv, pool := newHistoryTestServer(t)
	sensorID := insertSensor(t, ctx, pool)
	// No readings inserted -- the range is valid but empty.

	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)

	resp, err := srv.GetSensorReadingHistory(ctx, &pb.GetSensorReadingHistoryRequest{
		SensorId: sensorID,
		From:     timestamppb.New(from),
		To:       timestamppb.New(to),
	})

	require.NoError(t, err, "an empty range must be OK, never an error")
	assert.Empty(t, resp.Points)
	assert.False(t, resp.Capped)
	assert.Equal(t, uint32(0), resp.ExcludedInvalidCount)
	assert.Equal(t, uint32(historyPointCap), resp.PointCap, "point_cap must always be reported so the UI never hardcodes it")
}

func TestGetSensorReadingHistory_Server_OnlyInvalidReadings_OKZeroPointsNonZeroInvalidCount(t *testing.T) {
	ctx := context.Background()
	srv, pool := newHistoryTestServer(t)
	sensorID := insertSensor(t, ctx, pool)

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	insertReadings(t, ctx, pool, sensorID, 5, start, false)

	from := start.Add(-time.Hour)
	to := start.Add(time.Hour)

	resp, err := srv.GetSensorReadingHistory(ctx, &pb.GetSensorReadingHistoryRequest{
		SensorId: sensorID,
		From:     timestamppb.New(from),
		To:       timestamppb.New(to),
	})

	require.NoError(t, err, "an all-invalid range is still OK, not an error")
	assert.Empty(t, resp.Points)
	assert.Equal(t, uint32(5), resp.ExcludedInvalidCount,
		"excluded_invalid_count is what lets the UI explain the empty chart")
}
