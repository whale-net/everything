//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never even compiles it.
// See the go_test target's gotags in BUILD.bazel and
// //libs/go/dbtest/README.md for how to run it.
//
// It proves FR73's load-bearing property -- and closes defect 2 in the root
// plan -- against a *real* Repository.GetSensor and Repository.InsertReading
// (real SQL, not stubRepo.GetSensor's hardcoded not-found -- see
// handler_test.go's "FR73: handleSensorReading's cache-miss/
// invalidation-driven re-read path" section for the pure-Go coverage this
// file complements): a region assignment commits, this process's SensorCache
// is invalidated, and the very next reading -- with no manifest republish
// and no config push -- is stamped with the new region, regardless of
// whether the API or this process's own ApplyConfigRegions wrote it.
//
// The cross-process broadcast itself (a real RabbitMQ fanout exchange
// delivering the event to every replica) is proven separately by
// invalidation_integration_test.go; this file stands in for "the Subscriber
// received the event" with a direct ApplyInvalidation call, exactly as that
// package's own doc comment on Event's idempotent, re-readable design
// intends: what a Subscriber's handler does with an event it has received
// does not depend on how that event arrived.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //leaflab/processor:cross_process_defect_integration_test --test_output=all
package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	firmwarepb "github.com/whale-net/everything/firmware/proto"
	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/invalidation"
	"github.com/whale-net/everything/libs/go/dbtest"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// discardTestLogger is a *slog.Logger that throws away everything it's
// given -- this file is its own go_test target/binary (see BUILD.bazel),
// separate from handler_test.go's, so its helpers (newTestHandler included)
// aren't visible here; duplicated in miniature rather than shared, same as
// leaflab/api's own integration test files do for the analogous helper.
func discardTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// crossProcessSchema is self-contained DDL mirroring the shape of board,
// sensor_type, sensor, sensor_region_history, device_config and
// sensor_reading as of migration 013 -- deliberately not a dependency on
// leaflab/migrate's migrations (see dbtest's own doc comment on
// Options.Schema and this package's other integration test files for the
// same pattern). No TimescaleDB extension/hypertable: this file never
// exercises time-range query performance, only correctness of a handful of
// rows.
const crossProcessSchema = `
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
		board_id       BIGINT NOT NULL REFERENCES board(board_id),
		sensor_type_id BIGINT NOT NULL REFERENCES sensor_type(sensor_type_id),
		region_id      BIGINT,
		name           VARCHAR(128) NOT NULL,
		unit           VARCHAR(16) NOT NULL,
		i2c_address    SMALLINT,
		mux_path       JSONB NOT NULL DEFAULT '[]'::jsonb,
		registered_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		UNIQUE (board_id, name)
	);

	CREATE TABLE sensor_region_history (
		history_id  BIGSERIAL PRIMARY KEY,
		sensor_id   BIGINT NOT NULL REFERENCES sensor(sensor_id),
		region_id   BIGINT NOT NULL,
		valid_from  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		valid_to    TIMESTAMPTZ
	);
	CREATE INDEX idx_sensor_region_history_current ON sensor_region_history(sensor_id) WHERE valid_to IS NULL;

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

	CREATE TABLE sensor_reading (
		reading_id     BIGSERIAL PRIMARY KEY,
		sensor_id      BIGINT NOT NULL REFERENCES sensor(sensor_id),
		region_id      BIGINT,
		value          DOUBLE PRECISION NOT NULL,
		valid          BOOLEAN NOT NULL DEFAULT TRUE,
		uptime_s       INTEGER NOT NULL,
		recorded_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		config_version BIGINT
	);
`

