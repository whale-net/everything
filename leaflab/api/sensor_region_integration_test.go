//go:build integration

// Real-Postgres integration coverage for #1379's FR51/FR52:
// Repository.AssignSensorRegion and Repository.RenameSensor
// (sensor_region.go). Neither RPC has server.go wiring yet -- exercised
// directly against Repository, exactly as regions_lifecycle_integration_
// test.go does for CreateRegion/RenameRegion/RetireRegion (see that
// file's and sensor_region.go's own doc comments for the precedent).
//
// Schema is self-contained hand-written DDL mirroring the relevant shape of
// migration 001/009/011 (sensor, sensor_region_history, sensor_name_history
// -- post-011's renamed table, not the pre-rename sensor_label),
// migration 015 (household, household_membership, board.household_id,
// region.household_id) and migration 033 (boundary_capture, FR20) --
// deliberately not shared with this package's other integration test
// files' schemas (each is its own go_test target's independent srcs list;
// see dbtest_helpers_integration_test.go's own doc comment on why
// testSchema isn't shared either). CREATE EXTENSION timescaledb and the
// timescale/timescaledb image are required because AssignSensorRegion's
// FR20 boundary capture (leaflab/api/capture.Recorder) calls Postgres's
// time_bucket() -- see leaflab/api/capture/boundary_capture_integration_
// test.go's own doc comment for the same requirement.
//
// invalidationPub is left nil throughout (Repository.SetInvalidationPublisher
// is never called) -- there is no real-RabbitMQ test double in this repo
// (see push_device_config_invariant_integration_test.go's doc comment and
// server.go's own RewireSensor precedent), so these tests cannot observe a
// published invalidation.Event directly. What FR73 requires each publish
// call site to carry (in particular RenameSensor's PriorSensorName, so a
// cache entry under the *prior* name gets evicted) is proven at the
// consuming end instead, against a real invalidation.Event value, by
// leaflab/processor/handler_test.go's "FR73: handleSensorReading's
// cache-miss/invalidation-driven re-read path" section (KindName cases).
//
// See //libs/go/dbtest's README for how to run integration tests like this
// one.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //leaflab/api:sensor_region_integration_test --test_output=all
package main

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/leaflab/api/audit"
	"github.com/whale-net/everything/leaflab/api/authz"
	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/libs/go/dbtest"
)

// sraTimescaleImage matches captureTimescaleImage in
// leaflab/api/capture/boundary_capture_integration_test.go -- AssignSensorRegion's
// FR20 capture.Recorder.Record call needs a real time_bucket(), a
// TimescaleDB function not present in plain postgres. Declared again here
// rather than reused, for the same "Bazel test targets do not always share
// compilation" reason given elsewhere in this package.
const sraTimescaleImage = "timescale/timescaledb:latest-pg16"

