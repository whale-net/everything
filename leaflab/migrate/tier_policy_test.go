//go:build integration

// This file only builds under the "integration" build tag -- see
// migrations_test.go's header comment for why, and //libs/go/dbtest's
// README for how to run it. It covers FR71/NFR5 (migration 025):
// the granularity tiers themselves, and the retention/refresh policy
// ordering constraints that protect FR20's boundary captures.
package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

// setUpAllMigrations applies every migration against a fresh TimescaleDB
// database and returns the ready pool.
func setUpAllMigrations(ctx context.Context, t *testing.T) *dbtest.Postgres {
	t.Helper()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Image: timescaleDBImage})
	sqlDB := openDB(t, db)
	runner := migrate.NewRunner(sqlDB, migrations, "migrations")
	if err := runner.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}
	return db
}

// createTestSensor creates a board, a region, and a sensor on that board
// placed in that region, returning the sensor_id. household_id is left
// NULL throughout (nullable per migrations 016/017) -- this test is about
// the tier aggregates, not household attribution.
func createTestSensor(ctx context.Context, t *testing.T, db *dbtest.Postgres, deviceID string) (sensorID, regionID int64) {
	t.Helper()

	var boardID int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO board (device_id, registered_at, last_seen_at)
		VALUES ($1, NOW(), NOW())
		RETURNING board_id
	`, deviceID).Scan(&boardID); err != nil {
		t.Fatalf("insert board: %v", err)
	}

	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO region (name) VALUES ($1)
		RETURNING region_id
	`, "region-"+deviceID).Scan(&regionID); err != nil {
		t.Fatalf("insert region: %v", err)
	}

	var sensorTypeID int64
	if err := db.Pool.QueryRow(ctx, `
		SELECT sensor_type_id FROM sensor_type WHERE name = 'temperature'
	`).Scan(&sensorTypeID); err != nil {
		t.Fatalf("find temperature sensor_type: %v", err)
	}

	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO sensor (board_id, sensor_type_id, region_id, name, unit, registered_at)
		VALUES ($1, $2, $3, 'temp-1', 'degC', NOW())
		RETURNING sensor_id
	`, boardID, sensorTypeID, regionID).Scan(&sensorID); err != nil {
		t.Fatalf("insert sensor: %v", err)
	}

	return sensorID, regionID
}

// TestGranularityTiersDefinedDirectlyOnHypertableNoDimensionJoin verifies
// the acceptance criterion "Aggregates contain no dimension join — assert
// on the definition": both continuous aggregate views' stored SQL
// definitions select only from sensor_reading, and reference no other
// table (region, plant, sensor, board) by name.
func TestGranularityTiersDefinedDirectlyOnHypertableNoDimensionJoin(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := setUpAllMigrations(ctx, t)

	dimensionTables := []string{"region ", "plant ", "sensor ", "board ", "region\n", "plant\n"}

	for _, view := range []string{"sensor_reading_5m", "sensor_reading_1h"} {
		var def string
		if err := db.Pool.QueryRow(ctx, `
			SELECT view_definition FROM timescaledb_information.continuous_aggregates WHERE view_name = $1
		`, view).Scan(&def); err != nil {
			t.Fatalf("query continuous aggregate view_definition for %s: %v", view, err)
		}

		lowerDef := strings.ToLower(def)
		if strings.Contains(lowerDef, "join") {
			t.Errorf("NFR5: %s definition contains a JOIN, expected none: %s", view, def)
		}
		if !strings.Contains(lowerDef, "sensor_reading") {
			t.Errorf("NFR5: %s definition does not reference sensor_reading directly: %s", view, def)
		}
		for _, tbl := range dimensionTables {
			if strings.Contains(lowerDef, tbl) {
				t.Errorf("NFR5: %s definition references dimension table %q, expected no dimension join: %s", view, tbl, def)
			}
		}
	}
}

// TestVerifyTierPolicyOrderingPassesForConfiguredPolicies verifies that,
// as migration 025 configures the two tiers' refresh policies, the
// ordering-assertion function reports the ordering holds.
func TestVerifyTierPolicyOrderingPassesForConfiguredPolicies(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := setUpAllMigrations(ctx, t)

	var ok bool
	if err := db.Pool.QueryRow(ctx, `SELECT verify_tier_policy_ordering()`).Scan(&ok); err != nil {
		t.Fatalf("NFR5: verify_tier_policy_ordering() raised for the as-configured policies: %v", err)
	}
	if !ok {
		t.Error("expected verify_tier_policy_ordering() to return TRUE for the as-configured policies")
	}
}

// TestVerifyTierPolicyOrderingRaisesWhenRefreshCouldReachDroppedRaw
// verifies the acceptance criterion "A refresh scheduled against a window
// whose raw has been dropped is prevented, not merely unlikely — assert
// the ordering constraint, not the schedule." It does so by mutating the
// live policy configuration to violate the constraint (end_offset past the
// 13-month raw floor) and confirming verify_tier_policy_ordering() raises
// against the *actual* configuration, not a hardcoded assumption that
// would have missed this.
func TestVerifyTierPolicyOrderingRaisesWhenRefreshCouldReachDroppedRaw(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := setUpAllMigrations(ctx, t)

	// Find the refresh job for sensor_reading_5m and push its end_offset
	// past the 13-month raw retention floor -- the exact failure mode the
	// function exists to catch.
	var jobID int64
	if err := db.Pool.QueryRow(ctx, `
		SELECT job_id
		FROM timescaledb_information.jobs
		WHERE proc_name = 'policy_refresh_continuous_aggregate'
		  AND hypertable_name = 'sensor_reading_5m'
	`).Scan(&jobID); err != nil {
		t.Fatalf("find sensor_reading_5m refresh job: %v", err)
	}

	// Merge onto the existing config rather than replacing it wholesale --
	// alter_job requires mat_hypertable_id to remain present.
	if _, err := db.Pool.Exec(ctx, `
		SELECT alter_job($1, config => config || jsonb_build_object('end_offset', '20 months', 'start_offset', '21 months'))
		FROM timescaledb_information.jobs WHERE job_id = $1
	`, jobID); err != nil {
		t.Fatalf("alter_job to a bad end_offset: %v", err)
	}

	_, err := db.Pool.Exec(ctx, `SELECT verify_tier_policy_ordering()`)
	if err == nil {
		t.Fatal("NFR5: expected verify_tier_policy_ordering() to raise once a refresh end_offset exceeds the raw retention floor, got no error")
	}
	if !strings.Contains(err.Error(), "raw retention floor") {
		t.Errorf("expected the raised error to name the raw retention floor as the violated constraint, got: %v", err)
	}
}

// TestEnforceRawRetentionGatedOnCaptureCompletion verifies the acceptance
// criterion "An FR20 boundary capture that has not completed blocks raw
// retention for its chunk": with raw_retention_captures_complete()
// temporarily forced to FALSE, enforce_raw_retention() must not drop an
// old chunk; with it forced back to TRUE, the same call must drop it. This
// exercises the gate itself (the ordering guarantee this migration commits
// to), independent of #1208's real boundary-capture table landing later.
func TestEnforceRawRetentionGatedOnCaptureCompletion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := setUpAllMigrations(ctx, t)
	sensorID, _ := createTestSensor(ctx, t, db, "device-retention-gate")

	oldRecordedAt := time.Now().Add(-14 * 30 * 24 * time.Hour) // ~14 months back, past the 13-month floor
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO sensor_reading (sensor_id, value, valid, uptime_s, recorded_at)
		VALUES ($1, 42.0, TRUE, 0, $2)
	`, sensorID, oldRecordedAt); err != nil {
		t.Fatalf("insert old raw reading: %v", err)
	}

	countRows := func() int {
		t.Helper()
		var n int
		if err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM sensor_reading WHERE sensor_id = $1`, sensorID).Scan(&n); err != nil {
			t.Fatalf("count sensor_reading rows: %v", err)
		}
		return n
	}

	if got := countRows(); got != 1 {
		t.Fatalf("expected 1 seeded raw row before retention runs, got %d", got)
	}

	// Force the FR20 gate closed: captures incomplete for this cutoff.
	if _, err := db.Pool.Exec(ctx, `
		CREATE OR REPLACE FUNCTION raw_retention_captures_complete(cutoff TIMESTAMPTZ)
		RETURNS BOOLEAN AS $$ BEGIN RETURN FALSE; END; $$ LANGUAGE plpgsql;
	`); err != nil {
		t.Fatalf("force raw_retention_captures_complete to FALSE: %v", err)
	}

	if _, err := db.Pool.Exec(ctx, `CALL enforce_raw_retention(0, '{}'::jsonb)`); err != nil {
		t.Fatalf("CALL enforce_raw_retention (gate closed): %v", err)
	}
	if got := countRows(); got != 1 {
		t.Errorf("FR20: expected the old chunk to survive while captures are incomplete, but row count is %d", got)
	}

	// Open the gate: captures now complete for this cutoff.
	if _, err := db.Pool.Exec(ctx, `
		CREATE OR REPLACE FUNCTION raw_retention_captures_complete(cutoff TIMESTAMPTZ)
		RETURNS BOOLEAN AS $$ BEGIN RETURN TRUE; END; $$ LANGUAGE plpgsql;
	`); err != nil {
		t.Fatalf("force raw_retention_captures_complete to TRUE: %v", err)
	}

	if _, err := db.Pool.Exec(ctx, `CALL enforce_raw_retention(0, '{}'::jsonb)`); err != nil {
		t.Fatalf("CALL enforce_raw_retention (gate open): %v", err)
	}
	if got := countRows(); got != 0 {
		t.Errorf("expected the old chunk to be dropped once captures are complete, but row count is %d", got)
	}
}

// TestFiveMinuteTierRetentionPolicyConfigured verifies the acceptance
// criterion "Three tiers exist with the stated retentions" for the
// 5-minute tier specifically: migration 025 configures an actual
// policy_retention job for sensor_reading_5m with drop_after == 90 days,
// not merely a comment claiming so.
func TestFiveMinuteTierRetentionPolicyConfigured(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := setUpAllMigrations(ctx, t)

	var dropAfterEqualsNinetyDays bool
	if err := db.Pool.QueryRow(ctx, `
		SELECT (config->>'drop_after')::INTERVAL = INTERVAL '90 days'
		FROM timescaledb_information.jobs
		WHERE proc_name = 'policy_retention' AND hypertable_name = 'sensor_reading_5m'
	`).Scan(&dropAfterEqualsNinetyDays); err != nil {
		t.Fatalf("A12: expected a policy_retention job configured for sensor_reading_5m, got: %v", err)
	}
	if !dropAfterEqualsNinetyDays {
		t.Error("A12: expected sensor_reading_5m's retention policy drop_after to equal 90 days")
	}
}

// TestHourlyTierHasNoRetentionPolicy verifies the acceptance criterion "No
// tier coarser than hourly exists" and A12's "hourly indefinitely": no
// policy_retention job is configured for sensor_reading_1h at all, so
// nothing ever drops hourly data.
func TestHourlyTierHasNoRetentionPolicy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := setUpAllMigrations(ctx, t)

	var count int
	if err := db.Pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM timescaledb_information.jobs
		WHERE proc_name = 'policy_retention' AND hypertable_name = 'sensor_reading_1h'
	`).Scan(&count); err != nil {
		t.Fatalf("query policy_retention jobs for sensor_reading_1h: %v", err)
	}
	if count != 0 {
		t.Errorf("A12: expected no retention policy on sensor_reading_1h (retained indefinitely), found %d", count)
	}
}

