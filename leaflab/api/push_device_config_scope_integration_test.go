//go:build integration

// Real-Postgres integration coverage for FR82's config push scope
// semantics: per-entry provenance persistence (device_config_entry,
// migration 028), EDIT-scope materialisation against a real accepted base
// (never a device-reported manifest, FR49), and the stored-payload <->
// scenario-file round trip (FR82.7).
//
// Every scenario here composes the same private handler methods
// PushDeviceConfig itself calls (resolveConfigEntries, resolveRemoveKeys,
// config.Materialise, Repository.InsertDeviceConfigNextVersion,
// Repository.GetLatestAcceptedConfig) directly against a real Repository,
// rather than driving the full RPC end to end: s.publisher is a concrete
// *rmq.Publisher this repo has no in-process fake for, so a push that
// reaches Publish panics on a nil receiver -- see
// push_device_config_invariant_integration_test.go's identical note and
// server_push_device_config_scope_test.go's fakeRepo-level coverage of the
// same scope semantics, which stops short of Publish the same way. That
// coverage already proves PushDeviceConfig's handler wires these pieces
// together in this exact order; this file's job is proving each piece
// behaves correctly against real SQL.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //leaflab/api:push_device_config_scope_integration_test --test_output=all
package main

import (
	"context"
	"os"
	"testing"

	"github.com/bazelbuild/rules_go/go/runfiles"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	configpb "github.com/whale-net/everything/firmware/proto/config"
	pushconfig "github.com/whale-net/everything/leaflab/api/config"
	"github.com/whale-net/everything/leaflab/hwkey"
	"github.com/whale-net/everything/libs/go/dbtest"
)

// scopeSchema is self-contained hand-written DDL (see this package's other
// integration test files' own doc comments on why each keeps its own
// hermetic schema rather than sharing one across go_test targets/binaries):
// board, sensor_type, device_config (migration 007), device_config_entry
// (migration 028) and device_config_removal (migration 031) -- FR82's own
// write surface -- plus a minimal sensor table standing in for a
// device-reported manifest (used only by
// TestEditMaterialisationBase_NeverTheReportedManifest_RealDB) and
// sensor_reading/sensor_name_history so a "kept row" assertion has
// something real to check.
const scopeSchema = `
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

	CREATE TABLE device_config (
		config_id        BIGSERIAL   PRIMARY KEY,
		board_id         BIGINT      NOT NULL REFERENCES board(board_id) ON DELETE RESTRICT,
		version          BIGINT      NOT NULL,
		config_json      JSONB       NOT NULL,
		accepted         BOOLEAN     NOT NULL DEFAULT FALSE,
		pushed_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		acked_at         TIMESTAMPTZ,
		rejection_reason TEXT,
		UNIQUE (board_id, version)
	);

	-- Mirrors migration 028_config_entry_provenance.up.sql exactly.
	CREATE TABLE device_config_entry (
		device_config_entry_id BIGSERIAL PRIMARY KEY,
		config_id      BIGINT NOT NULL REFERENCES device_config(config_id) ON DELETE CASCADE,
		i2c_address    SMALLINT,
		mux_path       JSONB NOT NULL DEFAULT '[]'::jsonb,
		sensor_type_id BIGINT NOT NULL REFERENCES sensor_type(sensor_type_id),
		provenance     TEXT NOT NULL CHECK (provenance IN ('authored', 'materialised'))
	);
	CREATE INDEX idx_device_config_entry_config_id ON device_config_entry(config_id);
	CREATE UNIQUE INDEX idx_device_config_entry_hw_key
		ON device_config_entry(config_id, i2c_address, sensor_type_id, (mux_path::text))
		WHERE i2c_address IS NOT NULL;

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

	-- Stands in for the device-reported manifest FR82.3 says must never
	-- contribute to an EDIT push's materialisation base.
	CREATE TABLE sensor (
		sensor_id      BIGSERIAL PRIMARY KEY,
		board_id       BIGINT NOT NULL REFERENCES board(board_id),
		sensor_type_id BIGINT NOT NULL REFERENCES sensor_type(sensor_type_id),
		name           VARCHAR(128) NOT NULL,
		i2c_address    SMALLINT,
		mux_path       JSONB NOT NULL DEFAULT '[]'::jsonb,
		UNIQUE (board_id, name)
	);

	CREATE TABLE sensor_reading (
		reading_id  BIGSERIAL PRIMARY KEY,
		sensor_id   BIGINT NOT NULL REFERENCES sensor(sensor_id),
		value       DOUBLE PRECISION NOT NULL,
		recorded_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);

	-- Mirrors migration 016_audit_log's column set (schema only, like
	-- dbtest_helpers_integration_test.go's own testSchema) -- needed
	-- because InsertDeviceConfigNextVersion now writes an audit_log row in
	-- the same transaction as the device_config row (FR8, NFR6.2).
	CREATE TABLE audit_log (
		audit_id             BIGSERIAL PRIMARY KEY,
		actor_subject        TEXT NOT NULL,
		actor_kind           TEXT NOT NULL,
		target_household_id  BIGINT NULL,
		action                TEXT NOT NULL,
		entity_kind           TEXT NOT NULL,
		entity_id             TEXT NULL,
		reason                TEXT NULL,
		occurred_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		correlation_id        TEXT NULL
	);
`

