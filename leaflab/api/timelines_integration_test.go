//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never even compiles it.
// See the go_test target's gotags in BUILD.bazel and
// //libs/go/dbtest/README.md for how to run it.
//
// It proves FR53's GetSensorTimelines against a real Postgres
// sensor/sensor_name_history/sensor_hw_history/sensor_region_history
// schema: the three timelines return independently and each paginates on
// its own, their intervals stay aligned on one time axis, a closed
// pre-migration-013 hardware interval renders its address as absent (never
// 0) while an open interval renders a genuine 0x00, two sensor rows for
// one physical SHT3x chip produce two distinct hardware timelines, an
// empty region timeline is not an error, and a nonexistent sensor_id
// renders NFR2's not-found. Schema is self-contained hand-written DDL
// mirroring migrations 001/005/009/011/013/014 (see dbtest's own doc
// comment on Options.Schema) -- this file is its own go_test target
// (timelines_integration_test in BUILD.bazel), compiled as a separate test
// binary from the package's other integration test files, so
// discardLogger/insertBoard are defined locally here rather than shared
// (same intentional duplication as identity_integration_test.go; see that
// file's doc comment).
package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	firmwarepb "github.com/whale-net/everything/firmware/proto"
	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/libs/go/dbtest"
)

// discardLogger is a *slog.Logger that throws away everything it's given.
// Duplicated from this package's other integration test files: see this
// file's doc comment for why (separate go_test target/binary).
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func insertBoard(t *testing.T, pool *pgxpool.Pool, deviceID string) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(),
		`INSERT INTO board (device_id) VALUES ($1) RETURNING board_id`, deviceID).Scan(&id)
	if err != nil {
		t.Fatalf("insert board %s: %v", deviceID, err)
	}
	return id
}

// timelinesSchema mirrors migrations 001 + 005/009/011 (SCD2 rename) +
// 013 (sensor_hw_history.i2c_address) + 014 (temporal indexes) as they
// apply to the four tables GetSensorTimelines reads: sensor, sensor_type,
// sensor_name_history, sensor_hw_history, sensor_region_history, plus
// region and board as their referenced parents.
const timelinesSchema = `
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

	CREATE TABLE region (
		region_id        BIGSERIAL PRIMARY KEY,
		parent_region_id BIGINT REFERENCES region(region_id) ON DELETE RESTRICT,
		name             VARCHAR(255) NOT NULL,
		description      TEXT,
		created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);

	CREATE TABLE sensor (
		sensor_id      BIGSERIAL PRIMARY KEY,
		board_id       BIGINT NOT NULL REFERENCES board(board_id),
		sensor_type_id BIGINT NOT NULL REFERENCES sensor_type(sensor_type_id),
		region_id      BIGINT REFERENCES region(region_id),
		name           VARCHAR(128) NOT NULL,
		unit           VARCHAR(16) NOT NULL,
		i2c_address    SMALLINT,
		mux_path       JSONB NOT NULL DEFAULT '[]'::jsonb,
		registered_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		UNIQUE (board_id, name)
	);

	CREATE TABLE sensor_name_history (
		sensor_name_history_id BIGSERIAL PRIMARY KEY,
		sensor_id  BIGINT NOT NULL REFERENCES sensor(sensor_id),
		name       TEXT NOT NULL,
		valid_from TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		valid_to   TIMESTAMPTZ
	);
	CREATE INDEX idx_sensor_name_history_current  ON sensor_name_history(sensor_id) WHERE valid_to IS NULL;
	CREATE INDEX idx_sensor_name_history_temporal ON sensor_name_history(sensor_id, valid_from, valid_to);

	-- Mirrors sensor_hw_history's shape post-migration-013: i2c_address is
	-- nullable (NULL = "not recorded", never 0 -- FR16.2).
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

	CREATE TABLE sensor_region_history (
		history_id BIGSERIAL PRIMARY KEY,
		sensor_id  BIGINT NOT NULL REFERENCES sensor(sensor_id),
		region_id  BIGINT NOT NULL REFERENCES region(region_id),
		valid_from TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		valid_to   TIMESTAMPTZ
	);
	CREATE INDEX idx_sensor_region_history_current  ON sensor_region_history(sensor_id) WHERE valid_to IS NULL;
	CREATE INDEX idx_sensor_region_history_temporal ON sensor_region_history(sensor_id, valid_from, valid_to);
`

