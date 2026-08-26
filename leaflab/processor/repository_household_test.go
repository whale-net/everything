//go:build integration

package main

import (

	"fmt"	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whale-net/everything/libs/go/dbtest"
)

// TestApplyConfigRegions_RejectsRejectedRegion verifies that ApplyConfigRegions
// re-validates regions at apply time and skips+audits entries from foreign
// households, with nothing written for that entry. (FR1.3 apply-time re-validation)
func TestApplyConfigRegions_RejectsRejectedRegion(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires database")
	}

	pg := dbtest.NewPostgres(t, dbtest.Options{
		Schema: leaflabSchema,
	})
	defer pg.Pool.Close()
	ctx := context.Background()

	repo := NewRepository(pg.Pool)

	// Set up two households
	household1ID := int64(1)
	household2ID := int64(2)

	// Create regions for each household
	var region1ID, region999ID int64
	err := pg.Pool.QueryRow(ctx, `
		INSERT INTO region (household_id, name) VALUES ($1, 'region-1')
		RETURNING region_id
	`, household1ID).Scan(&region1ID)
	if err != nil {
		t.Fatalf("create region 1: %v", err)
	}

	// Create a region for household 2 (foreign)
	err = pg.Pool.QueryRow(ctx, `
		INSERT INTO region (household_id, name) VALUES ($1, 'region-999')
		RETURNING region_id
	`, household2ID).Scan(&region999ID)
	if err != nil {
		t.Fatalf("create region 999: %v", err)
	}

	// Create a board in household 1
	var boardID int64
	err = pg.Pool.QueryRow(ctx, `
		INSERT INTO board (device_id, household_id, registered_at, last_seen_at)
		VALUES ('test-device', $1, NOW(), NOW())
		RETURNING board_id
	`, household1ID).Scan(&boardID)
	if err != nil {
		t.Fatalf("create board: %v", err)
	}

	// Create a sensor in this board
	var sensorID int64
	err = pg.Pool.QueryRow(ctx, `
		INSERT INTO sensor (board_id, sensor_type_id, name, unit, region_id)
		VALUES ($1, 1, 'sensor-0', 'unit', NULL)
		RETURNING sensor_id
	`, boardID).Scan(&sensorID)
	if err != nil {
		t.Fatalf("create sensor: %v", err)
	}

	// Push a config that tries to assign the foreign region
	configJSON := []byte(`{
		"device_id": "test-device",
		"sensors": [
			{
				"name": "sensor-0",
				"region_id": ` + jsonInt(region999ID) + `,
				"i2c_address": 64
			}
		]
	}`)

	err = repo.UpsertDeviceConfig(ctx, boardID, 1, configJSON)
	if err != nil {
		t.Fatalf("upsert device config: %v", err)
	}

	// Mark the config as accepted
	err = repo.AckDeviceConfig(ctx, boardID, 1, true, "")
	if err != nil {
		t.Fatalf("ack device config: %v", err)
	}

	// Now apply the config - it should skip the entry because region 999 belongs to household 2
	err = repo.ApplyConfigRegions(ctx, boardID, 1)
	if err != nil {
		t.Fatalf("apply config regions: %v", err)
	}

	// Verify that sensor.region_id was NOT changed (still NULL)
	var currentRegionID *int64
	err = pg.Pool.QueryRow(ctx, `
		SELECT region_id FROM sensor WHERE sensor_id = $1
	`, sensorID).Scan(&currentRegionID)
	if err != nil {
		t.Fatalf("get sensor region: %v", err)
	}
	if currentRegionID != nil {
		t.Errorf("sensor region_id: expected NULL (not written), got %d", *currentRegionID)
	}

	// Verify that an audit record was created for the skip
	var auditCount int64
	err = pg.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM audit_record
		WHERE target_household_id = $1 AND action = 'skip_config_entry'
	`, household1ID).Scan(&auditCount)
	if err != nil {
		t.Fatalf("count audit records: %v", err)
	}
	if auditCount == 0 {
		t.Errorf("audit_record: expected skip record to be created, got none")
	}
}

// TestApplyConfigRegions_StalnessCheck verifies that a payload pushed BEFORE
// the sensor's current region interval opened is skipped, while one pushed AFTER
// is applied. Uses pushed_at (not acked_at) for the comparison. (FR1.3 staleness)
func TestApplyConfigRegions_StalnessCheck(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires database")
	}

	pg := dbtest.NewPostgres(t, dbtest.Options{
		Schema: leaflabSchema,
	})
	defer pg.Pool.Close()
	ctx := context.Background()

	repo := NewRepository(pg.Pool)

	householdID := int64(1)

	// Create a region for this household
	var regionID int64
	err := pg.Pool.QueryRow(ctx, `
		INSERT INTO region (household_id, name) VALUES ($1, 'region-1')
		RETURNING region_id
	`, householdID).Scan(&regionID)
	if err != nil {
		t.Fatalf("create region: %v", err)
	}

	// Create a board in household 1
	var boardID int64
	err = pg.Pool.QueryRow(ctx, `
		INSERT INTO board (device_id, household_id, registered_at, last_seen_at)
		VALUES ('test-device', $1, NOW(), NOW())
		RETURNING board_id
	`, householdID).Scan(&boardID)
	if err != nil {
		t.Fatalf("create board: %v", err)
	}

	// Create a sensor
	var sensorID int64
	nowMinus1Hour := time.Now().Add(-time.Hour)
	err = pg.Pool.QueryRow(ctx, `
		INSERT INTO sensor (board_id, sensor_type_id, name, unit)
		VALUES ($1, 1, 'sensor-0', 'unit')
		RETURNING sensor_id
	`, boardID).Scan(&sensorID)
	if err != nil {
		t.Fatalf("create sensor: %v", err)
	}

	// Give the sensor a region assignment that opened 1 hour ago
	err = pg.Pool.QueryRow(ctx, `
		INSERT INTO sensor_region_history (sensor_id, region_id, valid_from)
		VALUES ($1, $2, $3)
		RETURNING valid_from
	`, sensorID, regionID, nowMinus1Hour).Scan()
	if err != nil {
		t.Fatalf("set initial region history: %v", err)
	}

	// Also update sensor.region_id to match
	_, err = pg.Pool.Exec(ctx, `
		UPDATE sensor SET region_id = $1 WHERE sensor_id = $2
	`, regionID, sensorID)
	if err != nil {
		t.Fatalf("set sensor region: %v", err)
	}

	// Get the current region's valid_from time for reference
	var currentValidFrom time.Time
	err = pg.Pool.QueryRow(ctx, `
		SELECT valid_from FROM sensor_region_history
		WHERE sensor_id = $1 AND valid_to IS NULL
	`, sensorID).Scan(&currentValidFrom)
	if err != nil {
		t.Fatalf("get current region valid_from: %v", err)
	}

	// Scenario A: Push a config BEFORE the current region interval opened
	// This should be skipped because pushed_at < valid_from
	pushTimeBefore := currentValidFrom.Add(-10 * time.Minute)
	configJSONBefore := []byte(`{
		"device_id": "test-device",
		"sensors": [{"name": "sensor-0", "region_id": ` + jsonInt(regionID) + `, "i2c_address": 64}]
	}`)

	_, err = pg.Pool.Exec(ctx, `
		INSERT INTO device_config (board_id, version, config_json, pushed_at)
		VALUES ($1, 1, $2, $3)
	`, boardID, configJSONBefore, pushTimeBefore)
	if err != nil {
		t.Fatalf("insert stale config: %v", err)
	}

	_, err = pg.Pool.Exec(ctx, `
		UPDATE device_config SET accepted = true WHERE board_id = $1 AND version = 1
	`, boardID)
	if err != nil {
		t.Fatalf("ack stale config: %v", err)
	}

	err = repo.ApplyConfigRegions(ctx, boardID, 1)
	if err != nil {
		t.Fatalf("apply stale config: %v", err)
	}

	// Verify sensor region was not changed
	var regionAfterStale *int64
	err = pg.Pool.QueryRow(ctx, `
		SELECT region_id FROM sensor WHERE sensor_id = $1
	`, sensorID).Scan(&regionAfterStale)
	if err != nil {
		t.Fatalf("get sensor region after stale: %v", err)
	}
	if regionAfterStale == nil || *regionAfterStale != regionID {
		t.Errorf("after stale push: sensor region should still be %d, got %v", regionID, regionAfterStale)
	}

	// Verify audit record for staleness skip
	var staleAuditCount int64
	err = pg.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM audit_record
		WHERE config_version = 1 AND reason LIKE '%before%'
	`).Scan(&staleAuditCount)
	if err != nil {
		t.Fatalf("count stale audit records: %v", err)
	}
	if staleAuditCount == 0 {
		t.Errorf("expected staleness skip audit record, got none")
	}

	// Scenario B: Push a config AFTER the current region interval opened
	// This should be applied
	pushTimeAfter := currentValidFrom.Add(10 * time.Minute)
	var newRegionID int64
	err = pg.Pool.QueryRow(ctx, `
		INSERT INTO region (household_id, name) VALUES ($1, 'region-2')
		RETURNING region_id
	`, householdID).Scan(&newRegionID)
	if err != nil {
		t.Fatalf("create new region: %v", err)
	}

	configJSONAfter := []byte(`{
		"device_id": "test-device",
		"sensors": [{"name": "sensor-0", "region_id": ` + jsonInt(newRegionID) + `, "i2c_address": 64}]
	}`)

	_, err = pg.Pool.Exec(ctx, `
		INSERT INTO device_config (board_id, version, config_json, pushed_at)
		VALUES ($1, 2, $2, $3)
	`, boardID, configJSONAfter, pushTimeAfter)
	if err != nil {
		t.Fatalf("insert fresh config: %v", err)
	}

	_, err = pg.Pool.Exec(ctx, `
		UPDATE device_config SET accepted = true WHERE board_id = $1 AND version = 2
	`, boardID)
	if err != nil {
		t.Fatalf("ack fresh config: %v", err)
	}

	err = repo.ApplyConfigRegions(ctx, boardID, 2)
	if err != nil {
		t.Fatalf("apply fresh config: %v", err)
	}

	// Verify sensor region WAS changed to the new region
	var regionAfterFresh *int64
	err = pg.Pool.QueryRow(ctx, `
		SELECT region_id FROM sensor WHERE sensor_id = $1
	`, sensorID).Scan(&regionAfterFresh)
	if err != nil {
		t.Fatalf("get sensor region after fresh: %v", err)
	}
	if regionAfterFresh == nil || *regionAfterFresh != newRegionID {
		t.Errorf("after fresh push: sensor region should be %d, got %v", newRegionID, regionAfterFresh)
	}
}

