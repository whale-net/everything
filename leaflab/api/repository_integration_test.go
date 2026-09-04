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
	"sync"
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

	-- corrective_push_attempts/corrective_push_outstanding_version (NFR4,
	-- migration 016) added for RenameSensor's (#1770) atomic counter-reset
	-- coverage below.
	CREATE TABLE sensor (
		sensor_id                           BIGSERIAL PRIMARY KEY,
		board_id                            BIGINT NOT NULL REFERENCES board(board_id) ON DELETE RESTRICT,
		sensor_type_id                      BIGINT NOT NULL REFERENCES sensor_type(sensor_type_id),
		name                                VARCHAR(128) NOT NULL,
		unit                                VARCHAR(16) NOT NULL,
		registered_at                       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		last_seen_at                        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		corrective_push_attempts            INT NOT NULL DEFAULT 0,
		corrective_push_outstanding_version BIGINT,
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

	-- FR10/FR14 role grants (leaflab/migrate/migrations/016_m2_ownership_rename.up.sql),
	-- added for HasRole/GrantRole/RevokeRole coverage.
	CREATE TABLE leaflab_user_role (
		leaflab_user_role_id BIGSERIAL   PRIMARY KEY,
		leaflab_user_id      BIGINT      NOT NULL REFERENCES leaflab_user(leaflab_user_id) ON DELETE CASCADE,
		role                 TEXT        NOT NULL,
		valid_from           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		valid_to             TIMESTAMPTZ
	);

	CREATE UNIQUE INDEX idx_leaflab_user_role_current
		ON leaflab_user_role(leaflab_user_id, role) WHERE valid_to IS NULL;
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

// ─── #1765: ClaimBoard ──────────────────────────────────────────────────────

// countOpenOwnerRows returns the number of open (valid_to IS NULL)
// board_owner_history rows for boardID -- idx_board_owner_history_current's
// partial UNIQUE index guarantees this is 0 or 1 in a consistent database,
// but the race test below asserts the count directly rather than trusting
// that guarantee blindly.
func countOpenOwnerRows(t *testing.T, pool *pgxpool.Pool, boardID int64) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM board_owner_history WHERE board_id = $1 AND valid_to IS NULL`,
		boardID).Scan(&count); err != nil {
		t.Fatalf("count open board_owner_history rows for board %d: %v", boardID, err)
	}
	return count
}

// TestClaimBoard_UnownedBoard_OpensOneRow proves the straightforward case
// against real Postgres: claiming an unowned board opens exactly one open
// board_owner_history row for the claimant.
func TestClaimBoard_UnownedBoard_OpensOneRow(t *testing.T) {
	repo, pool := newTestRepository(t)
	boardID := seedBoard(t, pool, "board-claim-unowned")
	userID := seedLeafLabUser(t, pool, "sub-claim-unowned")

	if err := repo.ClaimBoard(context.Background(), boardID, userID); err != nil {
		t.Fatalf("ClaimBoard: %v", err)
	}

	gotOwnerID, owned, err := repo.GetCurrentBoardOwner(context.Background(), boardID)
	if err != nil {
		t.Fatalf("GetCurrentBoardOwner: %v", err)
	}
	if !owned {
		t.Fatalf("expected owned=true after ClaimBoard")
	}
	if gotOwnerID != userID {
		t.Errorf("expected owner %d, got %d", userID, gotOwnerID)
	}
	if got := countOpenOwnerRows(t, pool, boardID); got != 1 {
		t.Errorf("expected exactly 1 open board_owner_history row, got %d", got)
	}
}

// TestClaimBoard_AlreadyOwned_ErrBoardAlreadyOwned_RecordUntouched proves
// FR2 against real Postgres: a second claim on an already-owned board maps
// the 23505 unique-violation to ErrBoardAlreadyOwned, and the existing open
// row -- same board_owner_history_id, same valid_from -- is left completely
// untouched (not closed, not reassigned).
func TestClaimBoard_AlreadyOwned_ErrBoardAlreadyOwned_RecordUntouched(t *testing.T) {
	repo, pool := newTestRepository(t)
	boardID := seedBoard(t, pool, "board-claim-owned")
	firstOwnerID := seedLeafLabUser(t, pool, "sub-claim-first")
	secondOwnerID := seedLeafLabUser(t, pool, "sub-claim-second")
	openBoardOwnerHistory(t, pool, boardID, firstOwnerID)

	var wantHistoryID int64
	var wantValidFrom time.Time
	if err := pool.QueryRow(context.Background(),
		`SELECT board_owner_history_id, valid_from FROM board_owner_history WHERE board_id = $1 AND valid_to IS NULL`,
		boardID).Scan(&wantHistoryID, &wantValidFrom); err != nil {
		t.Fatalf("read pre-claim ownership record: %v", err)
	}

	err := repo.ClaimBoard(context.Background(), boardID, secondOwnerID)
	if !errors.Is(err, ErrBoardAlreadyOwned) {
		t.Fatalf("expected ErrBoardAlreadyOwned, got %v", err)
	}

	var gotHistoryID int64
	var gotOwnerID int64
	var gotValidFrom time.Time
	var gotValidTo *time.Time
	if err := pool.QueryRow(context.Background(),
		`SELECT board_owner_history_id, leaflab_user_id, valid_from, valid_to FROM board_owner_history WHERE board_id = $1 AND valid_to IS NULL`,
		boardID).Scan(&gotHistoryID, &gotOwnerID, &gotValidFrom, &gotValidTo); err != nil {
		t.Fatalf("read post-claim ownership record: %v", err)
	}
	if gotHistoryID != wantHistoryID {
		t.Errorf("expected the same board_owner_history_id %d, got %d -- a refused claim must not touch the existing row", wantHistoryID, gotHistoryID)
	}
	if !gotValidFrom.Equal(wantValidFrom) {
		t.Errorf("expected valid_from unchanged (%v), got %v", wantValidFrom, gotValidFrom)
	}
	if gotValidTo != nil {
		t.Errorf("expected valid_to still NULL after a refused claim, got %v", gotValidTo)
	}
	if gotOwnerID != firstOwnerID {
		t.Errorf("expected the original owner %d to remain, got %d", firstOwnerID, gotOwnerID)
	}
	if got := countOpenOwnerRows(t, pool, boardID); got != 1 {
		t.Errorf("expected exactly 1 open board_owner_history row after the refused claim, got %d", got)
	}
}

// TestClaimBoard_ConcurrentClaims_ExactlyOneWinner is Testing criterion 7
// (NFR2): two goroutines call Repository.ClaimBoard concurrently for the
// same unowned board. Exactly one must return nil and exactly one must
// return ErrBoardAlreadyOwned, and the database must be left with exactly
// one open board_owner_history row -- never zero (both silently failing),
// never two (both silently winning), regardless of which goroutine the
// database happens to serialize first.
//
// This is what actually proves NFR2: idx_board_owner_history_current (a
// partial UNIQUE index on board_id WHERE valid_to IS NULL) rejects the
// loser's INSERT with a 23505 unique-violation, which ClaimBoard maps to
// ErrBoardAlreadyOwned -- not an application-level "SELECT then INSERT"
// check racing against itself. See repository.go's ClaimBoard doc comment.
//
// Red/green: the issue's Testing section calls for confirming this goes
// red under a read-then-write implementation and green under the atomic
// INSERT. That swap-and-revert was performed by hand against ClaimBoard in
// repository.go (temporarily replacing the plain INSERT with a
// SELECT-for-unowned-then-INSERT sequence) and confirmed this test flakes
// to "2 winners" under load -- not preserved in the committed source, since
// a real defect would otherwise ship gated behind a code path nothing
// exercises by default.
func TestClaimBoard_ConcurrentClaims_ExactlyOneWinner(t *testing.T) {
	repo, pool := newTestRepository(t)
	boardID := seedBoard(t, pool, "board-claim-race")
	firstUserID := seedLeafLabUser(t, pool, "sub-claim-race-1")
	secondUserID := seedLeafLabUser(t, pool, "sub-claim-race-2")

	const attempts = 20
	for i := 0; i < attempts; i++ {
		func() {
			t.Helper()

			var wg sync.WaitGroup
			errs := make([]error, 2)
			userIDs := [2]int64{firstUserID, secondUserID}

			wg.Add(2)
			for g := 0; g < 2; g++ {
				go func(g int) {
					defer wg.Done()
					errs[g] = repo.ClaimBoard(context.Background(), boardID, userIDs[g])
				}(g)
			}
			wg.Wait()

			var nilCount, alreadyOwnedCount int
			for _, err := range errs {
				switch {
				case err == nil:
					nilCount++
				case errors.Is(err, ErrBoardAlreadyOwned):
					alreadyOwnedCount++
				default:
					t.Fatalf("attempt %d: unexpected error from ClaimBoard: %v", i, err)
				}
			}
			if nilCount != 1 || alreadyOwnedCount != 1 {
				t.Fatalf("attempt %d: expected exactly one nil and one ErrBoardAlreadyOwned, got %d nil and %d ErrBoardAlreadyOwned (errs=%v)",
					i, nilCount, alreadyOwnedCount, errs)
			}
			if got := countOpenOwnerRows(t, pool, boardID); got != 1 {
				t.Fatalf("attempt %d: expected exactly 1 open board_owner_history row after the race, got %d", i, got)
			}

			// Reset for the next attempt: close the winning row so the board
			// is unowned again, keeping every attempt an identical race on a
			// freshly-unowned board rather than accumulating history rows
			// that would change which branch (open vs. already-owned) each
			// subsequent attempt's INSERT takes.
			closeBoardOwnerHistory(t, pool, boardID)
		}()
	}
}

// ─── #1775: HasRole / GrantRole / RevokeRole (FR10, FR14) ──────────────────

// countRoleRows returns (total rows, open rows) for leaflabUserID/role in
// leaflab_user_role, so tests can assert the SCD2 shape directly rather than
// re-deriving it through HasRole alone.
func countRoleRows(t *testing.T, pool *pgxpool.Pool, leaflabUserID int64, role string) (total, open int) {
	t.Helper()
	ctx := context.Background()
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM leaflab_user_role WHERE leaflab_user_id = $1 AND role = $2`,
		leaflabUserID, role).Scan(&total); err != nil {
		t.Fatalf("count total role rows: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM leaflab_user_role WHERE leaflab_user_id = $1 AND role = $2 AND valid_to IS NULL`,
		leaflabUserID, role).Scan(&open); err != nil {
		t.Fatalf("count open role rows: %v", err)
	}
	return total, open
}