// newTimelinesTestServer starts a real Postgres container, applies
// timelinesSchema, and returns a LeafLabAPIServer backed by a real
// Repository plus the raw pool for fixture setup. publisher is nil: no
// test in this file exercises a publish path.
func newTimelinesTestServer(t *testing.T) (*LeafLabAPIServer, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: timelinesSchema})
	repo := NewRepository(db.Pool)
	return NewLeafLabAPIServer(repo, nil, nil, nil, nil, nil, discardLogger()), db.Pool
}

func insertSensorType(t *testing.T, pool *pgxpool.Pool, name, unit string) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(),
		`INSERT INTO sensor_type (name, default_unit) VALUES ($1, $2) RETURNING sensor_type_id`,
		name, unit,
	).Scan(&id)
	if err != nil {
		t.Fatalf("insert sensor_type %s: %v", name, err)
	}
	return id
}

func insertRegion(t *testing.T, pool *pgxpool.Pool, name string) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(),
		`INSERT INTO region (name) VALUES ($1) RETURNING region_id`, name,
	).Scan(&id)
	if err != nil {
		t.Fatalf("insert region %s: %v", name, err)
	}
	return id
}

// insertSensor seeds a sensor row. i2cAddr == nil means no known hardware
// address (mux_path stays the column default, "[]").
func insertSensor(t *testing.T, pool *pgxpool.Pool, boardID, sensorTypeID int64, name, unit string, i2cAddr *int32) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(), `
		INSERT INTO sensor (board_id, sensor_type_id, name, unit, i2c_address)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING sensor_id
	`, boardID, sensorTypeID, name, unit, i2cAddr).Scan(&id)
	if err != nil {
		t.Fatalf("insert sensor %s: %v", name, err)
	}
	return id
}

// insertNameInterval inserts one sensor_name_history row with explicit
// valid_from/valid_to, so tests can construct a specific timeline shape
// (e.g. a rename at a known instant) rather than relying on NOW().
func insertNameInterval(t *testing.T, pool *pgxpool.Pool, sensorID int64, name string, validFrom time.Time, validTo *time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO sensor_name_history (sensor_id, name, valid_from, valid_to) VALUES ($1, $2, $3, $4)
	`, sensorID, name, validFrom, validTo); err != nil {
		t.Fatalf("insert sensor_name_history for sensor %d: %v", sensorID, err)
	}
}

// insertHWInterval inserts one sensor_hw_history row with explicit
// valid_from/valid_to and an optional i2c_address (nil = not recorded,
// distinct from a present 0 -- FR16.2).
func insertHWInterval(t *testing.T, pool *pgxpool.Pool, sensorID int64, i2cAddr *int32, validFrom time.Time, validTo *time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO sensor_hw_history (sensor_id, i2c_address, valid_from, valid_to) VALUES ($1, $2, $3, $4)
	`, sensorID, i2cAddr, validFrom, validTo); err != nil {
		t.Fatalf("insert sensor_hw_history for sensor %d: %v", sensorID, err)
	}
}

