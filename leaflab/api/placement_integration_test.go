//go:build integration

// This file only builds under the "integration" build tag, same as
// repository_integration_test.go -- see that file's doc comment for why
// (Docker-less machines, `bazel test //...` never even compiling it, let
// alone running it).
//
// M3 placement tests (#2314): Repository.PlaceSensor is the sole writer of
// sensor.region_id / sensor_region_history (FR7), SCD2 close-and-open per
// AGENTS.md § SCD2, and a placement change is visible to the very next
// reading insert (FR8/FR9) -- readings written before the move keep the
// region they were stamped with, readings after attribute to the new region
// immediately, with no device round trip.
//
// Schema setup runs the real migrations via newLeafLabTestPool
// (testdb_integration_test.go), so these tests exercise the actual schema --
// including migration 011's valid_from/valid_to rename of
// sensor_region_history's interval columns.
package main

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedPlacementRegion inserts a region row and returns its region_id.
// Named distinctly from migration017SeedRegion/migration016SeedRegion so the
// file drops into the shared api_integration_test package without collisions.
func seedPlacementRegion(t *testing.T, pool *pgxpool.Pool, name string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO region (name) VALUES ($1) RETURNING region_id`, name).Scan(&id); err != nil {
		t.Fatalf("seed region %s: %v", name, err)
	}
	return id
}

// placementRow is one sensor_region_history row as the tests see it.
type placementRow struct {
	RegionID int64
	ValidTo  *time.Time
}

// placementRows returns the sensor's full sensor_region_history,
// oldest-first, so tests can assert the exact close-and-open shape.
func placementRows(t *testing.T, pool *pgxpool.Pool, sensorID int64) []placementRow {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT region_id, valid_to
		FROM sensor_region_history
		WHERE sensor_id = $1
		ORDER BY valid_from, history_id
	`, sensorID)
	if err != nil {
		t.Fatalf("query sensor_region_history for sensor %d: %v", sensorID, err)
	}
	defer rows.Close()

	var out []placementRow
	for rows.Next() {
		var r placementRow
		if err := rows.Scan(&r.RegionID, &r.ValidTo); err != nil {
			t.Fatalf("scan sensor_region_history row: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate sensor_region_history: %v", err)
	}
	return out
}

// openPlacementRegions returns the region_ids of the sensor's open
// (valid_to IS NULL) sensor_region_history rows. A well-formed SCD2 state
// has at most one.
func openPlacementRegions(t *testing.T, pool *pgxpool.Pool, sensorID int64) []int64 {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT region_id FROM sensor_region_history
		WHERE sensor_id = $1 AND valid_to IS NULL
	`, sensorID)
	if err != nil {
		t.Fatalf("query open sensor_region_history for sensor %d: %v", sensorID, err)
	}
	defer rows.Close()

	var regions []int64
	for rows.Next() {
		var regionID int64
		if err := rows.Scan(&regionID); err != nil {
			t.Fatalf("scan open sensor_region_history row: %v", err)
		}
		regions = append(regions, regionID)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate open sensor_region_history: %v", err)
	}
	return regions
}

// mirrorRegion reads sensor.region_id (nil = unplaced).
func mirrorRegion(t *testing.T, pool *pgxpool.Pool, sensorID int64) *int64 {
	t.Helper()
	var regionID *int64
	if err := pool.QueryRow(context.Background(),
		`SELECT region_id FROM sensor WHERE sensor_id = $1`, sensorID).Scan(&regionID); err != nil {
		t.Fatalf("read sensor.region_id for sensor %d: %v", sensorID, err)
	}
	return regionID
}

// mirrorRegionValue dereferences mirrorRegion for the not-unplaced case.
func mirrorRegionValue(t *testing.T, pool *pgxpool.Pool, sensorID int64) int64 {
	t.Helper()
	regionID := mirrorRegion(t, pool, sensorID)
	require.NotNil(t, regionID, "sensor %d should have a region_id mirror", sensorID)
	return *regionID
}

// insertReadingAtCurrentPlacement writes one sensor_reading row using the
// exact SQL of leaflab/processor/repository.go's Repository.InsertReading
// (FR8/FR9's production write path), copied verbatim. processor_lib is
// package main in a different binary and not importable from this package,
// the same way convergence_integration_test.go's
// insertUserInitiatedConfigNextVersion copies leaflab-api's statement into
// the processor's integration tests -- the *pattern* both sides share is
// what's under test here, against the real schema. The inserted row's
// region_id is whatever the sensor row's current placement was at insert
// time; read it back with readingAttribution.
func insertReadingAtCurrentPlacement(ctx context.Context, pool *pgxpool.Pool, sensorID int64, value float64, recordedAt time.Time) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO sensor_reading (sensor_id, region_id, value, valid, uptime_s, recorded_at, config_version)
		SELECT $1, s.region_id, $2, TRUE, 42, $3, NULL
		FROM sensor s
		WHERE s.sensor_id = $1
	`, sensorID, value, recordedAt)
	return err
}