// TestRevokeRole_ClosesRowThenGrantRole_OpensSecondRow is Testing criterion
// 8: RevokeRole sets valid_to and leaves the row present; a subsequent
// GrantRole opens a second row, so the table shows one closed and one open
// grant.
func TestRevokeRole_ClosesRowThenGrantRole_OpensSecondRow(t *testing.T) {
	repo, pool := newTestRepository(t)
	ctx := context.Background()
	userID := seedLeafLabUser(t, pool, "sub-revoke-then-grant")

	if err := repo.GrantRole(ctx, userID, adminRole); err != nil {
		t.Fatalf("initial GrantRole: %v", err)
	}
	if err := repo.RevokeRole(ctx, userID, adminRole); err != nil {
		t.Fatalf("RevokeRole: %v", err)
	}

	total, open := countRoleRows(t, pool, userID, adminRole)
	if total != 1 || open != 0 {
		t.Fatalf("expected 1 total/0 open rows immediately after revoke, got total=%d open=%d", total, open)
	}

	if err := repo.GrantRole(ctx, userID, adminRole); err != nil {
		t.Fatalf("GrantRole after revoke: %v", err)
	}

	total, open = countRoleRows(t, pool, userID, adminRole)
	if total != 2 {
		t.Fatalf("expected the revoked row to be preserved (2 total rows: one closed, one open), got %d", total)
	}
	if open != 1 {
		t.Fatalf("expected exactly one open row after revoke-then-grant, got %d", open)
	}
}