// TestApplyConfigRegions_NoPartialApplication verifies that if a config
// entry validates at push time but is rejected at apply time (e.g. the region
// was deleted or reassigned), it is skipped (no partial application of valid entries).
func TestApplyConfigRegions_SkipDontPartial(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires database")
	}

	pg := dbtest.NewPostgres(t, dbtest.Options{
		Schema: leaflabSchema,
	})
	defer pg.Pool.Close()
	ctx := context.Background()

	repo := NewRepository(pg.Pool)

	householdID := int64(1)

	// Create two regions
	var region1ID, region2ID int64
	err := pg.Pool.QueryRow(ctx, `
		INSERT INTO region (household_id, name) VALUES ($1, 'region-1')
		RETURNING region_id
	`, householdID).Scan(&region1ID)
	if err != nil {
		t.Fatalf("create region 1: %v", err)
	}

	err = pg.Pool.QueryRow(ctx, `
		INSERT INTO region (household_id, name) VALUES ($1, 'region-2')
		RETURNING region_id
	`, householdID).Scan(&region2ID)
	if err != nil {
		t.Fatalf("create region 2: %v", err)
	}

	// Create a board
	var boardID int64
	err = pg.Pool.QueryRow(ctx, `
		INSERT INTO board (device_id, household_id, registered_at, last_seen_at)
		VALUES ('test-device', $1, NOW(), NOW())
		RETURNING board_id
	`, householdID).Scan(&boardID)
	if err != nil {
		t.Fatalf("create board: %v", err)
	}

	// Create two sensors
	var sensor1ID, sensor2ID int64
	err = pg.Pool.QueryRow(ctx, `
		INSERT INTO sensor (board_id, sensor_type_id, name, unit)
		VALUES ($1, 1, 'sensor-0', 'unit')
		RETURNING sensor_id
	`, boardID).Scan(&sensor1ID)
	if err != nil {
		t.Fatalf("create sensor 1: %v", err)
	}

	err = pg.Pool.QueryRow(ctx, `
		INSERT INTO sensor (board_id, sensor_type_id, name, unit)
		VALUES ($1, 1, 'sensor-1', 'unit')
		RETURNING sensor_id
	`, boardID).Scan(&sensor2ID)
	if err != nil {
		t.Fatalf("create sensor 2: %v", err)
	}

	// Push a config with two valid entries
	configJSON := []byte(`{
		"device_id": "test-device",
		"sensors": [
			{"name": "sensor-0", "region_id": ` + jsonInt(region1ID) + `, "i2c_address": 64},
			{"name": "sensor-1", "region_id": ` + jsonInt(region2ID) + `, "i2c_address": 65}
		]
	}`)

	err = repo.UpsertDeviceConfig(ctx, boardID, 1, configJSON)
	if err != nil {
		t.Fatalf("upsert device config: %v", err)
	}

	err = repo.AckDeviceConfig(ctx, boardID, 1, true, "")
	if err != nil {
		t.Fatalf("ack device config: %v", err)
	}

	// Before apply, change region2's household to a different one (simulating reassignment)
	_, err = pg.Pool.Exec(ctx, `
		UPDATE region SET household_id = 999 WHERE region_id = $1
	`, region2ID)
	if err != nil {
		t.Fatalf("update region household: %v", err)
	}

	// Apply the config - it should process but skip sensor-1 due to household mismatch
	err = repo.ApplyConfigRegions(ctx, boardID, 1)
	if err != nil {
		t.Fatalf("apply config: %v", err)
	}

	// Verify sensor-0 WAS updated (first entry is still valid)
	var region0 *int64
	err = pg.Pool.QueryRow(ctx, `
		SELECT region_id FROM sensor WHERE sensor_id = $1
	`, sensor1ID).Scan(&region0)
	if err != nil {
		t.Fatalf("get sensor 0 region: %v", err)
	}
	if region0 == nil || *region0 != region1ID {
		t.Errorf("sensor-0 region: expected %d, got %v", region1ID, region0)
	}

	// Verify sensor-1 was NOT updated (second entry failed validation)
	var region1 *int64
	err = pg.Pool.QueryRow(ctx, `
		SELECT region_id FROM sensor WHERE sensor_id = $1
	`, sensor2ID).Scan(&region1)
	if err != nil {
		t.Fatalf("get sensor 1 region: %v", err)
	}
	if region1 != nil {
		t.Errorf("sensor-1 region: expected NULL (not written), got %d", *region1)
	}

	// Verify audit record was created for sensor-1 skip
	var auditCount int64
	err = pg.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM audit_record
		WHERE entity_id = $1 AND action = 'skip_config_entry'
	`, sensor2ID).Scan(&auditCount)
	if err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if auditCount == 0 {
		t.Errorf("expected audit record for sensor-1 skip, got none")
	}
}

// leaflabSchema provides a minimal schema for testing.
const leaflabSchema = `
CREATE TABLE IF NOT EXISTS household (
	household_id BIGSERIAL PRIMARY KEY,
	name TEXT NOT NULL UNIQUE
);

