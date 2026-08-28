//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never even compiles it.
// See the go_test target's gotags in BUILD.bazel and
// //libs/go/dbtest/README.md for how to run it.
//
// It proves FR16/FR16.3/FR16.4/FR17's identity resolution against a real
// Postgres sensor/sensor_hw_history/sensor_name_history/sensor_reading
// schema -- not the pure-Go elimination logic (that's
// leaflab/processor/handler_test.go's job, on the manifest path), but this
// package's own read-only case-1/case-2 resolution (identity.go), the
// FR16.4 swap refusal, the FR17 case-3 refusal, and RewireSensor's write
// path (Repository.RewireSensorHW), all against real SQL: the unique
// (board_id, i2c_address, sensor_type_id, mux_path::text) index a rewire
// must not violate, and the foreign keys that prove readings/name history
// stay attached to an unchanged sensor_id.
//
// This file is its own go_test target (identity_integration_test in
// BUILD.bazel), compiled as a separate test binary from
// response_contract_integration_test.go's target -- so discardLogger,
// countRows and insertBoard are defined locally here rather than shared,
// even though both files happen to define same-shaped helpers of the same
// name. That's intentional duplication, not an oversight: see this
// package's other integration test file for the twin definitions.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	firmwarepb "github.com/whale-net/everything/firmware/proto"
	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/api/authz"
	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/libs/go/dbtest"
)

// discardLogger is a *slog.Logger that throws away everything it's given.
// Duplicated from response_contract_integration_test.go: see this file's
// package doc comment for why (separate go_test target/binary).
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// allScope/stubAuthz are duplicated from dbtest_helpers_integration_test.go
// (separate go_test target/binary, same rationale as discardLogger above):
// this file's tests never exercise FR5 household scoping, only FR16/FR16.3/
// FR16.4/FR17 identity resolution, so a Scope that never filters keeps this
// fixture hermetic without a household/household_membership schema.
type allScope struct{}

func (allScope) Permits(ref authz.EntityRef, res authz.Resolution) bool { return true }
func (allScope) Filter(argStart int) (string, []any)                    { return "TRUE", nil }

type stubAuthz struct{}

func (stubAuthz) ScopeForPrincipal(ctx context.Context, principalSubject string) (authz.Scope, error) {
	return allScope{}, nil
}

func (stubAuthz) ResolveBoardByDeviceID(ctx context.Context, deviceID string) (authz.EntityRef, authz.Resolution, error) {
	panic("not used by this file's tests")
}

// Resolve always reports Unclaimed: PushDeviceConfig's FR1.2 handler calls
// this to resolve the pushing board's household before reaching this
// file's own FR16/FR17 identity check, and this fixture's boards
// (insertBoard) are never given a household_ownership row -- there is no
// household schema here, only the identity tables this file's tests
// actually exercise. FR1.2's own AssertSameHousehold refusal path is
// covered separately (leaflab/api/push_device_config_invariant_integration_test.go).
func (stubAuthz) Resolve(ctx context.Context, ref authz.EntityRef) (authz.Resolution, error) {
	return authz.Resolution{Unclaimed: true}, nil
}

func countRows(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&n); err != nil {
		t.Fatalf("count rows in %s: %v", table, err)
	}
	return n
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

const identitySchema = `
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

	-- Mirrors sensor's shape as of migration 013 (board_id, i2c_address,
	-- sensor_type_id, mux_path -- the FR18 canonical hardware key columns
	-- -- plus name, the case-2 anchor).
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

	-- Same index shape as idx_sensor_hw_address (migration 011): this is
	-- the constraint a rewire that reuses another sensor's key would
	-- violate if it ever tried to INSERT rather than UPDATE in place.
	CREATE UNIQUE INDEX idx_sensor_hw_address
		ON sensor(board_id, i2c_address, sensor_type_id, (mux_path::text))
		WHERE i2c_address IS NOT NULL;

	CREATE TABLE sensor_hw_history (
		history_id  BIGSERIAL PRIMARY KEY,
		sensor_id   BIGINT NOT NULL REFERENCES sensor(sensor_id),
		mux_path    JSONB NOT NULL DEFAULT '[]'::jsonb,
		i2c_address SMALLINT,
		valid_from  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		valid_to    TIMESTAMPTZ
	);
	CREATE INDEX idx_sensor_hw_history_current ON sensor_hw_history(sensor_id) WHERE valid_to IS NULL;

	CREATE TABLE sensor_name_history (
		sensor_name_history_id BIGSERIAL PRIMARY KEY,
		sensor_id  BIGINT NOT NULL REFERENCES sensor(sensor_id),
		name       TEXT NOT NULL,
		valid_from TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		valid_to   TIMESTAMPTZ
	);

	CREATE TABLE sensor_reading (
		reading_id  BIGSERIAL PRIMARY KEY,
		sensor_id   BIGINT NOT NULL REFERENCES sensor(sensor_id),
		value       DOUBLE PRECISION NOT NULL,
		recorded_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);

	CREATE TABLE device_config (
		config_id            BIGSERIAL   PRIMARY KEY,
		board_id             BIGINT      NOT NULL REFERENCES board(board_id) ON DELETE RESTRICT,
		version              BIGINT      NOT NULL,
		config_json          JSONB       NOT NULL,
		accepted             BOOLEAN     NOT NULL DEFAULT FALSE,
		pushed_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		acked_at             TIMESTAMPTZ,
		rejection_reason     TEXT,
		push_group_id        BIGINT,
		derived_from_version BIGINT,
		UNIQUE (board_id, version)
	);
`

