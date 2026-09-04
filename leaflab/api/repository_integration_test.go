//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which builds and runs the whole tree, including on Docker-less
// machines) never even compiles it, let alone runs it. See the go_test
// target's gotags in BUILD.bazel and //libs/go/dbtest's README for how to
// run it.
//
// Schema here is hand-written, self-contained DDL scoped to exactly what
// ListBoardsWithState (#1497) and ListSensorDetailsForBoard/GetBoardIdentity
// (#1498) need (board, sensor_type, sensor, sensor_name_history,
// sensor_reading, plus a v_sensor_current view mirroring the real one's
// name/type resolution) — per dbtest's own convention, it deliberately does
// not depend on leaflab/migrate's real migrations (whose embed.FS lives in
// package main there and isn't importable) and it skips the TimescaleDB
// extension/hypertable call: the queries under test have no
// hypertable-specific behavior, and dbtest's default image
// (postgres:16-alpine) doesn't ship the TimescaleDB extension anyway.
package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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

	-- SCD2 name history (leaflab/migrate/migrations/011_scd2_naming.up.sql:
	-- sensor_label renamed to sensor_name_history). valid_to IS NULL = the
	-- current open row for that sensor.
	CREATE TABLE sensor_name_history (
		sensor_name_history_id BIGSERIAL PRIMARY KEY,
		sensor_id              BIGINT NOT NULL REFERENCES sensor(sensor_id) ON DELETE CASCADE,
		name                   TEXT NOT NULL,
		valid_from             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		valid_to               TIMESTAMPTZ
	);

	-- Mirrors the columns ListSensorDetailsForBoard actually reads off the
	-- real v_sensor_current (leaflab/migrate/migrations/012_views.up.sql):
	-- current name resolved via the sensor_name_history SCD2 join, plus the
	-- sensor_type join. Region/board timestamp columns are intentionally
	-- omitted here — this task's query never reads them.
	CREATE VIEW v_sensor_current AS
	SELECT
		s.sensor_id,
		s.board_id,
		b.device_id,
		snh.name  AS sensor_name,
		s.unit    AS sensor_unit,
		s.sensor_type_id,
		st.name   AS sensor_type_name
	FROM sensor s
	JOIN board b ON b.board_id = s.board_id
	JOIN sensor_type st ON st.sensor_type_id = s.sensor_type_id
	LEFT JOIN sensor_name_history snh
		ON snh.sensor_id = s.sensor_id
		AND snh.valid_to IS NULL;

	-- Ownership shape (leaflab/migrate/migrations/013_ownership.up.sql),
	-- added for GetCurrentBoardOwner coverage: SCD2 board_owner_history,
	-- unowned expressed as the absence of an open (valid_to IS NULL) row,
	-- never as a NULL owner on an open row.
	CREATE TABLE leaflab_user (
		leaflab_user_id BIGSERIAL PRIMARY KEY,
		oidc_sub        TEXT NOT NULL UNIQUE
	);

	CREATE TABLE board_owner_history (
		board_owner_history_id BIGSERIAL   PRIMARY KEY,
		board_id                BIGINT      NOT NULL REFERENCES board(board_id) ON DELETE CASCADE,
		leaflab_user_id         BIGINT      NOT NULL REFERENCES leaflab_user(leaflab_user_id),
		valid_from              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		valid_to                TIMESTAMPTZ
	);

	CREATE UNIQUE INDEX idx_board_owner_history_current
		ON board_owner_history(board_id) WHERE valid_to IS NULL;
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

// seedLeafLabUser inserts a leaflab_user row and returns its ID.
func seedLeafLabUser(t *testing.T, pool *pgxpool.Pool, oidcSub string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO leaflab_user (oidc_sub) VALUES ($1) RETURNING leaflab_user_id`, oidcSub).Scan(&id); err != nil {
		t.Fatalf("seed leaflab_user %s: %v", oidcSub, err)
	}
	return id
}

// openBoardOwnerHistory inserts an open (valid_to IS NULL) board_owner_history
// row for boardID/ownerUserID.
func openBoardOwnerHistory(t *testing.T, pool *pgxpool.Pool, boardID, ownerUserID int64) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO board_owner_history (board_id, leaflab_user_id) VALUES ($1, $2)`,
		boardID, ownerUserID); err != nil {
		t.Fatalf("open board_owner_history for board %d owner %d: %v", boardID, ownerUserID, err)
	}
}

