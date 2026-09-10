//go:build integration

// This file only builds under the "integration" build tag, same as
// convergence_integration_test.go -- see that file's doc comment for why
// (Docker-less machines, `bazel test //...` never even compiling it, let
// alone running it).
//
// M3 NFR4 test (#2314): the device-config push/ack path in
// leaflab/processor never creates or closes a sensor_region_history row and
// never touches sensor.region_id, no matter what region value the config
// carried on the wire. SensorConfig.region_id stays present on the wire
// (NFR3: the device ignores it), but the ack is not a placement write --
// leaflab-api's PlaceSensor is the sole placement writer, so a placement
// change takes effect without any device round trip.
//
// The test drives the real MessageHandler.Handle with the real routing keys
// and the real Repository against the real migrations (newLeafLabTestPool
// in testdb_integration_test.go), so it exercises exactly the code path a
// device ack flows through in production. If a placement write ever
// reappears on the ack path (regression of the deleted ApplyConfigRegions),
// the placement assertions here fail -- that is the red half of this test's
// red/green contract.
package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	firmwarepb "github.com/whale-net/everything/firmware/proto"
	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/libs/go/rmq"
	"google.golang.org/protobuf/proto"
)

// seedNFR4Region inserts a region and returns its region_id. Named
// distinctly from the api package's seedPlacementRegion so the two
// integration packages stay independent.
func seedNFR4Region(t *testing.T, pool *pgxpool.Pool, name string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO region (name) VALUES ($1) RETURNING region_id`, name).Scan(&id); err != nil {
		t.Fatalf("seed region %s: %v", name, err)
	}
	return id
}

// placeSensorDirectly seeds a placement the way leaflab-api's PlaceSensor
// would: one open sensor_region_history row plus the sensor.region_id
// mirror, written with the same three statements (raw SQL, since
// processor_lib cannot import the api's package-main Repository -- same
// precedent as convergence_integration_test.go's
// insertUserInitiatedConfigNextVersion copying leaflab-api's statement
// verbatim).
func placeSensorDirectly(t *testing.T, pool *pgxpool.Pool, sensorID, regionID int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		UPDATE sensor_region_history SET valid_to = NOW()
		WHERE sensor_id = $1 AND valid_to IS NULL
	`, sensorID); err != nil {
		t.Fatalf("close open sensor_region_history for sensor %d: %v", sensorID, err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO sensor_region_history (sensor_id, region_id) VALUES ($1, $2)`,
		sensorID, regionID); err != nil {
		t.Fatalf("insert sensor_region_history for sensor %d: %v", sensorID, err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE sensor SET region_id = $2 WHERE sensor_id = $1`, sensorID, regionID); err != nil {
		t.Fatalf("set sensor.region_id for sensor %d: %v", sensorID, err)
	}
}

// nfr4Placement is the full placement state NFR4 must leave untouched:
// every sensor_region_history row for the sensor (region + open/closed),
// the open row's region (nil = none open), and the sensor.region_id mirror.
type nfr4Placement struct {
	rows   []nfr4HistoryRow
	open   []int64
	mirror *int64
}

type nfr4HistoryRow struct {
	regionID int64
	validTo  *time.Time
}

