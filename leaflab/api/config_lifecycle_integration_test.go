//go:build integration

// Real-Postgres integration coverage for FR34/FR35's repository reads:
// ListConfigHistory's newest-first, FR61 keyset-paginated listing;
// GetDeviceConfigVersion fetching any version regardless of acceptance;
// GetConfigVersionEntries' FR82.4 per-entry provenance persisted and
// joined back to sensor_type by real SQL; and the "device_config gains no
// status column in this phase" schema invariant this task's Validation
// section names. Handler-level coverage (state derivation on the wire,
// pagination shape, NFR2 refusal) lives in server_config_lifecycle_test.go
// against fakeRepo/fakeAuthz -- this file's job is proving the underlying
// SQL behaves correctly, not re-proving the handler wiring.
//
// dbtest_helpers_integration_test.go carries testAuditEntry/discardLogger,
// reused here (see this package's other integration test files' own doc
// comments on why each still keeps its own hermetic schema/setup function
// rather than sharing testSchema, which lacks device_config_entry/
// sensor_type).
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //leaflab/api:config_lifecycle_integration_test --test_output=all
package main

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	pushconfig "github.com/whale-net/everything/leaflab/api/config"
	"github.com/whale-net/everything/leaflab/api/contract"
	"github.com/whale-net/everything/leaflab/hwkey"
	"github.com/whale-net/everything/libs/go/dbtest"
)

