//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never even compiles it.
// See the go_test target's gotags in BUILD.bazel and
// //libs/go/dbtest/README.md for how to run it.
//
// It proves migration 013 (FR16.1, FR16.2, NFR8) against a real Postgres
// database, applying the full leaflab migration chain (the same
// migrations embed.FS main.go uses) up to version 12, seeding fixture data
// that exercises every case FR16.2's backfill has to get right, then
// applying 013 and asserting on the result:
//
//   - an open interval backfills i2c_address from sensor.i2c_address;
//   - a closed interval is left NULL, even when the owning sensor's current
//     i2c_address is non-NULL -- never "not recorded" turned into a
//     fabricated 0;
//   - a genuinely-0x00-addressed sensor's open interval backfills to the
//     literal 0, not NULL -- the sentinel and "absent" are distinguished;
//   - two sensor rows sharing one physical (i2c_address, mux_path) --
//     modelling one SHT3x producing both a temperature and a humidity
//     sensor row -- each get their own interval, independently backfilled;
//   - both NFR6.1 index shapes exist afterwards;
//   - sensor_reading counts and region_id distribution are untouched
//     (NFR8) -- 013 never touches sensor_reading;
//   - .down.sql cleanly reverses the column and the new index, leaving the
//     pre-existing open-interval index alone.
package main

import (
	"context"
	"database/sql"
	"maps"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

// timescaleImage matches leaflab/Tiltfile's local-dev Postgres image --
// migration 001 requires the timescaledb extension, which the default
// dbtest postgres:16-alpine image does not carry.
const timescaleImage = "timescale/timescaledb:latest-pg16"

// openDB opens a *sql.DB against db's isolated dbtest database/role, using
// the pgx stdlib driver Runner requires, and registers it for cleanup.
// Mirrors libs/go/migrate/migrate_integration_test.go's helper of the same
// name.
func openDB(t *testing.T, db *dbtest.Postgres) *sql.DB {
	t.Helper()

	sqlDB, err := sql.Open("pgx", db.ConnString)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	if err := sqlDB.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}

	return sqlDB
}

// fixture holds the sensor_id/history_id rows a test needs to assert
// against, keyed by a short mnemonic.
type fixture struct {
	boardID int64

	openAddrSensorID    int64 // real address 0x23, open interval
	openAddrHistoryID   int64
	closedAddrSensorID  int64 // real address 0x44 now, but the interval under test is closed
	closedAddrHistoryID int64
	zeroAddrSensorID    int64 // real 0x00 address, open interval
	zeroAddrHistoryID   int64

	// One SHT3x behind the same (i2c_address, mux_path): two sensor rows
	// (temperature, humidity), each with its own open interval.
	shtTempSensorID      int64
	shtTempHistoryID     int64
	shtHumiditySensorID  int64
	shtHumidityHistoryID int64
}