// newIdentityTestServer starts a real Postgres container, applies
// identitySchema, and returns a LeafLabAPIServer backed by a real
// Repository plus the raw pool for fixture setup/assertions. publisher is
// nil, same as newTestServer: every RPC this file exercises either never
// reaches the publish step (refusals) or is exercised via the unexported
// checkPushConfigIdentity directly (the non-refusal FR16 case-1/case-2
// checks), never via a successful PushDeviceConfig end-to-end.
func newIdentityTestServer(t *testing.T) (*LeafLabAPIServer, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: identitySchema})
	repo := NewRepository(db.Pool)
	return NewLeafLabAPIServer(repo, stubAuthz{}, nil, nil, discardLogger(), defaultPollIntervalBounds), db.Pool
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

func insertHWHistory(t *testing.T, pool *pgxpool.Pool, sensorID int64, i2cAddr *int32) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO sensor_hw_history (sensor_id, i2c_address) VALUES ($1, $2)
	`, sensorID, i2cAddr); err != nil {
		t.Fatalf("insert sensor_hw_history for sensor %d: %v", sensorID, err)
	}
}

func insertNameHistory(t *testing.T, pool *pgxpool.Pool, sensorID int64, name string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO sensor_name_history (sensor_id, name) VALUES ($1, $2)
	`, sensorID, name); err != nil {
		t.Fatalf("insert sensor_name_history for sensor %d: %v", sensorID, err)
	}
}

