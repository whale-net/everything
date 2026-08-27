//go:build integration

// This file only builds under the "integration" build tag (see
// repository_hw_history_integration_test.go's own doc comment for why).
//
// It proves Repository.CloseRemovedSensorHWHistory (FR82.6) against a real
// Postgres database: given a device_config_removal row (migration 031,
// written by leaflab/api's InsertDeviceConfigNextVersion at push time) for
// an accepted config version, it closes the matching sensor's currently
// open sensor_hw_history interval -- and leaves everything else (other
// sensors, a version with no removals) untouched. Schema is self-contained
// DDL mirroring the real tables' shape as of migrations 007/013/031,
// deliberately not a dependency on leaflab/migrate's migrations (see
// repository_hw_history_integration_test.go's own note on staying
// hermetic).
package main

import (
	"context"
	"testing"

	"github.com/whale-net/everything/libs/go/dbtest"
)

const closeRemovedHWHistorySchema = `
	CREATE TABLE board (
		board_id  BIGSERIAL PRIMARY KEY,
		device_id VARCHAR(64) NOT NULL UNIQUE
	);

	CREATE TABLE sensor_type (
		sensor_type_id BIGSERIAL PRIMARY KEY,
		name           VARCHAR(64) NOT NULL UNIQUE,
		default_unit   VARCHAR(16) NOT NULL
	);

	CREATE TABLE sensor (
		sensor_id      BIGSERIAL PRIMARY KEY,
		board_id       BIGINT NOT NULL REFERENCES board(board_id),
		sensor_type_id BIGINT NOT NULL REFERENCES sensor_type(sensor_type_id),
		name           VARCHAR(128) NOT NULL,
		i2c_address    SMALLINT,
		mux_path       JSONB NOT NULL DEFAULT '[]'::jsonb
	);

	-- Mirrors sensor_hw_history's shape as of migration 013.
	CREATE TABLE sensor_hw_history (
		history_id  BIGSERIAL PRIMARY KEY,
		sensor_id   BIGINT NOT NULL REFERENCES sensor(sensor_id),
		mux_path    JSONB NOT NULL DEFAULT '[]'::jsonb,
		i2c_address SMALLINT,
		valid_from  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		valid_to    TIMESTAMPTZ
	);
	CREATE INDEX idx_sensor_hw_history_current ON sensor_hw_history(sensor_id) WHERE valid_to IS NULL;

	-- Mirrors migration 007's device_config shape.
	CREATE TABLE device_config (
		config_id   BIGSERIAL PRIMARY KEY,
		board_id    BIGINT NOT NULL REFERENCES board(board_id),
		version     BIGINT NOT NULL,
		config_json JSONB NOT NULL,
		UNIQUE (board_id, version)
	);

	-- Mirrors migration 031_device_config_removal.up.sql exactly.
	CREATE TABLE device_config_removal (
		device_config_removal_id BIGSERIAL PRIMARY KEY,
		config_id      BIGINT NOT NULL REFERENCES device_config(config_id) ON DELETE CASCADE,
		i2c_address    SMALLINT NOT NULL,
		mux_path       JSONB NOT NULL DEFAULT '[]'::jsonb,
		sensor_type_id BIGINT NOT NULL REFERENCES sensor_type(sensor_type_id),
		form           TEXT NOT NULL CHECK (form IN ('full_key', 'chip_key'))
	);
	CREATE INDEX idx_device_config_removal_config_id ON device_config_removal(config_id);
`

// closeRemovedHWHistoryFixture seeds a board, a sensor_type and two
// sensors on that board (illuminanceID/temperatureID), each with a single
// open sensor_hw_history interval matching its own row on sensor.
type closeRemovedHWHistoryFixture struct {
	boardID          int64
	illuminanceType  int64
	temperatureType  int64
	lightSensorID    int64
	tempSensorID     int64
}

func seedCloseRemovedHWHistoryFixture(ctx context.Context, t *testing.T, db *dbtest.Postgres) closeRemovedHWHistoryFixture {
	t.Helper()
	var f closeRemovedHWHistoryFixture

	if err := db.Pool.QueryRow(ctx,
		`INSERT INTO board (device_id) VALUES ($1) RETURNING board_id`, "leaflab-close-removed",
	).Scan(&f.boardID); err != nil {
		t.Fatalf("insert board: %v", err)
	}
	if err := db.Pool.QueryRow(ctx,
		`INSERT INTO sensor_type (name, default_unit) VALUES ('illuminance', 'lx') RETURNING sensor_type_id`,
	).Scan(&f.illuminanceType); err != nil {
		t.Fatalf("insert sensor_type illuminance: %v", err)
	}
	if err := db.Pool.QueryRow(ctx,
		`INSERT INTO sensor_type (name, default_unit) VALUES ('temperature', 'degC') RETURNING sensor_type_id`,
	).Scan(&f.temperatureType); err != nil {
		t.Fatalf("insert sensor_type temperature: %v", err)
	}

	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO sensor (board_id, sensor_type_id, name, i2c_address) VALUES ($1, $2, 'light', 35)
		RETURNING sensor_id
	`, f.boardID, f.illuminanceType).Scan(&f.lightSensorID); err != nil {
		t.Fatalf("insert sensor light: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO sensor (board_id, sensor_type_id, name, i2c_address) VALUES ($1, $2, 'temp', 68)
		RETURNING sensor_id
	`, f.boardID, f.temperatureType).Scan(&f.tempSensorID); err != nil {
		t.Fatalf("insert sensor temp: %v", err)
	}

	for _, sensorID := range []int64{f.lightSensorID, f.tempSensorID} {
		if _, err := db.Pool.Exec(ctx, `
			INSERT INTO sensor_hw_history (sensor_id, i2c_address, mux_path)
			SELECT sensor_id, i2c_address, mux_path FROM sensor WHERE sensor_id = $1
		`, sensorID); err != nil {
			t.Fatalf("seed sensor_hw_history for sensor %d: %v", sensorID, err)
		}
	}

	return f
}