// seedFixture inserts, at schema version 12 (pre-013: sensor_hw_history has
// no i2c_address column yet), one board, five sensor rows, and their
// sensor_hw_history intervals covering every case FR16.2 must handle.
func seedFixture(ctx context.Context, t *testing.T, db *dbtest.Postgres) fixture {
	t.Helper()
	var f fixture

	if err := db.Pool.QueryRow(ctx,
		`INSERT INTO board (device_id) VALUES ($1) RETURNING board_id`, "leaflab-fixture",
	).Scan(&f.boardID); err != nil {
		t.Fatalf("insert board: %v", err)
	}

	var tempTypeID, humidityTypeID int64
	if err := db.Pool.QueryRow(ctx, `SELECT sensor_type_id FROM sensor_type WHERE name = 'temperature'`).Scan(&tempTypeID); err != nil {
		t.Fatalf("lookup temperature sensor_type: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT sensor_type_id FROM sensor_type WHERE name = 'humidity'`).Scan(&humidityTypeID); err != nil {
		t.Fatalf("lookup humidity sensor_type: %v", err)
	}

	insertSensor := func(name string, i2cAddress int16, muxPath string) int64 {
		var sensorID int64
		if err := db.Pool.QueryRow(ctx, `
			INSERT INTO sensor (board_id, sensor_type_id, name, unit, i2c_address, mux_path)
			VALUES ($1, $2, $3, 'degC', $4, $5::jsonb)
			RETURNING sensor_id
		`, f.boardID, tempTypeID, name, i2cAddress, muxPath).Scan(&sensorID); err != nil {
			t.Fatalf("insert sensor %q: %v", name, err)
		}
		return sensorID
	}

	insertOpenInterval := func(sensorID int64, muxPath string) int64 {
		var historyID int64
		if err := db.Pool.QueryRow(ctx, `
			INSERT INTO sensor_hw_history (sensor_id, mux_path) VALUES ($1, $2::jsonb)
			RETURNING history_id
		`, sensorID, muxPath).Scan(&historyID); err != nil {
			t.Fatalf("insert open interval for sensor %d: %v", sensorID, err)
		}
		return historyID
	}

	insertClosedInterval := func(sensorID int64, muxPath string) int64 {
		var historyID int64
		if err := db.Pool.QueryRow(ctx, `
			INSERT INTO sensor_hw_history (sensor_id, mux_path, valid_from, valid_to)
			VALUES ($1, $2::jsonb, NOW() - INTERVAL '30 days', NOW() - INTERVAL '1 day')
			RETURNING history_id
		`, sensorID, muxPath).Scan(&historyID); err != nil {
			t.Fatalf("insert closed interval for sensor %d: %v", sensorID, err)
		}
		return historyID
	}

	// Case: open interval, sensor's current address is a real non-zero
	// value (0x23 = 35) -- should backfill to 35.
	f.openAddrSensorID = insertSensor("open-addr", 35, `[]`)
	f.openAddrHistoryID = insertOpenInterval(f.openAddrSensorID, `[]`)

	// Case: closed interval. The owning sensor's *current* address (0x5A =
	// 90 -- deliberately distinct from the SHT3x fixture's 0x44 below, to
	// avoid colliding with idx_sensor_hw_address) is non-NULL, but the
	// closed interval was never recorded and must stay NULL -- proving the
	// backfill keys off valid_to, not off whatever the sensor's address
	// happens to be today.
	f.closedAddrSensorID = insertSensor("closed-addr", 90, `[]`)
	f.closedAddrHistoryID = insertClosedInterval(f.closedAddrSensorID, `[]`)

	// Case: open interval, sensor's real address is the literal 0x00 --
	// the hwkey.AddressOpt "unknown address" sentinel value, but here it is
	// asserted as the sensor's genuine, currently-registered address.
	// Backfill must write literal 0, not leave it NULL as if absent.
	f.zeroAddrSensorID = insertSensor("zero-addr", 0, `[]`)
	f.zeroAddrHistoryID = insertOpenInterval(f.zeroAddrSensorID, `[]`)

	// Case: one SHT3x (i2c_address 0x44, no mux) produces two sensor rows
	// -- temperature and humidity -- sharing the same physical address.
	// Each gets its own sensor_hw_history interval.
	f.shtTempSensorID = insertSensor("sht-temp", 0x44, `[]`)
	f.shtTempHistoryID = insertOpenInterval(f.shtTempSensorID, `[]`)
	var shtHumiditySensorID int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO sensor (board_id, sensor_type_id, name, unit, i2c_address, mux_path)
		VALUES ($1, $2, 'sht-humidity', 'pct', $3, '[]'::jsonb)
		RETURNING sensor_id
	`, f.boardID, humidityTypeID, 0x44).Scan(&shtHumiditySensorID); err != nil {
		t.Fatalf("insert sht-humidity sensor: %v", err)
	}
	f.shtHumiditySensorID = shtHumiditySensorID
	f.shtHumidityHistoryID = insertOpenInterval(f.shtHumiditySensorID, `[]`)

	return f
}

// TestMigration013_I2CAddressBackfill is the FR16.2 backfill contract test:
// open intervals get the sensor's address, closed intervals stay NULL, and
// a genuinely-0x00-addressed sensor's open interval gets the literal 0, not
// NULL.
func TestMigration013_I2CAddressBackfill(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Image: timescaleImage})
	sqlDB := openDB(t, db)

	runner := migrate.NewRunner(sqlDB, migrations, "migrations")
	if err := runner.Migrate(12); err != nil {
		t.Fatalf("migrate to version 12: %v", err)
	}

	f := seedFixture(ctx, t, db)

	// Target version 13 specifically, not Up() -- this test is about
	// migration 013's backfill behavior, not "whatever the latest
	// migration is". Using Up() here would silently start asserting on
	// later migrations' schema too, and break every time a new migration
	// (e.g. 014) is added on top.
	if err := runner.Migrate(13); err != nil {
		t.Fatalf("Migrate(13) (apply 013): %v", err)
	}

	version, dirty, err := runner.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if dirty {
		t.Fatal("expected clean state after Migrate(13), got dirty")
	}
	if version != 13 {
		t.Fatalf("expected version 13 after Migrate(13), got %d", version)
	}

	getAddr := func(historyID int64) *int16 {
		var addr *int16
		if err := db.Pool.QueryRow(ctx,
			`SELECT i2c_address FROM sensor_hw_history WHERE history_id = $1`, historyID,
		).Scan(&addr); err != nil {
			t.Fatalf("select i2c_address for history_id %d: %v", historyID, err)
		}
		return addr
	}

	// Open interval backfills from the sensor's address (35).
	if got := getAddr(f.openAddrHistoryID); got == nil || *got != 35 {
		t.Errorf("open interval i2c_address: want 35, got %v", got)
	}

	// Closed interval must stay NULL, never fabricated from the sensor's
	// current (non-NULL) address.
	if got := getAddr(f.closedAddrHistoryID); got != nil {
		t.Errorf("closed interval i2c_address: want NULL (not recorded, pre-migration), got %d", *got)
	}

	// Genuinely-0x00 open interval backfills to literal 0, not NULL -- the
	// sentinel and "absent" are distinguished.
	if got := getAddr(f.zeroAddrHistoryID); got == nil {
		t.Error("zero-address open interval i2c_address: want literal 0, got NULL")
	} else if *got != 0 {
		t.Errorf("zero-address open interval i2c_address: want 0, got %d", *got)
	}

	// One SHT3x, two sensor rows, two distinct intervals -- both backfilled
	// to the shared physical address (0x44 = 68), independently.
	if got := getAddr(f.shtTempHistoryID); got == nil || *got != 0x44 {
		t.Errorf("sht-temp interval i2c_address: want 0x44, got %v", got)
	}
	if got := getAddr(f.shtHumidityHistoryID); got == nil || *got != 0x44 {
		t.Errorf("sht-humidity interval i2c_address: want 0x44, got %v", got)
	}
	if f.shtTempHistoryID == f.shtHumidityHistoryID {
		t.Fatal("sht-temp and sht-humidity must be distinct sensor_hw_history rows")
	}
	if f.shtTempSensorID == f.shtHumiditySensorID {
		t.Fatal("sht-temp and sht-humidity must be distinct sensor rows (one SHT3x is two sensor rows)")
	}
}

// TestMigration013_IndexesExist asserts both NFR6.1 index shapes exist
// after 013: the pre-existing open-interval partial index
// (idx_sensor_hw_history_current, from migrations 005/011) and the new
// temporal index (idx_sensor_hw_history_temporal) 013 adds.
func TestMigration013_IndexesExist(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Image: timescaleImage})
	sqlDB := openDB(t, db)

	runner := migrate.NewRunner(sqlDB, migrations, "migrations")
	if err := runner.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}

	for _, indexName := range []string{"idx_sensor_hw_history_current", "idx_sensor_hw_history_temporal"} {
		var exists bool
		if err := db.Pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_indexes
				WHERE schemaname = 'public' AND tablename = 'sensor_hw_history' AND indexname = $1
			)
		`, indexName).Scan(&exists); err != nil {
			t.Fatalf("check pg_indexes for %s: %v", indexName, err)
		}
		if !exists {
			t.Errorf("expected index %s to exist on sensor_hw_history after migration 013, it does not", indexName)
		}
	}
}

