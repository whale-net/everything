//go:build integration

// Real-Postgres integration coverage for FR1.2/FR1.3's apply-time
// re-validation in ApplyConfigRegions (repository.go): household
// ownership re-checked immediately before each write (not just at push
// time -- the region may have been reassigned to a different household in
// the interim), push-vs-interval staleness, and skip-and-audit semantics
// (an invalid entry is skipped, not failed, and the rest of an
// otherwise-valid config batch still applies). This exercises the actual
// SQL -- the recursive region-household CTE, the sensor_region_history
// SCD2 write, the FR8 audit_log write -- not a fake.
//
// Schema is self-contained hand-written DDL, mirroring the
// household/region/board/sensor/sensor_region_history/device_config/
// audit_log shape ApplyConfigRegions' queries touch (see migrations
// 001_initial_schema, 009_sensor_schema_v2, 011_scd2_naming,
// 015_ownership, 016_audit_log) -- deliberately narrower than the real
// migrations, only what this file's queries need. See
// //libs/go/dbtest's README for how to run integration tests like this
// one.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //leaflab/processor:apply_config_regions_integration_test --test_output=all
package main

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/encoding/protojson"

	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/api/audit"
	"github.com/whale-net/everything/libs/go/dbtest"
)

const applyConfigRegionsTestSchema = `
	CREATE TABLE household (
		household_id BIGSERIAL PRIMARY KEY
	);

	CREATE TABLE region (
		region_id        BIGSERIAL PRIMARY KEY,
		parent_region_id BIGINT REFERENCES region(region_id),
		household_id     BIGINT REFERENCES household(household_id)
	);

	CREATE TABLE board (
		board_id      BIGSERIAL PRIMARY KEY,
		device_id     VARCHAR(64) NOT NULL UNIQUE,
		household_id  BIGINT REFERENCES household(household_id),
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
		board_id       BIGINT NOT NULL REFERENCES board(board_id),
		sensor_type_id BIGINT NOT NULL REFERENCES sensor_type(sensor_type_id),
		region_id      BIGINT REFERENCES region(region_id),
		name           VARCHAR(128) NOT NULL,
		unit           VARCHAR(16) NOT NULL,
		i2c_address    SMALLINT,
		mux_path       JSONB NOT NULL DEFAULT '[]'::jsonb,
		registered_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		last_seen_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		UNIQUE (board_id, name)
	);

	CREATE TABLE sensor_region_history (
		history_id BIGSERIAL PRIMARY KEY,
		sensor_id  BIGINT NOT NULL REFERENCES sensor(sensor_id),
		region_id  BIGINT NOT NULL REFERENCES region(region_id),
		valid_from TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		valid_to   TIMESTAMPTZ
	);
	CREATE INDEX idx_srh_current ON sensor_region_history(sensor_id) WHERE valid_to IS NULL;

	CREATE TABLE device_config (
		config_id        BIGSERIAL   PRIMARY KEY,
		board_id         BIGINT      NOT NULL REFERENCES board(board_id),
		version          BIGINT      NOT NULL,
		config_json      JSONB       NOT NULL,
		accepted         BOOLEAN     NOT NULL DEFAULT FALSE,
		pushed_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		acked_at         TIMESTAMPTZ,
		rejection_reason TEXT,
		UNIQUE (board_id, version)
	);

	CREATE TABLE audit_log (
		audit_id            BIGSERIAL PRIMARY KEY,
		actor_subject       TEXT NOT NULL,
		actor_kind          TEXT NOT NULL,
		target_household_id BIGINT NULL,
		action              TEXT NOT NULL,
		entity_kind         TEXT NOT NULL,
		entity_id           TEXT NULL,
		reason              TEXT NULL,
		occurred_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		correlation_id      TEXT NULL
	);
`

func newApplyConfigRegionsTestRepo(t *testing.T) (*Repository, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: applyConfigRegionsTestSchema})
	return NewRepository(db.Pool), db.Pool
}

func acrInsertHousehold(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO household DEFAULT VALUES RETURNING household_id`).Scan(&id); err != nil {
		t.Fatalf("insert household: %v", err)
	}
	return id
}

func acrInsertRootRegion(t *testing.T, pool *pgxpool.Pool, householdID int64) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO region (household_id) VALUES ($1) RETURNING region_id`, householdID).Scan(&id); err != nil {
		t.Fatalf("insert region for household %d: %v", householdID, err)
	}
	return id
}