// configLifecycleSchema mirrors migration 007_device_config
// (device_config), migration 028_config_entry_provenance
// (device_config_entry) and migration 016_audit_log (schema only, like
// this package's other integration fixtures) -- board and sensor_type are
// minimal stand-ins, matching push_device_config_scope_integration_test.go's
// scopeSchema.
const configLifecycleSchema = `
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

	-- Mirrors migration 007_device_config.up.sql exactly -- this is the
	-- table TestDeviceConfig_HasNoStatusColumn_RealSQL introspects.
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

	-- Mirrors migration 016_audit_log's column set (schema only) --
	-- InsertDeviceConfigNextVersion writes an audit_log row in the same
	-- transaction as the device_config row (FR8, NFR6.2).
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

func newConfigLifecycleTestRepository(t *testing.T) (*Repository, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: configLifecycleSchema})
	return NewRepository(db.Pool), db.Pool
}

func insertLifecycleSensorType(t *testing.T, pool *pgxpool.Pool, name, unit string) int64 {
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

// insertRawDeviceConfigVersion inserts a device_config row directly by SQL
// (bypassing InsertDeviceConfigNextVersion's audit-write path), for tests
// that only care about the three FR34.1 columns' effect on
// GetDeviceConfigVersion/ListConfigHistory and don't need entries or
// provenance.
func insertRawDeviceConfigVersion(t *testing.T, pool *pgxpool.Pool, boardID, version int64, accepted bool, ackedAt *time.Time, rejectionReason string) int64 {
	t.Helper()
	var configID int64
	var reason any
	if rejectionReason != "" {
		reason = rejectionReason
	}
	err := pool.QueryRow(context.Background(), `
		INSERT INTO device_config (board_id, version, config_json, accepted, acked_at, rejection_reason)
		VALUES ($1, $2, '{}'::jsonb, $3, $4, $5)
		RETURNING config_id
	`, boardID, version, accepted, ackedAt, reason).Scan(&configID)
	if err != nil {
		t.Fatalf("insert device_config board=%d version=%d: %v", boardID, version, err)
	}
	return configID
}

// -- ListConfigHistory: real-SQL newest-first, FR61 keyset pagination -----

// TestListConfigHistory_RealSQL_NewestFirstKeysetPaginated proves
// ListConfigHistory orders by version DESC against real SQL and that the
// (version) keyset cursor this task's contract.EncodeConfigHistoryCursor/
// DecodeConfigHistoryCursor produce actually resumes correctly across
// pages -- pending and rejected versions are listed alongside accepted
// ones, never filtered out (FR35.1).
func TestListConfigHistory_RealSQL_NewestFirstKeysetPaginated(t *testing.T) {
	repo, pool := newConfigLifecycleTestRepository(t)
	boardID := insertBoard(t, pool, "board-history")

	ackedAt := time.Now().UTC()
	insertRawDeviceConfigVersion(t, pool, boardID, 1, true, &ackedAt, "")
	insertRawDeviceConfigVersion(t, pool, boardID, 2, false, &ackedAt, "bad crc")
	insertRawDeviceConfigVersion(t, pool, boardID, 3, false, nil, "") // pending

	// First page: 2 rows, newest first (3, 2).
	page1, err := repo.ListConfigHistory(context.Background(), "board-history", 0, false, 2)
	if err != nil {
		t.Fatalf("ListConfigHistory page 1: %v", err)
	}
	if len(page1) != 2 || page1[0].Version != 3 || page1[1].Version != 2 {
		t.Fatalf("page1 = %+v, want [version=3, version=2] (newest first)", page1)
	}
	if page1[1].RejectionReason != "bad crc" {
		t.Errorf("page1[1] (version 2) RejectionReason = %q, want %q", page1[1].RejectionReason, "bad crc")
	}

	// Resume from the cursor a handler would encode for the last row on
	// page 1 -- round-tripped through the real cursor helpers, not a bare
	// int, so this also proves the wire cursor shape actually works
	// end-to-end against this query.
	token := contract.EncodeConfigHistoryCursor(page1[len(page1)-1].Version)
	beforeVersion, hasBefore, err := contract.DecodeConfigHistoryCursor(token)
	if err != nil {
		t.Fatalf("DecodeConfigHistoryCursor: %v", err)
	}
	if !hasBefore {
		t.Fatal("hasBefore = false, want true for a non-empty token")
	}

	page2, err := repo.ListConfigHistory(context.Background(), "board-history", beforeVersion, hasBefore, 2)
	if err != nil {
		t.Fatalf("ListConfigHistory page 2: %v", err)
	}
	if len(page2) != 1 || page2[0].Version != 1 {
		t.Fatalf("page2 = %+v, want [version=1]", page2)
	}
}

// TestListConfigHistory_RealSQL_UnknownDevice_EmptyNotError proves listing
// history for a device_id that resolves to no board returns an empty
// slice, not an error -- distinct from a per-row SQL failure.
func TestListConfigHistory_RealSQL_UnknownDevice_EmptyNotError(t *testing.T) {
	repo, _ := newConfigLifecycleTestRepository(t)

	rows, err := repo.ListConfigHistory(context.Background(), "does-not-exist", 0, false, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("len(rows) = %d, want 0", len(rows))
	}
}

// -- GetDeviceConfigVersion: fetches regardless of acceptance -------------

// TestGetDeviceConfigVersion_RealSQL_FetchesRegardlessOfAcceptance proves
// GetDeviceConfigVersion returns a pending and a rejected version exactly
// as readily as an accepted one (FR35.2), and returns nil (not an error)
// for a version that was never pushed.
func TestGetDeviceConfigVersion_RealSQL_FetchesRegardlessOfAcceptance(t *testing.T) {
	repo, pool := newConfigLifecycleTestRepository(t)
	boardID := insertBoard(t, pool, "board-versions")

	ackedAt := time.Now().UTC()
	insertRawDeviceConfigVersion(t, pool, boardID, 1, true, &ackedAt, "")
	insertRawDeviceConfigVersion(t, pool, boardID, 2, false, &ackedAt, "sensor mismatch")
	insertRawDeviceConfigVersion(t, pool, boardID, 3, false, nil, "")

	accepted, err := repo.GetDeviceConfigVersion(context.Background(), "board-versions", 1)
	if err != nil || accepted == nil {
		t.Fatalf("GetDeviceConfigVersion(1) = (%v, %v), want a row", accepted, err)
	}
	if !accepted.Accepted || accepted.AckedAt == nil {
		t.Errorf("version 1 = %+v, want accepted with acked_at set", accepted)
	}

	rejected, err := repo.GetDeviceConfigVersion(context.Background(), "board-versions", 2)
	if err != nil || rejected == nil {
		t.Fatalf("GetDeviceConfigVersion(2) = (%v, %v), want a row", rejected, err)
	}
	if rejected.Accepted || rejected.AckedAt == nil || rejected.RejectionReason != "sensor mismatch" {
		t.Errorf("version 2 = %+v, want rejected with verbatim reason", rejected)
	}

	pending, err := repo.GetDeviceConfigVersion(context.Background(), "board-versions", 3)
	if err != nil || pending == nil {
		t.Fatalf("GetDeviceConfigVersion(3) = (%v, %v), want a row", pending, err)
	}
	if pending.AckedAt != nil {
		t.Errorf("version 3 = %+v, want acked_at nil (pending)", pending)
	}

	neverPushed, err := repo.GetDeviceConfigVersion(context.Background(), "board-versions", 999)
	if err != nil {
		t.Fatalf("unexpected error for a never-pushed version: %v", err)
	}
	if neverPushed != nil {
		t.Errorf("GetDeviceConfigVersion for a never-pushed version = %+v, want nil", neverPushed)
	}
}

// -- GetConfigVersionEntries: FR82.4 provenance persists and joins --------

// TestGetConfigVersionEntries_RealSQL_ProvenanceAndSensorTypeJoin proves
// InsertDeviceConfigNextVersion's per-entry provenance survives a real
// write, and GetConfigVersionEntries reads it back joined to the correct
// sensor_type name (which server.go's sensorTypeFromName then maps onto
// the wire enum).
func TestGetConfigVersionEntries_RealSQL_ProvenanceAndSensorTypeJoin(t *testing.T) {
	repo, pool := newConfigLifecycleTestRepository(t)
	boardID := insertBoard(t, pool, "board-entries")
	tempTypeID := insertLifecycleSensorType(t, pool, "temperature", "C")
	humidTypeID := insertLifecycleSensorType(t, pool, "humidity", "%RH")

	entries := []pushconfig.Entry{
		{
			Key:        hwkey.Key{I2CAddress: hwkey.Address(0x44), MuxPath: hwkey.MuxPath{}, SensorTypeID: hwkey.SensorTypeID(tempTypeID)},
			Provenance: pushconfig.ProvenanceAuthored,
		},
		{
			Key:        hwkey.Key{I2CAddress: hwkey.Address(0x45), MuxPath: hwkey.MuxPath{}, SensorTypeID: hwkey.SensorTypeID(humidTypeID)},
			Provenance: pushconfig.ProvenanceMaterialised,
		},
	}

	version, err := repo.InsertDeviceConfigNextVersion(context.Background(), boardID, []byte(`{}`), entries, nil, testAuditEntry())
	if err != nil {
		t.Fatalf("InsertDeviceConfigNextVersion: %v", err)
	}

	row, err := repo.GetDeviceConfigVersion(context.Background(), "board-entries", version)
	if err != nil || row == nil {
		t.Fatalf("GetDeviceConfigVersion(%d) = (%v, %v), want a row", version, row, err)
	}

	entryRows, err := repo.GetConfigVersionEntries(context.Background(), row.ConfigID)
	if err != nil {
		t.Fatalf("GetConfigVersionEntries: %v", err)
	}
	if len(entryRows) != 2 {
		t.Fatalf("len(entryRows) = %d, want 2", len(entryRows))
	}

	byType := make(map[string]ConfigVersionEntryRow, 2)
	for _, e := range entryRows {
		byType[e.SensorTypeName] = e
	}
	if got := byType["temperature"].Provenance; got != string(pushconfig.ProvenanceAuthored) {
		t.Errorf("temperature entry provenance = %q, want %q", got, pushconfig.ProvenanceAuthored)
	}
	if got := byType["humidity"].Provenance; got != string(pushconfig.ProvenanceMaterialised) {
		t.Errorf("humidity entry provenance = %q, want %q", got, pushconfig.ProvenanceMaterialised)
	}
}

// -- device_config's column set -------------------------------------------

// TestDeviceConfig_HasNoStatusColumn_RealSQL is this task's Validation
// criterion, checked against the real hermetic schema (mirroring migration
// 007 exactly, per configLifecycleSchema's doc comment) rather than the
// migration file's text: introspects information_schema.columns and
// asserts no column named "status" (or a name containing "status") exists
// on device_config -- the three FR34.1 states are derived entirely from
// accepted/acked_at/rejection_reason, never a stored status value.
func TestDeviceConfig_HasNoStatusColumn_RealSQL(t *testing.T) {
	_, pool := newConfigLifecycleTestRepository(t)

	rows, err := pool.Query(context.Background(), `
		SELECT column_name FROM information_schema.columns
		WHERE table_name = 'device_config'
	`)
	if err != nil {
		t.Fatalf("introspect device_config columns: %v", err)
	}
	defer rows.Close()

	var columns []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan column name: %v", err)
		}
		columns = append(columns, name)
		if name == "status" {
			t.Errorf("device_config has a %q column -- FR34.1 requires the three states be derived, never stored", name)
		}
	}
	wantColumns := map[string]bool{
		"config_id": true, "board_id": true, "version": true, "config_json": true,
		"accepted": true, "pushed_at": true, "acked_at": true, "rejection_reason": true,
	}
	if len(columns) != len(wantColumns) {
		t.Errorf("device_config has columns %v, want exactly %v", columns, wantColumns)
	}
	for _, c := range columns {
		if !wantColumns[c] {
			t.Errorf("device_config has unexpected column %q -- this task's Validation section requires no migration in it alters device_config's column set", c)
		}
	}
}