const sensorRegionTestSchema = `
	CREATE EXTENSION IF NOT EXISTS timescaledb;

	CREATE TABLE household (
		household_id BIGSERIAL PRIMARY KEY
	);

	CREATE TABLE household_membership (
		household_membership_id BIGSERIAL PRIMARY KEY,
		household_id             BIGINT NOT NULL REFERENCES household(household_id),
		principal_subject        TEXT NOT NULL,
		valid_from                TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		valid_to                  TIMESTAMPTZ
	);
	CREATE INDEX idx_sra_household_membership_current
		ON household_membership(principal_subject) WHERE valid_to IS NULL;

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

	CREATE TABLE sensor (
		sensor_id     BIGSERIAL PRIMARY KEY,
		board_id      BIGINT NOT NULL REFERENCES board(board_id),
		region_id     BIGINT REFERENCES region(region_id),
		name          VARCHAR(128) NOT NULL,
		registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);

	CREATE TABLE sensor_region_history (
		history_id BIGSERIAL PRIMARY KEY,
		sensor_id  BIGINT NOT NULL REFERENCES sensor(sensor_id),
		region_id  BIGINT NOT NULL REFERENCES region(region_id),
		valid_from TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		valid_to   TIMESTAMPTZ
	);
	CREATE INDEX idx_sra_srh_current ON sensor_region_history(sensor_id) WHERE valid_to IS NULL;

	-- post-011's name: sensor_label -> sensor_name_history.
	CREATE TABLE sensor_name_history (
		history_id BIGSERIAL PRIMARY KEY,
		sensor_id  BIGINT NOT NULL REFERENCES sensor(sensor_id),
		name       VARCHAR(128) NOT NULL,
		valid_from TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		valid_to   TIMESTAMPTZ
	);
	CREATE INDEX idx_sra_snh_current ON sensor_name_history(sensor_id) WHERE valid_to IS NULL;

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

	-- migration 033, verbatim shape -- see boundary_capture_integration_test.go.
	CREATE TABLE boundary_capture (
		capture_id    BIGSERIAL PRIMARY KEY,
		sensor_id     BIGINT NOT NULL REFERENCES sensor(sensor_id) ON DELETE RESTRICT,
		boundary_at   TIMESTAMPTZ NOT NULL,
		tier          TEXT NOT NULL,
		bucket_start  TIMESTAMPTZ NOT NULL,
		state         TEXT NOT NULL DEFAULT 'pending',
		completed_at  TIMESTAMPTZ,
		CONSTRAINT sra_boundary_capture_tier_check
			CHECK (tier IN ('five_minute', 'hourly')),
		CONSTRAINT sra_boundary_capture_state_check
			CHECK (state IN ('pending', 'completed')),
		CONSTRAINT sra_boundary_capture_completed_at_check
			CHECK ((state = 'completed') = (completed_at IS NOT NULL))
	);
`

func newSensorRegionTestRepository(t *testing.T) (*Repository, *authz.PGResolver, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Image: sraTimescaleImage, Schema: sensorRegionTestSchema})
	repo := NewRepository(db.Pool)
	resolver := authz.NewPGResolver(db.Pool)
	return repo, resolver, db.Pool
}

func sraInsertHousehold(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO household DEFAULT VALUES RETURNING household_id`).Scan(&id); err != nil {
		t.Fatalf("insert household: %v", err)
	}
	return id
}

func sraInsertMembership(t *testing.T, pool *pgxpool.Pool, householdID int64, subject string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO household_membership (household_id, principal_subject) VALUES ($1, $2)`,
		householdID, subject); err != nil {
		t.Fatalf("insert membership for %q: %v", subject, err)
	}
}

func sraScopeFor(t *testing.T, resolver *authz.PGResolver, subject string) authz.Scope {
	t.Helper()
	scope, err := resolver.ScopeForPrincipal(context.Background(), subject)
	if err != nil {
		t.Fatalf("ScopeForPrincipal(%q): %v", subject, err)
	}
	return scope
}

func sraInsertRegion(t *testing.T, pool *pgxpool.Pool, householdID int64) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO region (household_id) VALUES ($1) RETURNING region_id`, householdID).Scan(&id); err != nil {
		t.Fatalf("insert region for household %d: %v", householdID, err)
	}
	return id
}

func sraInsertBoard(t *testing.T, pool *pgxpool.Pool, deviceID string, householdID int64) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO board (device_id, household_id) VALUES ($1, $2) RETURNING board_id`,
		deviceID, householdID).Scan(&id); err != nil {
		t.Fatalf("insert board %s: %v", deviceID, err)
	}
	return id
}

func sraInsertSensor(t *testing.T, pool *pgxpool.Pool, boardID int64, regionID *int64, name string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO sensor (board_id, region_id, name) VALUES ($1, $2, $3) RETURNING sensor_id
	`, boardID, regionID, name).Scan(&id); err != nil {
		t.Fatalf("insert sensor %s: %v", name, err)
	}
	return id
}

func sraOpenRegionHistory(t *testing.T, pool *pgxpool.Pool, sensorID, regionID int64, validFrom time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO sensor_region_history (sensor_id, region_id, valid_from) VALUES ($1, $2, $3)`,
		sensorID, regionID, validFrom); err != nil {
		t.Fatalf("insert open sensor_region_history for sensor %d: %v", sensorID, err)
	}
}