func acrInsertBoard(t *testing.T, pool *pgxpool.Pool, deviceID string, householdID int64) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO board (device_id, household_id) VALUES ($1, $2) RETURNING board_id`,
		deviceID, householdID).Scan(&id); err != nil {
		t.Fatalf("insert board %s: %v", deviceID, err)
	}
	return id
}

func acrInsertSensorType(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO sensor_type (name, default_unit) VALUES ('illuminance', 'lx') RETURNING sensor_type_id`).Scan(&id); err != nil {
		t.Fatalf("insert sensor_type: %v", err)
	}
	return id
}

// acrInsertSensor inserts a sensor at i2cAddr on boardID, currently placed
// at regionID (nil = no placement yet). mux_path is always the empty
// array, matching a directly-on-root-bus sensor and this file's config
// entries (which never set MuxPath) -- see ApplyConfigRegions' hops
// marshal, which produces "[]" for a nil/empty MuxPath.
func acrInsertSensor(t *testing.T, pool *pgxpool.Pool, boardID, sensorTypeID int64, name string, i2cAddr uint32, regionID *int64) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO sensor (board_id, sensor_type_id, region_id, name, unit, i2c_address, mux_path)
		VALUES ($1, $2, $3, $4, 'lx', $5, '[]'::jsonb)
		RETURNING sensor_id
	`, boardID, sensorTypeID, regionID, name, i2cAddr).Scan(&id); err != nil {
		t.Fatalf("insert sensor %s: %v", name, err)
	}
	return id
}

func acrInsertOpenRegionHistory(t *testing.T, pool *pgxpool.Pool, sensorID, regionID int64, validFrom time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO sensor_region_history (sensor_id, region_id, valid_from) VALUES ($1, $2, $3)`,
		sensorID, regionID, validFrom); err != nil {
		t.Fatalf("insert open region history for sensor %d: %v", sensorID, err)
	}
}

func acrInsertDeviceConfig(t *testing.T, pool *pgxpool.Pool, boardID, version int64, sensors []*configpb.SensorConfig, pushedAt time.Time) {
	t.Helper()
	cfg := &configpb.DeviceConfig{Sensors: sensors}
	b, err := protojson.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal device config: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO device_config (board_id, version, config_json, accepted, pushed_at)
		VALUES ($1, $2, $3::jsonb, TRUE, $4)
	`, boardID, version, b, pushedAt); err != nil {
		t.Fatalf("insert device_config board=%d version=%d: %v", boardID, version, err)
	}
}

func acrSensorRegionID(t *testing.T, pool *pgxpool.Pool, sensorID int64) *int64 {
	t.Helper()
	var regionID *int64
	if err := pool.QueryRow(context.Background(),
		`SELECT region_id FROM sensor WHERE sensor_id = $1`, sensorID).Scan(&regionID); err != nil {
		t.Fatalf("read sensor %d region_id: %v", sensorID, err)
	}
	return regionID
}

func acrCountRows(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("count rows in %s: %v", table, err)
	}
	return n
}

// TestApplyConfigRegions_SameHouseholdReassignment_AppliesAndRecordsHistory
// is the "not over-refused" companion case: a region reassignment within
// the pushing board's own household applies cleanly against real SQL --
// sensor.region_id is updated, the old sensor_region_history interval is
// closed, and a new open interval is recorded for the new region -- with
// no skip.
func TestApplyConfigRegions_SameHouseholdReassignment_AppliesAndRecordsHistory(t *testing.T) {
	repo, pool := newApplyConfigRegionsTestRepo(t)
	ctx := context.Background()

	householdA := acrInsertHousehold(t, pool)
	boardID := acrInsertBoard(t, pool, "device-a", householdA)
	sensorTypeID := acrInsertSensorType(t, pool)
	regionOld := acrInsertRootRegion(t, pool, householdA)
	regionNew := acrInsertRootRegion(t, pool, householdA)
	sensorID := acrInsertSensor(t, pool, boardID, sensorTypeID, "sensor-1", 0x10, &regionOld)

	openedAt := time.Now().Add(-1 * time.Hour)
	acrInsertOpenRegionHistory(t, pool, sensorID, regionOld, openedAt)

	pushedAt := time.Now()
	acrInsertDeviceConfig(t, pool, boardID, 1, []*configpb.SensorConfig{
		{I2CAddress: 0x10, RegionId: uint32(regionNew)},
	}, pushedAt)

	skips, _, err := repo.ApplyConfigRegions(ctx, boardID, 1)
	if err != nil {
		t.Fatalf("ApplyConfigRegions: %v", err)
	}
	if len(skips) != 0 {
		t.Fatalf("skips = %+v, want none for a same-household reassignment", skips)
	}

	gotRegionID := acrSensorRegionID(t, pool, sensorID)
	if gotRegionID == nil || *gotRegionID != regionNew {
		t.Errorf("sensor.region_id after apply = %v, want %d", gotRegionID, regionNew)
	}

	var closedCount, openCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM sensor_region_history WHERE sensor_id = $1 AND valid_to IS NOT NULL`, sensorID).Scan(&closedCount); err != nil {
		t.Fatalf("count closed history rows: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM sensor_region_history WHERE sensor_id = $1 AND valid_to IS NULL`, sensorID).Scan(&openCount); err != nil {
		t.Fatalf("count open history rows: %v", err)
	}
	if closedCount != 1 {
		t.Errorf("closed sensor_region_history rows = %d, want 1 (the old regionOld interval)", closedCount)
	}
	if openCount != 1 {
		t.Errorf("open sensor_region_history rows = %d, want 1 (the new regionNew interval)", openCount)
	}

	var openRegionID int64
	if err := pool.QueryRow(ctx, `SELECT region_id FROM sensor_region_history WHERE sensor_id = $1 AND valid_to IS NULL`, sensorID).Scan(&openRegionID); err != nil {
		t.Fatalf("read open history row region_id: %v", err)
	}
	if openRegionID != regionNew {
		t.Errorf("open sensor_region_history.region_id = %d, want %d", openRegionID, regionNew)
	}
}