func insertRegionInterval(t *testing.T, pool *pgxpool.Pool, sensorID, regionID int64, validFrom time.Time, validTo *time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO sensor_region_history (sensor_id, region_id, valid_from, valid_to) VALUES ($1, $2, $3, $4)
	`, sensorID, regionID, validFrom, validTo); err != nil {
		t.Fatalf("insert sensor_region_history for sensor %d: %v", sensorID, err)
	}
}

func timePtr(t time.Time) *time.Time { return &t }
func addr32(v int32) *int32          { return &v }

// TestGetSensorTimelines_ThreeIndependentTimelines_EachPaginates covers the
// Testing-phase criterion "three timelines return independently; each
// paginates": a sensor with multiple name, hardware, and region intervals
// requested with page_size=1 on each returns exactly one interval per
// timeline plus a non-empty next_page_token for each -- proving the three
// page cursors are tracked and returned independently, not as one combined
// cursor.
func TestGetSensorTimelines_ThreeIndependentTimelines_EachPaginates(t *testing.T) {
	server, pool := newTimelinesTestServer(t)
	ctx := context.Background()

	boardID := insertBoard(t, pool, "leaflab-tl-paging")
	typeID := insertSensorType(t, pool, "temperature", "degC")
	regionA := insertRegion(t, pool, "room-a")
	regionB := insertRegion(t, pool, "room-b")
	sensorID := insertSensor(t, pool, boardID, typeID, "temp", "degC", addr32(0x23))

	base := time.Now().Add(-24 * time.Hour).Truncate(time.Millisecond)
	insertNameInterval(t, pool, sensorID, "temp-old", base, timePtr(base.Add(time.Hour)))
	insertNameInterval(t, pool, sensorID, "temp", base.Add(time.Hour), nil)

	insertHWInterval(t, pool, sensorID, addr32(0x23), base, timePtr(base.Add(2*time.Hour)))
	insertHWInterval(t, pool, sensorID, addr32(0x44), base.Add(2*time.Hour), nil)

	insertRegionInterval(t, pool, sensorID, regionA, base, timePtr(base.Add(3*time.Hour)))
	insertRegionInterval(t, pool, sensorID, regionB, base.Add(3*time.Hour), nil)

	resp, err := server.GetSensorTimelines(ctx, &pb.GetSensorTimelinesRequest{
		SensorId:     sensorID,
		NamePage:     &pb.PageRequest{PageSize: 1},
		HardwarePage: &pb.PageRequest{PageSize: 1},
		RegionPage:   &pb.PageRequest{PageSize: 1},
	})
	if err != nil {
		t.Fatalf("GetSensorTimelines: %v", err)
	}

	if len(resp.NameIntervals) != 1 {
		t.Errorf("len(NameIntervals) = %d, want 1 (page_size=1)", len(resp.NameIntervals))
	}
	if resp.NamePage.GetNextPageToken() == "" {
		t.Error("NamePage.NextPageToken empty, want more pages to remain")
	}
	if len(resp.HardwareIntervals) != 1 {
		t.Errorf("len(HardwareIntervals) = %d, want 1 (page_size=1)", len(resp.HardwareIntervals))
	}
	if resp.HardwarePage.GetNextPageToken() == "" {
		t.Error("HardwarePage.NextPageToken empty, want more pages to remain")
	}
	if len(resp.RegionIntervals) != 1 {
		t.Errorf("len(RegionIntervals) = %d, want 1 (page_size=1)", len(resp.RegionIntervals))
	}
	if resp.RegionPage.GetNextPageToken() == "" {
		t.Error("RegionPage.NextPageToken empty, want more pages to remain")
	}

	// Advancing only the name timeline's cursor must not disturb the other
	// two: re-request with the name page's token and page_size big enough
	// to exhaust everything, holding hardware/region page requests unset
	// (first page) -- each timeline's own state is independent.
	resp2, err := server.GetSensorTimelines(ctx, &pb.GetSensorTimelinesRequest{
		SensorId: sensorID,
		NamePage: &pb.PageRequest{PageToken: resp.NamePage.GetNextPageToken(), PageSize: 10},
	})
	if err != nil {
		t.Fatalf("GetSensorTimelines page 2: %v", err)
	}
	if len(resp2.NameIntervals) != 1 || resp2.NameIntervals[0].Name != "temp" {
		t.Errorf("NameIntervals page 2 = %+v, want the second (current) name interval", resp2.NameIntervals)
	}
	if resp2.NamePage.GetNextPageToken() != "" {
		t.Errorf("NamePage.NextPageToken on last page = %q, want empty", resp2.NamePage.GetNextPageToken())
	}
	// Hardware/region timelines, requested fresh (no token), still start
	// from their own first interval -- proving the name timeline's cursor
	// advance had no effect on them.
	if len(resp2.HardwareIntervals) != 2 || resp2.HardwareIntervals[0].GetI2CAddress() != 0x23 {
		t.Errorf("HardwareIntervals (fresh page) = %+v, want both hw intervals starting at 0x23, unaffected by name pagination", resp2.HardwareIntervals)
	}
	if len(resp2.RegionIntervals) != 2 || resp2.RegionIntervals[0].RegionId != regionA {
		t.Errorf("RegionIntervals (fresh page) = %+v, want both region intervals starting at %d, unaffected by name pagination", resp2.RegionIntervals, regionA)
	}
}

// TestGetSensorTimelines_IntervalsAlignedOnOneTimeAxis covers "intervals
// are aligned: for a sensor renamed at T1 and rewired at T2, the two
// timelines' intervals are comparable on one axis and the boundaries land
// where expected" -- both timelines are read at full page size and their
// valid_from/valid_to boundaries are asserted directly against T1/T2.
func TestGetSensorTimelines_IntervalsAlignedOnOneTimeAxis(t *testing.T) {
	server, pool := newTimelinesTestServer(t)
	ctx := context.Background()

	boardID := insertBoard(t, pool, "leaflab-tl-aligned")
	typeID := insertSensorType(t, pool, "temperature", "degC")
	sensorID := insertSensor(t, pool, boardID, typeID, "temp-after", "degC", addr32(0x44))

	t0 := time.Now().Add(-10 * time.Hour).Truncate(time.Millisecond)
	t1 := t0.Add(2 * time.Hour) // renamed at T1
	t2 := t0.Add(5 * time.Hour) // rewired at T2

	insertNameInterval(t, pool, sensorID, "temp-before", t0, timePtr(t1))
	insertNameInterval(t, pool, sensorID, "temp-after", t1, nil)

	insertHWInterval(t, pool, sensorID, addr32(0x23), t0, timePtr(t2))
	insertHWInterval(t, pool, sensorID, addr32(0x44), t2, nil)

	resp, err := server.GetSensorTimelines(ctx, &pb.GetSensorTimelinesRequest{
		SensorId:     sensorID,
		NamePage:     &pb.PageRequest{PageSize: 10},
		HardwarePage: &pb.PageRequest{PageSize: 10},
	})
	if err != nil {
		t.Fatalf("GetSensorTimelines: %v", err)
	}

	if len(resp.NameIntervals) != 2 || len(resp.HardwareIntervals) != 2 {
		t.Fatalf("got %d name intervals, %d hw intervals, want 2 and 2", len(resp.NameIntervals), len(resp.HardwareIntervals))
	}

	// The name timeline's boundary lands at T1, on the same millis-since-
	// epoch axis as the hardware timeline's boundary at T2.
	nameBoundary := resp.NameIntervals[0].Interval.ValidTo.UnixMillis
	if nameBoundary != t1.UnixMilli() {
		t.Errorf("name interval[0].ValidTo = %d, want T1 = %d", nameBoundary, t1.UnixMilli())
	}
	if resp.NameIntervals[1].Interval.ValidTo != nil {
		t.Errorf("name interval[1].ValidTo = %v, want nil (still open)", resp.NameIntervals[1].Interval.ValidTo)
	}

	hwBoundary := resp.HardwareIntervals[0].Interval.ValidTo.UnixMillis
	if hwBoundary != t2.UnixMilli() {
		t.Errorf("hw interval[0].ValidTo = %d, want T2 = %d", hwBoundary, t2.UnixMilli())
	}
	if resp.HardwareIntervals[1].Interval.ValidTo != nil {
		t.Errorf("hw interval[1].ValidTo = %v, want nil (still open)", resp.HardwareIntervals[1].Interval.ValidTo)
	}

	// T1 < T2 on the shared axis: the two boundaries are directly
	// comparable, exactly what "aligned" (a shared Interval shape and time
	// axis) requires, per api.proto's doc comment.
	if !(nameBoundary < hwBoundary) {
		t.Errorf("name boundary %d not before hw boundary %d, want T1 < T2 to hold on the shared axis", nameBoundary, hwBoundary)
	}
}

// TestGetSensorTimelines_ClosedPreMigrationHWInterval_AddressNotRecorded
// covers "a closed pre-migration hardware interval reports the address as
// not recorded, distinct from 0": a closed sensor_hw_history row with a
// NULL i2c_address (migration 013's exact backfill outcome for a
// pre-migration closed interval) renders with I2CAddress unset, not a
// present 0.
func TestGetSensorTimelines_ClosedPreMigrationHWInterval_AddressNotRecorded(t *testing.T) {
	server, pool := newTimelinesTestServer(t)
	ctx := context.Background()

	boardID := insertBoard(t, pool, "leaflab-tl-premigration")
	typeID := insertSensorType(t, pool, "humidity", "pct")
	sensorID := insertSensor(t, pool, boardID, typeID, "hum", "pct", addr32(0x50))

	t0 := time.Now().Add(-48 * time.Hour).Truncate(time.Millisecond)
	t1 := t0.Add(time.Hour)
	// Closed, pre-migration-013 interval: i2c_address left NULL, exactly
	// migration 013's "closed intervals deliberately left NULL" step.
	insertHWInterval(t, pool, sensorID, nil, t0, timePtr(t1))
	// Current open interval: a real, present address.
	insertHWInterval(t, pool, sensorID, addr32(0x50), t1, nil)

	resp, err := server.GetSensorTimelines(ctx, &pb.GetSensorTimelinesRequest{
		SensorId:     sensorID,
		HardwarePage: &pb.PageRequest{PageSize: 10},
	})
	if err != nil {
		t.Fatalf("GetSensorTimelines: %v", err)
	}
	if len(resp.HardwareIntervals) != 2 {
		t.Fatalf("len(HardwareIntervals) = %d, want 2", len(resp.HardwareIntervals))
	}

	closed := resp.HardwareIntervals[0]
	if closed.I2CAddress != nil {
		t.Errorf("closed pre-migration interval I2CAddress = %v, want nil (not recorded, never 0)", closed.I2CAddress)
	}

	open := resp.HardwareIntervals[1]
	if open.I2CAddress == nil || *open.I2CAddress != 0x50 {
		t.Errorf("open interval I2CAddress = %v, want present 0x50", open.I2CAddress)
	}
}

// TestGetSensorTimelines_OpenHWInterval_GenuineZeroAddress covers "an open
// hardware interval reports the real address, including a genuine 0x00":
// I2CAddress must render as present-and-zero, never conflated with the
// absent case.
func TestGetSensorTimelines_OpenHWInterval_GenuineZeroAddress(t *testing.T) {
	server, pool := newTimelinesTestServer(t)
	ctx := context.Background()

	boardID := insertBoard(t, pool, "leaflab-tl-zero-addr")
	typeID := insertSensorType(t, pool, "illuminance", "lx")
	sensorID := insertSensor(t, pool, boardID, typeID, "lux", "lx", addr32(0x00))

	insertHWInterval(t, pool, sensorID, addr32(0x00), time.Now().Add(-time.Hour), nil)

	resp, err := server.GetSensorTimelines(ctx, &pb.GetSensorTimelinesRequest{
		SensorId:     sensorID,
		HardwarePage: &pb.PageRequest{PageSize: 10},
	})
	if err != nil {
		t.Fatalf("GetSensorTimelines: %v", err)
	}
	if len(resp.HardwareIntervals) != 1 {
		t.Fatalf("len(HardwareIntervals) = %d, want 1", len(resp.HardwareIntervals))
	}
	got := resp.HardwareIntervals[0]
	if got.I2CAddress == nil {
		t.Fatal("I2CAddress = nil, want present 0x00 (a genuine, recorded address, not absent)")
	}
	if *got.I2CAddress != 0 {
		t.Errorf("I2CAddress = %#x, want 0x00", *got.I2CAddress)
	}
}

// TestGetSensorTimelines_OneSHT3xTwoSensorRows_TwoDistinctHWTimelines
// covers "one SHT3x's two sensor rows produce two distinct hardware
// timelines": an SHT3x chip exposes both a temperature and a humidity
// virtual sensor at the same physical (i2c_address, mux_path), which are
// two separate `sensor` rows (per FR16.1's per-virtual-sensor identity) --
// each must have its own, independent hardware timeline keyed off its own
// sensor_id, not a timeline shared by address.
func TestGetSensorTimelines_OneSHT3xTwoSensorRows_TwoDistinctHWTimelines(t *testing.T) {
	server, pool := newTimelinesTestServer(t)
	ctx := context.Background()

	boardID := insertBoard(t, pool, "leaflab-tl-sht3x")
	tempTypeID := insertSensorType(t, pool, "temperature", "degC")
	humTypeID := insertSensorType(t, pool, "humidity", "pct")

	// Same physical chip: same i2c_address, but two sensor rows (one per
	// virtual sensor), as FR16.1 requires.
	tempSensorID := insertSensor(t, pool, boardID, tempTypeID, "sht3x-temp", "degC", addr32(0x44))
	humSensorID := insertSensor(t, pool, boardID, humTypeID, "sht3x-hum", "pct", addr32(0x44))

	t0 := time.Now().Add(-time.Hour)
	insertHWInterval(t, pool, tempSensorID, addr32(0x44), t0, nil)
	insertHWInterval(t, pool, humSensorID, addr32(0x44), t0, nil)
	// Also close and reopen the temperature sensor's interval once more so
	// its timeline has a different shape (2 rows) from the humidity
	// sensor's (1 row) -- proving they're read independently, not just
	// happening to look identical.
	if _, err := pool.Exec(ctx, `UPDATE sensor_hw_history SET valid_to = $2 WHERE sensor_id = $1`, tempSensorID, t0.Add(30*time.Minute)); err != nil {
		t.Fatalf("close temp hw interval: %v", err)
	}
	insertHWInterval(t, pool, tempSensorID, addr32(0x44), t0.Add(30*time.Minute), nil)

	tempResp, err := server.GetSensorTimelines(ctx, &pb.GetSensorTimelinesRequest{
		SensorId:     tempSensorID,
		HardwarePage: &pb.PageRequest{PageSize: 10},
	})
	if err != nil {
		t.Fatalf("GetSensorTimelines (temp sensor): %v", err)
	}
	humResp, err := server.GetSensorTimelines(ctx, &pb.GetSensorTimelinesRequest{
		SensorId:     humSensorID,
		HardwarePage: &pb.PageRequest{PageSize: 10},
	})
	if err != nil {
		t.Fatalf("GetSensorTimelines (hum sensor): %v", err)
	}

	if len(tempResp.HardwareIntervals) != 2 {
		t.Errorf("temp sensor HardwareIntervals = %d, want 2 (its own distinct timeline)", len(tempResp.HardwareIntervals))
	}
	if len(humResp.HardwareIntervals) != 1 {
		t.Errorf("hum sensor HardwareIntervals = %d, want 1 (its own distinct timeline, unaffected by temp sensor's extra interval)", len(humResp.HardwareIntervals))
	}
	for _, iv := range tempResp.HardwareIntervals {
		if iv.SensorType != firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE {
			t.Errorf("temp sensor interval SensorType = %v, want TEMPERATURE", iv.SensorType)
		}
	}
	for _, iv := range humResp.HardwareIntervals {
		if iv.SensorType != firmwarepb.SensorType_SENSOR_TYPE_HUMIDITY {
			t.Errorf("hum sensor interval SensorType = %v, want HUMIDITY", iv.SensorType)
		}
	}
}

// TestGetSensorTimelines_NoRegionHistory_EmptyNotError covers "a sensor
// with no region history returns an empty region timeline, not an error":
// a sensor with populated name/hardware history but zero
// sensor_region_history rows still returns success with an empty
// RegionIntervals slice.
func TestGetSensorTimelines_NoRegionHistory_EmptyNotError(t *testing.T) {
	server, pool := newTimelinesTestServer(t)
	ctx := context.Background()

	boardID := insertBoard(t, pool, "leaflab-tl-noregion")
	typeID := insertSensorType(t, pool, "temperature", "degC")
	sensorID := insertSensor(t, pool, boardID, typeID, "unplaced", "degC", addr32(0x23))
	insertNameInterval(t, pool, sensorID, "unplaced", time.Now().Add(-time.Hour), nil)
	insertHWInterval(t, pool, sensorID, addr32(0x23), time.Now().Add(-time.Hour), nil)

	resp, err := server.GetSensorTimelines(ctx, &pb.GetSensorTimelinesRequest{SensorId: sensorID})
	if err != nil {
		t.Fatalf("GetSensorTimelines with no region history returned an error, want success with an empty region timeline: %v", err)
	}
	if len(resp.RegionIntervals) != 0 {
		t.Errorf("len(RegionIntervals) = %d, want 0", len(resp.RegionIntervals))
	}
	if resp.RegionPage == nil {
		t.Error("RegionPage is nil, want a (empty-token) PageResponse even with zero intervals")
	}
}

// TestGetSensorTimelines_NonexistentSensor_NotFound covers "a non-member
// gets NFR2's not-found": this branch lineage has no household/authz model
// to distinguish "no membership" from "doesn't exist" (see server.go's
// GetSensorTimelines doc comment and scope note #1462) -- the only check
// available is sensor existence, which is also NFR2's non-existence oracle
// in the household-scoped world. A nonexistent sensor_id must render
// exactly the same not-found failure a real-but-inaccessible sensor would.
func TestGetSensorTimelines_NonexistentSensor_NotFound(t *testing.T) {
	server, _ := newTimelinesTestServer(t)
	ctx := context.Background()

	_, err := server.GetSensorTimelines(ctx, &pb.GetSensorTimelinesRequest{SensorId: 999999})
	if err == nil {
		t.Fatal("GetSensorTimelines for a nonexistent sensor_id returned nil error, want not-found")
	}
	detail, ok := contract.FromError(err)
	if !ok {
		t.Fatalf("error %v carries no Failure detail", err)
	}
	if detail.Class != string(contract.FailureNotFound) {
		t.Errorf("Class = %q, want %q", detail.Class, contract.FailureNotFound)
	}
}

// TestGetSensorTimelines_DroppedFromDesiredState_KeepsAllThree is the
// forward requirement stub the issue calls for: "a sensor dropped from a
// board's desired state (FR82.3, Phase 4) keeps all three timelines. Add
// the test now with a stubbed 'dropped' state so Phase 4 inherits a
// passing assertion." This branch lineage has no desired-state model at
// all yet (see server.go's GetSensorTimelines doc comment) -- there is no
// column or table to mark a sensor "dropped". The stub here is therefore
// the RPC's actual current behavior, asserted explicitly: a sensor with
// full history and no "desired state" involvement of any kind still
// returns all three timelines, proving GetSensorTimelines has no
// desired-state filter to break when Phase 4 introduces the concept. When
// Phase 4 lands a real desired-state table, this test should be extended
// to mark this sensor's row "dropped" there and re-assert the same
// unchanged outcome.
func TestGetSensorTimelines_DroppedFromDesiredState_KeepsAllThree(t *testing.T) {
	server, pool := newTimelinesTestServer(t)
	ctx := context.Background()

	boardID := insertBoard(t, pool, "leaflab-tl-dropped")
	typeID := insertSensorType(t, pool, "temperature", "degC")
	regionID := insertRegion(t, pool, "greenhouse")
	sensorID := insertSensor(t, pool, boardID, typeID, "orphaned", "degC", addr32(0x23))

	base := time.Now().Add(-time.Hour)
	insertNameInterval(t, pool, sensorID, "orphaned", base, nil)
	insertHWInterval(t, pool, sensorID, addr32(0x23), base, nil)
	insertRegionInterval(t, pool, sensorID, regionID, base, nil)

	resp, err := server.GetSensorTimelines(ctx, &pb.GetSensorTimelinesRequest{SensorId: sensorID})
	if err != nil {
		t.Fatalf("GetSensorTimelines: %v", err)
	}
	if len(resp.NameIntervals) != 1 {
		t.Errorf("len(NameIntervals) = %d, want 1 (kept)", len(resp.NameIntervals))
	}
	if len(resp.HardwareIntervals) != 1 {
		t.Errorf("len(HardwareIntervals) = %d, want 1 (kept)", len(resp.HardwareIntervals))
	}
	if len(resp.RegionIntervals) != 1 {
		t.Errorf("len(RegionIntervals) = %d, want 1 (kept)", len(resp.RegionIntervals))
	}
}