func sraOpenNameHistory(t *testing.T, pool *pgxpool.Pool, sensorID int64, name string, validFrom time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO sensor_name_history (sensor_id, name, valid_from) VALUES ($1, $2, $3)`,
		sensorID, name, validFrom); err != nil {
		t.Fatalf("insert open sensor_name_history for sensor %d: %v", sensorID, err)
	}
}

func sraInsertPendingDeviceConfig(t *testing.T, pool *pgxpool.Pool, boardID, version int64) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO device_config (board_id, version, config_json, accepted) VALUES ($1, $2, '{}'::jsonb, TRUE)
	`, boardID, version); err != nil {
		t.Fatalf("insert pending device_config for board %d: %v", boardID, err)
	}
}

func sraAuditEntry(actor string) audit.Entry {
	return audit.Entry{
		ActorSubject: actor,
		ActorKind:    audit.ActorKindHuman,
		Action:       "SensorWrite",
		EntityKind:   "sensor",
	}
}

func sraCountRows(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("count rows in %s: %v", table, err)
	}
	return n
}

func sraFailureOf(t *testing.T, err error) *pb.Failure {
	t.Helper()
	f, ok := contract.FromError(err)
	if !ok {
		t.Fatalf("error carries no contract.Failure detail: %v", err)
	}
	return f
}

func sraSensorRegionID(t *testing.T, pool *pgxpool.Pool, sensorID int64) *int64 {
	t.Helper()
	var regionID *int64
	if err := pool.QueryRow(context.Background(),
		`SELECT region_id FROM sensor WHERE sensor_id = $1`, sensorID).Scan(&regionID); err != nil {
		t.Fatalf("read sensor %d region_id: %v", sensorID, err)
	}
	return regionID
}

func sraSensorName(t *testing.T, pool *pgxpool.Pool, sensorID int64) string {
	t.Helper()
	var name string
	if err := pool.QueryRow(context.Background(),
		`SELECT name FROM sensor WHERE sensor_id = $1`, sensorID).Scan(&name); err != nil {
		t.Fatalf("read sensor %d name: %v", sensorID, err)
	}
	return name
}

// -- AssignSensorRegion (FR51) -------------------------------------------------

// TestAssignSensorRegion_FR51_CommitsImmediately_NoConfigVersion_NoDevicePush
// is the load-bearing FR51 test named in this task's Testing section: with
// no board-availability concept involved at all in the write path (the
// production query never checks board state -- see sensor_region.go's doc
// comment: "no device round trip, no config version bump and no board
// availability requirement"), the assignment commits, the
// sensor_region_history interval closes/opens, no device_config row is
// ever written, and (nothing exists on this Repository call path capable
// of publishing an MQTT push at all, unlike PushDeviceConfig) nothing
// reaches the device.
func TestAssignSensorRegion_FR51_CommitsImmediately_NoConfigVersion_NoDevicePush(t *testing.T) {
	repo, resolver, pool := newSensorRegionTestRepository(t)
	ctx := context.Background()

	householdA := sraInsertHousehold(t, pool)
	sraInsertMembership(t, pool, householdA, "alice")
	aliceScope := sraScopeFor(t, resolver, "alice")

	boardID := sraInsertBoard(t, pool, "device-a", householdA)
	regionOld := sraInsertRegion(t, pool, householdA)
	regionNew := sraInsertRegion(t, pool, householdA)
	sensorID := sraInsertSensor(t, pool, boardID, &regionOld, "sensor-1")
	openedAt := time.Now().Add(-1 * time.Hour)
	sraOpenRegionHistory(t, pool, sensorID, regionOld, openedAt)

	result, err := repo.AssignSensorRegion(ctx, sensorID, regionNew, aliceScope, sraAuditEntry("alice"))
	if err != nil {
		t.Fatalf("AssignSensorRegion: %v", err)
	}
	if result.SensorID != sensorID {
		t.Errorf("result.SensorID = %d, want %d", result.SensorID, sensorID)
	}
	if result.RegionID != regionNew {
		t.Errorf("result.RegionID = %d, want %d", result.RegionID, regionNew)
	}
	if result.AssignedAt.IsZero() {
		t.Error("result.AssignedAt is zero, want populated (FR64)")
	}

	if got := sraSensorRegionID(t, pool, sensorID); got == nil || *got != regionNew {
		t.Errorf("sensor.region_id after assign = %v, want %d", got, regionNew)
	}

	var closedCount, openCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM sensor_region_history WHERE sensor_id = $1 AND valid_to IS NOT NULL`, sensorID).Scan(&closedCount); err != nil {
		t.Fatalf("count closed history rows: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM sensor_region_history WHERE sensor_id = $1 AND valid_to IS NULL`, sensorID).Scan(&openCount); err != nil {
		t.Fatalf("count open history rows: %v", err)
	}
	if closedCount != 1 {
		t.Errorf("closed sensor_region_history rows = %d, want 1 (the old regionOld interval closed)", closedCount)
	}
	if openCount != 1 {
		t.Errorf("open sensor_region_history rows = %d, want 1 (the new regionNew interval)", openCount)
	}

	// FR51: no config version bump -- AssignSensorRegion never touches
	// device_config at all.
	if n := sraCountRows(t, pool, "device_config"); n != 0 {
		t.Errorf("device_config rows after AssignSensorRegion = %d, want 0 (no config version created, nothing pushed to the device)", n)
	}

	// FR8: exactly one audit row, naming the sensor and its household.
	if n := sraCountRows(t, pool, "audit_log"); n != 1 {
		t.Fatalf("audit_log rows after AssignSensorRegion = %d, want 1", n)
	}
	var targetHouseholdID int64
	var entityID string
	if err := pool.QueryRow(ctx, `SELECT target_household_id, entity_id FROM audit_log`).Scan(&targetHouseholdID, &entityID); err != nil {
		t.Fatalf("read audit_log row: %v", err)
	}
	if targetHouseholdID != householdA {
		t.Errorf("audit_log.target_household_id = %d, want %d", targetHouseholdID, householdA)
	}
	if wantID := strconv.FormatInt(sensorID, 10); entityID != wantID {
		t.Errorf("audit_log.entity_id = %q, want %q", entityID, wantID)
	}
}

