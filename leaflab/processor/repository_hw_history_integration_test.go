//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never even compiles it.
// See the go_test target's gotags in BUILD.bazel and
// //libs/go/dbtest/README.md for how to run it.
//
// It proves Repository.UpsertSensorHWHistory's SQL against a real Postgres
// database -- not the wiring covered by handler_test.go's stubRepo, but the
// actual open/close-interval logic (FR16.1, FR16.2), including the defect
// this migration closes: before it, an address-only change with an
// unchanged mux_path was silently dropped because i2c_address wasn't part
// of the "did anything change?" check at all. Schema is self-contained DDL
// mirroring sensor_hw_history's shape as of migration 013 (see
// leaflab/migrate/migrations/013_sensor_hw_history_i2c_address.up.sql) --
// deliberately not a dependency on leaflab/migrate's migrations, so this
// test stays hermetic (see dbtest's own doc comment on Options.Schema).
package main

import (
	"context"
	"testing"

	"github.com/whale-net/everything/leaflab/hwkey"
	"github.com/whale-net/everything/libs/go/dbtest"
)

const hwHistorySchema = `
	CREATE TABLE board (
		board_id  BIGSERIAL PRIMARY KEY,
		device_id VARCHAR(64) NOT NULL UNIQUE
	);

	CREATE TABLE sensor (
		sensor_id      BIGSERIAL PRIMARY KEY,
		board_id       BIGINT NOT NULL REFERENCES board(board_id),
		sensor_type_id BIGINT NOT NULL,
		name           VARCHAR(128) NOT NULL,
		unit           VARCHAR(16) NOT NULL
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

	CREATE INDEX idx_sensor_hw_history_current  ON sensor_hw_history(sensor_id) WHERE valid_to IS NULL;
	CREATE INDEX idx_sensor_hw_history_temporal ON sensor_hw_history(sensor_id, valid_from, valid_to);
`

// hwHistoryRow is what the test reads back per sensor_hw_history row.
type hwHistoryRow struct {
	historyID  int64
	muxText    string
	i2cAddress *int16
	open       bool
}