func newScopeIntegrationServer(t *testing.T) (*LeafLabAPIServer, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: scopeSchema})
	repo := NewRepository(db.Pool)
	return NewLeafLabAPIServer(repo, stubAuthz{}, nil, nil, nil, nil, discardLogger(), defaultPollIntervalBounds), db.Pool
}

func insertScopeSensorType(t *testing.T, pool *pgxpool.Pool, name, unit string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO sensor_type (name, default_unit) VALUES ($1, $2) RETURNING sensor_type_id`,
		name, unit,
	).Scan(&id); err != nil {
		t.Fatalf("insert sensor_type %s: %v", name, err)
	}
	return id
}

// markAccepted flips a device_config row to accepted=TRUE, standing in for
// AckDeviceConfig (leaflab/processor/repository.go) -- out of scope for
// this package/task, which owns the push side only. GetLatestAcceptedConfig
// only ever returns accepted=TRUE rows (migration 007), so every EDIT-base
// scenario here needs this to make a prior push usable as a base at all.
func markAccepted(t *testing.T, pool *pgxpool.Pool, boardID, version int64) {
	t.Helper()
	tag, err := pool.Exec(context.Background(),
		`UPDATE device_config SET accepted = TRUE WHERE board_id = $1 AND version = $2`,
		boardID, version)
	if err != nil {
		t.Fatalf("mark device_config board=%d version=%d accepted: %v", boardID, version, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("markAccepted board=%d version=%d affected %d rows, want 1", boardID, version, tag.RowsAffected())
	}
}

func countDeviceConfigEntryRows(t *testing.T, pool *pgxpool.Pool, configID int64) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM device_config_entry WHERE config_id = $1`, configID).Scan(&n); err != nil {
		t.Fatalf("count device_config_entry for config_id %d: %v", configID, err)
	}
	return n
}

func provenanceFor(t *testing.T, pool *pgxpool.Pool, configID int64, i2cAddr int16, sensorTypeID int64) string {
	t.Helper()
	var provenance string
	err := pool.QueryRow(context.Background(), `
		SELECT provenance FROM device_config_entry
		WHERE config_id = $1 AND i2c_address = $2 AND sensor_type_id = $3
	`, configID, i2cAddr, sensorTypeID).Scan(&provenance)
	if err != nil {
		t.Fatalf("provenance for config_id=%d i2c=%d type=%d: %v", configID, i2cAddr, sensorTypeID, err)
	}
	return provenance
}

// -- device_config_entry provenance persistence (FR82.4) --------------------