// TestAssignSensorRegion_FR20_RecordsBoundaryCaptureInSameTransaction is
// this task's FR20 test: the assignment records a boundary_capture row for
// the sensor at both captured tiers (five_minute, hourly), at the instant
// the new sensor_region_history interval opened -- and does so in the same
// transaction as the rest of the write (proven indirectly here: both the
// history row and the boundary_capture rows exist iff the single
// AssignSensorRegion call above succeeded, since Recorder.Record runs
// inside the same auditedWrite transaction -- see sensor_region.go).
func TestAssignSensorRegion_FR20_RecordsBoundaryCaptureInSameTransaction(t *testing.T) {
	repo, resolver, pool := newSensorRegionTestRepository(t)
	ctx := context.Background()

	householdA := sraInsertHousehold(t, pool)
	sraInsertMembership(t, pool, householdA, "alice")
	aliceScope := sraScopeFor(t, resolver, "alice")

	boardID := sraInsertBoard(t, pool, "device-a", householdA)
	regionOld := sraInsertRegion(t, pool, householdA)
	regionNew := sraInsertRegion(t, pool, householdA)
	sensorID := sraInsertSensor(t, pool, boardID, &regionOld, "sensor-1")
	sraOpenRegionHistory(t, pool, sensorID, regionOld, time.Now().Add(-1*time.Hour))

	result, err := repo.AssignSensorRegion(ctx, sensorID, regionNew, aliceScope, sraAuditEntry("alice"))
	if err != nil {
		t.Fatalf("AssignSensorRegion: %v", err)
	}

	rows, err := pool.Query(ctx, `SELECT tier, boundary_at, bucket_start, state FROM boundary_capture WHERE sensor_id = $1 ORDER BY tier`, sensorID)
	if err != nil {
		t.Fatalf("query boundary_capture: %v", err)
	}
	defer rows.Close()

	gotTiers := map[string]bool{}
	count := 0
	for rows.Next() {
		count++
		var tier, state string
		var boundaryAt, bucketStart time.Time
		if err := rows.Scan(&tier, &boundaryAt, &bucketStart, &state); err != nil {
			t.Fatalf("scan boundary_capture row: %v", err)
		}
		gotTiers[tier] = true
		if !boundaryAt.Equal(result.AssignedAt) {
			t.Errorf("boundary_capture(%s).boundary_at = %v, want %v (the assignment's AssignedAt)", tier, boundaryAt, result.AssignedAt)
		}
		if bucketStart.After(boundaryAt) {
			t.Errorf("boundary_capture(%s).bucket_start = %v, want <= boundary_at %v", tier, bucketStart, boundaryAt)
		}
		if state != "pending" {
			t.Errorf("boundary_capture(%s).state = %q, want %q", tier, state, "pending")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate boundary_capture rows: %v", err)
	}
	if count != 2 {
		t.Fatalf("boundary_capture row count for sensor %d = %d, want 2 (five_minute + hourly)", sensorID, count)
	}
	if !gotTiers["five_minute"] || !gotTiers["hourly"] {
		t.Errorf("boundary_capture tiers = %v, want both five_minute and hourly", gotTiers)
	}
}

// TestAssignSensorRegion_FR1_2_ForeignHouseholdRegion_Refused is FR1.2's
// enforcement point for this RPC: the target region must resolve to the
// sensor's board's own household. A region belonging to a different
// household is refused as invalid_argument naming region_id, and nothing
// is written -- no sensor.region_id change, no new history row, no audit
// row, no boundary_capture row.
func TestAssignSensorRegion_FR1_2_ForeignHouseholdRegion_Refused(t *testing.T) {
	repo, resolver, pool := newSensorRegionTestRepository(t)
	ctx := context.Background()

	householdA := sraInsertHousehold(t, pool)
	householdB := sraInsertHousehold(t, pool)
	sraInsertMembership(t, pool, householdA, "alice")
	aliceScope := sraScopeFor(t, resolver, "alice")

	boardID := sraInsertBoard(t, pool, "device-a", householdA)
	regionOwn := sraInsertRegion(t, pool, householdA)
	regionForeign := sraInsertRegion(t, pool, householdB)
	sensorID := sraInsertSensor(t, pool, boardID, &regionOwn, "sensor-1")
	sraOpenRegionHistory(t, pool, sensorID, regionOwn, time.Now().Add(-1*time.Hour))

	_, err := repo.AssignSensorRegion(ctx, sensorID, regionForeign, aliceScope, sraAuditEntry("alice"))
	if err == nil {
		t.Fatal("AssignSensorRegion to a foreign household's region returned nil error, want a refusal")
	}
	f := sraFailureOf(t, err)
	if f.Class != string(contract.FailureInvalidArgument) {
		t.Errorf("failure class = %q, want %q", f.Class, contract.FailureInvalidArgument)
	}
	if f.Field != "region_id" {
		t.Errorf("failure field = %q, want %q", f.Field, "region_id")
	}

	if got := sraSensorRegionID(t, pool, sensorID); got == nil || *got != regionOwn {
		t.Errorf("sensor.region_id after a refused assign = %v, want unchanged %d", got, regionOwn)
	}
	if n := sraCountRows(t, pool, "sensor_region_history"); n != 1 {
		t.Errorf("sensor_region_history rows after a refused assign = %d, want unchanged 1", n)
	}
	if n := sraCountRows(t, pool, "audit_log"); n != 0 {
		t.Errorf("audit_log rows after a refused assign = %d, want 0 (refused before the write transaction)", n)
	}
	if n := sraCountRows(t, pool, "boundary_capture"); n != 0 {
		t.Errorf("boundary_capture rows after a refused assign = %d, want 0", n)
	}
}

// TestAssignSensorRegion_NonMember_NotFound proves NFR2's "no existence
// oracle": a caller with no membership in any household is refused as
// not-found (the same class a nonexistent sensor_id would produce),
// regardless of whether the sensor genuinely exists.
func TestAssignSensorRegion_NonMember_NotFound(t *testing.T) {
	repo, resolver, pool := newSensorRegionTestRepository(t)
	ctx := context.Background()

	householdA := sraInsertHousehold(t, pool)
	sraInsertMembership(t, pool, householdA, "alice")

	boardID := sraInsertBoard(t, pool, "device-a", householdA)
	regionA := sraInsertRegion(t, pool, householdA)
	sensorID := sraInsertSensor(t, pool, boardID, &regionA, "sensor-1")
	sraOpenRegionHistory(t, pool, sensorID, regionA, time.Now().Add(-1*time.Hour))

	// bob has no household_membership row at all.
	bobScope := sraScopeFor(t, resolver, "bob")
	_, err := repo.AssignSensorRegion(ctx, sensorID, regionA, bobScope, sraAuditEntry("bob"))
	if !errors.Is(err, ErrSensorNotFound) {
		t.Errorf("AssignSensorRegion by a non-member error = %v, want ErrSensorNotFound", err)
	}
	if n := sraCountRows(t, pool, "audit_log"); n != 0 {
		t.Errorf("audit_log rows after a non-member's refused assign = %d, want 0", n)
	}
}

// -- RenameSensor (FR52) ------------------------------------------------------

// TestRenameSensor_FR52_WritesOnlyNameHistory_NoConfigVersion_ReportsPushNeeded
// proves FR52's full contract: the rename writes only sensor_name_history
// (and the sensor.name cache) -- sensor_region_history is never touched --
// no device_config row is ever created by the rename itself, and the
// response states whether a config push is still needed: false when
// nothing else is pending, true once the sensor's board already has an
// unrelated device_config push awaiting an ack -- proving the mixed case
// ("the region/name parts committed, the device parts pending") reports
// the two separately rather than treating the whole response as pending.
func TestRenameSensor_FR52_WritesOnlyNameHistory_NoConfigVersion_ReportsPushNeeded(t *testing.T) {
	repo, resolver, pool := newSensorRegionTestRepository(t)
	ctx := context.Background()

	householdA := sraInsertHousehold(t, pool)
	sraInsertMembership(t, pool, householdA, "alice")
	aliceScope := sraScopeFor(t, resolver, "alice")

	boardID := sraInsertBoard(t, pool, "device-a", householdA)
	regionA := sraInsertRegion(t, pool, householdA)
	sensorID := sraInsertSensor(t, pool, boardID, &regionA, "Old Name")
	sraOpenNameHistory(t, pool, sensorID, "Old Name", time.Now().Add(-1*time.Hour))

	// -- No pending device_config: ConfigPushNeeded must be false. --
	renamed, err := repo.RenameSensor(ctx, sensorID, "New Name", aliceScope, sraAuditEntry("alice"))
	if err != nil {
		t.Fatalf("RenameSensor: %v", err)
	}
	if renamed.Name != "New Name" {
		t.Errorf("renamed.Name = %q, want %q", renamed.Name, "New Name")
	}
	if renamed.ConfigPushNeeded {
		t.Error("ConfigPushNeeded = true with no pending device_config, want false")
	}
	if got := sraSensorName(t, pool, sensorID); got != "New Name" {
		t.Errorf("sensor.name after rename = %q, want %q", got, "New Name")
	}

	var closedCount, openCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM sensor_name_history WHERE sensor_id = $1 AND valid_to IS NOT NULL`, sensorID).Scan(&closedCount); err != nil {
		t.Fatalf("count closed name-history rows: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM sensor_name_history WHERE sensor_id = $1 AND valid_to IS NULL`, sensorID).Scan(&openCount); err != nil {
		t.Fatalf("count open name-history rows: %v", err)
	}
	if closedCount != 1 {
		t.Errorf("closed sensor_name_history rows = %d, want 1 (the prior 'Old Name' interval)", closedCount)
	}
	if openCount != 1 {
		t.Errorf("open sensor_name_history rows = %d, want 1 (the new 'New Name' interval)", openCount)
	}
	// FR52: no config version, and sensor_region_history is untouched by a
	// rename.
	if n := sraCountRows(t, pool, "device_config"); n != 0 {
		t.Errorf("device_config rows after RenameSensor = %d, want 0", n)
	}
	if n := sraCountRows(t, pool, "sensor_region_history"); n != 0 {
		t.Errorf("sensor_region_history rows after RenameSensor = %d, want 0 -- a rename must write only name history", n)
	}
	if n := sraCountRows(t, pool, "audit_log"); n != 1 {
		t.Errorf("audit_log rows after RenameSensor = %d, want 1", n)
	}

	// -- A pending device_config push exists for this sensor's board:
	// ConfigPushNeeded must now be true, but the rename still must not add
	// a new device_config row of its own. --
	sraInsertPendingDeviceConfig(t, pool, boardID, 1)
	renamedAgain, err := repo.RenameSensor(ctx, sensorID, "Newer Name", aliceScope, sraAuditEntry("alice"))
	if err != nil {
		t.Fatalf("RenameSensor (with a pending device_config): %v", err)
	}
	if !renamedAgain.ConfigPushNeeded {
		t.Error("ConfigPushNeeded = false with a pending device_config on the sensor's board, want true")
	}
	if n := sraCountRows(t, pool, "device_config"); n != 1 {
		t.Errorf("device_config rows after a second RenameSensor = %d, want still 1 (the pre-existing pending push, unmodified by the rename)", n)
	}
}