func seedHWHistorySensor(ctx context.Context, t *testing.T, db *dbtest.Postgres) int64 {
	t.Helper()
	var boardID int64
	if err := db.Pool.QueryRow(ctx,
		`INSERT INTO board (device_id) VALUES ($1) RETURNING board_id`, "leaflab-hwhist",
	).Scan(&boardID); err != nil {
		t.Fatalf("insert board: %v", err)
	}
	var sensorID int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO sensor (board_id, sensor_type_id, name, unit)
		VALUES ($1, 1, 'temp', 'degC')
		RETURNING sensor_id
	`, boardID).Scan(&sensorID); err != nil {
		t.Fatalf("insert sensor: %v", err)
	}
	return sensorID
}

func hwHistoryRows(ctx context.Context, t *testing.T, db *dbtest.Postgres, sensorID int64) []hwHistoryRow {
	t.Helper()
	rows, err := db.Pool.Query(ctx, `
		SELECT history_id, mux_path::text, i2c_address, valid_to IS NULL
		FROM sensor_hw_history
		WHERE sensor_id = $1
		ORDER BY history_id
	`, sensorID)
	if err != nil {
		t.Fatalf("query sensor_hw_history for sensor %d: %v", sensorID, err)
	}
	defer rows.Close()

	var out []hwHistoryRow
	for rows.Next() {
		var r hwHistoryRow
		if err := rows.Scan(&r.historyID, &r.muxText, &r.i2cAddress, &r.open); err != nil {
			t.Fatalf("scan sensor_hw_history row: %v", err)
		}
		out = append(out, r)
	}
	return out
}

// TestUpsertSensorHWHistory_AddressOnlyChangeOpensNewInterval is the defect
// this migration closes: before i2c_address was tracked in
// sensor_hw_history at all, an address change with the mux_path unchanged
// had nothing to compare against and was silently dropped -- the open
// interval just sat there under the stale address forever. Now the "did
// anything change?" check also compares i2c_address, so an address-only
// change closes the old interval and opens a new one carrying the new
// address, exactly like a mux_path change always did.
func TestUpsertSensorHWHistory_AddressOnlyChangeOpensNewInterval(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: hwHistorySchema})
	sensorID := seedHWHistorySensor(ctx, t, db)
	repo := NewRepository(db.Pool)

	// Initial address, empty mux_path.
	hw1 := &HardwareAddress{I2CAddress: hwkey.Address(0x23)}
	if err := repo.UpsertSensorHWHistory(ctx, sensorID, hw1); err != nil {
		t.Fatalf("UpsertSensorHWHistory (initial): %v", err)
	}

	rows := hwHistoryRows(ctx, t, db, sensorID)
	if len(rows) != 1 {
		t.Fatalf("after initial insert: expected 1 row, got %d", len(rows))
	}
	if !rows[0].open {
		t.Fatal("after initial insert: expected the row to be open")
	}
	if rows[0].i2cAddress == nil || *rows[0].i2cAddress != 0x23 {
		t.Fatalf("after initial insert: i2c_address = %v, want 0x23", rows[0].i2cAddress)
	}

	// Address-only change: mux_path unchanged (still empty), i2c_address
	// changes from 0x23 to 0x44. Before this migration's fix, this call
	// would have been treated as a no-op (mux_path::text matched) and
	// silently dropped the address change.
	hw2 := &HardwareAddress{I2CAddress: hwkey.Address(0x44)}
	if err := repo.UpsertSensorHWHistory(ctx, sensorID, hw2); err != nil {
		t.Fatalf("UpsertSensorHWHistory (address-only change): %v", err)
	}

	rows = hwHistoryRows(ctx, t, db, sensorID)
	if len(rows) != 2 {
		t.Fatalf("after address-only change: expected 2 rows (old closed + new open), got %d", len(rows))
	}

	old, next := rows[0], rows[1]
	if old.open {
		t.Error("after address-only change: expected the original row to be closed")
	}
	if old.i2cAddress == nil || *old.i2cAddress != 0x23 {
		t.Errorf("after address-only change: original row's i2c_address = %v, want unchanged 0x23", old.i2cAddress)
	}
	if !next.open {
		t.Error("after address-only change: expected a new open row")
	}
	if next.i2cAddress == nil || *next.i2cAddress != 0x44 {
		t.Errorf("after address-only change: new row's i2c_address = %v, want 0x44", next.i2cAddress)
	}
	if old.muxText != next.muxText {
		t.Errorf("mux_path should be unchanged across the address-only change: old=%s new=%s", old.muxText, next.muxText)
	}

	// Calling again with the same (address, mux_path) is a no-op: no third
	// row, the open row stays open.
	if err := repo.UpsertSensorHWHistory(ctx, sensorID, hw2); err != nil {
		t.Fatalf("UpsertSensorHWHistory (repeat, unchanged): %v", err)
	}
	rows = hwHistoryRows(ctx, t, db, sensorID)
	if len(rows) != 2 {
		t.Fatalf("after repeating an unchanged call: expected still 2 rows, got %d", len(rows))
	}
}

// TestUpsertSensorHWHistory_NullVersusZeroAddress proves the repository
// layer distinguishes an absent hardware address (hw == nil, writes NULL)
// from a genuinely-present 0x00 address (hw.I2CAddress = hwkey.Address(0),
// writes the literal 0) -- FR16.2's "never write 0 for absent" contract,
// exercised on the write path rather than the migration backfill.
func TestUpsertSensorHWHistory_NullVersusZeroAddress(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: hwHistorySchema})
	sensorID := seedHWHistorySensor(ctx, t, db)
	repo := NewRepository(db.Pool)

	if err := repo.UpsertSensorHWHistory(ctx, sensorID, nil); err != nil {
		t.Fatalf("UpsertSensorHWHistory (absent): %v", err)
	}
	rows := hwHistoryRows(ctx, t, db, sensorID)
	if len(rows) != 1 {
		t.Fatalf("after absent hw: expected 1 row, got %d", len(rows))
	}
	if rows[0].i2cAddress != nil {
		t.Fatalf("after absent hw: i2c_address = %d, want NULL", *rows[0].i2cAddress)
	}

	// A genuinely-present 0x00 address must open a new interval (this is
	// an address change: absent -> present-0) and must write the literal
	// 0, not leave it NULL as if still absent.
	hw := &HardwareAddress{I2CAddress: hwkey.Address(0)}
	if err := repo.UpsertSensorHWHistory(ctx, sensorID, hw); err != nil {
		t.Fatalf("UpsertSensorHWHistory (present 0x00): %v", err)
	}
	rows = hwHistoryRows(ctx, t, db, sensorID)
	if len(rows) != 2 {
		t.Fatalf("after present-0x00 hw: expected 2 rows, got %d", len(rows))
	}
	last := rows[len(rows)-1]
	if !last.open {
		t.Fatal("after present-0x00 hw: expected a new open row")
	}
	if last.i2cAddress == nil {
		t.Fatal("after present-0x00 hw: i2c_address = NULL, want literal 0")
	}
	if *last.i2cAddress != 0 {
		t.Errorf("after present-0x00 hw: i2c_address = %d, want 0", *last.i2cAddress)
	}
}

// TestUpsertSensorHWHistory_MuxPathOnlyChangeStillOpensNewInterval is a
// regression guard: the address-only fix must not have broken the
// pre-existing mux_path-only change detection.
func TestUpsertSensorHWHistory_MuxPathOnlyChangeStillOpensNewInterval(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: hwHistorySchema})
	sensorID := seedHWHistorySensor(ctx, t, db)
	repo := NewRepository(db.Pool)

	hw1 := &HardwareAddress{I2CAddress: hwkey.Address(0x23), MuxPath: hwkey.MuxPath{{MuxAddress: 0x70, MuxChannel: 1}}}
	if err := repo.UpsertSensorHWHistory(ctx, sensorID, hw1); err != nil {
		t.Fatalf("UpsertSensorHWHistory (initial): %v", err)
	}

	hw2 := &HardwareAddress{I2CAddress: hwkey.Address(0x23), MuxPath: hwkey.MuxPath{{MuxAddress: 0x70, MuxChannel: 2}}}
	if err := repo.UpsertSensorHWHistory(ctx, sensorID, hw2); err != nil {
		t.Fatalf("UpsertSensorHWHistory (mux-only change): %v", err)
	}

	rows := hwHistoryRows(ctx, t, db, sensorID)
	if len(rows) != 2 {
		t.Fatalf("after mux-only change: expected 2 rows, got %d", len(rows))
	}
	if rows[0].open {
		t.Error("after mux-only change: expected the original row to be closed")
	}
	if !rows[1].open {
		t.Error("after mux-only change: expected a new open row")
	}
	if rows[0].i2cAddress == nil || rows[1].i2cAddress == nil || *rows[0].i2cAddress != *rows[1].i2cAddress {
		t.Errorf("i2c_address should be unchanged across the mux-only change: old=%v new=%v", rows[0].i2cAddress, rows[1].i2cAddress)
	}
}
