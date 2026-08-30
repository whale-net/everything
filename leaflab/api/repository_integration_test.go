//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which builds and runs the whole tree, including on Docker-less
// machines) never even compiles it, let alone runs it. See the go_test
// target's gotags in BUILD.bazel and //libs/go/dbtest's README for how to
// run it.
//
// Schema here is hand-written, self-contained DDL scoped to exactly what
// ListBoardsWithState needs (board, sensor_type, sensor, sensor_reading) —
// per dbtest's own convention, it deliberately does not depend on
// leaflab/migrate's real migrations (whose embed.FS lives in package main
// there and isn't importable) and it skips the TimescaleDB
// extension/hypertable call: the aggregate query under test has no
// hypertable-specific behavior, and dbtest's default image
// (postgres:16-alpine) doesn't ship the TimescaleDB extension anyway.
package main

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/libs/go/dbtest"

	pb "github.com/whale-net/everything/leaflab/api/proto"
)

const testSchema = `
	CREATE TABLE board (
		board_id      BIGSERIAL PRIMARY KEY,
		device_id     VARCHAR(64) NOT NULL UNIQUE,
		registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);

	CREATE TABLE sensor_type (
		sensor_type_id BIGSERIAL PRIMARY KEY,
		name           VARCHAR(64) NOT NULL UNIQUE,
		default_unit   VARCHAR(16) NOT NULL
	);

	CREATE TABLE sensor (
		sensor_id      BIGSERIAL PRIMARY KEY,
		board_id       BIGINT NOT NULL REFERENCES board(board_id) ON DELETE RESTRICT,
		sensor_type_id BIGINT NOT NULL REFERENCES sensor_type(sensor_type_id),
		name           VARCHAR(128) NOT NULL,
		unit           VARCHAR(16) NOT NULL,
		registered_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		last_seen_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		UNIQUE (board_id, name)
	);

	CREATE TABLE sensor_reading (
		reading_id  BIGSERIAL PRIMARY KEY,
		sensor_id   BIGINT NOT NULL REFERENCES sensor(sensor_id) ON DELETE RESTRICT,
		value       DOUBLE PRECISION NOT NULL,
		valid       BOOLEAN NOT NULL DEFAULT TRUE,
		uptime_ms   INTEGER NOT NULL,
		recorded_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);
`

// newTestRepository starts a real Postgres container (via dbtest), applies
// the self-contained schema above, and returns a ready Repository plus the
// raw pool for fixture setup.
func newTestRepository(t *testing.T) (*Repository, *pgxpool.Pool) {
	t.Helper()
	db := dbtest.NewPostgres(context.Background(), t, dbtest.Options{Schema: testSchema})
	return NewRepository(db.Pool), db.Pool
}

func seedBoard(t *testing.T, pool *pgxpool.Pool, deviceID string) int64 {
	t.Helper()
	var boardID int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO board (device_id) VALUES ($1) RETURNING board_id`, deviceID).Scan(&boardID); err != nil {
		t.Fatalf("seed board %s: %v", deviceID, err)
	}
	return boardID
}

func seedSensorType(t *testing.T, pool *pgxpool.Pool, name string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO sensor_type (name, default_unit) VALUES ($1, 'unit') RETURNING sensor_type_id`, name).Scan(&id); err != nil {
		t.Fatalf("seed sensor_type %s: %v", name, err)
	}
	return id
}

func seedSensor(t *testing.T, pool *pgxpool.Pool, boardID, sensorTypeID int64, name string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO sensor (board_id, sensor_type_id, name, unit)
		VALUES ($1, $2, $3, 'unit')
		RETURNING sensor_id`, boardID, sensorTypeID, name).Scan(&id); err != nil {
		t.Fatalf("seed sensor %s: %v", name, err)
	}
	return id
}

// seedReading inserts a sensor_reading row at an explicit recorded_at (so
// tests control recency precisely rather than relying on wall-clock NOW()),
// with an explicit valid flag.
func seedReading(t *testing.T, pool *pgxpool.Pool, sensorID int64, recordedAt time.Time, valid bool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO sensor_reading (sensor_id, value, valid, uptime_ms, recorded_at)
		VALUES ($1, 1.0, $2, 1000, $3)`, sensorID, valid, recordedAt); err != nil {
		t.Fatalf("seed reading for sensor %d: %v", sensorID, err)
	}
}