// TestGrantRevokeGrant_HasRoleTrueOnlyWhileOpen is Testing criterion 9:
// grant → revoke → grant leaves exactly one open grant and two closed rows;
// HasRole is true only while a grant is open.
func TestGrantRevokeGrant_HasRoleTrueOnlyWhileOpen(t *testing.T) {
	repo, pool := newTestRepository(t)
	ctx := context.Background()
	userID := seedLeafLabUser(t, pool, "sub-grant-revoke-grant")

	// Before any grant: HasRole is false.
	has, err := repo.HasRole(ctx, userID, adminRole)
	if err != nil {
		t.Fatalf("HasRole before any grant: %v", err)
	}
	if has {
		t.Fatal("expected HasRole=false before any grant exists")
	}

	// Grant #1.
	if err := repo.GrantRole(ctx, userID, adminRole); err != nil {
		t.Fatalf("GrantRole #1: %v", err)
	}
	has, err = repo.HasRole(ctx, userID, adminRole)
	if err != nil {
		t.Fatalf("HasRole after grant #1: %v", err)
	}
	if !has {
		t.Fatal("expected HasRole=true immediately after GrantRole")
	}

	// Revoke.
	if err := repo.RevokeRole(ctx, userID, adminRole); err != nil {
		t.Fatalf("RevokeRole: %v", err)
	}
	has, err = repo.HasRole(ctx, userID, adminRole)
	if err != nil {
		t.Fatalf("HasRole after revoke: %v", err)
	}
	if has {
		t.Fatal("expected HasRole=false after revoke -- the closed row must not count")
	}

	// Grant #2 (re-grant after revocation).
	if err := repo.GrantRole(ctx, userID, adminRole); err != nil {
		t.Fatalf("GrantRole #2: %v", err)
	}
	has, err = repo.HasRole(ctx, userID, adminRole)
	if err != nil {
		t.Fatalf("HasRole after grant #2: %v", err)
	}
	if !has {
		t.Fatal("expected HasRole=true after re-granting")
	}

	total, open := countRoleRows(t, pool, userID, adminRole)
	if total != 2 {
		t.Fatalf("expected exactly 2 closed rows preserved from the grant/revoke/grant sequence (grant #1's row closed, grant #2's row open), got %d total", total)
	}
	if open != 1 {
		t.Fatalf("expected exactly 1 open grant after grant-revoke-grant, got %d", open)
	}
}