// readNFR4Placement snapshots the sensor's placement state.
func readNFR4Placement(t *testing.T, pool *pgxpool.Pool, sensorID int64) nfr4Placement {
	t.Helper()
	ctx := context.Background()
	var openRegion int64

	var p nfr4Placement
	rows, err := pool.Query(ctx, `
		SELECT region_id, valid_to FROM sensor_region_history
		WHERE sensor_id = $1 ORDER BY valid_from, history_id
	`, sensorID)
	if err != nil {
		t.Fatalf("query sensor_region_history for sensor %d: %v", sensorID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var r nfr4HistoryRow
		if err := rows.Scan(&r.regionID, &r.validTo); err != nil {
			t.Fatalf("scan sensor_region_history row: %v", err)
		}
		p.rows = append(p.rows, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate sensor_region_history: %v", err)
	}

	if err := pool.QueryRow(ctx, `
		SELECT region_id FROM sensor_region_history
		WHERE sensor_id = $1 AND valid_to IS NULL
	`, sensorID).Scan(&openRegion); err == nil {
		p.open = []int64{openRegion}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("query open sensor_region_history for sensor %d: %v", sensorID, err)
	}

	if err := pool.QueryRow(ctx,
		`SELECT region_id FROM sensor WHERE sensor_id = $1`, sensorID).Scan(&p.mirror); err != nil {
		t.Fatalf("read sensor.region_id for sensor %d: %v", sensorID, err)
	}
	return p
}

// TestConfigPushAndAck_RegionOnWire_NeverTouchesPlacement_NFR4: a
// device_config push and its ack, carrying a SensorConfig whose region_id
// points at a *different* region than the sensor's actual placement, must
// not create or close any sensor_region_history row and must not move the
// sensor.region_id mirror -- on push, on accepted ack, or on rejected ack.
// The ack must still record the acceptance (so the placement assertion is
// not vacuously green), and readings written after the ack must still
// attribute to the region PlaceSensor chose.
func TestConfigPushAndAck_RegionOnWire_NeverTouchesPlacement_NFR4(t *testing.T) {
	repo, pool := newProcessorIntegrationTestDB(t)
	ctx := context.Background()

	deviceID := "leaflab-nfr4-ack"
	regionA := seedNFR4Region(t, pool, "nfr4-shelf-a")
	regionB := seedNFR4Region(t, pool, "nfr4-shelf-b")

	boardID := seedProcessorBoard(t, pool, deviceID)
	sensorID := seedProcessorSensor(t, pool, boardID, "soil-nfr4")
	// Give the sensor a hardware identity matching the pushed SensorConfig,
	// exactly what the retired ack-path writer (ApplyConfigRegions) matched
	// sensors by -- so if that write path ever comes back, this test sees it.
	const i2cAddress = 0x38
	if _, err := pool.Exec(ctx,
		`UPDATE sensor SET i2c_address = $2, mux_path = '[]'::jsonb WHERE sensor_id = $1`,
		sensorID, i2cAddress); err != nil {
		t.Fatalf("set sensor hardware identity: %v", err)
	}

	// The sensor's real placement: region A, via the sole placement writer's
	// SCD2 shape (open history row + mirror).
	placeSensorDirectly(t, pool, sensorID, regionA)
	before := readNFR4Placement(t, pool, sensorID)
	if len(before.open) != 1 || before.open[0] != regionA {
		t.Fatalf("fixture: expected sensor placed in region %d, got open rows %v", regionA, before.open)
	}

	// The wire config says region B (a value that would rewrite placement if
	// the ack path were still a placement writer).
	wireConfig := &configpb.DeviceConfig{
		DeviceId: deviceID,
		Version:  1,
		Sensors: []*configpb.SensorConfig{{
			Name:       "soil-nfr4",
			I2CAddress: i2cAddress,
			SensorType: firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE,
			RegionId:   uint32(regionB),
			Enabled:    proto.Bool(true),
		}},
	}
	configBody, err := proto.Marshal(wireConfig)
	if err != nil {
		t.Fatalf("marshal DeviceConfig: %v", err)
	}

	handler := NewMessageHandler(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		repo, NewSensorCache(), nil)

	// 1. The push itself records the config but must not touch placement.
	if err := handler.Handle(ctx, rmq.Message{
		RoutingKey: "leaflab." + deviceID + ".config",
		Body:       configBody,
	}); err != nil {
		t.Fatalf("Handle(config push): %v", err)
	}
	if got := readNFR4Placement(t, pool, sensorID); !nfr4PlacementEqual(before, got) {
		t.Fatalf("config push changed placement: before %+v, after %+v", before, got)
	}
	var pushed int64
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM device_config WHERE board_id = $1 AND version = 1`, boardID).Scan(&pushed); err != nil {
		t.Fatalf("read device_config row for version 1: %v", err)
	}
	if pushed != 1 {
		t.Fatalf("pushed config not recorded (device_config rows for version 1: %d)", pushed)
	}

	// 2. The device accepts the config (its ack carries no placement data;
	// the old writer acted here). The ack must record acceptance and still
	// not touch placement.
	ackBody, err := proto.Marshal(&configpb.DeviceConfigAck{
		DeviceId:       deviceID,
		AppliedVersion: 1,
		Accepted:       true,
	})
	if err != nil {
		t.Fatalf("marshal DeviceConfigAck: %v", err)
	}
	if err := handler.Handle(ctx, rmq.Message{
		RoutingKey: "leaflab." + deviceID + ".config.ack",
		Body:       ackBody,
	}); err != nil {
		t.Fatalf("Handle(config ack): %v", err)
	}
	var accepted bool
	if err := pool.QueryRow(ctx,
		`SELECT accepted FROM device_config WHERE board_id = $1 AND version = 1`, boardID).Scan(&accepted); err != nil {
		t.Fatalf("read device_config acceptance: %v", err)
	}
	if !accepted {
		t.Fatal("accepted ack was not recorded on device_config -- placement assertions would be vacuous")
	}
	if got := readNFR4Placement(t, pool, sensorID); !nfr4PlacementEqual(before, got) {
		t.Fatalf("accepted config ack changed placement: before %+v, after %+v", before, got)
	}

	// 3. A rejected ack with the same region on the wire: still no placement
	// write (NFR4 -- "regardless of the region value carried on the wire").
	wireConfig.Version = 2
	configBody2, err := proto.Marshal(wireConfig)
	if err != nil {
		t.Fatalf("marshal DeviceConfig v2: %v", err)
	}
	if err := handler.Handle(ctx, rmq.Message{
		RoutingKey: "leaflab." + deviceID + ".config",
		Body:       configBody2,
	}); err != nil {
		t.Fatalf("Handle(config push v2): %v", err)
	}
	ackBody2, err := proto.Marshal(&configpb.DeviceConfigAck{
		DeviceId:       deviceID,
		AppliedVersion: 2,
		Accepted:       false,
		Reason:         "device rejected the push",
	})
	if err != nil {
		t.Fatalf("marshal rejected DeviceConfigAck: %v", err)
	}
	if err := handler.Handle(ctx, rmq.Message{
		RoutingKey: "leaflab." + deviceID + ".config.ack",
		Body:       ackBody2,
	}); err != nil {
		t.Fatalf("Handle(rejected config ack): %v", err)
	}
	if got := readNFR4Placement(t, pool, sensorID); !nfr4PlacementEqual(before, got) {
		t.Fatalf("rejected config ack changed placement: before %+v, after %+v", before, got)
	}

	// 4. Region B never appears anywhere in the sensor's placement history,
	// and readings continue to attribute to region A (the placement the sole
	// writer chose) after the whole push/ack cycle.
	var regionBHistoryCount int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM sensor_region_history WHERE sensor_id = $1 AND region_id = $2`,
		sensorID, regionB).Scan(&regionBHistoryCount); err != nil {
		t.Fatalf("count region-B history rows: %v", err)
	}
	if regionBHistoryCount != 0 {
		t.Fatalf("region %d leaked into sensor_region_history (%d rows)", regionB, regionBHistoryCount)
	}

	if err := repo.InsertReading(ctx, sensorID, 21.5, true, 120, time.Now(), nil); err != nil {
		t.Fatalf("InsertReading after acks: %v", err)
	}
	var readingRegion *int64
	if err := pool.QueryRow(ctx,
		`SELECT region_id FROM sensor_reading WHERE sensor_id = $1`, sensorID).Scan(&readingRegion); err != nil {
		t.Fatalf("read post-ack reading: %v", err)
	}
	if readingRegion == nil || *readingRegion != regionA {
		t.Fatalf("post-ack reading attributed to %v, want region %d (ack must not retarget attribution)", readingRegion, regionA)
	}
}

// nfr4PlacementEqual compares two placement snapshots, including the nil
// mirror case.
func nfr4PlacementEqual(a, b nfr4Placement) bool {
	if len(a.rows) != len(b.rows) || len(a.open) != len(b.open) {
		return false
	}
	for i := range a.rows {
		if a.rows[i].regionID != b.rows[i].regionID {
			return false
		}
		if (a.rows[i].validTo == nil) != (b.rows[i].validTo == nil) {
			return false
		}
		if a.rows[i].validTo != nil && !a.rows[i].validTo.Equal(*b.rows[i].validTo) {
			return false
		}
	}
	for i := range a.open {
		if a.open[i] != b.open[i] {
			return false
		}
	}
	if (a.mirror == nil) != (b.mirror == nil) {
		return false
	}
	return a.mirror == nil || *a.mirror == *b.mirror
}

// TestInsertReading_AttributesAtInsertTime_FR8_FR9 guards the production
// reader half of FR8/FR9: InsertReading resolves the reading's region from
// the sensor row's *current* placement at insert time, so a placement move
// (leaflab-api PlaceSensor's write, mirrored here raw-SQL since processor_lib
// and the api are separate package-main binaries) attributes every reading
// written after it to the new region immediately, while readings written
// before it keep the region they were stamped with. The api package's
// TestPlaceSensor_Move_ReadingAttribution_FR8_FR9 proves this against the
// real PlaceSensor writer with a verbatim copy of this INSERT; this test
// proves it against the real InsertReading with the move seeded raw --
// together they cover both production halves of the pair.
func TestInsertReading_AttributesAtInsertTime_FR8_FR9(t *testing.T) {
	repo, pool := newProcessorIntegrationTestDB(t)
	ctx := context.Background()

	boardID := seedProcessorBoard(t, pool, "leaflab-attr-fr9")
	sensorID := seedProcessorSensor(t, pool, boardID, "attr-fr9")
	regionA := seedNFR4Region(t, pool, "attr-fr9-a")
	regionB := seedNFR4Region(t, pool, "attr-fr9-b")

	base := time.Now()

	// Unplaced: a reading carries no region at all.
	if err := repo.InsertReading(ctx, sensorID, 1.0, true, 10, base, nil); err != nil {
		t.Fatalf("InsertReading (unplaced): %v", err)
	}

	// Placed in region A: the next reading stamps region A.
	placeSensorDirectly(t, pool, sensorID, regionA)
	if err := repo.InsertReading(ctx, sensorID, 2.0, true, 20, base.Add(time.Minute), nil); err != nil {
		t.Fatalf("InsertReading (region A): %v", err)
	}

	// Move to region B (the raw-SQL shape of api's PlaceSensor close-and-open
	// plus mirror): the very next reading must attribute to B with no device
	// round trip (FR9).
	placeSensorDirectly(t, pool, sensorID, regionB)
	if err := repo.InsertReading(ctx, sensorID, 3.0, true, 30, base.Add(2*time.Minute), nil); err != nil {
		t.Fatalf("InsertReading (region B): %v", err)
	}

	rows, err := pool.Query(ctx, `
		SELECT region_id FROM sensor_reading
		WHERE sensor_id = $1
		ORDER BY recorded_at, reading_id
	`, sensorID)
	if err != nil {
		t.Fatalf("query readings: %v", err)
	}
	defer rows.Close()
	var got []*int64
	for rows.Next() {
		var r *int64
		if err := rows.Scan(&r); err != nil {
			t.Fatalf("scan reading region: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate readings: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 readings, got %d", len(got))
	}
	if got[0] != nil {
		t.Fatalf("reading before any placement has region %v, want nil", *got[0])
	}
	if got[1] == nil || *got[1] != regionA {
		t.Fatalf("reading written before the move attributed to %v, want region %d (FR8: placement changes never rewrite old readings)", got[1], regionA)
	}
	if got[2] == nil || *got[2] != regionB {
		t.Fatalf("reading written after the move attributed to %v, want region %d (FR9: attribution must reflect the move immediately)", got[2], regionB)
	}
}