func hwHistoryOpen(ctx context.Context, t *testing.T, db *dbtest.Postgres, sensorID int64) bool {
	t.Helper()
	var open bool
	if err := db.Pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM sensor_hw_history WHERE sensor_id = $1 AND valid_to IS NULL)
	`, sensorID).Scan(&open); err != nil {
		t.Fatalf("check open hw history for sensor %d: %v", sensorID, err)
	}
	return open
}

func insertCloseRemovedHWHistoryConfig(ctx context.Context, t *testing.T, db *dbtest.Postgres, boardID, version int64) int64 {
	t.Helper()
	var configID int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO device_config (board_id, version, config_json) VALUES ($1, $2, '{}'::jsonb)
		RETURNING config_id
	`, boardID, version).Scan(&configID); err != nil {
		t.Fatalf("insert device_config board=%d version=%d: %v", boardID, version, err)
	}
	return configID
}

// TestCloseRemovedSensorHWHistory_ClosesOnlyTheRemovedEntry proves FR82.6's
// core contract: a device_config_removal row for an accepted version
// closes exactly the sensor_hw_history interval its canonical hardware key
// resolves to on this board, leaving every other sensor's interval (on the
// same board, and one not named by this removal at all) open.
func TestCloseRemovedSensorHWHistory_ClosesOnlyTheRemovedEntry(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: closeRemovedHWHistorySchema})
	f := seedCloseRemovedHWHistoryFixture(ctx, t, db)
	repo := NewRepository(db.Pool)

	configID := insertCloseRemovedHWHistoryConfig(ctx, t, db, f.boardID, 2)
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO device_config_removal (config_id, i2c_address, mux_path, sensor_type_id, form)
		VALUES ($1, 35, '[]'::jsonb, $2, 'full_key')
	`, configID, f.illuminanceType); err != nil {
		t.Fatalf("insert device_config_removal: %v", err)
	}

	if !hwHistoryOpen(ctx, t, db, f.lightSensorID) || !hwHistoryOpen(ctx, t, db, f.tempSensorID) {
		t.Fatal("fixture setup: both sensors should start with an open hw history interval")
	}

	if err := repo.CloseRemovedSensorHWHistory(ctx, f.boardID, 2); err != nil {
		t.Fatalf("CloseRemovedSensorHWHistory: %v", err)
	}

	if hwHistoryOpen(ctx, t, db, f.lightSensorID) {
		t.Error("removed sensor's hw history interval is still open, want closed")
	}
	if !hwHistoryOpen(ctx, t, db, f.tempSensorID) {
		t.Error("untouched sensor's hw history interval was closed, want still open")
	}
}

// TestCloseRemovedSensorHWHistory_NoRemovalRows_IsANoOp proves a version
// with no device_config_removal rows (a COMPLETE push, or an EDIT push
// with no removes) closes nothing -- FR82.6 only ever applies to entries a
// push's remove list actually dropped.
func TestCloseRemovedSensorHWHistory_NoRemovalRows_IsANoOp(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: closeRemovedHWHistorySchema})
	f := seedCloseRemovedHWHistoryFixture(ctx, t, db)
	repo := NewRepository(db.Pool)

	insertCloseRemovedHWHistoryConfig(ctx, t, db, f.boardID, 3)

	if err := repo.CloseRemovedSensorHWHistory(ctx, f.boardID, 3); err != nil {
		t.Fatalf("CloseRemovedSensorHWHistory: %v", err)
	}

	if !hwHistoryOpen(ctx, t, db, f.lightSensorID) || !hwHistoryOpen(ctx, t, db, f.tempSensorID) {
		t.Error("a version with no device_config_removal rows closed an interval, want both still open")
	}
}

// TestCloseRemovedSensorHWHistory_NoMatchingSensor_IsToleratedNotAnError
// proves a removal naming a canonical hardware key with no matching sensor
// row on this board (should not happen in practice, but the method must
// not fail the whole ack over it) is silently skipped rather than
// returning an error.
func TestCloseRemovedSensorHWHistory_NoMatchingSensor_IsToleratedNotAnError(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: closeRemovedHWHistorySchema})
	f := seedCloseRemovedHWHistoryFixture(ctx, t, db)
	repo := NewRepository(db.Pool)

	configID := insertCloseRemovedHWHistoryConfig(ctx, t, db, f.boardID, 4)
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO device_config_removal (config_id, i2c_address, mux_path, sensor_type_id, form)
		VALUES ($1, 99, '[]'::jsonb, $2, 'full_key')
	`, configID, f.illuminanceType); err != nil {
		t.Fatalf("insert device_config_removal: %v", err)
	}

	if err := repo.CloseRemovedSensorHWHistory(ctx, f.boardID, 4); err != nil {
		t.Fatalf("CloseRemovedSensorHWHistory with an unmatched removal returned an error, want a tolerated no-op: %v", err)
	}

	if !hwHistoryOpen(ctx, t, db, f.lightSensorID) || !hwHistoryOpen(ctx, t, db, f.tempSensorID) {
		t.Error("an unmatched removal closed an unrelated interval, want both still open")
	}
}