// TestInsertDeviceConfigNextVersion_RecordsProvenancePerEntry proves a
// COMPLETE-shaped write (every entry authored) round-trips through real SQL
// into one device_config_entry row per entry, each carrying the correct
// canonical hardware key and provenance value.
func TestInsertDeviceConfigNextVersion_RecordsProvenancePerEntry(t *testing.T) {
	server, pool := newScopeIntegrationServer(t)
	ctx := context.Background()

	boardID, err := server.repo.GetOrCreateBoard(ctx, "device-a")
	if err != nil {
		t.Fatalf("GetOrCreateBoard: %v", err)
	}
	illuminanceID := insertScopeSensorType(t, pool, "illuminance", "lx")

	sensors := []*configpb.SensorConfig{
		{Name: "light", I2CAddress: 0x23},
	}
	entries, err := server.resolveConfigEntries(ctx, sensors)
	if err != nil {
		t.Fatalf("resolveConfigEntries: %v", err)
	}
	// resolveConfigEntries only resolves a sensor_type from an explicit
	// SensorType enum value on the wire (see its own doc comment); this
	// fixture names the type via sensor_type_id directly instead, mirroring
	// what a caller-visible "sensor_type" field resolution would produce.
	entries[0].Key.SensorTypeID = hwkey.SensorTypeID(illuminanceID)
	entries[0].Provenance = pushconfig.ProvenanceAuthored

	configJSON, err := protojson.Marshal(&configpb.DeviceConfig{DeviceId: "device-a", Sensors: sensors})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}

	version, err := server.repo.InsertDeviceConfigNextVersion(ctx, boardID, configJSON, entries, nil, testAuditEntry())
	if err != nil {
		t.Fatalf("InsertDeviceConfigNextVersion: %v", err)
	}
	if version != 1 {
		t.Fatalf("version = %d, want 1 (first push)", version)
	}

	var configID int64
	if err := pool.QueryRow(ctx, `SELECT config_id FROM device_config WHERE board_id = $1 AND version = $2`, boardID, version).Scan(&configID); err != nil {
		t.Fatalf("look up config_id: %v", err)
	}

	if got := countDeviceConfigEntryRows(t, pool, configID); got != 1 {
		t.Fatalf("device_config_entry rows = %d, want 1", got)
	}
	if got := provenanceFor(t, pool, configID, 0x23, illuminanceID); got != string(pushconfig.ProvenanceAuthored) {
		t.Errorf("provenance = %q, want %q", got, pushconfig.ProvenanceAuthored)
	}
}

// TestInsertDeviceConfigNextVersion_UnresolvedSensorType_SkipsRowStillStoresJSON
// covers this task's documented behavior for a single-virtual chip (e.g.
// BH1750, which carries no explicit sensor_type): the entry is still fully
// present in config_json, but no device_config_entry row is written for it
// (its SensorTypeID is the unresolved sentinel 0, which would violate that
// table's NOT NULL sensor_type_id FK) -- the write must not fail or drop
// the entry from storage either way.
func TestInsertDeviceConfigNextVersion_UnresolvedSensorType_SkipsRowStillStoresJSON(t *testing.T) {
	server, pool := newScopeIntegrationServer(t)
	ctx := context.Background()

	boardID, err := server.repo.GetOrCreateBoard(ctx, "device-a")
	if err != nil {
		t.Fatalf("GetOrCreateBoard: %v", err)
	}

	sensors := []*configpb.SensorConfig{
		{Name: "light", I2CAddress: 0x23, ChipType: configpb.ChipType_CHIP_TYPE_BH1750},
	}
	entries, err := server.resolveConfigEntries(ctx, sensors) // resolveSensorTypeID has no catalog rows at all here
	if err != nil {
		t.Fatalf("resolveConfigEntries: %v", err)
	}
	if entries[0].Key.SensorTypeID != 0 {
		t.Fatalf("SensorTypeID = %d, want the unresolved sentinel 0", entries[0].Key.SensorTypeID)
	}
	entries[0].Provenance = pushconfig.ProvenanceAuthored

	configJSON, err := protojson.Marshal(&configpb.DeviceConfig{DeviceId: "device-a", Sensors: sensors})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}

	version, err := server.repo.InsertDeviceConfigNextVersion(ctx, boardID, configJSON, entries, nil, testAuditEntry())
	if err != nil {
		t.Fatalf("InsertDeviceConfigNextVersion with an unresolved sensor_type returned an error, want success: %v", err)
	}

	var configID int64
	var storedJSON []byte
	if err := pool.QueryRow(ctx, `SELECT config_id, config_json FROM device_config WHERE board_id = $1 AND version = $2`, boardID, version).Scan(&configID, &storedJSON); err != nil {
		t.Fatalf("look up stored config: %v", err)
	}

	if got := countDeviceConfigEntryRows(t, pool, configID); got != 0 {
		t.Errorf("device_config_entry rows = %d, want 0 -- an unresolved sensor_type must not get a provenance row", got)
	}

	var stored configpb.DeviceConfig
	if err := protojson.Unmarshal(storedJSON, &stored); err != nil {
		t.Fatalf("unmarshal stored config_json: %v", err)
	}
	if len(stored.Sensors) != 1 || stored.Sensors[0].Name != "light" {
		t.Errorf("stored sensors = %+v, want the 'light' entry still present despite its unresolved sensor_type", stored.Sensors)
	}
}