// TestMigration013_ReadingsUnaffected proves NFR8: 013 never touches
// sensor_reading, so reading counts and region_id distribution recorded
// before 013 must be byte-for-byte identical after it.
func TestMigration013_ReadingsUnaffected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Image: timescaleImage})
	sqlDB := openDB(t, db)

	runner := migrate.NewRunner(sqlDB, migrations, "migrations")
	if err := runner.Migrate(12); err != nil {
		t.Fatalf("migrate to version 12: %v", err)
	}

	var regionID int64
	if err := db.Pool.QueryRow(ctx,
		`INSERT INTO region (name) VALUES ('greenhouse') RETURNING region_id`,
	).Scan(&regionID); err != nil {
		t.Fatalf("insert region: %v", err)
	}
	var boardID int64
	if err := db.Pool.QueryRow(ctx,
		`INSERT INTO board (device_id) VALUES ('leaflab-readings') RETURNING board_id`,
	).Scan(&boardID); err != nil {
		t.Fatalf("insert board: %v", err)
	}
	var sensorTypeID int64
	if err := db.Pool.QueryRow(ctx, `SELECT sensor_type_id FROM sensor_type WHERE name = 'temperature'`).Scan(&sensorTypeID); err != nil {
		t.Fatalf("lookup temperature sensor_type: %v", err)
	}
	var sensorID int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO sensor (board_id, sensor_type_id, region_id, name, unit)
		VALUES ($1, $2, $3, 'temp', 'degC')
		RETURNING sensor_id
	`, boardID, sensorTypeID, regionID).Scan(&sensorID); err != nil {
		t.Fatalf("insert sensor: %v", err)
	}

	for i := 0; i < 5; i++ {
		if _, err := db.Pool.Exec(ctx, `
			INSERT INTO sensor_reading (sensor_id, region_id, value, uptime_s)
			VALUES ($1, $2, $3, $4)
		`, sensorID, regionID, float64(i), 1000+i); err != nil {
			t.Fatalf("insert reading %d: %v", i, err)
		}
	}

	countBefore, distBefore := readingStats(ctx, t, db)

	if err := runner.Up(); err != nil {
		t.Fatalf("Up (apply 013): %v", err)
	}

	countAfter, distAfter := readingStats(ctx, t, db)

	if countBefore != countAfter {
		t.Errorf("sensor_reading count changed by migration 013: before=%d after=%d", countBefore, countAfter)
	}
	if !maps.Equal(distBefore, distAfter) {
		t.Errorf("region_id distribution changed by migration 013: before=%v after=%v", distBefore, distAfter)
	}
}

// readingStats returns the total row count and a stable summary of the
// region_id distribution (region_id -> count) in sensor_reading.
func readingStats(ctx context.Context, t *testing.T, db *dbtest.Postgres) (int, map[int64]int) {
	t.Helper()
	var count int
	if err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM sensor_reading`).Scan(&count); err != nil {
		t.Fatalf("count sensor_reading: %v", err)
	}
	rows, err := db.Pool.Query(ctx, `SELECT region_id, COUNT(*) FROM sensor_reading GROUP BY region_id`)
	if err != nil {
		t.Fatalf("region_id distribution: %v", err)
	}
	defer rows.Close()
	dist := make(map[int64]int)
	for rows.Next() {
		var regionID int64
		var n int
		if err := rows.Scan(&regionID, &n); err != nil {
			t.Fatalf("scan region_id distribution row: %v", err)
		}
		dist[regionID] = n
	}
	return count, dist
}