// TestEnforceRawRetentionDoesNotDropWithinFloor verifies A12's "raw at
// least 13 months" as a lower bound, not merely that something old
// eventually gets dropped: with the FR20 gate open, a raw row 12 months
// old (inside the 13-month floor) must survive a call to
// enforce_raw_retention, while a row past the floor is dropped in the same
// call. Without this, a regression that shortened the cutoff (e.g. to 90
// days) would pass every other test in this file.
func TestEnforceRawRetentionDoesNotDropWithinFloor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := setUpAllMigrations(ctx, t)
	sensorID, _ := createTestSensor(ctx, t, db, "device-retention-floor")

	withinFloor := time.Now().Add(-12 * 30 * 24 * time.Hour) // ~12 months back: inside the 13-month floor
	pastFloor := time.Now().Add(-14 * 30 * 24 * time.Hour)   // ~14 months back: past the 13-month floor
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO sensor_reading (sensor_id, value, valid, uptime_s, recorded_at)
		VALUES ($1, 1.0, TRUE, 0, $2), ($1, 2.0, TRUE, 0, $3)
	`, sensorID, withinFloor, pastFloor); err != nil {
		t.Fatalf("insert raw readings: %v", err)
	}

	rowExists := func(value float64) bool {
		t.Helper()
		var n int
		if err := db.Pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM sensor_reading WHERE sensor_id = $1 AND value = $2
		`, sensorID, value).Scan(&n); err != nil {
			t.Fatalf("count sensor_reading rows for value %v: %v", value, err)
		}
		return n > 0
	}

	if _, err := db.Pool.Exec(ctx, `CALL enforce_raw_retention(0, '{}'::jsonb)`); err != nil {
		t.Fatalf("CALL enforce_raw_retention: %v", err)
	}

	if rowExists(2.0) {
		t.Error("A12: expected the row past the 13-month floor (~14 months old) to be dropped, but it survived")
	}
	if !rowExists(1.0) {
		t.Error("A12: expected the row within the 13-month floor (~12 months old) to survive, but it was dropped")
	}
}