func insertReading(t *testing.T, pool *pgxpool.Pool, sensorID int64, value float64) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO sensor_reading (sensor_id, value) VALUES ($1, $2)
	`, sensorID, value); err != nil {
		t.Fatalf("insert sensor_reading for sensor %d: %v", sensorID, err)
	}
}

func addr(v int32) *int32 { return &v }

// assertRefusal fails t unless err carries a pb.Failure detail of class
// refused_with_alternative, and returns the decoded detail for further
// assertions (e.g. on reason/alternative text).
func assertRefusal(t *testing.T, err error) *pb.Failure {
	t.Helper()
	if err == nil {
		t.Fatal("expected a refusal error, got nil")
	}
	detail, ok := contract.FromError(err)
	if !ok {
		t.Fatalf("error %v carries no Failure detail", err)
	}
	if detail.Class != string(contract.FailureRefusedWithAlternative) {
		t.Errorf("Class = %q, want %q", detail.Class, contract.FailureRefusedWithAlternative)
	}
	if detail.Alternative == "" {
		t.Error("Alternative is empty, want a stated alternative (FR59.3)")
	}
	return detail
}

// TestRewireSensor_PreservesIdentityAndHistory covers FR16 on the explicit
// API rewire path: a sensor's address changes, sensor_id does not, and
// everything keyed on it (readings, name history) stays attached because
// it was never touched -- proven here by re-reading them after the rewire,
// not just by construction.
func TestRewireSensor_PreservesIdentityAndHistory(t *testing.T) {
	server, pool := newIdentityTestServer(t)
	ctx := context.Background()

	boardID := insertBoard(t, pool, "leaflab-rewire")
	typeID := insertSensorType(t, pool, "temperature", "degC")
	sensorID := insertSensor(t, pool, boardID, typeID, "temp", "degC", addr(0x23))
	insertHWHistory(t, pool, sensorID, addr(0x23))
	insertNameHistory(t, pool, sensorID, "temp")
	insertReading(t, pool, sensorID, 21.5)

	resp, err := server.RewireSensor(ctx, &pb.RewireSensorRequest{
		DeviceId:   "leaflab-rewire",
		Name:       "temp",
		I2CAddress: 0x44,
	})
	if err != nil {
		t.Fatalf("RewireSensor: %v", err)
	}
	if resp.SensorId != sensorID {
		t.Errorf("SensorId = %d, want unchanged %d (identity must survive a rewire)", resp.SensorId, sensorID)
	}

	// No fork: still exactly one sensor row for this physical sensor.
	if got := countRows(t, pool, "sensor"); got != 1 {
		t.Errorf("sensor rows after rewire = %d, want 1 (no fork)", got)
	}

	var newAddr int32
	if err := pool.QueryRow(ctx, `SELECT i2c_address FROM sensor WHERE sensor_id = $1`, sensorID).Scan(&newAddr); err != nil {
		t.Fatalf("read back sensor i2c_address: %v", err)
	}
	if newAddr != 0x44 {
		t.Errorf("sensor.i2c_address = 0x%02x, want 0x44", newAddr)
	}

	// sensor_hw_history: old interval closed, new interval open, both
	// belonging to the same sensor_id.
	rows, err := pool.Query(ctx, `
		SELECT i2c_address, valid_to IS NULL FROM sensor_hw_history
		WHERE sensor_id = $1 ORDER BY history_id
	`, sensorID)
	if err != nil {
		t.Fatalf("query sensor_hw_history: %v", err)
	}
	defer rows.Close()
	type hwRow struct {
		addr int32
		open bool
	}
	var hwRows []hwRow
	for rows.Next() {
		var r hwRow
		if err := rows.Scan(&r.addr, &r.open); err != nil {
			t.Fatalf("scan sensor_hw_history: %v", err)
		}
		hwRows = append(hwRows, r)
	}
	if len(hwRows) != 2 {
		t.Fatalf("sensor_hw_history rows for sensor %d = %d, want 2 (closed + open)", sensorID, len(hwRows))
	}
	if hwRows[0].open || hwRows[0].addr != 0x23 {
		t.Errorf("first sensor_hw_history row = %+v, want closed at 0x23", hwRows[0])
	}
	if !hwRows[1].open || hwRows[1].addr != 0x44 {
		t.Errorf("second sensor_hw_history row = %+v, want open at 0x44", hwRows[1])
	}

	// Readings and name history stay attached: still exactly one of each,
	// still keyed on the unchanged sensor_id.
	if got := countRows(t, pool, "sensor_reading"); got != 1 {
		t.Errorf("sensor_reading rows after rewire = %d, want 1 (reading must stay attached)", got)
	}
	var readingSensorID int64
	if err := pool.QueryRow(ctx, `SELECT sensor_id FROM sensor_reading LIMIT 1`).Scan(&readingSensorID); err != nil {
		t.Fatalf("read back sensor_reading.sensor_id: %v", err)
	}
	if readingSensorID != sensorID {
		t.Errorf("sensor_reading.sensor_id = %d, want unchanged %d", readingSensorID, sensorID)
	}

	if got := countRows(t, pool, "sensor_name_history"); got != 1 {
		t.Errorf("sensor_name_history rows after rewire = %d, want 1 (name history must stay attached, untouched by a pure hardware rewire)", got)
	}
}

// TestRewireSensor_NoExistingSensor_RefusedFR17_WritesNothing covers
// FR17's refusal contract on RewireSensor itself: with no sensor named
// req.Name, applying the rewire would establish a new identity, so it's
// refused before writing anything.
func TestRewireSensor_NoExistingSensor_RefusedFR17_WritesNothing(t *testing.T) {
	server, pool := newIdentityTestServer(t)
	ctx := context.Background()

	_, err := server.RewireSensor(ctx, &pb.RewireSensorRequest{
		DeviceId:   "leaflab-rewire-missing",
		Name:       "does_not_exist",
		I2CAddress: 0x44,
	})
	detail := assertRefusal(t, err)
	if detail.Field != "name" {
		t.Errorf("Field = %q, want %q", detail.Field, "name")
	}

	if got := countRows(t, pool, "sensor"); got != 0 {
		t.Errorf("sensor rows after refused rewire = %d, want 0 (refusal must write nothing)", got)
	}
	if got := countRows(t, pool, "sensor_hw_history"); got != 0 {
		t.Errorf("sensor_hw_history rows after refused rewire = %d, want 0", got)
	}
}

// TestCheckPushConfigIdentity_FR16Case1_HWMatchDifferentName_NoRefusal
// covers FR16 case 1 / FR16.3's rewire-with-rename outcome: an entry whose
// hardware key matches an existing sensor, under a different name,
// continues that sensor's identity -- checkPushConfigIdentity must not
// refuse it. checkPushConfigIdentity is called directly (not through
// PushDeviceConfig) because this outcome falls through to a real publish,
// and newIdentityTestServer's publisher is nil -- see its doc comment.
func TestCheckPushConfigIdentity_FR16Case1_HWMatchDifferentName_NoRefusal(t *testing.T) {
	server, pool := newIdentityTestServer(t)
	ctx := context.Background()

	boardID := insertBoard(t, pool, "leaflab-case1")
	typeID := insertSensorType(t, pool, "illuminance", "lx")
	insertSensor(t, pool, boardID, typeID, "old_name", "lx", addr(0x23))

	sensors := []*configpb.SensorConfig{
		{
			Name:       "new_name",
			SensorType: firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE,
			I2CAddress: 0x23, // same hardware key, different name
		},
	}
	if err := server.checkPushConfigIdentity(ctx, boardID, sensors); err != nil {
		t.Fatalf("checkPushConfigIdentity refused an FR16 case-1 (hw match, renamed) entry: %v", err)
	}
}

// TestCheckPushConfigIdentity_FR16Case2_NameMatchDifferentHW_NoRefusal
// covers FR16 case 2: an entry whose name matches an existing sensor, at a
// new hardware address that collides with nothing else, continues that
// sensor's identity.
func TestCheckPushConfigIdentity_FR16Case2_NameMatchDifferentHW_NoRefusal(t *testing.T) {
	server, pool := newIdentityTestServer(t)
	ctx := context.Background()

	boardID := insertBoard(t, pool, "leaflab-case2")
	typeID := insertSensorType(t, pool, "temperature", "degC")
	insertSensor(t, pool, boardID, typeID, "temp", "degC", addr(0x23))

	sensors := []*configpb.SensorConfig{
		{
			Name:       "temp", // stable anchor
			SensorType: firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE,
			I2CAddress: 0x44, // moved
		},
	}
	if err := server.checkPushConfigIdentity(ctx, boardID, sensors); err != nil {
		t.Fatalf("checkPushConfigIdentity refused an FR16 case-2 (name match, rewired) entry: %v", err)
	}
}

// TestCheckPushConfigIdentity_FR16_4_Swap_RefusedNamingBothEntries_NoFork
// covers FR16.4: a config version that exchanges two existing sensors'
// hardware keys is refused, naming both entries, and mints no new sensor
// row for either -- the "no fork" assertion the issue calls for
// explicitly (sensor row count unchanged).
func TestCheckPushConfigIdentity_FR16_4_Swap_RefusedNamingBothEntries_NoFork(t *testing.T) {
	server, pool := newIdentityTestServer(t)
	ctx := context.Background()

	boardID := insertBoard(t, pool, "leaflab-swap")
	typeID := insertSensorType(t, pool, "temperature", "degC")
	insertSensor(t, pool, boardID, typeID, "sensor_a", "degC", addr(0x23))
	insertSensor(t, pool, boardID, typeID, "sensor_b", "degC", addr(0x44))

	// Exchange: entry "sensor_a" now reports sensor_b's old hardware key
	// (0x44), and entry "sensor_b" now reports sensor_a's old key (0x23).
	// Each entry's own hw-match and name-match resolve to two different
	// existing sensors -- exactly FR16.4's swap.
	sensors := []*configpb.SensorConfig{
		{Name: "sensor_a", SensorType: firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE, I2CAddress: 0x44},
		{Name: "sensor_b", SensorType: firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE, I2CAddress: 0x23},
	}

	err := server.checkPushConfigIdentity(ctx, boardID, sensors)
	detail := assertRefusal(t, err)
	for _, name := range []string{"sensor_a", "sensor_b"} {
		if !strings.Contains(detail.Reason, name) {
			t.Errorf("refusal reason %q does not name entry %q", detail.Reason, name)
		}
	}

	if got := countRows(t, pool, "sensor"); got != 2 {
		t.Errorf("sensor rows after refused swap = %d, want 2 (no fork, no third sensor minted)", got)
	}
}

// TestFR16_4_Swap_NotCaughtByWithinPayloadCollisionCheck proves the claim
// FR16.4's requirement text makes: a within-payload collision check --
// looking only for a name or hardware key repeated across entries in the
// *same* payload -- cannot see a swap, because a swap's two entries carry
// distinct names and distinct hardware keys from each other; the collision
// is against pre-push DB state, which a within-payload check never
// consults. withinPayloadCollision below is a minimal stand-in for such a
// check (this codebase's actual FR39 within-payload validator lives on a
// different branch, not in this task's ancestry) -- it returns false
// (no collision found) for the exact swap payload
// TestCheckPushConfigIdentity_FR16_4_Swap_RefusedNamingBothEntries_NoFork
// proves checkPushConfigIdentity correctly refuses.
func TestFR16_4_Swap_NotCaughtByWithinPayloadCollisionCheck(t *testing.T) {
	sensors := []*configpb.SensorConfig{
		{Name: "sensor_a", SensorType: firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE, I2CAddress: 0x44},
		{Name: "sensor_b", SensorType: firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE, I2CAddress: 0x23},
	}
	if withinPayloadCollision(sensors) {
		t.Fatal("withinPayloadCollision flagged the swap payload; it should find nothing, since neither entry's own name or hardware key repeats within the payload -- that's the point FR16.4's requirement text makes: this class of check cannot catch a swap")
	}
}

// withinPayloadCollision reports whether any two entries in sensors share
// a name or a (sensor_type, i2c_address) hardware key -- exactly what a
// within-payload collision check (FR39) looks for, and only that: it never
// consults existing DB state, so it cannot see a swap (see
// TestFR16_4_Swap_NotCaughtByWithinPayloadCollisionCheck).
func withinPayloadCollision(sensors []*configpb.SensorConfig) bool {
	names := make(map[string]bool, len(sensors))
	keys := make(map[string]bool, len(sensors))
	for _, sc := range sensors {
		if names[sc.Name] {
			return true
		}
		names[sc.Name] = true
		key := fmt.Sprintf("%d:%d", sc.SensorType, sc.I2CAddress)
		if keys[key] {
			return true
		}
		keys[key] = true
	}
	return false
}

// TestPushDeviceConfig_FR17_NewIdentityRefused_RealPushPath_WritesNothing
// covers FR17 on the real push path (not a dry run, per FR82): an entry
// that continues neither an existing hardware key nor an existing name is
// refused before InsertDeviceConfigNextVersion or Publish is ever reached
// -- exercised through PushDeviceConfig itself, not checkPushConfigIdentity
// directly, so a regression that moved the check after the write (or
// after a dry-run-only path) would be caught here. publisher stays nil
// (see newIdentityTestServer's doc comment); the refusal must occur before
// Publish is ever reached, or this test would panic on a nil publisher
// instead of failing cleanly.
func TestPushDeviceConfig_FR17_NewIdentityRefused_RealPushPath_WritesNothing(t *testing.T) {
	server, pool := newIdentityTestServer(t)
	ctx := context.Background()

	boardID := insertBoard(t, pool, "leaflab-fr17")
	typeID := insertSensorType(t, pool, "temperature", "degC")
	insertSensor(t, pool, boardID, typeID, "temp", "degC", addr(0x23))

	_, err := server.PushDeviceConfig(ctx, &pb.PushDeviceConfigRequest{
		DeviceId: "leaflab-fr17",
		Scope:    pb.PushScope_PUSH_SCOPE_COMPLETE,
		Sensors: []*configpb.SensorConfig{
			{
				Name:       "brand_new_sensor",
				SensorType: firmwarepb.SensorType_SENSOR_TYPE_HUMIDITY,
				I2CAddress: 0x99, // matches no existing hw key
			},
		},
	})
	detail := assertRefusal(t, err)
	if !strings.Contains(detail.Reason, "brand_new_sensor") {
		t.Errorf("refusal reason %q does not name the offending entry", detail.Reason)
	}
	if !strings.Contains(detail.Reason, "history") {
		t.Errorf("refusal reason %q does not name the consequence (history will not follow)", detail.Reason)
	}
	if !strings.Contains(detail.Alternative, "RewireSensor") {
		t.Errorf("Alternative %q does not offer the rewire path", detail.Alternative)
	}

	if got := countRows(t, pool, "device_config"); got != 0 {
		t.Errorf("device_config rows after FR17 refusal = %d, want 0 (real push path must write nothing)", got)
	}
	// Still just the one pre-existing sensor: FR17's whole point is that
	// this entry never got the chance to establish a second identity.
	if got := countRows(t, pool, "sensor"); got != 1 {
		t.Errorf("sensor rows after FR17 refusal = %d, want 1 (unchanged)", got)
	}
}