// TestMigration013_UpDownClean proves .down.sql cleanly reverses 013: the
// i2c_address column and idx_sensor_hw_history_temporal are gone, while the
// pre-existing idx_sensor_hw_history_current (from migrations 005/011) is
// left untouched.
func TestMigration013_UpDownClean(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Image: timescaleImage})
	sqlDB := openDB(t, db)

	runner := migrate.NewRunner(sqlDB, migrations, "migrations")
	if err := runner.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if err := runner.Migrate(12); err != nil {
		t.Fatalf("Migrate(12) (rolls back 013): %v", err)
	}

	version, dirty, err := runner.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if dirty {
		t.Fatal("expected clean state after rolling back 013, got dirty")
	}
	if version != 12 {
		t.Fatalf("expected version 12 after rolling back 013, got %d", version)
	}

	if _, err := db.Pool.Exec(ctx, `SELECT i2c_address FROM sensor_hw_history`); err == nil {
		t.Error("expected sensor_hw_history.i2c_address to no longer exist after rolling back 013")
	}

	var temporalExists, currentExists bool
	if err := db.Pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE tablename = 'sensor_hw_history' AND indexname = 'idx_sensor_hw_history_temporal')
	`).Scan(&temporalExists); err != nil {
		t.Fatalf("check idx_sensor_hw_history_temporal: %v", err)
	}
	if temporalExists {
		t.Error("expected idx_sensor_hw_history_temporal to be dropped after rolling back 013")
	}
	if err := db.Pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE tablename = 'sensor_hw_history' AND indexname = 'idx_sensor_hw_history_current')
	`).Scan(&currentExists); err != nil {
		t.Fatalf("check idx_sensor_hw_history_current: %v", err)
	}
	if !currentExists {
		t.Error("expected pre-existing idx_sensor_hw_history_current to survive rolling back 013")
	}
}