// crossProcessFixture seeds one board with one temperature sensor at a known
// hardware address, currently assigned to regionOld, and returns its
// sensor_id.
func crossProcessFixture(ctx context.Context, t *testing.T, db *dbtest.Postgres, deviceID string, regionOld int64) (sensorID int64) {
	t.Helper()

	var boardID int64
	if err := db.Pool.QueryRow(ctx,
		`INSERT INTO board (device_id) VALUES ($1) RETURNING board_id`, deviceID,
	).Scan(&boardID); err != nil {
		t.Fatalf("insert board: %v", err)
	}

	var typeID int64
	if err := db.Pool.QueryRow(ctx,
		`INSERT INTO sensor_type (name, default_unit) VALUES ('temperature', 'degC') RETURNING sensor_type_id`,
	).Scan(&typeID); err != nil {
		t.Fatalf("insert sensor_type: %v", err)
	}

	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO sensor (board_id, sensor_type_id, region_id, name, unit, i2c_address)
		VALUES ($1, $2, $3, 'temp', 'degC', $4)
		RETURNING sensor_id
	`, boardID, typeID, regionOld, 0x23).Scan(&sensorID); err != nil {
		t.Fatalf("insert sensor: %v", err)
	}
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO sensor_region_history (sensor_id, region_id) VALUES ($1, $2)`, sensorID, regionOld,
	); err != nil {
		t.Fatalf("insert sensor_region_history: %v", err)
	}
	return sensorID
}

// lastReadingRegion returns the region_id sensor_reading recorded for the
// most recently inserted reading for sensorID, and how long ago recorded_at
// was relative to since.
func lastReadingRegion(ctx context.Context, t *testing.T, db *dbtest.Postgres, sensorID int64) (regionID *int64, recordedAt time.Time) {
	t.Helper()
	if err := db.Pool.QueryRow(ctx, `
		SELECT region_id, recorded_at FROM sensor_reading
		WHERE sensor_id = $1 ORDER BY reading_id DESC LIMIT 1
	`, sensorID).Scan(&regionID, &recordedAt); err != nil {
		t.Fatalf("query latest sensor_reading for sensor %d: %v", sensorID, err)
	}
	return regionID, recordedAt
}

func marshalReading(t *testing.T, value float32, uptimeMs uint32) []byte {
	t.Helper()
	b, err := proto.Marshal(&firmwarepb.SensorReading{Value: value, UptimeMs: uptimeMs})
	if err != nil {
		t.Fatalf("marshal SensorReading: %v", err)
	}
	return b
}