func rowByBoardID(rows []BoardWithReadingRow, boardID int64) (BoardWithReadingRow, bool) {
	for _, r := range rows {
		if r.BoardID == boardID {
			return r, true
		}
	}
	return BoardWithReadingRow{}, false
}

// TestListBoardsWithState_RecentReadingIsReporting proves a board whose only
// reading is 1 minute old surfaces LastReadingAt within the reporting
// window.
func TestListBoardsWithState_RecentReadingIsReporting(t *testing.T) {
	repo, pool := newTestRepository(t)
	stID := seedSensorType(t, pool, "temperature")
	boardID := seedBoard(t, pool, "board-recent")
	sensorID := seedSensor(t, pool, boardID, stID, "temp")
	seedReading(t, pool, sensorID, time.Now().Add(-1*time.Minute), true)

	rows, err := repo.ListBoardsWithState(context.Background())
	if err != nil {
		t.Fatalf("ListBoardsWithState: %v", err)
	}

	row, ok := rowByBoardID(rows, boardID)
	if !ok {
		t.Fatalf("expected board %d in results, got %+v", boardID, rows)
	}
	if row.LastReadingAt == nil {
		t.Fatal("expected non-nil LastReadingAt for a board with a recent reading")
	}
	if reportingState(row.LastReadingAt, time.Now()) != pb.ReportingState_REPORTING_STATE_REPORTING {
		t.Errorf("expected REPORTING for a 1-minute-old reading, got state derived from %v", *row.LastReadingAt)
	}
}

// TestListBoardsWithState_OldReadingIsStale proves a board whose only
// reading is 30 minutes old is STALE.
func TestListBoardsWithState_OldReadingIsStale(t *testing.T) {
	repo, pool := newTestRepository(t)
	stID := seedSensorType(t, pool, "temperature")
	boardID := seedBoard(t, pool, "board-stale")
	sensorID := seedSensor(t, pool, boardID, stID, "temp")
	seedReading(t, pool, sensorID, time.Now().Add(-30*time.Minute), true)

	rows, err := repo.ListBoardsWithState(context.Background())
	if err != nil {
		t.Fatalf("ListBoardsWithState: %v", err)
	}

	row, ok := rowByBoardID(rows, boardID)
	if !ok {
		t.Fatalf("expected board %d in results, got %+v", boardID, rows)
	}
	if row.LastReadingAt == nil {
		t.Fatal("expected non-nil LastReadingAt for a board with a 30-minute-old reading")
	}
	if got := reportingState(row.LastReadingAt, time.Now()); got != pb.ReportingState_REPORTING_STATE_STALE {
		t.Errorf("expected STALE for a 30-minute-old reading, got %v", got)
	}
}

// TestListBoardsWithState_SensorsButNoReadingsIsNeverReported proves a board
// with sensors but zero readings is NEVER_REPORTED.
func TestListBoardsWithState_SensorsButNoReadingsIsNeverReported(t *testing.T) {
	repo, pool := newTestRepository(t)
	stID := seedSensorType(t, pool, "temperature")
	boardID := seedBoard(t, pool, "board-no-readings")
	seedSensor(t, pool, boardID, stID, "temp")

	rows, err := repo.ListBoardsWithState(context.Background())
	if err != nil {
		t.Fatalf("ListBoardsWithState: %v", err)
	}

	row, ok := rowByBoardID(rows, boardID)
	if !ok {
		t.Fatalf("expected board %d in results, got %+v", boardID, rows)
	}
	if row.LastReadingAt != nil {
		t.Errorf("expected nil LastReadingAt for a board with sensors but no readings, got %v", *row.LastReadingAt)
	}
}