// readingAttribution returns the sensor's readings' region_ids,
// oldest-first (nil for a reading written while the sensor was unplaced).
func readingAttribution(t *testing.T, pool *pgxpool.Pool, sensorID int64) []*int64 {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT region_id FROM sensor_reading
		WHERE sensor_id = $1
		ORDER BY recorded_at, reading_id
	`, sensorID)
	if err != nil {
		t.Fatalf("query sensor_reading for sensor %d: %v", sensorID, err)
	}
	defer rows.Close()

	var regions []*int64
	for rows.Next() {
		var regionID *int64
		if err := rows.Scan(&regionID); err != nil {
			t.Fatalf("scan sensor_reading row: %v", err)
		}
		regions = append(regions, regionID)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate sensor_reading: %v", err)
	}
	return regions
}

// TestPlaceSensor_FirstPlacement_OpensHistoryRow_SetsMirror: placing a sensor
// that had no placement opens exactly one open sensor_region_history row and
// sets the sensor.region_id mirror (FR7). Nothing to close, nothing else
// written.
func TestPlaceSensor_FirstPlacement_OpensHistoryRow_SetsMirror(t *testing.T) {
	repo, pool := newTestRepository(t)
	ctx := context.Background()

	boardID := seedBoard(t, pool, "leaflab-place-first")
	typeID := seedSensorType(t, pool, "temperature")
	sensorID := seedSensor(t, pool, boardID, typeID, "first-place")
	regionID := seedPlacementRegion(t, pool, "shelf-a")

	require.NoError(t, repo.PlaceSensor(ctx, sensorID, regionID))

	rows := placementRows(t, pool, sensorID)
	require.Len(t, rows, 1, "exactly one sensor_region_history row after first placement")
	assert.Nil(t, rows[0].ValidTo, "the row must be open (valid_to IS NULL)")
	assert.Equal(t, regionID, rows[0].RegionID)
	assert.Equal(t, regionID, mirrorRegionValue(t, pool, sensorID),
		"sensor.region_id mirror must match the placement")
}

// TestPlaceSensor_Move_ClosesOldRow_OpensNewRow: moving an already-placed
// sensor closes the old history row (keeping its region recorded) and opens
// exactly one new row for the new region, with the mirror following (FR7,
// SCD2 close-and-open in one transaction).
func TestPlaceSensor_Move_ClosesOldRow_OpensNewRow(t *testing.T) {
	repo, pool := newTestRepository(t)
	ctx := context.Background()

	boardID := seedBoard(t, pool, "leaflab-place-move")
	typeID := seedSensorType(t, pool, "temperature")
	sensorID := seedSensor(t, pool, boardID, typeID, "mover")
	regionA := seedPlacementRegion(t, pool, "room-1")
	regionB := seedPlacementRegion(t, pool, "room-2")

	require.NoError(t, repo.PlaceSensor(ctx, sensorID, regionA))
	require.NoError(t, repo.PlaceSensor(ctx, sensorID, regionB))

	rows := placementRows(t, pool, sensorID)
	require.Len(t, rows, 2, "a move is recorded as closed old row + open new row")
	assert.Equal(t, regionA, rows[0].RegionID, "old row keeps the region it was placed in")
	assert.NotNil(t, rows[0].ValidTo, "old row must be closed (valid_to set)")
	assert.Equal(t, regionB, rows[1].RegionID)
	assert.Nil(t, rows[1].ValidTo, "exactly one open row after the move")
	assert.Equal(t, regionB, mirrorRegionValue(t, pool, sensorID),
		"mirror must follow the move")
}

// TestPlaceSensor_IdempotentSameRegion_NoHistoryWrite: placing a sensor into
// the region it already occupies writes nothing -- a repeated UI submit must
// not fabricate a placement "move" in the history.
func TestPlaceSensor_IdempotentSameRegion_NoHistoryWrite(t *testing.T) {
	repo, pool := newTestRepository(t)
	ctx := context.Background()

	boardID := seedBoard(t, pool, "leaflab-place-idem")
	typeID := seedSensorType(t, pool, "temperature")
	sensorID := seedSensor(t, pool, boardID, typeID, "stayer")
	regionID := seedPlacementRegion(t, pool, "shelf-b")

	require.NoError(t, repo.PlaceSensor(ctx, sensorID, regionID))
	rowsBefore := placementRows(t, pool, sensorID)
	require.Len(t, rowsBefore, 1)

	require.NoError(t, repo.PlaceSensor(ctx, sensorID, regionID))

	rows := placementRows(t, pool, sensorID)
	require.Len(t, rows, 1, "same-region placement must not add a history row")
	assert.Nil(t, rows[0].ValidTo)
}

// TestPlaceSensor_UnknownRegion_NoWrite: an unknown region_id is refused
// (ErrRegionNotFound) and touches nothing -- no history row, mirror
// unchanged.
func TestPlaceSensor_UnknownRegion_NoWrite(t *testing.T) {
	repo, pool := newTestRepository(t)
	ctx := context.Background()

	boardID := seedBoard(t, pool, "leaflab-place-badregion")
	typeID := seedSensorType(t, pool, "temperature")
	sensorID := seedSensor(t, pool, boardID, typeID, "stays-put")
	regionA := seedPlacementRegion(t, pool, "shelf-c")

	// Establish a real placement, then try to move it to a nonexistent one.
	require.NoError(t, repo.PlaceSensor(ctx, sensorID, regionA))
	rowsBefore := placementRows(t, pool, sensorID)

	err := repo.PlaceSensor(ctx, sensorID, 999999)
	require.ErrorIs(t, err, ErrRegionNotFound)

	assert.Equal(t, rowsBefore, placementRows(t, pool, sensorID),
		"refused placement must not touch sensor_region_history")
	assert.Equal(t, regionA, mirrorRegionValue(t, pool, sensorID),
		"refused placement must not touch sensor.region_id")
}

// TestPlaceSensor_UnknownSensor_NoWrite: an unknown sensor_id is refused
// (ErrSensorNotFound) and writes nothing anywhere.
func TestPlaceSensor_UnknownSensor_NoWrite(t *testing.T) {
	repo, pool := newTestRepository(t)
	ctx := context.Background()

	regionID := seedPlacementRegion(t, pool, "shelf-c")

	var historyCount int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM sensor_region_history`).Scan(&historyCount))

	err := repo.PlaceSensor(ctx, 999999, regionID)
	require.ErrorIs(t, err, ErrSensorNotFound)

	var historyCountAfter int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM sensor_region_history`).Scan(&historyCountAfter))
	assert.Equal(t, historyCount, historyCountAfter,
		"refused placement must not touch sensor_region_history")
}

// TestPlaceSensor_Move_ReadingAttribution_FR8_FR9: a placement change takes
// effect for the very next reading written after it, and readings written
// before it keep the region they were stamped with. This is the
// end-to-end-place-with-the-real-writer half of FR8/FR9: the sensor is
// placed and moved through the real PlaceSensor, readings go through
// processor InsertReading's exact INSERT ... SELECT (copied verbatim above),
// so the test proves the pair -- PlaceSensor's mirror update plus
// InsertReading's insert-time resolution -- attributes correctly, with no
// device round trip in between.
func TestPlaceSensor_Move_ReadingAttribution_FR8_FR9(t *testing.T) {
	repo, pool := newTestRepository(t)
	ctx := context.Background()

	boardID := seedBoard(t, pool, "leaflab-place-attr")
	typeID := seedSensorType(t, pool, "temperature")
	sensorID := seedSensor(t, pool, boardID, typeID, "attribution")
	regionA := seedPlacementRegion(t, pool, "greenhouse")
	regionB := seedPlacementRegion(t, pool, "propagation")

	// Before any placement, readings carry no region at all.
	base := time.Now()
	require.NoError(t, insertReadingAtCurrentPlacement(ctx, pool, sensorID, 1.0, base))

	// Placed in region A; the next reading stamps region A.
	require.NoError(t, repo.PlaceSensor(ctx, sensorID, regionA))
	require.NoError(t, insertReadingAtCurrentPlacement(ctx, pool, sensorID, 2.0, base.Add(time.Minute)))

	// Move to region B (pure DB write -- no device round trip, LB2); the
	// next reading must attribute to B immediately (FR9).
	require.NoError(t, repo.PlaceSensor(ctx, sensorID, regionB))
	require.NoError(t, insertReadingAtCurrentPlacement(ctx, pool, sensorID, 3.0, base.Add(2*time.Minute)))

	regions := readingAttribution(t, pool, sensorID)
	require.Len(t, regions, 3)
	assert.Nil(t, regions[0], "reading before any placement has no region")
	require.NotNil(t, regions[1])
	assert.Equal(t, regionA, *regions[1],
		"reading written before the move keeps the old region (FR8: a placement change never rewrites old readings)")
	require.NotNil(t, regions[2])
	assert.Equal(t, regionB, *regions[2],
		"reading written after the move attributes to the new region immediately (FR9: no device round trip)")
}