// TestAnyOpenGrantExists_ReflectsGlobalState proves AnyOpenGrantExists
// answers across all users, not just one -- the property the first-sign-in
// bootstrap (leaflab/ui/handlers_auth.go's maybeBootstrapAdmin) depends on.
func TestAnyOpenGrantExists_ReflectsGlobalState(t *testing.T) {
	repo, pool := newTestRepository(t)
	ctx := context.Background()

	exists, err := repo.AnyOpenGrantExists(ctx, adminRole)
	if err != nil {
		t.Fatalf("AnyOpenGrantExists on empty table: %v", err)
	}
	if exists {
		t.Fatal("expected AnyOpenGrantExists=false with zero leaflab_user_role rows")
	}

	userID := seedLeafLabUser(t, pool, "sub-any-open-grant")
	if err := repo.GrantRole(ctx, userID, adminRole); err != nil {
		t.Fatalf("GrantRole: %v", err)
	}

	exists, err = repo.AnyOpenGrantExists(ctx, adminRole)
	if err != nil {
		t.Fatalf("AnyOpenGrantExists after grant: %v", err)
	}
	if !exists {
		t.Fatal("expected AnyOpenGrantExists=true once any user holds an open grant")
	}
}

// ─── #1777: ReassignBoardOwner / ClearBoardOwner (FR12, FR13) ──────────────