// closeBoardOwnerHistory closes the current open board_owner_history row for
// boardID, mirroring the SCD2 close-and-open write path (AGENTS.md § SCD2).
func closeBoardOwnerHistory(t *testing.T, pool *pgxpool.Pool, boardID int64) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`UPDATE board_owner_history SET valid_to = NOW() WHERE board_id = $1 AND valid_to IS NULL`,
		boardID); err != nil {
		t.Fatalf("close current board_owner_history row for board %d: %v", boardID, err)
	}
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

// seedSensor inserts a sensor row and its initial (open) sensor_name_history
// row, so ListSensorDetailsForBoard's read of v_sensor_current sees a name
// without every caller having to seed the history table separately.
func seedSensor(t *testing.T, pool *pgxpool.Pool, boardID, sensorTypeID int64, name string) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO sensor (board_id, sensor_type_id, name, unit)
		VALUES ($1, $2, $3, 'unit')
		RETURNING sensor_id`, boardID, sensorTypeID, name).Scan(&id); err != nil {
		t.Fatalf("seed sensor %s: %v", name, err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO sensor_name_history (sensor_id, name) VALUES ($1, $2)`, id, name); err != nil {
		t.Fatalf("seed sensor_name_history for sensor %d: %v", id, err)
	}
	return id
}