// TestRenameSensor_EmptyName_Refused proves FR59.2's validation clause:
// an empty (or whitespace-only) name is refused as invalid_argument,
// naming the name field, and nothing is written.
func TestRenameSensor_EmptyName_Refused(t *testing.T) {
	repo, resolver, pool := newSensorRegionTestRepository(t)
	ctx := context.Background()

	householdA := sraInsertHousehold(t, pool)
	sraInsertMembership(t, pool, householdA, "alice")
	aliceScope := sraScopeFor(t, resolver, "alice")

	boardID := sraInsertBoard(t, pool, "device-a", householdA)
	regionA := sraInsertRegion(t, pool, householdA)
	sensorID := sraInsertSensor(t, pool, boardID, &regionA, "Old Name")
	sraOpenNameHistory(t, pool, sensorID, "Old Name", time.Now().Add(-1*time.Hour))

	_, err := repo.RenameSensor(ctx, sensorID, "   ", aliceScope, sraAuditEntry("alice"))
	if err == nil {
		t.Fatal("RenameSensor with a whitespace-only name returned nil error, want a refusal")
	}
	f := sraFailureOf(t, err)
	if f.Class != string(contract.FailureInvalidArgument) {
		t.Errorf("failure class = %q, want %q", f.Class, contract.FailureInvalidArgument)
	}
	if f.Field != "name" {
		t.Errorf("failure field = %q, want %q", f.Field, "name")
	}
	if got := sraSensorName(t, pool, sensorID); got != "Old Name" {
		t.Errorf("sensor.name after a refused rename = %q, want unchanged %q", got, "Old Name")
	}
	if n := sraCountRows(t, pool, "audit_log"); n != 0 {
		t.Errorf("audit_log rows after a refused rename = %d, want 0", n)
	}
}