// TestFR73_SecondProcessRegionAssignment_ReflectedAfterInvalidation is the
// load-bearing test named in the issue's Testing section: "with the
// processor running, commit a region assignment through a second process,
// then publish a reading > 5s later; assert sensor_reading.region_id is the
// new region, with no manifest republish and no config push." It writes the
// region change directly against Postgres -- standing in for "a second
// process" (e.g. the API's direct assignment, FR51) committing it outside
// this handler entirely -- then waits past FR73's 5s bound before
// publishing the reading, proving the bound is not what makes this work: the
// invalidation, applied once (representing the Subscriber having received
// it -- the delivery itself is proven by invalidation_integration_test.go),
// is what makes it work regardless of how long after the reading arrives.
func TestFR73_SecondProcessRegionAssignment_ReflectedAfterInvalidation(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: crossProcessSchema})
	repo := NewRepository(db.Pool)
	h := NewMessageHandler(discardTestLogger(), repo, NewSensorCache(), nil)

	const deviceID = "leaflab-fr73-secondproc"
	regionOld, regionNew := int64(1), int64(2)
	sensorID := crossProcessFixture(ctx, t, db, deviceID, regionOld)

	// "the processor running ... with a sensor cached": pre-warm exactly as
	// handleManifest would, under the region that was current before the
	// second process's write below.
	h.cache.Set(deviceID, "temp", SensorInfo{SensorID: sensorID, RegionID: &regionOld})

	// "commit a region assignment through a second process": a direct SQL
	// write against the same database, with no involvement from this
	// handler at all -- exactly how the API's direct region assignment
	// (FR51, Phase 5) would commit it.
	if _, err := db.Pool.Exec(ctx, `UPDATE sensor SET region_id = $2 WHERE sensor_id = $1`, sensorID, regionNew); err != nil {
		t.Fatalf("simulate second-process region write: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE sensor_region_history SET valid_to = NOW() WHERE sensor_id = $1 AND valid_to IS NULL`, sensorID); err != nil {
		t.Fatalf("close region history: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO sensor_region_history (sensor_id, region_id) VALUES ($1, $2)`, sensorID, regionNew); err != nil {
		t.Fatalf("open new region history: %v", err)
	}

	// The signal: this process's Subscriber having received the
	// cross-process invalidation.Event for this change (main.go wires
	// exactly this call into invalidationSub.Start's handler).
	ApplyInvalidation(h.cache, invalidation.Event{Kind: invalidation.KindRegion, DeviceID: deviceID, SensorID: sensorID, SensorName: "temp"})

	// "a reading arrives > 5s later": literally wait past the bound, so
	// this test cannot be satisfied by some time-bounded fallback to the
	// old cached value -- only by the invalidation having already happened.
	time.Sleep(5*time.Second + 200*time.Millisecond)

	before := time.Now()
	// No manifest republish, no config push -- just the reading.
	if err := h.handleSensorReading(ctx, deviceID, "temp", marshalReading(t, 21.5, 60000)); err != nil {
		t.Fatalf("handleSensorReading: %v", err)
	}

	gotRegion, recordedAt := lastReadingRegion(ctx, t, db, sensorID)
	if gotRegion == nil || *gotRegion != regionNew {
		t.Fatalf("sensor_reading.region_id = %v, want the NEW region %d -- a cached view outlived the fact it caches", gotRegion, regionNew)
	}
	if recordedAt.Before(before) {
		t.Errorf("recorded_at %v is before the reading was even submitted (%v)", recordedAt, before)
	}
}

// TestFR73_ProcessorOwnApplyConfigRegions_ReflectedAfterInvalidation is the
// issue's second load-bearing case: "same test with the assignment written
// by the processor's own ApplyConfigRegions -- both writers are covered."
// Unlike the test above (a raw SQL write standing in for a second process),
// this test drives the region change through handleConfigAck -- the actual
// call site (handler.go:322) that invokes Repository.ApplyConfigRegions
// against real SQL -- so both of FR73's named writer surfaces are proven
// against a real database, not just the API-shaped one.
func TestFR73_ProcessorOwnApplyConfigRegions_ReflectedAfterInvalidation(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: crossProcessSchema})
	repo := NewRepository(db.Pool)
	h := NewMessageHandler(discardTestLogger(), repo, NewSensorCache(), nil)

	const deviceID = "leaflab-fr73-ownapply"
	regionOld, regionNew := int64(1), int64(2)
	sensorID := crossProcessFixture(ctx, t, db, deviceID, regionOld)
	h.cache.Set(deviceID, "temp", SensorInfo{SensorID: sensorID, RegionID: &regionOld})

	// A DeviceConfig accepted by the device, assigning this sensor's
	// hardware address to the new region -- ApplyConfigRegions matches by
	// (board_id, i2c_address, mux_path), same as the device manifest path.
	boardID, err := repo.UpsertBoard(ctx, deviceID)
	if err != nil {
		t.Fatalf("UpsertBoard: %v", err)
	}
	cfg := &configpb.DeviceConfig{
		DeviceId: deviceID,
		Version:  5,
		Sensors: []*configpb.SensorConfig{
			{Name: "temp", I2CAddress: 0x23, RegionId: uint32(regionNew), SensorType: firmwarepb.SensorType_SENSOR_TYPE_UNKNOWN},
		},
	}
	configJSON, err := protojson.Marshal(cfg)
	if err != nil {
		t.Fatalf("protojson marshal DeviceConfig: %v", err)
	}
	if err := repo.UpsertDeviceConfig(ctx, boardID, 5, configJSON); err != nil {
		t.Fatalf("UpsertDeviceConfig: %v", err)
	}

	ack := &configpb.DeviceConfigAck{DeviceId: deviceID, AppliedVersion: 5, Accepted: true}
	ackBody, err := proto.Marshal(ack)
	if err != nil {
		t.Fatalf("marshal ack: %v", err)
	}
	// The writer under test: this process's own ApplyConfigRegions, via the
	// real handleConfigAck call site, against real SQL.
	if err := h.handleConfigAck(ctx, deviceID, ackBody); err != nil {
		t.Fatalf("handleConfigAck: %v", err)
	}

	var dbRegion int64
	if err := db.Pool.QueryRow(ctx, `SELECT region_id FROM sensor WHERE sensor_id = $1`, sensorID).Scan(&dbRegion); err != nil {
		t.Fatalf("read back sensor.region_id: %v", err)
	}
	if dbRegion != regionNew {
		t.Fatalf("ApplyConfigRegions did not commit the new region: sensor.region_id = %d, want %d", dbRegion, regionNew)
	}

	// The signal: main.go's Subscriber receiving the event handleConfigAck
	// published for this change (invalidationPub is nil in this test --
	// nothing was actually broadcast -- but the change this event describes
	// really did come from the ApplyConfigRegions call above).
	ApplyInvalidation(h.cache, invalidation.Event{Kind: invalidation.KindRegion, DeviceID: deviceID, SensorID: sensorID, SensorName: "temp"})

	if err := h.handleSensorReading(ctx, deviceID, "temp", marshalReading(t, 22.0, 1000)); err != nil {
		t.Fatalf("handleSensorReading: %v", err)
	}

	gotRegion, _ := lastReadingRegion(ctx, t, db, sensorID)
	if gotRegion == nil || *gotRegion != regionNew {
		t.Fatalf("sensor_reading.region_id = %v, want the NEW region %d written by this process's own ApplyConfigRegions", gotRegion, regionNew)
	}
}