CREATE TABLE IF NOT EXISTS region (
	region_id BIGSERIAL PRIMARY KEY,
	household_id BIGINT NOT NULL REFERENCES household(household_id),
	parent_region_id BIGINT REFERENCES region(region_id),
	name TEXT NOT NULL UNIQUE
);

CREATE TABLE IF NOT EXISTS board (
	board_id BIGSERIAL PRIMARY KEY,
	device_id TEXT NOT NULL UNIQUE,
	household_id BIGINT NOT NULL REFERENCES household(household_id),
	registered_at TIMESTAMP NOT NULL,
	last_seen_at TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS sensor_type (
	sensor_type_id BIGSERIAL PRIMARY KEY,
	name TEXT NOT NULL UNIQUE,
	default_unit TEXT
);

CREATE TABLE IF NOT EXISTS sensor (
	sensor_id BIGSERIAL PRIMARY KEY,
	board_id BIGINT NOT NULL REFERENCES board(board_id),
	sensor_type_id BIGINT NOT NULL REFERENCES sensor_type(sensor_type_id),
	name TEXT NOT NULL,
	unit TEXT,
	region_id BIGINT REFERENCES region(region_id),
	i2c_address BIGINT,
	mux_path JSONB,
	UNIQUE(board_id, name)
);

CREATE TABLE IF NOT EXISTS sensor_region_history (
	sensor_id BIGINT NOT NULL REFERENCES sensor(sensor_id),
	region_id BIGINT NOT NULL REFERENCES region(region_id),
	valid_from TIMESTAMP NOT NULL DEFAULT NOW(),
	valid_to TIMESTAMP,
	PRIMARY KEY(sensor_id, valid_from)
);

CREATE TABLE IF NOT EXISTS device_config (
	board_id BIGINT NOT NULL REFERENCES board(board_id),
	version BIGINT NOT NULL,
	config_json JSONB NOT NULL,
	pushed_at TIMESTAMP NOT NULL DEFAULT NOW(),
	accepted BOOLEAN DEFAULT FALSE,
	acked_at TIMESTAMP,
	rejection_reason TEXT,
	PRIMARY KEY(board_id, version)
);

CREATE TABLE IF NOT EXISTS audit_record (
	audit_id BIGSERIAL PRIMARY KEY,
	created_at TIMESTAMP NOT NULL DEFAULT NOW(),
	actor_subject TEXT,
	target_household_id BIGINT REFERENCES household(household_id),
	action TEXT NOT NULL,
	entity_type TEXT,
	entity_id BIGINT,
	config_version BIGINT,
	i2c_address BIGINT,
	mux_path JSONB,
	reason TEXT
);

-- Insert a test household and sensor type if they don't exist
INSERT INTO household (name) VALUES ('test-household') ON CONFLICT DO NOTHING;
INSERT INTO sensor_type (name, default_unit) VALUES ('temperature', '°C') ON CONFLICT DO NOTHING;
`

// jsonInt is a helper to format an int64 as JSON in test fixtures
func jsonInt(i int64) string {
	return fmt.Sprintf("%d", i)
}