// TestApplyConfigRegions_ForeignHouseholdEntry_SkippedNotFailed_AppliesRestAndAuditsSkip
// is this task's apply-time defect reproduction over real SQL: a config
// batch has one entry naming a region that has since drifted to a
// different household than the pushing board's own (re-validated here,
// not just at push time, because time passes between push and ack). FR1.3
// requires that entry skipped -- not the whole apply failed -- with the
// rest of the batch still applied, and exactly one FR8 audit_log row
// recorded for the skip.
func TestApplyConfigRegions_ForeignHouseholdEntry_SkippedNotFailed_AppliesRestAndAuditsSkip(t *testing.T) {
	repo, pool := newApplyConfigRegionsTestRepo(t)
	ctx := context.Background()

	householdA := acrInsertHousehold(t, pool)
	householdB := acrInsertHousehold(t, pool)
	boardID := acrInsertBoard(t, pool, "device-a", householdA)
	sensorTypeID := acrInsertSensorType(t, pool)
	regionAGood := acrInsertRootRegion(t, pool, householdA)
	regionBForeign := acrInsertRootRegion(t, pool, householdB)

	sensorGoodID := acrInsertSensor(t, pool, boardID, sensorTypeID, "sensor-good", 0x10, nil)
	sensorBadID := acrInsertSensor(t, pool, boardID, sensorTypeID, "sensor-bad", 0x11, nil)

	acrInsertDeviceConfig(t, pool, boardID, 1, []*configpb.SensorConfig{
		{I2CAddress: 0x10, RegionId: uint32(regionAGood)},
		{I2CAddress: 0x11, RegionId: uint32(regionBForeign)},
	}, time.Now())

	skips, _, err := repo.ApplyConfigRegions(ctx, boardID, 1)
	if err != nil {
		t.Fatalf("ApplyConfigRegions: %v", err)
	}
	if len(skips) != 1 {
		t.Fatalf("skips = %+v, want exactly 1 (the foreign-household entry)", skips)
	}
	if skips[0].SensorID != sensorBadID {
		t.Errorf("skipped SensorID = %d, want %d (sensor-bad)", skips[0].SensorID, sensorBadID)
	}
	if skips[0].Reason != reasonForeignHousehold {
		t.Errorf("skip Reason = %q, want %q", skips[0].Reason, reasonForeignHousehold)
	}

	// The rest of the batch (sensor-good) must still apply.
	gotGoodRegion := acrSensorRegionID(t, pool, sensorGoodID)
	if gotGoodRegion == nil || *gotGoodRegion != regionAGood {
		t.Errorf("sensor-good.region_id = %v, want %d -- a foreign-household entry elsewhere in the batch must not block valid entries", gotGoodRegion, regionAGood)
	}

	// The skipped entry itself must be unchanged.
	gotBadRegion := acrSensorRegionID(t, pool, sensorBadID)
	if gotBadRegion != nil {
		t.Errorf("sensor-bad.region_id = %v, want nil (unchanged) -- a skipped entry must not be written", gotBadRegion)
	}

	if n := acrCountRows(t, pool, "audit_log"); n != 1 {
		t.Fatalf("audit_log row count after one skip = %d, want exactly 1", n)
	}
	var actorKind, action, entityKind, entityID, reason string
	var targetHouseholdID int64
	if err := pool.QueryRow(ctx, `
		SELECT actor_kind, target_household_id, action, entity_kind, entity_id, reason FROM audit_log
	`).Scan(&actorKind, &targetHouseholdID, &action, &entityKind, &entityID, &reason); err != nil {
		t.Fatalf("read audit_log row: %v", err)
	}
	if actorKind != string(audit.ActorKindSystem) {
		t.Errorf("actor_kind = %q, want %q (no authenticated caller in an MQTT ack path)", actorKind, audit.ActorKindSystem)
	}
	if targetHouseholdID != householdA {
		t.Errorf("target_household_id = %d, want %d (the pushing board's own household)", targetHouseholdID, householdA)
	}
	if action != audit.ActionApplyConfigRegionSkip {
		t.Errorf("action = %q, want %q", action, audit.ActionApplyConfigRegionSkip)
	}
	if entityKind != audit.EntityKindSensor {
		t.Errorf("entity_kind = %q, want %q", entityKind, audit.EntityKindSensor)
	}
	if wantEntityID := strconv.FormatInt(sensorBadID, 10); entityID != wantEntityID {
		t.Errorf("entity_id = %q, want %q", entityID, wantEntityID)
	}
	if reason != reasonForeignHousehold {
		t.Errorf("reason = %q, want %q", reason, reasonForeignHousehold)
	}
}