// TestReassignBoardOwner_OpensExactlyOneRowForNewOwner_ClosesPrevious is
// Testing criterion 12: after a reassign, board_owner_history has exactly
// one open row for the new owner and the previous row is closed with
// valid_to set -- the old row still exists (never deleted, never UPDATEd
// in place; AGENTS.md § SCD2's close-and-open).
func TestReassignBoardOwner_OpensExactlyOneRowForNewOwner_ClosesPrevious(t *testing.T) {
	repo, pool := newTestRepository(t)
	ctx := context.Background()
	boardID := seedBoard(t, pool, "board-reassign")
	firstOwnerID := seedLeafLabUser(t, pool, "sub-reassign-first")
	secondOwnerID := seedLeafLabUser(t, pool, "sub-reassign-second")
	openBoardOwnerHistory(t, pool, boardID, firstOwnerID)

	if err := repo.ReassignBoardOwner(ctx, boardID, secondOwnerID); err != nil {
		t.Fatalf("ReassignBoardOwner: %v", err)
	}

	// Exactly one open row, and it names the new owner.
	if got := countOpenOwnerRows(t, pool, boardID); got != 1 {
		t.Fatalf("expected exactly 1 open board_owner_history row after reassign, got %d", got)
	}
	gotOwnerID, owned, err := repo.GetCurrentBoardOwner(ctx, boardID)
	if err != nil {
		t.Fatalf("GetCurrentBoardOwner after reassign: %v", err)
	}
	if !owned || gotOwnerID != secondOwnerID {
		t.Fatalf("expected current owner %d after reassign, got owned=%v owner=%d", secondOwnerID, owned, gotOwnerID)
	}

	// The previous row still exists, closed (valid_to set) -- not deleted,
	// not UPDATEd in place to the new owner.
	var (
		totalRows       int
		firstOwnerRows  int
		closedFirstRows int
	)
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM board_owner_history WHERE board_id = $1`, boardID).Scan(&totalRows); err != nil {
		t.Fatalf("count all board_owner_history rows: %v", err)
	}
	if totalRows != 2 {
		t.Fatalf("expected exactly 2 board_owner_history rows total (old closed + new open) after one reassign, got %d", totalRows)
	}
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM board_owner_history WHERE board_id = $1 AND leaflab_user_id = $2`,
		boardID, firstOwnerID).Scan(&firstOwnerRows); err != nil {
		t.Fatalf("count first-owner rows: %v", err)
	}
	if firstOwnerRows != 1 {
		t.Fatalf("expected the first owner's original row to still exist untouched, got %d rows for that owner", firstOwnerRows)
	}
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM board_owner_history WHERE board_id = $1 AND leaflab_user_id = $2 AND valid_to IS NOT NULL`,
		boardID, firstOwnerID).Scan(&closedFirstRows); err != nil {
		t.Fatalf("count closed first-owner rows: %v", err)
	}
	if closedFirstRows != 1 {
		t.Fatalf("expected the first owner's row to be closed (valid_to set), got %d closed rows", closedFirstRows)
	}
}

// TestClearBoardOwner_ClosesRowOpensNone_ThenClaimableByAnyone is Testing
// criterion 13: after a clear, there are zero open rows for that board and
// the previous row is closed; a subsequent ClaimBoard by any signed-in user
// succeeds (FR13 -> FR6 -> FR1 continuity).
func TestClearBoardOwner_ClosesRowOpensNone_ThenClaimableByAnyone(t *testing.T) {
	repo, pool := newTestRepository(t)
	ctx := context.Background()
	boardID := seedBoard(t, pool, "board-clear")
	ownerID := seedLeafLabUser(t, pool, "sub-clear-owner")
	newClaimantID := seedLeafLabUser(t, pool, "sub-clear-claimant")
	openBoardOwnerHistory(t, pool, boardID, ownerID)

	if err := repo.ClearBoardOwner(ctx, boardID); err != nil {
		t.Fatalf("ClearBoardOwner: %v", err)
	}

	if got := countOpenOwnerRows(t, pool, boardID); got != 0 {
		t.Fatalf("expected zero open board_owner_history rows after clear, got %d", got)
	}
	if _, owned, err := repo.GetCurrentBoardOwner(ctx, boardID); err != nil {
		t.Fatalf("GetCurrentBoardOwner after clear: %v", err)
	} else if owned {
		t.Fatal("expected owned=false after clear")
	}

	var closedRows int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM board_owner_history WHERE board_id = $1 AND leaflab_user_id = $2 AND valid_to IS NOT NULL`,
		boardID, ownerID).Scan(&closedRows); err != nil {
		t.Fatalf("count closed rows: %v", err)
	}
	if closedRows != 1 {
		t.Fatalf("expected the previous owner's row to still exist, closed, got %d closed rows", closedRows)
	}

	// FR13 -> FR6 -> FR1 continuity: the board behaves exactly like any
	// other unowned board, claimable by any signed-in user.
	if err := repo.ClaimBoard(ctx, boardID, newClaimantID); err != nil {
		t.Fatalf("ClaimBoard after clear must succeed (FR13 -> FR6 -> FR1 continuity): %v", err)
	}
	gotOwnerID, owned, err := repo.GetCurrentBoardOwner(ctx, boardID)
	if err != nil {
		t.Fatalf("GetCurrentBoardOwner after post-clear claim: %v", err)
	}
	if !owned || gotOwnerID != newClaimantID {
		t.Fatalf("expected the new claimant %d to own the board after a post-clear claim, got owned=%v owner=%d", newClaimantID, owned, gotOwnerID)
	}
}