// renameSensor closes the sensor's current open sensor_name_history row and
// opens a new one with newName — proving v_sensor_current (and therefore
// ListSensorDetailsForBoard) reads the *current* name off the SCD2 join
// rather than a stale sensor.name cache or the sensor's original name.
func renameSensor(t *testing.T, pool *pgxpool.Pool, sensorID int64, newName string) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`UPDATE sensor_name_history SET valid_to = NOW() WHERE sensor_id = $1 AND valid_to IS NULL`, sensorID); err != nil {
		t.Fatalf("close current name history row for sensor %d: %v", sensorID, err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO sensor_name_history (sensor_id, name) VALUES ($1, $2)`, sensorID, newName); err != nil {
		t.Fatalf("open new name history row for sensor %d: %v", sensorID, err)
	}
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

func rowBySensorID(rows []SensorDetailRow, sensorID int64) (SensorDetailRow, bool) {
	for _, r := range rows {
		if r.SensorID == sensorID {
			return r, true
		}
	}
	return SensorDetailRow{}, false
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

// ─── #1498: ListSensorDetailsForBoard / GetBoardIdentity ────────────────────
//
// reportingState's own boundary cases (threshold edges, future timestamps)
// are already covered by TestReportingState in server_test.go and by the
// board-level tests above; these tests exercise the query shape and
// scan/row behavior that's unique to the per-sensor path (v_sensor_current's
// name resolution, the per-sensor LATERAL "latest reading" join, and
// GetBoardIdentity's NotFound signal), not the threshold math itself.

// TestListSensorDetailsForBoard_RecentReadingIsReporting proves a sensor
// whose only reading is 1 minute old surfaces REPORTING with the latest
// reading populated.
func TestListSensorDetailsForBoard_RecentReadingIsReporting(t *testing.T) {
	repo, pool := newTestRepository(t)
	stID := seedSensorType(t, pool, "temperature")
	boardID := seedBoard(t, pool, "board-detail-recent")
	sensorID := seedSensor(t, pool, boardID, stID, "temp-1")
	seedReading(t, pool, sensorID, time.Now().Add(-1*time.Minute), true)

	rows, err := repo.ListSensorDetailsForBoard(context.Background(), boardID)
	if err != nil {
		t.Fatalf("ListSensorDetailsForBoard: %v", err)
	}

	row, ok := rowBySensorID(rows, sensorID)
	if !ok {
		t.Fatalf("expected sensor %d in results, got %+v", sensorID, rows)
	}
	if row.LatestRecordedAt == nil || row.LatestValue == nil || row.LatestValid == nil {
		t.Fatalf("expected a populated latest reading for a sensor with a recent reading, got %+v", row)
	}
	if got := reportingState(row.LatestRecordedAt, time.Now()); got != pb.ReportingState_REPORTING_STATE_REPORTING {
		t.Errorf("expected REPORTING for a 1-minute-old reading, got %v", got)
	}
}

// TestListSensorDetailsForBoard_OldReadingIsStaleButStillPopulated proves a
// sensor whose only reading is 30 minutes old is STALE, and — per FR6/FR7 —
// the latest value is still returned rather than hidden: stale does not
// mean hidden.
func TestListSensorDetailsForBoard_OldReadingIsStaleButStillPopulated(t *testing.T) {
	repo, pool := newTestRepository(t)
	stID := seedSensorType(t, pool, "temperature")
	boardID := seedBoard(t, pool, "board-detail-stale")
	sensorID := seedSensor(t, pool, boardID, stID, "temp-1")
	seedReading(t, pool, sensorID, time.Now().Add(-30*time.Minute), true)

	rows, err := repo.ListSensorDetailsForBoard(context.Background(), boardID)
	if err != nil {
		t.Fatalf("ListSensorDetailsForBoard: %v", err)
	}

	row, ok := rowBySensorID(rows, sensorID)
	if !ok {
		t.Fatalf("expected sensor %d in results, got %+v", sensorID, rows)
	}
	if row.LatestRecordedAt == nil || row.LatestValue == nil {
		t.Fatalf("expected the stale reading to still be returned (not hidden), got %+v", row)
	}
	if got := reportingState(row.LatestRecordedAt, time.Now()); got != pb.ReportingState_REPORTING_STATE_STALE {
		t.Errorf("expected STALE for a 30-minute-old reading, got %v", got)
	}
}

// TestListSensorDetailsForBoard_NoReadingsIsNeverReported proves a sensor
// with zero readings comes back as a normal row — NEVER_REPORTED, no latest
// reading, no error — rather than being omitted or causing a failure.
func TestListSensorDetailsForBoard_NoReadingsIsNeverReported(t *testing.T) {
	repo, pool := newTestRepository(t)
	stID := seedSensorType(t, pool, "temperature")
	boardID := seedBoard(t, pool, "board-detail-no-readings")
	sensorID := seedSensor(t, pool, boardID, stID, "temp-1")

	rows, err := repo.ListSensorDetailsForBoard(context.Background(), boardID)
	if err != nil {
		t.Fatalf("ListSensorDetailsForBoard: %v", err)
	}

	row, ok := rowBySensorID(rows, sensorID)
	if !ok {
		t.Fatalf("expected sensor %d in results, got %+v", sensorID, rows)
	}
	if row.LatestRecordedAt != nil || row.LatestValue != nil || row.LatestValid != nil {
		t.Errorf("expected nil latest reading fields for a sensor with no readings, got %+v", row)
	}
	if got := reportingState(row.LatestRecordedAt, time.Now()); got != pb.ReportingState_REPORTING_STATE_NEVER_REPORTED {
		t.Errorf("expected NEVER_REPORTED for a sensor with no readings, got %v", got)
	}
}

// TestListSensorDetailsForBoard_InvalidRecentReadingIsReportingAndMarkedInvalid
// is the single most important case in #1498's Testing section: a recent
// reading with valid = FALSE must still (a) count toward REPORTING state and
// (b) be returned with LatestValid = false, so the UI can render it marked
// invalid rather than hiding it or skipping the sensor.
//
// Red/green proof (see also this repository's top-level doc comment): with
// ListSensorDetailsForBoard's LATERAL join changed to add `AND sr.valid` (or
// with the handler branching on LatestValid to omit the reading), this test
// goes red — LatestRecordedAt/LatestValue/LatestValid come back nil because
// the only reading this sensor has is invalid, so the "reading returned"
// assertion below fails. That failure is exactly the bug FR7 calls out:
// validity must never be used as a filter on which readings are visible.
func TestListSensorDetailsForBoard_InvalidRecentReadingIsReportingAndMarkedInvalid(t *testing.T) {
	repo, pool := newTestRepository(t)
	stID := seedSensorType(t, pool, "temperature")
	boardID := seedBoard(t, pool, "board-detail-invalid-recent")
	sensorID := seedSensor(t, pool, boardID, stID, "temp-invalid")
	seedReading(t, pool, sensorID, time.Now().Add(-1*time.Minute), false)

	rows, err := repo.ListSensorDetailsForBoard(context.Background(), boardID)
	if err != nil {
		t.Fatalf("ListSensorDetailsForBoard: %v", err)
	}

	row, ok := rowBySensorID(rows, sensorID)
	if !ok {
		t.Fatalf("expected sensor %d in results, got %+v", sensorID, rows)
	}
	if row.LatestRecordedAt == nil || row.LatestValid == nil {
		t.Fatalf("expected the invalid-but-recent reading to still be returned (not filtered out), got %+v", row)
	}
	if *row.LatestValid != false {
		t.Errorf("expected LatestValid = false to be carried through as a display flag, got %v", *row.LatestValid)
	}
	if got := reportingState(row.LatestRecordedAt, time.Now()); got != pb.ReportingState_REPORTING_STATE_REPORTING {
		t.Errorf("expected REPORTING for a recent reading regardless of valid=false, got %v", got)
	}
}

// TestListSensorDetailsForBoard_MixedStatesOneBoard proves a single call
// returns a reporting sensor, a stale sensor, and a never-reported sensor
// together in one response, with no error and no row dropped.
func TestListSensorDetailsForBoard_MixedStatesOneBoard(t *testing.T) {
	repo, pool := newTestRepository(t)
	stID := seedSensorType(t, pool, "temperature")
	boardID := seedBoard(t, pool, "board-detail-mixed")

	reportingSensorID := seedSensor(t, pool, boardID, stID, "reporting-sensor")
	seedReading(t, pool, reportingSensorID, time.Now().Add(-1*time.Minute), true)

	staleSensorID := seedSensor(t, pool, boardID, stID, "stale-sensor")
	seedReading(t, pool, staleSensorID, time.Now().Add(-30*time.Minute), true)

	neverReportedSensorID := seedSensor(t, pool, boardID, stID, "never-reported-sensor")

	rows, err := repo.ListSensorDetailsForBoard(context.Background(), boardID)
	if err != nil {
		t.Fatalf("ListSensorDetailsForBoard: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 sensors on a mixed-state board, got %d: %+v", len(rows), rows)
	}

	now := time.Now()

	reportingRow, ok := rowBySensorID(rows, reportingSensorID)
	if !ok || reportingState(reportingRow.LatestRecordedAt, now) != pb.ReportingState_REPORTING_STATE_REPORTING {
		t.Errorf("expected sensor %d to be REPORTING, got %+v", reportingSensorID, reportingRow)
	}

	staleRow, ok := rowBySensorID(rows, staleSensorID)
	if !ok || reportingState(staleRow.LatestRecordedAt, now) != pb.ReportingState_REPORTING_STATE_STALE {
		t.Errorf("expected sensor %d to be STALE, got %+v", staleSensorID, staleRow)
	}

	neverRow, ok := rowBySensorID(rows, neverReportedSensorID)
	if !ok || reportingState(neverRow.LatestRecordedAt, now) != pb.ReportingState_REPORTING_STATE_NEVER_REPORTED {
		t.Errorf("expected sensor %d to be NEVER_REPORTED, got %+v", neverReportedSensorID, neverRow)
	}
}

// TestListSensorDetailsForBoard_NoSensorsReturnsEmptyList proves a board
// with zero sensors returns an empty (not nil-error, not partial) list.
func TestListSensorDetailsForBoard_NoSensorsReturnsEmptyList(t *testing.T) {
	repo, pool := newTestRepository(t)
	boardID := seedBoard(t, pool, "board-detail-no-sensors")

	rows, err := repo.ListSensorDetailsForBoard(context.Background(), boardID)
	if err != nil {
		t.Fatalf("ListSensorDetailsForBoard: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected an empty sensor list for a board with no sensors, got %+v", rows)
	}
}

// TestListSensorDetailsForBoard_RenamedSensorReturnsCurrentName proves a
// sensor with two sensor_name_history rows (one closed, one open) surfaces
// the current (open) name — i.e. sensor identity really comes from
// v_sensor_current's SCD2 join, not a hand-rolled or stale lookup.
func TestListSensorDetailsForBoard_RenamedSensorReturnsCurrentName(t *testing.T) {
	repo, pool := newTestRepository(t)
	stID := seedSensorType(t, pool, "temperature")
	boardID := seedBoard(t, pool, "board-detail-renamed")
	sensorID := seedSensor(t, pool, boardID, stID, "old-name")
	renameSensor(t, pool, sensorID, "new-name")

	rows, err := repo.ListSensorDetailsForBoard(context.Background(), boardID)
	if err != nil {
		t.Fatalf("ListSensorDetailsForBoard: %v", err)
	}

	row, ok := rowBySensorID(rows, sensorID)
	if !ok {
		t.Fatalf("expected sensor %d in results, got %+v", sensorID, rows)
	}
	if row.SensorName != "new-name" {
		t.Errorf("expected the current name %q from the open sensor_name_history row, got %q", "new-name", row.SensorName)
	}
}

// TestGetBoardIdentity_UnknownBoardReturnsErrNoRows proves the repository
// layer's contract for an unknown board_id: an unwrapped pgx.ErrNoRows that
// the handler maps to codes.NotFound (see LeafLabAPIServer.GetBoardDetail in
// server.go).
func TestGetBoardIdentity_UnknownBoardReturnsErrNoRows(t *testing.T) {
	repo, _ := newTestRepository(t)

	_, err := repo.GetBoardIdentity(context.Background(), 999999)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected pgx.ErrNoRows for an unknown board_id, got %v", err)
	}
}

// ─── #1763: GetCurrentBoardOwner ────────────────────────────────────────────

// TestGetCurrentBoardOwner_NoRowsIsUnowned proves a board with no
// board_owner_history rows at all comes back owned=false, per
// 013_ownership.up.sql's convention that unowned is the absence of an open
// row, never a NULL owner on an open row.
func TestGetCurrentBoardOwner_NoRowsIsUnowned(t *testing.T) {
	repo, pool := newTestRepository(t)
	boardID := seedBoard(t, pool, "board-owner-none")

	_, owned, err := repo.GetCurrentBoardOwner(context.Background(), boardID)
	if err != nil {
		t.Fatalf("GetCurrentBoardOwner: %v", err)
	}
	if owned {
		t.Fatalf("expected owned=false for a board with no board_owner_history rows")
	}
}

// TestGetCurrentBoardOwner_OpenRowReturnsOwner proves the straightforward
// case: one open board_owner_history row resolves to that owner.
func TestGetCurrentBoardOwner_OpenRowReturnsOwner(t *testing.T) {
	repo, pool := newTestRepository(t)
	boardID := seedBoard(t, pool, "board-owner-open")
	ownerID := seedLeafLabUser(t, pool, "sub-owner-open")
	openBoardOwnerHistory(t, pool, boardID, ownerID)

	gotOwnerID, owned, err := repo.GetCurrentBoardOwner(context.Background(), boardID)
	if err != nil {
		t.Fatalf("GetCurrentBoardOwner: %v", err)
	}
	if !owned {
		t.Fatalf("expected owned=true for a board with an open board_owner_history row")
	}
	if gotOwnerID != ownerID {
		t.Errorf("expected owner %d, got %d", ownerID, gotOwnerID)
	}
}

// TestGetCurrentBoardOwner_ClosedThenReopenedReturnsCurrentOwner is the case
// #1763's Testing criteria call out explicitly: a board that was claimed by
// one user, released (closed row), and then claimed by a second user
// (reopened row) must resolve to the *current* open row's owner, not the
// closed/original one, and must use the valid_to IS NULL predicate rather
// than e.g. the most-recently-inserted row or MAX(board_owner_history_id).
// This proves GetCurrentBoardOwner reads the SCD2 "current" slice correctly
// against real Postgres (idx_board_owner_history_current), not just against
// an in-memory fake.
func TestGetCurrentBoardOwner_ClosedThenReopenedReturnsCurrentOwner(t *testing.T) {
	repo, pool := newTestRepository(t)
	boardID := seedBoard(t, pool, "board-owner-reopened")
	firstOwnerID := seedLeafLabUser(t, pool, "sub-owner-first")
	secondOwnerID := seedLeafLabUser(t, pool, "sub-owner-second")

	// Claim by the first owner, then release (close-and-no-open, i.e. an
	// unowned gap) to prove the closed row alone does not still count.
	openBoardOwnerHistory(t, pool, boardID, firstOwnerID)
	closeBoardOwnerHistory(t, pool, boardID)

	if _, owned, err := repo.GetCurrentBoardOwner(context.Background(), boardID); err != nil {
		t.Fatalf("GetCurrentBoardOwner after release: %v", err)
	} else if owned {
		t.Fatalf("expected owned=false immediately after the only board_owner_history row was closed")
	}

	// Re-claim by a second owner: close-then-open per the SCD2 write path
	// (AGENTS.md § SCD2) -- here there's nothing open to close, so this is
	// just the open half, mirroring C25's re-claim of a released board.
	openBoardOwnerHistory(t, pool, boardID, secondOwnerID)

	gotOwnerID, owned, err := repo.GetCurrentBoardOwner(context.Background(), boardID)
	if err != nil {
		t.Fatalf("GetCurrentBoardOwner after re-claim: %v", err)
	}
	if !owned {
		t.Fatalf("expected owned=true after re-claim opened a new board_owner_history row")
	}
	if gotOwnerID != secondOwnerID {
		t.Errorf("expected the reopened row's owner %d (not the closed row's owner %d), got %d",
			secondOwnerID, firstOwnerID, gotOwnerID)
	}
}