// TestHourlyTierMinMaxEqualsRawRestricted verifies the acceptance criterion
// "Min and max at the hourly tier equal the raw-restricted computation":
// after refreshing sensor_reading_1h over a window of raw readings, the
// aggregate's min/max for that sensor's bucket equal MIN/MAX computed
// directly from the underlying valid raw rows.
func TestHourlyTierMinMaxEqualsRawRestricted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := setUpAllMigrations(ctx, t)
	sensorID, _ := createTestSensor(ctx, t, db, "device-hourly-minmax")

	bucketHour := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	values := []float64{18.5, 22.1, 19.9, 15.0, 21.3} // min=15.0, max=22.1
	for i, v := range values {
		if _, err := db.Pool.Exec(ctx, `
			INSERT INTO sensor_reading (sensor_id, value, valid, uptime_s, recorded_at)
			VALUES ($1, $2, TRUE, 0, $3)
		`, sensorID, v, bucketHour.Add(time.Duration(i)*10*time.Minute)); err != nil {
			t.Fatalf("insert raw reading %d: %v", i, err)
		}
	}
	// An invalid reading inside the same bucket must not affect min/max.
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO sensor_reading (sensor_id, value, valid, uptime_s, recorded_at)
		VALUES ($1, 999.0, FALSE, 0, $2)
	`, sensorID, bucketHour.Add(5*time.Minute)); err != nil {
		t.Fatalf("insert invalid raw reading: %v", err)
	}

	if _, err := db.Pool.Exec(ctx, `CALL refresh_continuous_aggregate('sensor_reading_1h', NULL, NULL)`); err != nil {
		t.Fatalf("refresh_continuous_aggregate(sensor_reading_1h): %v", err)
	}

	var aggMin, aggMax float64
	var aggCount int64
	if err := db.Pool.QueryRow(ctx, `
		SELECT min_value, max_value, reading_count
		FROM sensor_reading_1h
		WHERE sensor_id = $1 AND bucket_start = $2
	`, sensorID, bucketHour).Scan(&aggMin, &aggMax, &aggCount); err != nil {
		t.Fatalf("query sensor_reading_1h bucket: %v", err)
	}

	var rawMin, rawMax float64
	var rawCount int64
	if err := db.Pool.QueryRow(ctx, `
		SELECT MIN(value), MAX(value), COUNT(*)
		FROM sensor_reading
		WHERE sensor_id = $1 AND valid = TRUE
		  AND recorded_at >= $2 AND recorded_at < $2 + INTERVAL '1 hour'
	`, sensorID, bucketHour).Scan(&rawMin, &rawMax, &rawCount); err != nil {
		t.Fatalf("query raw-restricted min/max: %v", err)
	}

	if aggMin != rawMin {
		t.Errorf("hourly tier min %v does not equal raw-restricted min %v", aggMin, rawMin)
	}
	if aggMax != rawMax {
		t.Errorf("hourly tier max %v does not equal raw-restricted max %v", aggMax, rawMax)
	}
	if aggCount != rawCount {
		t.Errorf("hourly tier reading_count %d does not equal raw-restricted count %d (invalid rows must be excluded from both)", aggCount, rawCount)
	}
	if aggCount != int64(len(values)) {
		t.Errorf("expected reading_count %d (invalid reading excluded), got %d", len(values), aggCount)
	}
}