// TestReassignBoardOwner_IntervalsDoNotOverlap is Testing criterion 14: the
// closed row's valid_to equals or precedes the new row's valid_from --
// close-then-open, never open-then-close, so there is never a moment where
// two owners' rows are simultaneously open (which idx_board_owner_history_
// current's partial UNIQUE index would reject anyway, but this proves the
// timestamps themselves are correctly ordered, not just that the count
// happens to end at one).
func TestReassignBoardOwner_IntervalsDoNotOverlap(t *testing.T) {
	repo, pool := newTestRepository(t)
	ctx := context.Background()
	boardID := seedBoard(t, pool, "board-reassign-intervals")
	firstOwnerID := seedLeafLabUser(t, pool, "sub-intervals-first")
	secondOwnerID := seedLeafLabUser(t, pool, "sub-intervals-second")
	openBoardOwnerHistory(t, pool, boardID, firstOwnerID)

	if err := repo.ReassignBoardOwner(ctx, boardID, secondOwnerID); err != nil {
		t.Fatalf("ReassignBoardOwner: %v", err)
	}

	var closedValidTo, openValidFrom time.Time
	if err := pool.QueryRow(ctx,
		`SELECT valid_to FROM board_owner_history WHERE board_id = $1 AND leaflab_user_id = $2`,
		boardID, firstOwnerID).Scan(&closedValidTo); err != nil {
		t.Fatalf("read closed row's valid_to: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT valid_from FROM board_owner_history WHERE board_id = $1 AND leaflab_user_id = $2`,
		boardID, secondOwnerID).Scan(&openValidFrom); err != nil {
		t.Fatalf("read open row's valid_from: %v", err)
	}
	if closedValidTo.After(openValidFrom) {
		t.Fatalf("expected the closed row's valid_to (%v) to equal or precede the new row's valid_from (%v) -- intervals must not overlap", closedValidTo, openValidFrom)
	}
}

// -- RenameSensor (#1770, FR4) -------------------------------------------

// nameHistoryRow is one sensor_name_history row read back for assertions
// below -- validTo is nil for the currently open row.
type nameHistoryRow struct {
	name      string
	validFrom time.Time
	validTo   *time.Time
}

// nameHistoryRows returns every sensor_name_history row for sensorID,
// ordered oldest first (by valid_from), so a test can assert both the
// count and the sequence.
func nameHistoryRows(t *testing.T, pool *pgxpool.Pool, sensorID int64) []nameHistoryRow {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT name, valid_from, valid_to FROM sensor_name_history WHERE sensor_id = $1 ORDER BY valid_from`, sensorID)
	if err != nil {
		t.Fatalf("query sensor_name_history for sensor %d: %v", sensorID, err)
	}
	defer rows.Close()

	var out []nameHistoryRow
	for rows.Next() {
		var r nameHistoryRow
		if err := rows.Scan(&r.name, &r.validFrom, &r.validTo); err != nil {
			t.Fatalf("scan sensor_name_history row: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate sensor_name_history rows: %v", err)
	}
	return out
}

// seedCorrectivePushState directly sets sensor's NFR4 counter columns,
// standing in for whatever prior corrective-push attempts left the sensor
// in this state -- RenameSensor's job under test is resetting exactly
// these two columns.
func seedCorrectivePushState(t *testing.T, pool *pgxpool.Pool, sensorID int64, attempts int, outstandingVersion *int64) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`UPDATE sensor SET corrective_push_attempts = $2, corrective_push_outstanding_version = $3 WHERE sensor_id = $1`,
		sensorID, attempts, outstandingVersion); err != nil {
		t.Fatalf("seed corrective-push state for sensor %d: %v", sensorID, err)
	}
}

// TestRenameSensor_ClosesOldRowOpensNewWithCurrentName is Testing criterion
// 9: after a rename there is exactly one open sensor_name_history row with
// the new name, and the previously-open row is closed (valid_to set) --
// the old row is never deleted.
func TestRenameSensor_ClosesOldRowOpensNewWithCurrentName(t *testing.T) {
	repo, pool := newTestRepository(t)
	boardID := seedBoard(t, pool, "device-a")
	typeID := seedSensorType(t, pool, "temperature")
	sensorID := seedSensor(t, pool, boardID, typeID, "original-name")

	if err := repo.RenameSensor(context.Background(), sensorID, "renamed"); err != nil {
		t.Fatalf("RenameSensor: %v", err)
	}

	rows := nameHistoryRows(t, pool, sensorID)
	if len(rows) != 2 {
		t.Fatalf("expected 2 sensor_name_history rows (old closed, new open), got %d: %+v", len(rows), rows)
	}
	if rows[0].name != "original-name" || rows[0].validTo == nil {
		t.Errorf("expected the first row to be the closed original-name row, got %+v", rows[0])
	}
	if rows[1].name != "renamed" || rows[1].validTo != nil {
		t.Errorf("expected the second row to be the open renamed row, got %+v", rows[1])
	}

	var sensorName string
	if err := pool.QueryRow(context.Background(), `SELECT name FROM sensor WHERE sensor_id = $1`, sensorID).Scan(&sensorName); err != nil {
		t.Fatalf("select sensor.name: %v", err)
	}
	if sensorName != "renamed" {
		t.Errorf("expected sensor.name to be kept in sync with the new sensor_name_history row, got %q", sensorName)
	}
}

// TestRenameSensor_TwoRenamesSequence_ThreeRowsNonOverlapping is Testing
// criterion 10: two renames in sequence produce three rows total (two
// closed, one open), with each row's [valid_from, valid_to) interval not
// overlapping the next.
func TestRenameSensor_TwoRenamesSequence_ThreeRowsNonOverlapping(t *testing.T) {
	repo, pool := newTestRepository(t)
	boardID := seedBoard(t, pool, "device-a")
	typeID := seedSensorType(t, pool, "temperature")
	sensorID := seedSensor(t, pool, boardID, typeID, "v1")

	if err := repo.RenameSensor(context.Background(), sensorID, "v2"); err != nil {
		t.Fatalf("first RenameSensor: %v", err)
	}
	if err := repo.RenameSensor(context.Background(), sensorID, "v3"); err != nil {
		t.Fatalf("second RenameSensor: %v", err)
	}

	rows := nameHistoryRows(t, pool, sensorID)
	if len(rows) != 3 {
		t.Fatalf("expected 3 sensor_name_history rows after two renames, got %d: %+v", len(rows), rows)
	}
	wantNames := []string{"v1", "v2", "v3"}
	for i, row := range rows {
		if row.name != wantNames[i] {
			t.Errorf("row %d: expected name %q, got %q", i, wantNames[i], row.name)
		}
		isLast := i == len(rows)-1
		if isLast && row.validTo != nil {
			t.Errorf("row %d (%q): expected the current row to be open (valid_to IS NULL), got valid_to=%v", i, row.name, *row.validTo)
		}
		if !isLast {
			if row.validTo == nil {
				t.Fatalf("row %d (%q): expected a closed row (valid_to set), got open", i, row.name)
			}
			// Non-overlapping: this row's close must not be after the next
			// row's open.
			if row.validTo.After(rows[i+1].validFrom) {
				t.Errorf("row %d (%q) closes at %v, after row %d (%q) opens at %v -- intervals overlap",
					i, row.name, *row.validTo, i+1, rows[i+1].name, rows[i+1].validFrom)
			}
		}
	}
}

// TestRenameSensor_ResetsCorrectivePushCountersAtomically is Testing
// criterion 11 (NFR4): a rename resets both corrective_push_attempts to 0
// and corrective_push_outstanding_version to NULL, in the same transaction
// as the name-history close-and-open.
//
// Red/green discipline: temporarily removing the third UPDATE statement
// (the counter reset) from Repository.RenameSensor while running this test
// turns it red on both assertions below (attempts stays 2,
// outstandingVersion stays non-nil); restoring the statement turns it green
// again. Exercised by hand during Testing (see the Testing-phase issue
// comment on #1770), not left as a permanent toggle in test code, since NFR4
// requires the reset to always happen, not to be an optional feature.
func TestRenameSensor_ResetsCorrectivePushCountersAtomically(t *testing.T) {
	repo, pool := newTestRepository(t)
	boardID := seedBoard(t, pool, "device-a")
	typeID := seedSensorType(t, pool, "temperature")
	sensorID := seedSensor(t, pool, boardID, typeID, "original-name")

	outstanding := int64(5)
	seedCorrectivePushState(t, pool, sensorID, 2, &outstanding)

	if err := repo.RenameSensor(context.Background(), sensorID, "renamed"); err != nil {
		t.Fatalf("RenameSensor: %v", err)
	}

	var attempts int
	var outstandingVersion *int64
	if err := pool.QueryRow(context.Background(),
		`SELECT corrective_push_attempts, corrective_push_outstanding_version FROM sensor WHERE sensor_id = $1`, sensorID,
	).Scan(&attempts, &outstandingVersion); err != nil {
		t.Fatalf("select corrective-push columns: %v", err)
	}
	if attempts != 0 {
		t.Errorf("expected corrective_push_attempts reset to 0 by the rename, got %d", attempts)
	}
	if outstandingVersion != nil {
		t.Errorf("expected corrective_push_outstanding_version reset to NULL by the rename, got %d", *outstandingVersion)
	}
}

// TestRenameSensor_SameBoardSameName_ErrSensorNameConflictNoWrite is Testing
// criterion 12's negative half, exercising the one narrow exception FR4
// carves out (see leaflab/DATA.md's "Sensor rename uniqueness" section):
// renaming a sensor to a name another sensor on the *same* board currently
// holds fails with ErrSensorNameConflict (mapped from Postgres' real 23505
// on sensor's UNIQUE(board_id, name)), and neither sensor_name_history nor
// sensor.name nor the NFR4 counters are touched.
func TestRenameSensor_SameBoardSameName_ErrSensorNameConflictNoWrite(t *testing.T) {
	repo, pool := newTestRepository(t)
	boardID := seedBoard(t, pool, "device-a")
	typeID := seedSensorType(t, pool, "temperature")
	sensorAID := seedSensor(t, pool, boardID, typeID, "temp-1")
	sensorBID := seedSensor(t, pool, boardID, typeID, "temp-2")

	outstanding := int64(9)
	seedCorrectivePushState(t, pool, sensorBID, 1, &outstanding)

	err := repo.RenameSensor(context.Background(), sensorBID, "temp-1")
	if !errors.Is(err, ErrSensorNameConflict) {
		t.Fatalf("expected ErrSensorNameConflict renaming sensor B to sensor A's name, got: %v", err)
	}

	// sensorAID must still hold its own name -- it should not have been
	// touched at all by the failed rename attempt on sensorBID.
	rowsA := nameHistoryRows(t, pool, sensorAID)
	if len(rowsA) != 1 || rowsA[0].name != "temp-1" {
		t.Errorf("expected sensor A's history untouched (1 row, temp-1), got %+v", rowsA)
	}

	// sensorBID's own history and sensor.name must be unchanged by the
	// refused rename -- the transaction must have rolled back in full.
	rowsB := nameHistoryRows(t, pool, sensorBID)
	if len(rowsB) != 1 || rowsB[0].name != "temp-2" || rowsB[0].validTo != nil {
		t.Errorf("expected sensor B's history untouched by the refused rename (1 open row, temp-2), got %+v", rowsB)
	}
	var sensorBName string
	if err := pool.QueryRow(context.Background(), `SELECT name FROM sensor WHERE sensor_id = $1`, sensorBID).Scan(&sensorBName); err != nil {
		t.Fatalf("select sensor B name: %v", err)
	}
	if sensorBName != "temp-2" {
		t.Errorf("expected sensor.name untouched by the refused rename, got %q", sensorBName)
	}
	var attempts int
	var outstandingVersion *int64
	if err := pool.QueryRow(context.Background(),
		`SELECT corrective_push_attempts, corrective_push_outstanding_version FROM sensor WHERE sensor_id = $1`, sensorBID,
	).Scan(&attempts, &outstandingVersion); err != nil {
		t.Fatalf("select corrective-push columns: %v", err)
	}
	if attempts != 1 || outstandingVersion == nil || *outstandingVersion != 9 {
		t.Errorf("expected the NFR4 counters untouched by the refused rename (rolled back), got attempts=%d outstandingVersion=%v", attempts, outstandingVersion)
	}
}

// TestRenameSensor_DifferentBoardsCanShareName is Testing criterion 12's
// positive half: FR4's "no uniqueness across boards" holds -- a sensor on
// one board can be renamed to a name another sensor on a *different* board
// already holds.
func TestRenameSensor_DifferentBoardsCanShareName(t *testing.T) {
	repo, pool := newTestRepository(t)
	typeID := seedSensorType(t, pool, "temperature")
	board1ID := seedBoard(t, pool, "device-1")
	board2ID := seedBoard(t, pool, "device-2")
	_ = seedSensor(t, pool, board1ID, typeID, "shared-name")
	sensor2ID := seedSensor(t, pool, board2ID, typeID, "other-name")

	if err := repo.RenameSensor(context.Background(), sensor2ID, "shared-name"); err != nil {
		t.Fatalf("expected renaming across boards to the same name to succeed, got: %v", err)
	}

	rows := nameHistoryRows(t, pool, sensor2ID)
	if len(rows) != 2 || rows[1].name != "shared-name" || rows[1].validTo != nil {
		t.Errorf("expected sensor 2's current name to be shared-name, got %+v", rows)
	}
}

// TestRenameSensor_NameFreedAfterOriginalHolderRenamedAway is Testing
// criterion 12's third documented case (leaflab/DATA.md): a name matching a
// sensor on the same board that was itself renamed away in the same request
// sequence succeeds -- the UNIQUE(board_id, name) constraint only ever
// blocks a collision against a currently-held name, not a historical one.
func TestRenameSensor_NameFreedAfterOriginalHolderRenamedAway(t *testing.T) {
	repo, pool := newTestRepository(t)
	boardID := seedBoard(t, pool, "device-a")
	typeID := seedSensorType(t, pool, "temperature")
	sensorAID := seedSensor(t, pool, boardID, typeID, "name-x")
	sensorBID := seedSensor(t, pool, boardID, typeID, "name-y")

	if err := repo.RenameSensor(context.Background(), sensorAID, "name-z"); err != nil {
		t.Fatalf("expected renaming sensor A away from name-x to succeed, got: %v", err)
	}
	if err := repo.RenameSensor(context.Background(), sensorBID, "name-x"); err != nil {
		t.Fatalf("expected sensor B to be able to take the now-freed name-x, got: %v", err)
	}

	rowsB := nameHistoryRows(t, pool, sensorBID)
	if len(rowsB) != 2 || rowsB[1].name != "name-x" || rowsB[1].validTo != nil {
		t.Errorf("expected sensor B's current name to be name-x, got %+v", rowsB)
	}
}