// TestListBoardsWithState_NoSensorsAtAllStillAppearsAsNeverReported proves a
// board with no sensors at all still appears in the list (FR4) and is
// NEVER_REPORTED (not filtered out, not an error).
func TestListBoardsWithState_NoSensorsAtAllStillAppearsAsNeverReported(t *testing.T) {
	repo, pool := newTestRepository(t)
	boardID := seedBoard(t, pool, "board-no-sensors")

	rows, err := repo.ListBoardsWithState(context.Background())
	if err != nil {
		t.Fatalf("ListBoardsWithState: %v", err)
	}

	row, ok := rowByBoardID(rows, boardID)
	if !ok {
		t.Fatalf("expected sensorless board %d to still appear in the list, got %+v", boardID, rows)
	}
	if row.LastReadingAt != nil {
		t.Errorf("expected nil LastReadingAt for a board with no sensors, got %v", *row.LastReadingAt)
	}
}

// TestListBoardsWithState_InvalidReadingStillCountsAsReporting is the case
// #1497 calls out as "most likely to be implemented wrong": a recent
// reading with valid = FALSE must still count toward REPORTING. This
// answers "is data arriving", not "is the data good".
func TestListBoardsWithState_InvalidReadingStillCountsAsReporting(t *testing.T) {
	repo, pool := newTestRepository(t)
	stID := seedSensorType(t, pool, "temperature")
	boardID := seedBoard(t, pool, "board-invalid-recent")
	sensorID := seedSensor(t, pool, boardID, stID, "temp")
	seedReading(t, pool, sensorID, time.Now().Add(-1*time.Minute), false)

	rows, err := repo.ListBoardsWithState(context.Background())
	if err != nil {
		t.Fatalf("ListBoardsWithState: %v", err)
	}

	row, ok := rowByBoardID(rows, boardID)
	if !ok {
		t.Fatalf("expected board %d in results, got %+v", boardID, rows)
	}
	if row.LastReadingAt == nil {
		t.Fatal("expected an invalid-but-recent reading to still populate LastReadingAt")
	}
	if got := reportingState(row.LastReadingAt, time.Now()); got != pb.ReportingState_REPORTING_STATE_REPORTING {
		t.Errorf("expected REPORTING for a recent reading regardless of valid=false, got %v", got)
	}
}

// TestListBoardsWithState_TwoBoardsIndependentStates proves one call returns
// both a reporting board and a stale board, with states resolved
// independently per board.
func TestListBoardsWithState_TwoBoardsIndependentStates(t *testing.T) {
	repo, pool := newTestRepository(t)
	stID := seedSensorType(t, pool, "temperature")

	reportingBoardID := seedBoard(t, pool, "board-both-reporting")
	reportingSensorID := seedSensor(t, pool, reportingBoardID, stID, "temp")
	seedReading(t, pool, reportingSensorID, time.Now().Add(-1*time.Minute), true)

	staleBoardID := seedBoard(t, pool, "board-both-stale")
	staleSensorID := seedSensor(t, pool, staleBoardID, stID, "temp")
	seedReading(t, pool, staleSensorID, time.Now().Add(-30*time.Minute), true)

	rows, err := repo.ListBoardsWithState(context.Background())
	if err != nil {
		t.Fatalf("ListBoardsWithState: %v", err)
	}

	reportingRow, ok := rowByBoardID(rows, reportingBoardID)
	if !ok || reportingRow.LastReadingAt == nil {
		t.Fatalf("expected reporting board %d with a non-nil LastReadingAt, got %+v", reportingBoardID, rows)
	}
	if got := reportingState(reportingRow.LastReadingAt, time.Now()); got != pb.ReportingState_REPORTING_STATE_REPORTING {
		t.Errorf("expected REPORTING for board %d, got %v", reportingBoardID, got)
	}

	staleRow, ok := rowByBoardID(rows, staleBoardID)
	if !ok || staleRow.LastReadingAt == nil {
		t.Fatalf("expected stale board %d with a non-nil LastReadingAt, got %+v", staleBoardID, rows)
	}
	if got := reportingState(staleRow.LastReadingAt, time.Now()); got != pb.ReportingState_REPORTING_STATE_STALE {
		t.Errorf("expected STALE for board %d, got %v", staleBoardID, got)
	}
}