// -- device_config_removal persistence (FR82.4/FR82.6) -----------------------

// TestInsertDeviceConfigNextVersion_RecordsRemovalPerEntry proves an
// EDIT-scope push's config.Materialise Result.Removed round-trips through
// real SQL into one device_config_removal row per dropped entry (migration
// 031), each carrying the correct canonical hardware key and RemoveForm --
// the bookkeeping leaflab/processor's CloseRemovedSensorHWHistory
// (FR82.6) later consults at ack time.
func TestInsertDeviceConfigNextVersion_RecordsRemovalPerEntry(t *testing.T) {
	server, pool := newScopeIntegrationServer(t)
	ctx := context.Background()

	boardID, err := server.repo.GetOrCreateBoard(ctx, "device-a")
	if err != nil {
		t.Fatalf("GetOrCreateBoard: %v", err)
	}
	temperatureID := insertScopeSensorType(t, pool, "temperature", "degC")
	humidityID := insertScopeSensorType(t, pool, "humidity", "%")

	base := []pushconfig.Entry{
		{
			Key:    hwkey.Key{I2CAddress: hwkey.Address(0x44), SensorTypeID: hwkey.SensorTypeID(temperatureID)},
			Sensor: &configpb.SensorConfig{Name: "temperature", I2CAddress: 0x44},
		},
		{
			Key:    hwkey.Key{I2CAddress: hwkey.Address(0x44), SensorTypeID: hwkey.SensorTypeID(humidityID)},
			Sensor: &configpb.SensorConfig{Name: "humidity", I2CAddress: 0x44},
		},
	}
	// Chip-key remove: drops both temperature and humidity at 0x44.
	result, err := pushconfig.Materialise(base, nil, []pushconfig.RemoveKey{{
		Chip:          base[0].Key.Chip(),
		HasSensorType: false,
	}})
	if err != nil {
		t.Fatalf("Materialise: %v", err)
	}
	if len(result.Entries) != 0 || len(result.Removed) != 2 {
		t.Fatalf("Materialise result = %+v, want both entries dropped", result)
	}

	configJSON, err := protojson.Marshal(&configpb.DeviceConfig{DeviceId: "device-a"})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	version, err := server.repo.InsertDeviceConfigNextVersion(ctx, boardID, configJSON, result.Entries, result.Removed, testAuditEntry())
	if err != nil {
		t.Fatalf("InsertDeviceConfigNextVersion: %v", err)
	}

	var configID int64
	if err := pool.QueryRow(ctx, `SELECT config_id FROM device_config WHERE board_id = $1 AND version = $2`, boardID, version).Scan(&configID); err != nil {
		t.Fatalf("look up config_id: %v", err)
	}

	rows, err := pool.Query(ctx, `
		SELECT i2c_address, sensor_type_id, form FROM device_config_removal WHERE config_id = $1 ORDER BY sensor_type_id
	`, configID)
	if err != nil {
		t.Fatalf("query device_config_removal: %v", err)
	}
	defer rows.Close()

	type removalRow struct {
		i2cAddress   int16
		sensorTypeID int64
		form         string
	}
	var got []removalRow
	for rows.Next() {
		var r removalRow
		if err := rows.Scan(&r.i2cAddress, &r.sensorTypeID, &r.form); err != nil {
			t.Fatalf("scan device_config_removal row: %v", err)
		}
		got = append(got, r)
	}

	if len(got) != 2 {
		t.Fatalf("device_config_removal rows = %d, want 2", len(got))
	}
	for _, r := range got {
		if r.i2cAddress != 0x44 {
			t.Errorf("i2c_address = %d, want 0x44", r.i2cAddress)
		}
		if r.form != "chip_key" {
			t.Errorf("form = %q, want %q (both dropped by the same chip-key remove)", r.form, "chip_key")
		}
	}
	if got[0].sensorTypeID != temperatureID && got[1].sensorTypeID != temperatureID {
		t.Errorf("device_config_removal rows = %+v, want one naming temperature's sensor_type_id %d", got, temperatureID)
	}
	if got[0].sensorTypeID != humidityID && got[1].sensorTypeID != humidityID {
		t.Errorf("device_config_removal rows = %+v, want one naming humidity's sensor_type_id %d", got, humidityID)
	}
}