// TestApplyConfigRegions_StalePush_SkippedNotApplied_AuditRowWritten is
// FR1.3's staleness clause: a payload pushed *before* the sensor's current
// region interval opened is skipped, not applied, even though the region
// it names is in the pushing board's own household -- push-time, not
// ack-time, is what's compared against the interval's valid_from.
func TestApplyConfigRegions_StalePush_SkippedNotApplied_AuditRowWritten(t *testing.T) {
	repo, pool := newApplyConfigRegionsTestRepo(t)
	ctx := context.Background()

	householdA := acrInsertHousehold(t, pool)
	boardID := acrInsertBoard(t, pool, "device-a", householdA)
	sensorTypeID := acrInsertSensorType(t, pool)
	regionOld := acrInsertRootRegion(t, pool, householdA)
	regionNew := acrInsertRootRegion(t, pool, householdA)
	sensorID := acrInsertSensor(t, pool, boardID, sensorTypeID, "sensor-1", 0x10, &regionOld)

	// The sensor's current region interval opened at intervalOpenedAt --
	// this represents a *second* writer (e.g. a later, faster-acked push,
	// or in Phase 5 the API itself per FR51) having reassigned the region
	// after this test's own push was issued but before it was applied.
	intervalOpenedAt := time.Now()
	acrInsertOpenRegionHistory(t, pool, sensorID, regionOld, intervalOpenedAt)

	// This config was pushed *before* that interval opened.
	stalePushedAt := intervalOpenedAt.Add(-1 * time.Hour)
	acrInsertDeviceConfig(t, pool, boardID, 1, []*configpb.SensorConfig{
		{I2CAddress: 0x10, RegionId: uint32(regionNew)},
	}, stalePushedAt)

	skips, _, err := repo.ApplyConfigRegions(ctx, boardID, 1)
	if err != nil {
		t.Fatalf("ApplyConfigRegions: %v", err)
	}
	if len(skips) != 1 {
		t.Fatalf("skips = %+v, want exactly 1 (the stale push)", skips)
	}
	if skips[0].SensorID != sensorID {
		t.Errorf("skipped SensorID = %d, want %d", skips[0].SensorID, sensorID)
	}
	if skips[0].Reason != reasonStalePush {
		t.Errorf("skip Reason = %q, want %q", skips[0].Reason, reasonStalePush)
	}

	gotRegionID := acrSensorRegionID(t, pool, sensorID)
	if gotRegionID == nil || *gotRegionID != regionOld {
		t.Errorf("sensor.region_id after a stale-push skip = %v, want %d (unchanged)", gotRegionID, regionOld)
	}

	if n := acrCountRows(t, pool, "audit_log"); n != 1 {
		t.Fatalf("audit_log row count after a stale-push skip = %d, want exactly 1", n)
	}
	var reason string
	if err := pool.QueryRow(ctx, `SELECT reason FROM audit_log`).Scan(&reason); err != nil {
		t.Fatalf("read audit_log reason: %v", err)
	}
	if reason != reasonStalePush {
		t.Errorf("audit_log reason = %q, want %q", reason, reasonStalePush)
	}
}