// TestFR73_Defect_StaleCacheOutlivesRegionChange_ReproducedThenFixed
// reproduces defect 2 in the root plan exactly as it exists on `main`
// today -- a cache invalidated by nothing keeps serving the old RegionID to
// every reading until the board reboots and republishes its manifest -- and
// then asserts this branch's fix (an explicit invalidation) closes it.
//
// The first half of this test is deliberately what `main` does: a region
// change commits, but *no* invalidation is ever applied (main has no such
// mechanism at all), so the cache -- still holding the pre-change entry --
// serves it to a reading recorded well after the commit. The second half
// applies the fix this branch adds (ApplyInvalidation, exactly as
// main.go's Subscriber calls it) and re-issues the reading, proving that
// applying it is what makes the next reading correct.
func TestFR73_Defect_StaleCacheOutlivesRegionChange_ReproducedThenFixed(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: crossProcessSchema})
	repo := NewRepository(db.Pool)
	h := NewMessageHandler(discardTestLogger(), repo, NewSensorCache(), nil)

	const deviceID = "leaflab-fr73-defect"
	regionOld, regionNew := int64(1), int64(2)
	sensorID := crossProcessFixture(ctx, t, db, deviceID, regionOld)
	h.cache.Set(deviceID, "temp", SensorInfo{SensorID: sensorID, RegionID: &regionOld})

	// The region change commits (any writer -- direct SQL here, standing in
	// for the API or the processor's own ApplyConfigRegions, both of which
	// are exercised end-to-end by the two tests above).
	if _, err := db.Pool.Exec(ctx, `UPDATE sensor SET region_id = $2 WHERE sensor_id = $1`, sensorID, regionNew); err != nil {
		t.Fatalf("commit region change: %v", err)
	}

	// --- Defect, reproduced: no invalidation happens (this is `main`
	// today -- there is no ApplyInvalidation call on that branch at all).
	// The cache still holds the pre-change entry, so the reading is stamped
	// with the stale region, no matter how long after the commit it
	// arrives, and no matter how many times this repeats -- "until the
	// board reboots and republishes its manifest" per the issue's own
	// framing of the defect.
	if err := h.handleSensorReading(ctx, deviceID, "temp", marshalReading(t, 20.0, 100)); err != nil {
		t.Fatalf("handleSensorReading (defect repro): %v", err)
	}
	gotRegion, _ := lastReadingRegion(ctx, t, db, sensorID)
	if gotRegion == nil || *gotRegion != regionOld {
		t.Fatalf("defect not reproduced: sensor_reading.region_id = %v, want the STALE region %d (main-today behaviour with no invalidation)", gotRegion, regionOld)
	}

	// --- Fix: this branch's mechanism -- an explicit invalidation, exactly
	// as main.go's Subscriber applies one on receiving the cross-process
	// event -- closes the defect.
	ApplyInvalidation(h.cache, invalidation.Event{Kind: invalidation.KindRegion, DeviceID: deviceID, SensorID: sensorID, SensorName: "temp"})

	if err := h.handleSensorReading(ctx, deviceID, "temp", marshalReading(t, 20.5, 200)); err != nil {
		t.Fatalf("handleSensorReading (post-fix): %v", err)
	}
	gotRegion, _ = lastReadingRegion(ctx, t, db, sensorID)
	if gotRegion == nil || *gotRegion != regionNew {
		t.Fatalf("fix did not close the defect: sensor_reading.region_id = %v, want the NEW region %d", gotRegion, regionNew)
	}
}