// TestRenameSensor_NonMember_NotFound mirrors
// TestAssignSensorRegion_NonMember_NotFound for RenameSensor -- NFR2's
// same not-found collapse.
func TestRenameSensor_NonMember_NotFound(t *testing.T) {
	repo, resolver, pool := newSensorRegionTestRepository(t)
	ctx := context.Background()

	householdA := sraInsertHousehold(t, pool)
	sraInsertMembership(t, pool, householdA, "alice")

	boardID := sraInsertBoard(t, pool, "device-a", householdA)
	regionA := sraInsertRegion(t, pool, householdA)
	sensorID := sraInsertSensor(t, pool, boardID, &regionA, "Old Name")
	sraOpenNameHistory(t, pool, sensorID, "Old Name", time.Now().Add(-1*time.Hour))

	bobScope := sraScopeFor(t, resolver, "bob")
	_, err := repo.RenameSensor(ctx, sensorID, "Bob's Name", bobScope, sraAuditEntry("bob"))
	if !errors.Is(err, ErrSensorNotFound) {
		t.Errorf("RenameSensor by a non-member error = %v, want ErrSensorNotFound", err)
	}
	if n := sraCountRows(t, pool, "audit_log"); n != 0 {
		t.Errorf("audit_log rows after a non-member's refused rename = %d, want 0", n)
	}
}