// TestInsertDeviceConfigNextVersion_RemovalUnresolvedSensorType_SkipsRowStillSucceeds
// mirrors TestInsertDeviceConfigNextVersion_UnresolvedSensorType_SkipsRowStillStoresJSON
// for the removal side: a removed entry whose SensorTypeID is the
// unresolved-catalog-type sentinel (0) would violate device_config_removal's
// sensor_type_id NOT NULL FK, so it is skipped rather than failing the
// whole write.
func TestInsertDeviceConfigNextVersion_RemovalUnresolvedSensorType_SkipsRowStillSucceeds(t *testing.T) {
	server, pool := newScopeIntegrationServer(t)
	ctx := context.Background()

	boardID, err := server.repo.GetOrCreateBoard(ctx, "device-a")
	if err != nil {
		t.Fatalf("GetOrCreateBoard: %v", err)
	}

	removed := []pushconfig.RemovedEntry{
		{
			Entry: pushconfig.Entry{
				Key:    hwkey.Key{I2CAddress: hwkey.Address(0x23), SensorTypeID: hwkey.SensorTypeID(0)},
				Sensor: &configpb.SensorConfig{Name: "light", I2CAddress: 0x23},
			},
			Form: pushconfig.RemoveFormFullKey,
		},
	}

	configJSON, err := protojson.Marshal(&configpb.DeviceConfig{DeviceId: "device-a"})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	version, err := server.repo.InsertDeviceConfigNextVersion(ctx, boardID, configJSON, nil, removed, testAuditEntry())
	if err != nil {
		t.Fatalf("InsertDeviceConfigNextVersion with an unresolved removal sensor_type returned an error, want success: %v", err)
	}

	var configID int64
	if err := pool.QueryRow(ctx, `SELECT config_id FROM device_config WHERE board_id = $1 AND version = $2`, boardID, version).Scan(&configID); err != nil {
		t.Fatalf("look up config_id: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM device_config_removal WHERE config_id = $1`, configID).Scan(&n); err != nil {
		t.Fatalf("count device_config_removal: %v", err)
	}
	if n != 0 {
		t.Errorf("device_config_removal rows = %d, want 0 -- an unresolved sensor_type must not get a removal row", n)
	}
}

// -- GetLatestAcceptedConfig: only accepted=TRUE (the EDIT base) -----------

// TestGetLatestAcceptedConfig_OnlyAcceptedRows proves a higher-versioned
// but not-yet-accepted push never becomes the EDIT materialisation base --
// GetLatestAcceptedConfig must return the highest *accepted* version, not
// simply the highest version.
func TestGetLatestAcceptedConfig_OnlyAcceptedRows(t *testing.T) {
	server, pool := newScopeIntegrationServer(t)
	ctx := context.Background()

	boardID, err := server.repo.GetOrCreateBoard(ctx, "device-a")
	if err != nil {
		t.Fatalf("GetOrCreateBoard: %v", err)
	}

	v1JSON, _ := protojson.Marshal(&configpb.DeviceConfig{DeviceId: "device-a", Sensors: []*configpb.SensorConfig{{Name: "v1"}}})
	v1, err := server.repo.InsertDeviceConfigNextVersion(ctx, boardID, v1JSON, nil, nil, testAuditEntry())
	if err != nil {
		t.Fatalf("insert v1: %v", err)
	}
	markAccepted(t, pool, boardID, v1)

	v2JSON, _ := protojson.Marshal(&configpb.DeviceConfig{DeviceId: "device-a", Sensors: []*configpb.SensorConfig{{Name: "v2-pending"}}})
	if _, err := server.repo.InsertDeviceConfigNextVersion(ctx, boardID, v2JSON, nil, nil, testAuditEntry()); err != nil {
		t.Fatalf("insert v2: %v", err)
	}
	// v2 deliberately left un-acked (accepted=FALSE, the column default).

	got, err := server.repo.GetLatestAcceptedConfig(ctx, "device-a")
	if err != nil {
		t.Fatalf("GetLatestAcceptedConfig: %v", err)
	}
	if got == nil {
		t.Fatal("GetLatestAcceptedConfig returned nil, want v1")
	}
	if len(got.Sensors) != 1 || got.Sensors[0].Name != "v1" {
		t.Errorf("GetLatestAcceptedConfig = %+v, want v1's payload -- v2 is not yet accepted and must not be used as an EDIT base", got.Sensors)
	}
}

// TestGetLatestAcceptedConfig_NoRowsAtAll_ReturnsNil is FR82.3's "no
// accepted config" condition at the repository layer: a board with no
// device_config rows at all (not even a pending one) gets nil, nil -- the
// signal PushDeviceConfig turns into config.Materialise's ErrNoAcceptedConfig.
func TestGetLatestAcceptedConfig_NoRowsAtAll_ReturnsNil(t *testing.T) {
	server, _ := newScopeIntegrationServer(t)
	ctx := context.Background()

	if _, err := server.repo.GetOrCreateBoard(ctx, "device-a"); err != nil {
		t.Fatalf("GetOrCreateBoard: %v", err)
	}

	got, err := server.repo.GetLatestAcceptedConfig(ctx, "device-a")
	if err != nil {
		t.Fatalf("GetLatestAcceptedConfig: %v", err)
	}
	if got != nil {
		t.Errorf("GetLatestAcceptedConfig = %+v, want nil", got)
	}
}

// -- EDIT materialisation base: real accepted config, never the manifest --

// TestEditMaterialisationBase_NeverTheReportedManifest_RealDB constructs a
// board whose `sensor` table (standing in for a device-reported manifest)
// disagrees with its accepted device_config, and proves an EDIT push's
// materialised entries come from the accepted config alone: this package's
// EDIT path (resolveConfigEntries + GetLatestAcceptedConfig +
// config.Materialise) never queries the `sensor` table at all.
func TestEditMaterialisationBase_NeverTheReportedManifest_RealDB(t *testing.T) {
	server, pool := newScopeIntegrationServer(t)
	ctx := context.Background()

	boardID, err := server.repo.GetOrCreateBoard(ctx, "device-a")
	if err != nil {
		t.Fatalf("GetOrCreateBoard: %v", err)
	}
	illuminanceID := insertScopeSensorType(t, pool, "illuminance", "lx")

	// The accepted config says "accepted-light".
	acceptedJSON, _ := protojson.Marshal(&configpb.DeviceConfig{
		DeviceId: "device-a",
		Sensors:  []*configpb.SensorConfig{{Name: "accepted-light", I2CAddress: 0x23}},
	})
	v1, err := server.repo.InsertDeviceConfigNextVersion(ctx, boardID, acceptedJSON, nil, nil, testAuditEntry())
	if err != nil {
		t.Fatalf("insert accepted config: %v", err)
	}
	markAccepted(t, pool, boardID, v1)

	// The device-reported manifest disagrees: same hardware key, different
	// name. If EDIT's base ever leaked from here, the materialised entry
	// below would be "manifest-light".
	if _, err := pool.Exec(ctx, `
		INSERT INTO sensor (board_id, sensor_type_id, name, i2c_address) VALUES ($1, $2, $3, $4)
	`, boardID, illuminanceID, "manifest-light", int16(0x23)); err != nil {
		t.Fatalf("insert manifest sensor: %v", err)
	}

	baseCfg, err := server.repo.GetLatestAcceptedConfig(ctx, "device-a")
	if err != nil {
		t.Fatalf("GetLatestAcceptedConfig: %v", err)
	}
	base, err := server.resolveConfigEntries(ctx, baseCfg.Sensors)
	if err != nil {
		t.Fatalf("resolveConfigEntries(base): %v", err)
	}

	result, err := pushconfig.Materialise(base, nil, nil) // no adds/removes: everything carries forward
	if err != nil {
		t.Fatalf("Materialise: %v", err)
	}
	if len(result.Entries) != 1 || result.Entries[0].Sensor.Name != "accepted-light" {
		t.Fatalf("materialised entries = %+v, want only 'accepted-light' -- the manifest must contribute nothing", result.Entries)
	}
}

// -- Round trip: stored payload <-> scenario file (FR82.7) ------------------

// TestScenarioFile_RoundTrip_RealDB loads a real
// leaflab/scripts/scenarios/*.json fixture, pushes it scope=COMPLETE
// (mirroring PushDeviceConfig's COMPLETE branch exactly: entries=adds,
// sensorsForStorage=req.Sensors unchanged), reads the stored config back,
// and asserts the sensors array is unchanged -- FR82.7's "a stored payload
// is accepted verbatim as a scenario file's sensors array, and vice versa"
// and FR82.2's "a scenario file pushed with scope=COMPLETE behaves
// identically before and after this requirement".
func TestScenarioFile_RoundTrip_RealDB(t *testing.T) {
	path, err := runfiles.Rlocation("_main/leaflab/scripts/scenarios/light-temp-humi-mux.json")
	if err != nil {
		t.Fatalf("runfiles.Rlocation(light-temp-humi-mux.json): %v (is it listed in this target's data attribute?)", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read scenario file: %v", err)
	}

	// The scenario file's top level (description/hardware/sensors) is not
	// itself a configpb message; only its "sensors" array is protojson-
	// shaped as []configpb.SensorConfig. Unmarshalling the whole document
	// as a DeviceConfig with DiscardUnknown skips description/hardware and
	// populates Sensors from the matching field.
	var scenario configpb.DeviceConfig
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, &scenario); err != nil {
		t.Fatalf("unmarshal scenario file as DeviceConfig: %v", err)
	}
	if len(scenario.Sensors) == 0 {
		t.Fatal("scenario file has no sensors -- fixture or unmarshalling is broken")
	}

	server, pool := newScopeIntegrationServer(t)
	ctx := context.Background()

	boardID, err := server.repo.GetOrCreateBoard(ctx, "device-a")
	if err != nil {
		t.Fatalf("GetOrCreateBoard: %v", err)
	}
	// The scenario's sensor_type-bearing entries (SHT3x temperature/
	// humidity) need catalog rows for resolveConfigEntries to resolve
	// them; the BH1750 entry has no sensor_type at all and resolves to the
	// documented sentinel regardless.
	insertScopeSensorType(t, pool, "temperature", "degC")
	insertScopeSensorType(t, pool, "humidity", "pct")

	// COMPLETE's own branch (server.go): entries=adds, all authored,
	// sensorsForStorage=req.Sensors verbatim.
	entries, err := server.resolveConfigEntries(ctx, scenario.Sensors)
	if err != nil {
		t.Fatalf("resolveConfigEntries: %v", err)
	}
	for i := range entries {
		entries[i].Provenance = pushconfig.ProvenanceAuthored
	}

	configJSON, err := protojson.Marshal(&configpb.DeviceConfig{DeviceId: "device-a", Sensors: scenario.Sensors})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	version, err := server.repo.InsertDeviceConfigNextVersion(ctx, boardID, configJSON, entries, nil, testAuditEntry())
	if err != nil {
		t.Fatalf("InsertDeviceConfigNextVersion: %v", err)
	}
	markAccepted(t, pool, boardID, version)

	stored, err := server.repo.GetLatestAcceptedConfig(ctx, "device-a")
	if err != nil {
		t.Fatalf("GetLatestAcceptedConfig: %v", err)
	}
	if len(stored.Sensors) != len(scenario.Sensors) {
		t.Fatalf("stored sensors count = %d, want %d", len(stored.Sensors), len(scenario.Sensors))
	}
	for i := range scenario.Sensors {
		if !proto.Equal(stored.Sensors[i], scenario.Sensors[i]) {
			t.Errorf("stored.Sensors[%d] = %v, want %v (byte-usable round trip)", i, stored.Sensors[i], scenario.Sensors[i])
		}
	}
}

// -- FR82.6: a dropped sensor keeps its row and readings ---------------------

// TestDroppedSensor_RowAndReadingsUntouched_RealDB proves this task's own
// write path (InsertDeviceConfigNextVersion / device_config_entry) never
// issues a DELETE against `sensor` or `sensor_reading` when an entry is
// dropped from a config version -- removal is removal from desired state,
// not deletion (FR82.6). This does NOT cover FR82.6's "hardware-history
// interval is closed at the accepted-at time of the version that dropped
// it": the push side now persists device_config_removal (migration 031)
// so the device's ack has something to consult, but closing the interval
// itself happens in leaflab/processor's AckDeviceConfig/
// CloseRemovedSensorHWHistory path, which is out of this package's own
// integration coverage -- see leaflab/processor's own tests.
func TestDroppedSensor_RowAndReadingsUntouched_RealDB(t *testing.T) {
	server, pool := newScopeIntegrationServer(t)
	ctx := context.Background()

	boardID, err := server.repo.GetOrCreateBoard(ctx, "device-a")
	if err != nil {
		t.Fatalf("GetOrCreateBoard: %v", err)
	}
	illuminanceID := insertScopeSensorType(t, pool, "illuminance", "lx")

	var sensorID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO sensor (board_id, sensor_type_id, name, i2c_address) VALUES ($1, $2, $3, $4)
		RETURNING sensor_id
	`, boardID, illuminanceID, "light", int16(0x23)).Scan(&sensorID); err != nil {
		t.Fatalf("insert sensor: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO sensor_reading (sensor_id, value) VALUES ($1, $2)`, sensorID, 42.0); err != nil {
		t.Fatalf("insert reading: %v", err)
	}

	// An accepted config, then an EDIT push that drops "light" entirely
	// (full canonical key remove).
	acceptedJSON, _ := protojson.Marshal(&configpb.DeviceConfig{
		DeviceId: "device-a",
		Sensors:  []*configpb.SensorConfig{{Name: "light", I2CAddress: 0x23}},
	})
	v1, err := server.repo.InsertDeviceConfigNextVersion(ctx, boardID, acceptedJSON, nil, nil, testAuditEntry())
	if err != nil {
		t.Fatalf("insert accepted config: %v", err)
	}
	markAccepted(t, pool, boardID, v1)

	baseCfg, err := server.repo.GetLatestAcceptedConfig(ctx, "device-a")
	if err != nil {
		t.Fatalf("GetLatestAcceptedConfig: %v", err)
	}
	base, err := server.resolveConfigEntries(ctx, baseCfg.Sensors)
	if err != nil {
		t.Fatalf("resolveConfigEntries(base): %v", err)
	}
	for i := range base {
		base[i].Key.SensorTypeID = hwkey.SensorTypeID(illuminanceID)
	}
	result, err := pushconfig.Materialise(base, nil, []pushconfig.RemoveKey{{
		Chip:          base[0].Key.Chip(),
		SensorTypeID:  base[0].Key.SensorTypeID,
		HasSensorType: true,
	}})
	if err != nil {
		t.Fatalf("Materialise: %v", err)
	}
	if len(result.Entries) != 0 || len(result.Removed) != 1 {
		t.Fatalf("Materialise result = %+v, want the one entry dropped", result)
	}

	editJSON, _ := protojson.Marshal(&configpb.DeviceConfig{DeviceId: "device-a", Sensors: nil})
	if _, err := server.repo.InsertDeviceConfigNextVersion(ctx, boardID, editJSON, result.Entries, result.Removed, testAuditEntry()); err != nil {
		t.Fatalf("insert edit config: %v", err)
	}

	var sensorCount, readingCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM sensor WHERE sensor_id = $1`, sensorID).Scan(&sensorCount); err != nil {
		t.Fatalf("count sensor: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM sensor_reading WHERE sensor_id = $1`, sensorID).Scan(&readingCount); err != nil {
		t.Fatalf("count sensor_reading: %v", err)
	}
	if sensorCount != 1 {
		t.Errorf("sensor rows for dropped sensor = %d, want 1 (kept, not deleted)", sensorCount)
	}
	if readingCount != 1 {
		t.Errorf("sensor_reading rows for dropped sensor = %d, want 1 (kept, not deleted)", readingCount)
	}
}
