//go:build integration

// Real-Postgres (TimescaleDB, for capture.Recorder's time_bucket call)
// integration coverage for #1380's FR74/FR24 (relocation-induced half):
// Repository.RelocateSubtree (leaflab/api/relocate.go), exercised the same
// way regions_lifecycle_integration_test.go/plants_lifecycle_integration_
// test.go/sensor_region_integration_test.go exercise their own Phase 5
// RPCs -- directly against Repository, no server.go wiring yet (see
// relocate.go's own doc comment).
//
// Schema is hand-written DDL mirroring the relevant shape of migration 001
// (plant, plant_type, sensor, sensor_reading), migration 015 (household,
// household_membership, board.household_id, region.household_id, plant.
// household_id), migration 011 (sensor_region_history), migration 012
// (v_region_path), migration 017 (plant_region_history.relocation_induced),
// migration 020 (region.retired_at/successor_region_id, v_region_household)
// and migration 033/034 (boundary_capture, sensor_region_history.
// relocation_induced) -- deliberately not shared with any other integration
// test file in this package, per capture's own "Bazel test targets do not
// always share compilation" precedent already cited by
// plants_lifecycle_integration_test.go. Requires the TimescaleDB image (not
// plain postgres) because capture.Recorder.Record calls time_bucket() and
// RelocateSubtree calls it for every moved sensor (FR20).
//
// FR73 invalidation coverage: invalidationPub is left nil throughout
// (Repository.SetInvalidationPublisher is never called), exactly like
// sensor_region_integration_test.go's own precedent -- there is no
// real-RabbitMQ test double in this repo, so a published invalidation.Event
// cannot be observed directly from this package's tests. What is proven
// here instead is that RelocateSubtree's publish loop is safe with a nil
// publisher (every test below calls it without panicking) -- the actual
// Event shape a moved sensor produces is identical to AssignSensorRegion's
// own (same invalidation.KindRegion construction, see relocate.go), which
// sensor_region_integration_test.go's own doc comment already defers to
// leaflab/processor/handler_test.go for.
//
// FR56 ("no plant ever reports unmonitored") has not landed on this branch
// lineage (grep the repo: no FR56 symbol) -- this task's own Testing
// section permits stubbing that clause until it has; see
// TestRelocateSubtree_FR56NeverReportsUnmonitored below.
//
// See //libs/go/dbtest's README for how to run integration tests like this
// one.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //leaflab/api:relocate_integration_test --test_output=all
package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/leaflab/api/audit"
	"github.com/whale-net/everything/leaflab/api/authz"
	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/libs/go/dbtest"
)

// relocTimescaleImage matches plantsTimescaleImage/sraTimescaleImage --
// RelocateSubtree's FR20 capture.Recorder.Record call needs a real
// time_bucket(), a TimescaleDB function not present in plain postgres.
const relocTimescaleImage = "timescale/timescaledb:latest-pg16"

// relocTestSchema is a hermetic, hand-written mirror of every table
// RelocateSubtree's composition touches -- region (+ v_region_path/
// v_region_household, migrations 012/020), sensor_reading (read by
// SetRegionParent's own pre-check, exercised by the FR50.5 round-trip test
// below), plant_type/plant/plant_region_history (migrations 001/015/017),
// board/sensor/sensor_region_history (migrations 001/011/015/034) and
// boundary_capture (migration 033) -- plus audit_log (migration 016).
//
// Deliberately omits migration 020's trg_region_parentage_immutable
// trigger: SetRegionParent's own descendant-reading refusal
// (regions.go) is a pre-check against sensor_reading run *before* any
// UPDATE is attempted, not a catch of the trigger's exception -- see that
// function's doc comment -- so the FR50.5 round-trip test below exercises
// the real caller-facing refusal without needing the trigger as a backstop.
const relocTestSchema = `
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
	CREATE INDEX idx_reloc_household_membership_current
		ON household_membership(principal_subject) WHERE valid_to IS NULL;

	CREATE TABLE region (
		region_id            BIGSERIAL PRIMARY KEY,
		parent_region_id     BIGINT REFERENCES region(region_id) ON DELETE RESTRICT,
		name                 VARCHAR(255) NOT NULL,
		description          TEXT,
		household_id         BIGINT REFERENCES household(household_id),
		created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		retired_at           TIMESTAMPTZ,
		successor_region_id  BIGINT REFERENCES region(region_id)
	);
	CREATE INDEX idx_reloc_region_active ON region(region_id) WHERE retired_at IS NULL;

	CREATE TABLE sensor_reading (
		reading_id  BIGSERIAL PRIMARY KEY,
		region_id   BIGINT REFERENCES region(region_id),
		value       DOUBLE PRECISION NOT NULL DEFAULT 0,
		recorded_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);

	-- v_region_path: migration 012_views, verbatim shape.
	CREATE VIEW v_region_path AS
	WITH RECURSIVE path AS (
		SELECT
			r.region_id,
			r.name,
			r.parent_region_id,
			ARRAY[r.region_id]::BIGINT[] AS path_ids,
			ARRAY[r.name]::TEXT[]        AS path_names,
			r.name::TEXT                 AS path_name,
			0                            AS depth
		FROM region r
		WHERE r.parent_region_id IS NULL

		UNION ALL

		SELECT
			r.region_id,
			r.name,
			r.parent_region_id,
			p.path_ids   || r.region_id,
			p.path_names || r.name,
			p.path_name  || ' / ' || r.name,
			p.depth + 1
		FROM region r
		JOIN path p ON p.region_id = r.parent_region_id
	)
	SELECT region_id, name, parent_region_id, path_ids, path_names, path_name, depth FROM path;

	-- v_region_household: migration 020_region_lifecycle, verbatim shape.
	CREATE VIEW v_region_household AS
	SELECT
		r.region_id,
		r.parent_region_id,
		r.name,
		r.description,
		r.retired_at,
		r.successor_region_id,
		root.household_id
	FROM region r
	JOIN v_region_path rp ON rp.region_id = r.region_id
	JOIN region root      ON root.region_id = rp.path_ids[1];

	CREATE TABLE plant_type (
		plant_type_id BIGSERIAL PRIMARY KEY,
		common_name   VARCHAR(128) NOT NULL
	);

	CREATE TABLE plant (
		plant_id      BIGSERIAL PRIMARY KEY,
		region_id     BIGINT NOT NULL REFERENCES region(region_id) ON DELETE RESTRICT,
		plant_type_id BIGINT NOT NULL REFERENCES plant_type(plant_type_id),
		household_id  BIGINT NOT NULL REFERENCES household(household_id),
		name          VARCHAR(128) NOT NULL,
		created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		removed_at    TIMESTAMPTZ
	);

	CREATE TABLE plant_region_history (
		plant_region_history_id BIGSERIAL PRIMARY KEY,
		plant_id            BIGINT NOT NULL REFERENCES plant(plant_id),
		region_id           BIGINT NOT NULL REFERENCES region(region_id),
		valid_from           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		valid_to             TIMESTAMPTZ,
		relocation_induced   BOOLEAN NOT NULL DEFAULT FALSE
	);
	CREATE INDEX idx_reloc_prh_plant_current ON plant_region_history(plant_id) WHERE valid_to IS NULL;
	CREATE INDEX idx_reloc_prh_region_current ON plant_region_history(region_id) WHERE valid_to IS NULL;

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
		name          VARCHAR(128) NOT NULL DEFAULT 'sensor',
		registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);

	CREATE TABLE sensor_region_history (
		history_id         BIGSERIAL PRIMARY KEY,
		sensor_id          BIGINT NOT NULL REFERENCES sensor(sensor_id),
		region_id          BIGINT NOT NULL REFERENCES region(region_id),
		valid_from          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		valid_to            TIMESTAMPTZ,
		relocation_induced  BOOLEAN NOT NULL DEFAULT FALSE
	);
	CREATE INDEX idx_reloc_srh_current ON sensor_region_history(sensor_id) WHERE valid_to IS NULL;

	CREATE TABLE boundary_capture (
		capture_id    BIGSERIAL PRIMARY KEY,
		sensor_id     BIGINT NOT NULL REFERENCES sensor(sensor_id) ON DELETE RESTRICT,
		boundary_at   TIMESTAMPTZ NOT NULL,
		tier          TEXT NOT NULL,
		bucket_start  TIMESTAMPTZ NOT NULL,
		state         TEXT NOT NULL DEFAULT 'pending',
		completed_at  TIMESTAMPTZ,
		CONSTRAINT reloc_boundary_capture_tier_check
			CHECK (tier IN ('five_minute', 'hourly')),
		CONSTRAINT reloc_boundary_capture_state_check
			CHECK (state IN ('pending', 'completed')),
		CONSTRAINT reloc_boundary_capture_completed_at_check
			CHECK ((state = 'completed') = (completed_at IS NOT NULL))
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
`

func newRelocTestRepository(t *testing.T) (*Repository, *authz.PGResolver, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Image: relocTimescaleImage, Schema: relocTestSchema})
	repo := NewRepository(db.Pool)
	resolver := authz.NewPGResolver(db.Pool)
	return repo, resolver, db.Pool
}

func relocInsertHousehold(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO household DEFAULT VALUES RETURNING household_id`).Scan(&id); err != nil {
		t.Fatalf("insert household: %v", err)
	}
	return id
}

func relocInsertMembership(t *testing.T, pool *pgxpool.Pool, householdID int64, subject string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO household_membership (household_id, principal_subject) VALUES ($1, $2)`,
		householdID, subject); err != nil {
		t.Fatalf("insert membership for %q: %v", subject, err)
	}
}

func relocScopeFor(t *testing.T, resolver *authz.PGResolver, subject string) authz.Scope {
	t.Helper()
	scope, err := resolver.ScopeForPrincipal(context.Background(), subject)
	if err != nil {
		t.Fatalf("ScopeForPrincipal(%q): %v", subject, err)
	}
	return scope
}

// relocAuditEntry returns a minimal valid audit.Entry for actor --
// RelocateSubtree only reads ActorSubject/ActorKind/CorrelationID off of
// it (see relocate.go's doc comment: the recorded Entry is built fresh via
// audit.NewRelocationEntry), so Action/EntityKind here are never observed.
func relocAuditEntry(actor string) audit.Entry {
	return audit.Entry{ActorSubject: actor, ActorKind: audit.ActorKindHuman}
}

func relocInsertRegionRaw(t *testing.T, pool *pgxpool.Pool, parentRegionID *int64, name string, householdID *int64) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO region (parent_region_id, name, household_id) VALUES ($1, $2, $3) RETURNING region_id
	`, parentRegionID, name, householdID).Scan(&id); err != nil {
		t.Fatalf("insert region %s: %v", name, err)
	}
	return id
}

func relocInsertPlantType(t *testing.T, pool *pgxpool.Pool, commonName string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO plant_type (common_name) VALUES ($1) RETURNING plant_type_id`,
		commonName).Scan(&id); err != nil {
		t.Fatalf("insert plant_type %s: %v", commonName, err)
	}
	return id
}

func relocInsertBoard(t *testing.T, pool *pgxpool.Pool, deviceID string, householdID int64) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO board (device_id, household_id) VALUES ($1, $2) RETURNING board_id`,
		deviceID, householdID).Scan(&id); err != nil {
		t.Fatalf("insert board %s: %v", deviceID, err)
	}
	return id
}

func relocInsertSensor(t *testing.T, pool *pgxpool.Pool, boardID int64) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO sensor (board_id) VALUES ($1) RETURNING sensor_id`,
		boardID).Scan(&id); err != nil {
		t.Fatalf("insert sensor on board %d: %v", boardID, err)
	}
	return id
}

func relocInsertReading(t *testing.T, pool *pgxpool.Pool, regionID int64, value float64, recordedAt time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO sensor_reading (region_id, value, recorded_at) VALUES ($1, $2, $3)`,
		regionID, value, recordedAt); err != nil {
		t.Fatalf("insert sensor_reading in region %d: %v", regionID, err)
	}
}

func relocCountRows(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("count rows in %s: %v", table, err)
	}
	return n
}

func relocFailureOf(t *testing.T, err error) *pb.Failure {
	t.Helper()
	f, ok := contract.FromError(err)
	if !ok {
		t.Fatalf("error carries no contract.Failure detail: %v", err)
	}
	return f
}

// relocSnapshot captures every row count RelocateSubtree's write
// transaction can affect, for the atomicity tests' "zero rows changed"
// assertion.
type relocSnapshot struct {
	regions    int
	plantHist  int
	sensorHist int
	captures   int
	auditRows  int
}

func relocTakeSnapshot(t *testing.T, pool *pgxpool.Pool) relocSnapshot {
	t.Helper()
	return relocSnapshot{
		regions:    relocCountRows(t, pool, "region"),
		plantHist:  relocCountRows(t, pool, "plant_region_history"),
		sensorHist: relocCountRows(t, pool, "sensor_region_history"),
		captures:   relocCountRows(t, pool, "boundary_capture"),
		auditRows:  relocCountRows(t, pool, "audit_log"),
	}
}

func (s relocSnapshot) assertUnchanged(t *testing.T, pool *pgxpool.Pool, label string) {
	t.Helper()
	after := relocTakeSnapshot(t, pool)
	if after != s {
		t.Errorf("%s: row counts changed -- before %+v, after %+v, want identical (atomicity violated)", label, s, after)
	}
}

// -- Happy path: mirror/move/retire, continuity, one audit record --------

// TestRelocateSubtree_MirrorsMovesRetires_OneAuditRecord_HistoryContinuous
// is FR74's core scenario: relocating Shelf (with a Pot beneath it, a
// sensor and two plants) under a second room mirrors the subtree, moves
// every current sensor/plant placement into the mirrored regions, retires
// the originals in place with successor references, and does all of it as
// exactly one audited write.
func TestRelocateSubtree_MirrorsMovesRetires_OneAuditRecord_HistoryContinuous(t *testing.T) {
	repo, resolver, pool := newRelocTestRepository(t)
	ctx := context.Background()

	householdID := relocInsertHousehold(t, pool)
	relocInsertMembership(t, pool, householdID, "alice")
	aliceScope := relocScopeFor(t, resolver, "alice")

	roomA, err := repo.CreateRegion(ctx, nil, "Room A", "", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Room A): %v", err)
	}
	roomB, err := repo.CreateRegion(ctx, nil, "Room B", "", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Room B, destination): %v", err)
	}
	shelf, err := repo.CreateRegion(ctx, &roomA.RegionID, "Shelf", "", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Shelf, under Room A): %v", err)
	}
	pot, err := repo.CreateRegion(ctx, &shelf.RegionID, "Pot", "", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Pot, under Shelf): %v", err)
	}

	plantTypeID := relocInsertPlantType(t, pool, "Fern")
	plantOnShelf, err := repo.CreatePlant(ctx, shelf.RegionID, plantTypeID, "Shelf Fern", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreatePlant (on Shelf): %v", err)
	}
	plantInPot, err := repo.CreatePlant(ctx, pot.RegionID, plantTypeID, "Pot Fern", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreatePlant (in Pot): %v", err)
	}

	boardID := relocInsertBoard(t, pool, "device-1", householdID)
	sensorID := relocInsertSensor(t, pool, boardID)
	if _, err := repo.AssignSensorRegion(ctx, sensorID, shelf.RegionID, aliceScope, relocAuditEntry("alice")); err != nil {
		t.Fatalf("AssignSensorRegion (sensor onto Shelf): %v", err)
	}

	auditBefore := relocCountRows(t, pool, "audit_log")

	result, err := repo.RelocateSubtree(ctx, shelf.RegionID, roomB.RegionID, "Reorganizing the grow tent", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("RelocateSubtree: %v", err)
	}
	if result.RegionsMirrored != 2 {
		t.Errorf("RegionsMirrored = %d, want 2 (Shelf + Pot)", result.RegionsMirrored)
	}
	if result.SensorPlacementsMoved != 1 {
		t.Errorf("SensorPlacementsMoved = %d, want 1", result.SensorPlacementsMoved)
	}
	if result.PlantPlacementsMoved != 2 {
		t.Errorf("PlantPlacementsMoved = %d, want 2", result.PlantPlacementsMoved)
	}
	if result.NewRoot.ParentRegionID == nil || *result.NewRoot.ParentRegionID != roomB.RegionID {
		t.Errorf("NewRoot.ParentRegionID = %v, want %d (Room B)", result.NewRoot.ParentRegionID, roomB.RegionID)
	}
	if result.NewRoot.Name != "Shelf" {
		t.Errorf("NewRoot.Name = %q, want %q (mirrored name preserved)", result.NewRoot.Name, "Shelf")
	}

	// -- Originals retired in place, successor references set (FR22.2) --
	shelfAfter, err := repo.GetRegionByID(ctx, shelf.RegionID)
	if err != nil {
		t.Fatalf("GetRegionByID (original Shelf): %v", err)
	}
	if shelfAfter.RetiredAt == nil {
		t.Error("original Shelf.RetiredAt is nil, want it retired")
	}
	if shelfAfter.SuccessorRegionID == nil || *shelfAfter.SuccessorRegionID != result.NewRoot.RegionID {
		t.Errorf("original Shelf.SuccessorRegionID = %v, want %d (mirrored Shelf)", shelfAfter.SuccessorRegionID, result.NewRoot.RegionID)
	}

	var mirroredPotID int64
	if err := pool.QueryRow(ctx, `SELECT region_id FROM region WHERE parent_region_id = $1 AND region_id != $2`,
		result.NewRoot.RegionID, result.NewRoot.RegionID).Scan(&mirroredPotID); err != nil {
		t.Fatalf("find mirrored Pot: %v", err)
	}
	potAfter, err := repo.GetRegionByID(ctx, pot.RegionID)
	if err != nil {
		t.Fatalf("GetRegionByID (original Pot): %v", err)
	}
	if potAfter.RetiredAt == nil {
		t.Error("original Pot.RetiredAt is nil, want it retired")
	}
	if potAfter.SuccessorRegionID == nil || *potAfter.SuccessorRegionID != mirroredPotID {
		t.Errorf("original Pot.SuccessorRegionID = %v, want %d (mirrored Pot)", potAfter.SuccessorRegionID, mirroredPotID)
	}

	// -- Sensor and plant history continuous across the relocation --------
	// Postgres's NOW() is fixed for the whole transaction (transaction_
	// timestamp()), so the closed old interval's valid_to, the opened new
	// interval's valid_from, and the retired original region's retired_at
	// must all be byte-identical -- proving no gap and no overlap.
	var sensorOldValidTo, sensorNewValidFrom time.Time
	var sensorNewInduced bool
	if err := pool.QueryRow(ctx, `SELECT valid_to FROM sensor_region_history WHERE sensor_id = $1 AND region_id = $2`,
		sensorID, shelf.RegionID).Scan(&sensorOldValidTo); err != nil {
		t.Fatalf("read sensor's old closed interval: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT valid_from, relocation_induced FROM sensor_region_history WHERE sensor_id = $1 AND valid_to IS NULL`,
		sensorID).Scan(&sensorNewValidFrom, &sensorNewInduced); err != nil {
		t.Fatalf("read sensor's new open interval: %v", err)
	}
	if !sensorOldValidTo.Equal(sensorNewValidFrom) {
		t.Errorf("sensor history has a gap/overlap: old valid_to = %v, new valid_from = %v, want equal", sensorOldValidTo, sensorNewValidFrom)
	}
	if !sensorNewInduced {
		t.Error("sensor's new interval relocation_induced = false, want true (FR24)")
	}
	if !shelfAfter.RetiredAt.Equal(sensorNewValidFrom) {
		t.Errorf("original Shelf.RetiredAt = %v, want equal to the relocation boundary %v (one shared transaction instant)", *shelfAfter.RetiredAt, sensorNewValidFrom)
	}

	timeline, ok, err := repo.GetPlantPlacementTimeline(ctx, plantOnShelf.PlantID)
	if err != nil || !ok {
		t.Fatalf("GetPlantPlacementTimeline (plant on Shelf): ok=%v err=%v", ok, err)
	}
	if len(timeline) != 2 {
		t.Fatalf("timeline for plant on Shelf has %d intervals, want 2 (original + relocation-induced)", len(timeline))
	}
	if timeline[0].RegionID != shelf.RegionID || timeline[0].RelocationInduced {
		t.Errorf("timeline[0] = %+v, want RegionID=%d RelocationInduced=false (CreatePlant's own opening interval)", timeline[0], shelf.RegionID)
	}
	if timeline[0].ValidTo == nil || !timeline[0].ValidTo.Equal(sensorNewValidFrom) {
		t.Errorf("timeline[0].ValidTo = %v, want equal to the relocation boundary %v (continuous, no gap)", timeline[0].ValidTo, sensorNewValidFrom)
	}
	if timeline[1].RegionID != result.NewRoot.RegionID || !timeline[1].RelocationInduced {
		t.Errorf("timeline[1] = %+v, want RegionID=%d RelocationInduced=true", timeline[1], result.NewRoot.RegionID)
	}
	if !timeline[1].ValidFrom.Equal(sensorNewValidFrom) {
		t.Errorf("timeline[1].ValidFrom = %v, want equal to the relocation boundary %v", timeline[1].ValidFrom, sensorNewValidFrom)
	}

	potTimeline, ok, err := repo.GetPlantPlacementTimeline(ctx, plantInPot.PlantID)
	if err != nil || !ok {
		t.Fatalf("GetPlantPlacementTimeline (plant in Pot): ok=%v err=%v", ok, err)
	}
	if len(potTimeline) != 2 || potTimeline[1].RegionID != mirroredPotID || !potTimeline[1].RelocationInduced {
		t.Fatalf("timeline for plant in Pot = %+v, want a second interval in the mirrored Pot (%d), relocation_induced", potTimeline, mirroredPotID)
	}

	// -- FR20: boundary captures for every moved boundary ------------------
	// AssignSensorRegion's own initial placement (fixture setup, above)
	// already wrote 2 boundary_capture rows for this sensor (its own FR20
	// coupling) -- this checks the 2 the relocation itself adds, at the
	// relocation's own boundary instant, not the fixture's.
	var captureCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM boundary_capture WHERE sensor_id = $1 AND boundary_at = $2`,
		sensorID, sensorNewValidFrom).Scan(&captureCount); err != nil {
		t.Fatalf("count boundary_capture rows for sensor %d at the relocation boundary: %v", sensorID, err)
	}
	if captureCount != 2 {
		t.Errorf("boundary_capture rows for the moved sensor at the relocation boundary = %d, want 2 (five_minute + hourly tiers)", captureCount)
	}

	// -- FR8: exactly one audit record for the whole operation -------------
	auditAfter := relocCountRows(t, pool, "audit_log")
	if auditAfter-auditBefore != 1 {
		t.Fatalf("audit_log rows added by RelocateSubtree = %d, want exactly 1 (not one per moved entity)", auditAfter-auditBefore)
	}
	var action, entityID, reason string
	if err := pool.QueryRow(ctx, `SELECT action, entity_id, reason FROM audit_log ORDER BY audit_id DESC LIMIT 1`).Scan(&action, &entityID, &reason); err != nil {
		t.Fatalf("read last audit_log row: %v", err)
	}
	if action != audit.ActionRelocateSubtree {
		t.Errorf("audit action = %q, want %q", action, audit.ActionRelocateSubtree)
	}
	if entityID != strconv.FormatInt(shelf.RegionID, 10) {
		t.Errorf("audit entity_id = %q, want %q (the relocated subtree's original root)", entityID, strconv.FormatInt(shelf.RegionID, 10))
	}
	if reason != "Reorganizing the grow tent" {
		t.Errorf("audit reason = %q, want the caller-supplied reason", reason)
	}
}

// -- Atomicity: a real write-time failure at each of the 3 composition steps --

// relocPoisonTrigger installs a trigger that raises exceptions once a
// caller-chosen row is touched, letting each atomicity test target exactly
// one of RelocateSubtree's three composition steps without weakening the
// schema's own FK integrity elsewhere. Installed only after every fixture
// row it must not poison already exists.
func relocPoisonTrigger(t *testing.T, pool *pgxpool.Pool, funcName, ddl string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), ddl); err != nil {
		t.Fatalf("install poison trigger %s: %v", funcName, err)
	}
}

// TestRelocateSubtree_Atomicity_MirrorStepFailure_ZeroRowsChanged injects a
// real write-time failure into step 1 (mirroring): the poisoned
// descendant's name is copied verbatim into its mirrored INSERT, which the
// trigger refuses -- even though the shelf's own mirror INSERT (this
// subtree's root) already succeeded earlier in the same step. The whole
// transaction must roll back: zero rows changed anywhere.
func TestRelocateSubtree_Atomicity_MirrorStepFailure_ZeroRowsChanged(t *testing.T) {
	repo, resolver, pool := newRelocTestRepository(t)
	ctx := context.Background()

	householdID := relocInsertHousehold(t, pool)
	relocInsertMembership(t, pool, householdID, "alice")
	aliceScope := relocScopeFor(t, resolver, "alice")

	roomA, err := repo.CreateRegion(ctx, nil, "Room A", "", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Room A): %v", err)
	}
	roomB, err := repo.CreateRegion(ctx, nil, "Room B", "", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Room B): %v", err)
	}
	shelf, err := repo.CreateRegion(ctx, &roomA.RegionID, "Shelf", "", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Shelf): %v", err)
	}
	// The poisoned descendant -- mirrored *after* Shelf's own root mirror
	// row already succeeded in this same step, so this test also proves a
	// step-1 failure rolls back a step-1 row that already committed to the
	// in-progress transaction, not just prevents new ones.
	if _, err := repo.CreateRegion(ctx, &shelf.RegionID, "POISON_MIRROR_STEP1", "", aliceScope, relocAuditEntry("alice")); err != nil {
		t.Fatalf("CreateRegion (poisoned descendant): %v", err)
	}

	relocPoisonTrigger(t, pool, "reloc_poison_mirror_insert", `
		CREATE FUNCTION reloc_poison_mirror_insert() RETURNS TRIGGER AS $$
		BEGIN
			IF NEW.name = 'POISON_MIRROR_STEP1' THEN
				RAISE EXCEPTION 'reloc test: refuse mirror insert (step 1 atomicity check)';
			END IF;
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql;
		CREATE TRIGGER trg_reloc_poison_mirror_insert BEFORE INSERT ON region
			FOR EACH ROW EXECUTE FUNCTION reloc_poison_mirror_insert();
	`)

	before := relocTakeSnapshot(t, pool)
	_, err = repo.RelocateSubtree(ctx, shelf.RegionID, roomB.RegionID, "Reorganizing", aliceScope, relocAuditEntry("alice"))
	if err == nil {
		t.Fatal("RelocateSubtree with a step-1 mirror failure returned nil error, want it to fail")
	}
	if !strings.Contains(err.Error(), "POISON_MIRROR_STEP1") && !strings.Contains(err.Error(), "refuse mirror insert") {
		t.Errorf("error = %v, want it to surface the step-1 poison trigger's failure", err)
	}
	before.assertUnchanged(t, pool, "step-1 mirror failure")

	unchanged, err := repo.GetRegionByID(ctx, shelf.RegionID)
	if err != nil {
		t.Fatalf("GetRegionByID (Shelf) after refused relocation: %v", err)
	}
	if unchanged.RetiredAt != nil || unchanged.SuccessorRegionID != nil {
		t.Errorf("Shelf after a rolled-back relocation = %+v, want RetiredAt/SuccessorRegionID both nil", unchanged)
	}
}

// TestRelocateSubtree_Atomicity_SensorMoveStepFailure_ZeroRowsChanged
// injects a real write-time failure into step 2a (moving sensor
// placements): one of two sensors on Shelf is poisoned by sensor_id, so
// its relocation-marked sensor_region_history INSERT fails -- after step 1
// (mirroring) already wrote real rows, and possibly after the other
// sensor's own move already wrote real rows too (map iteration order is
// unspecified). The whole transaction must roll back regardless of which
// sensor is processed first.
func TestRelocateSubtree_Atomicity_SensorMoveStepFailure_ZeroRowsChanged(t *testing.T) {
	repo, resolver, pool := newRelocTestRepository(t)
	ctx := context.Background()

	householdID := relocInsertHousehold(t, pool)
	relocInsertMembership(t, pool, householdID, "alice")
	aliceScope := relocScopeFor(t, resolver, "alice")

	roomA, err := repo.CreateRegion(ctx, nil, "Room A", "", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Room A): %v", err)
	}
	roomB, err := repo.CreateRegion(ctx, nil, "Room B", "", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Room B): %v", err)
	}
	shelf, err := repo.CreateRegion(ctx, &roomA.RegionID, "Shelf", "", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Shelf): %v", err)
	}

	boardID := relocInsertBoard(t, pool, "device-ok", householdID)
	okSensorID := relocInsertSensor(t, pool, boardID)
	if _, err := repo.AssignSensorRegion(ctx, okSensorID, shelf.RegionID, aliceScope, relocAuditEntry("alice")); err != nil {
		t.Fatalf("AssignSensorRegion (ok sensor): %v", err)
	}
	poisonBoardID := relocInsertBoard(t, pool, "device-poison", householdID)
	poisonSensorID := relocInsertSensor(t, pool, poisonBoardID)
	if _, err := repo.AssignSensorRegion(ctx, poisonSensorID, shelf.RegionID, aliceScope, relocAuditEntry("alice")); err != nil {
		t.Fatalf("AssignSensorRegion (poison sensor): %v", err)
	}

	relocPoisonTrigger(t, pool, "reloc_poison_sensor_move", fmt.Sprintf(`
		CREATE FUNCTION reloc_poison_sensor_move() RETURNS TRIGGER AS $$
		BEGIN
			IF NEW.sensor_id = %d THEN
				RAISE EXCEPTION 'reloc test: refuse sensor move insert (step 2 atomicity check)';
			END IF;
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql;
		CREATE TRIGGER trg_reloc_poison_sensor_move BEFORE INSERT ON sensor_region_history
			FOR EACH ROW EXECUTE FUNCTION reloc_poison_sensor_move();
	`, poisonSensorID))

	before := relocTakeSnapshot(t, pool)
	_, err = repo.RelocateSubtree(ctx, shelf.RegionID, roomB.RegionID, "Reorganizing", aliceScope, relocAuditEntry("alice"))
	if err == nil {
		t.Fatal("RelocateSubtree with a step-2 sensor-move failure returned nil error, want it to fail")
	}
	before.assertUnchanged(t, pool, "step-2 sensor-move failure")

	for _, sid := range []int64{okSensorID, poisonSensorID} {
		var currentRegion int64
		if err := pool.QueryRow(ctx, `SELECT region_id FROM sensor_region_history WHERE sensor_id = $1 AND valid_to IS NULL`, sid).Scan(&currentRegion); err != nil {
			t.Fatalf("read sensor %d current placement after rollback: %v", sid, err)
		}
		if currentRegion != shelf.RegionID {
			t.Errorf("sensor %d current region after rolled-back relocation = %d, want unchanged %d (original Shelf)", sid, currentRegion, shelf.RegionID)
		}
	}
}

// TestRelocateSubtree_Atomicity_RetireStepFailure_ZeroRowsChanged injects a
// real write-time failure into step 3 (retiring originals): after mirror
// (step 1) and the sensor/plant moves (steps 2a/2b) have all succeeded
// within the transaction, retiring Shelf itself is poisoned. The whole
// transaction -- including the mirror and both moves that already wrote
// real rows -- must roll back.
func TestRelocateSubtree_Atomicity_RetireStepFailure_ZeroRowsChanged(t *testing.T) {
	repo, resolver, pool := newRelocTestRepository(t)
	ctx := context.Background()

	householdID := relocInsertHousehold(t, pool)
	relocInsertMembership(t, pool, householdID, "alice")
	aliceScope := relocScopeFor(t, resolver, "alice")

	roomA, err := repo.CreateRegion(ctx, nil, "Room A", "", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Room A): %v", err)
	}
	roomB, err := repo.CreateRegion(ctx, nil, "Room B", "", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Room B): %v", err)
	}
	shelf, err := repo.CreateRegion(ctx, &roomA.RegionID, "Shelf", "", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Shelf): %v", err)
	}

	plantTypeID := relocInsertPlantType(t, pool, "Fern")
	plant, err := repo.CreatePlant(ctx, shelf.RegionID, plantTypeID, "Shelf Fern", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreatePlant: %v", err)
	}
	boardID := relocInsertBoard(t, pool, "device-1", householdID)
	sensorID := relocInsertSensor(t, pool, boardID)
	if _, err := repo.AssignSensorRegion(ctx, sensorID, shelf.RegionID, aliceScope, relocAuditEntry("alice")); err != nil {
		t.Fatalf("AssignSensorRegion: %v", err)
	}

	relocPoisonTrigger(t, pool, "reloc_poison_region_retire", fmt.Sprintf(`
		CREATE FUNCTION reloc_poison_region_retire() RETURNS TRIGGER AS $$
		BEGIN
			IF OLD.region_id = %d THEN
				RAISE EXCEPTION 'reloc test: refuse region retire update (step 3 atomicity check)';
			END IF;
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql;
		CREATE TRIGGER trg_reloc_poison_region_retire BEFORE UPDATE ON region
			FOR EACH ROW EXECUTE FUNCTION reloc_poison_region_retire();
	`, shelf.RegionID))

	before := relocTakeSnapshot(t, pool)
	_, err = repo.RelocateSubtree(ctx, shelf.RegionID, roomB.RegionID, "Reorganizing", aliceScope, relocAuditEntry("alice"))
	if err == nil {
		t.Fatal("RelocateSubtree with a step-3 retire failure returned nil error, want it to fail")
	}
	before.assertUnchanged(t, pool, "step-3 retire failure")

	shelfAfter, err := repo.GetRegionByID(ctx, shelf.RegionID)
	if err != nil {
		t.Fatalf("GetRegionByID (Shelf) after refused relocation: %v", err)
	}
	if shelfAfter.RetiredAt != nil {
		t.Error("Shelf.RetiredAt set after a rolled-back relocation, want nil")
	}

	var plantRegion int64
	if err := pool.QueryRow(ctx, `SELECT region_id FROM plant WHERE plant_id = $1`, plant.PlantID).Scan(&plantRegion); err != nil {
		t.Fatalf("read plant.region_id cache after rollback: %v", err)
	}
	if plantRegion != shelf.RegionID {
		t.Errorf("plant.region_id after rolled-back relocation = %d, want unchanged %d", plantRegion, shelf.RegionID)
	}
}

// -- No observable intermediate state: a concurrent reader during the move --

// TestRelocateSubtree_ConcurrentReader_NeverObservesIntermediateState
// proves the transaction is never visible half-applied: a slow-motion
// relocation (an artificial per-insert delay, so the window is wide enough
// to poll through) runs concurrently with a reader that repeatedly checks,
// via a second connection out of the same pool, that it never sees a
// mirrored child region without the original already retired, never sees a
// plant with zero currently-open placement intervals (FR56's own
// precondition -- a plant is "unmonitored" only if this is ever true), and
// never sees a sensor with more than one currently-open placement interval
// (FR1.2/NFR6.1's single-current-value invariant). Postgres's READ
// COMMITTED isolation makes this hold by construction once every write is
// inside one transaction (auditedWrite) -- this test exercises that
// property against the real database rather than assuming it.
func TestRelocateSubtree_ConcurrentReader_NeverObservesIntermediateState(t *testing.T) {
	repo, resolver, pool := newRelocTestRepository(t)
	ctx := context.Background()

	householdID := relocInsertHousehold(t, pool)
	relocInsertMembership(t, pool, householdID, "alice")
	aliceScope := relocScopeFor(t, resolver, "alice")

	roomA, err := repo.CreateRegion(ctx, nil, "Room A", "", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Room A): %v", err)
	}
	roomB, err := repo.CreateRegion(ctx, nil, "Room B", "", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Room B): %v", err)
	}
	shelf, err := repo.CreateRegion(ctx, &roomA.RegionID, "Shelf", "", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Shelf): %v", err)
	}
	pot, err := repo.CreateRegion(ctx, &shelf.RegionID, "Pot", "", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Pot): %v", err)
	}

	plantTypeID := relocInsertPlantType(t, pool, "Fern")
	plant, err := repo.CreatePlant(ctx, pot.RegionID, plantTypeID, "Pot Fern", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreatePlant: %v", err)
	}
	boardID := relocInsertBoard(t, pool, "device-1", householdID)
	sensorID := relocInsertSensor(t, pool, boardID)
	if _, err := repo.AssignSensorRegion(ctx, sensorID, pot.RegionID, aliceScope, relocAuditEntry("alice")); err != nil {
		t.Fatalf("AssignSensorRegion: %v", err)
	}

	// Slow down every mirrored region INSERT enough to give the reader a
	// real window -- installed after every fixture region above already
	// exists, so it only ever delays RelocateSubtree's own mirror step.
	relocPoisonTrigger(t, pool, "reloc_delay_mirror_insert", `
		CREATE FUNCTION reloc_delay_mirror_insert() RETURNS TRIGGER AS $$
		BEGIN
			PERFORM pg_sleep(0.05);
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql;
		CREATE TRIGGER trg_reloc_delay_mirror_insert BEFORE INSERT ON region
			FOR EACH ROW EXECUTE FUNCTION reloc_delay_mirror_insert();
	`)

	regionsBefore := relocCountRows(t, pool, "region")

	done := make(chan struct{})
	var sampleCount int64
	var violations []string
	var violationsMu sync.Mutex

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(done)
		if _, err := repo.RelocateSubtree(ctx, shelf.RegionID, roomB.RegionID, "Reorganizing", aliceScope, relocAuditEntry("alice")); err != nil {
			violationsMu.Lock()
			violations = append(violations, fmt.Sprintf("RelocateSubtree returned an error: %v", err))
			violationsMu.Unlock()
		}
	}()

	pollCtx := context.Background()
	for {
		select {
		case <-done:
			wg.Wait()
			goto assertions
		default:
		}
		atomic.AddInt64(&sampleCount, 1)

		var regionCountNow int
		if err := pool.QueryRow(pollCtx, `SELECT COUNT(*) FROM region`).Scan(&regionCountNow); err != nil {
			t.Fatalf("poll region count: %v", err)
		}
		if regionCountNow > regionsBefore {
			// A mirrored child now exists -- the original Shelf must
			// already be retired; a half-mirrored tree with an active
			// original is exactly the state FR74 forbids observing.
			var shelfRetired bool
			if err := pool.QueryRow(pollCtx, `SELECT retired_at IS NOT NULL FROM region WHERE region_id = $1`, shelf.RegionID).Scan(&shelfRetired); err != nil {
				t.Fatalf("poll Shelf retired state: %v", err)
			}
			if !shelfRetired {
				violationsMu.Lock()
				violations = append(violations, "observed a mirrored region while original Shelf was still active (half-mirrored tree)")
				violationsMu.Unlock()
			}
		}

		var openPlantIntervals int
		if err := pool.QueryRow(pollCtx, `SELECT COUNT(*) FROM plant_region_history WHERE plant_id = $1 AND valid_to IS NULL`, plant.PlantID).Scan(&openPlantIntervals); err != nil {
			t.Fatalf("poll plant open intervals: %v", err)
		}
		if openPlantIntervals == 0 {
			violationsMu.Lock()
			violations = append(violations, "observed the plant with zero open placement intervals (FR56: would report unmonitored)")
			violationsMu.Unlock()
		} else if openPlantIntervals > 1 {
			violationsMu.Lock()
			violations = append(violations, fmt.Sprintf("observed the plant with %d open placement intervals, want at most 1", openPlantIntervals))
			violationsMu.Unlock()
		}

		var openSensorIntervals int
		if err := pool.QueryRow(pollCtx, `SELECT COUNT(*) FROM sensor_region_history WHERE sensor_id = $1 AND valid_to IS NULL`, sensorID).Scan(&openSensorIntervals); err != nil {
			t.Fatalf("poll sensor open intervals: %v", err)
		}
		if openSensorIntervals != 1 {
			violationsMu.Lock()
			violations = append(violations, fmt.Sprintf("observed the sensor with %d open placement intervals, want exactly 1", openSensorIntervals))
			violationsMu.Unlock()
		}

		time.Sleep(time.Millisecond)
	}

assertions:
	if atomic.LoadInt64(&sampleCount) == 0 {
		t.Fatal("test setup: polling loop took zero samples -- the delay trigger did not create a real window")
	}
	if len(violations) > 0 {
		t.Fatalf("observed %d intermediate-state violations during relocation, e.g.: %v", len(violations), violations[0])
	}

	final, err := repo.GetRegionByID(ctx, shelf.RegionID)
	if err != nil {
		t.Fatalf("GetRegionByID (Shelf) after relocation: %v", err)
	}
	if final.RetiredAt == nil || final.SuccessorRegionID == nil {
		t.Errorf("Shelf after relocation completed = %+v, want retired with a successor", final)
	}
}

// -- Structural refusal before writing (FR50, FR59.3) ---------------------

// TestRelocateSubtree_StructuralRefusal_ThirteenthChild_RefusesWhole
// proves the mirrored root landing as a 13th child of an already-full
// destination is refused before anything is written -- naming the
// violation, per FR50.1/FR59.3.
func TestRelocateSubtree_StructuralRefusal_ThirteenthChild_RefusesWhole(t *testing.T) {
	repo, resolver, pool := newRelocTestRepository(t)
	ctx := context.Background()

	householdID := relocInsertHousehold(t, pool)
	relocInsertMembership(t, pool, householdID, "alice")
	aliceScope := relocScopeFor(t, resolver, "alice")

	roomA, err := repo.CreateRegion(ctx, nil, "Room A", "", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Room A): %v", err)
	}
	roomB, err := repo.CreateRegion(ctx, nil, "Crowded Room B", "", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Room B): %v", err)
	}
	shelf, err := repo.CreateRegion(ctx, &roomA.RegionID, "Shelf", "", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Shelf): %v", err)
	}
	for i := 0; i < maxRegionChildren; i++ {
		relocInsertRegionRaw(t, pool, &roomB.RegionID, "filler", nil)
	}

	before := relocTakeSnapshot(t, pool)
	_, err = repo.RelocateSubtree(ctx, shelf.RegionID, roomB.RegionID, "Reorganizing", aliceScope, relocAuditEntry("alice"))
	if err == nil {
		t.Fatal("RelocateSubtree onto an already-full parent returned nil error, want a structural refusal")
	}
	f := relocFailureOf(t, err)
	if f.Class != string(contract.FailureInvalidArgument) {
		t.Errorf("failure class = %q, want %q", f.Class, contract.FailureInvalidArgument)
	}
	if !strings.Contains(f.Reason, "Crowded Room B") {
		t.Errorf("failure reason = %q, want it to name the destination %q", f.Reason, "Crowded Room B")
	}
	before.assertUnchanged(t, pool, "13th-child structural refusal")
}

// TestRelocateSubtree_StructuralRefusal_ShelfUnderPot_RefusesWhole proves a
// relocation that would push a mirrored region beyond the deepest allowed
// level (a Shelf ending up under a Pot) is refused before anything is
// written, naming the violation.
func TestRelocateSubtree_StructuralRefusal_ShelfUnderPot_RefusesWhole(t *testing.T) {
	repo, resolver, pool := newRelocTestRepository(t)
	ctx := context.Background()

	householdID := relocInsertHousehold(t, pool)
	relocInsertMembership(t, pool, householdID, "alice")
	aliceScope := relocScopeFor(t, resolver, "alice")

	roomA, err := repo.CreateRegion(ctx, nil, "Room A", "", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Room A): %v", err)
	}
	shelfToMove, err := repo.CreateRegion(ctx, &roomA.RegionID, "Wandering Shelf", "", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Wandering Shelf): %v", err)
	}

	roomB, err := repo.CreateRegion(ctx, nil, "Room B", "", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Room B): %v", err)
	}
	shelfB, err := repo.CreateRegion(ctx, &roomB.RegionID, "Shelf B", "", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Shelf B): %v", err)
	}
	destinationPot, err := repo.CreateRegion(ctx, &shelfB.RegionID, "Destination Pot", "", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Destination Pot, depth 3): %v", err)
	}

	before := relocTakeSnapshot(t, pool)
	// Relocating Wandering Shelf under Destination Pot (depth 3) would
	// land the mirrored Shelf at depth 4 -- beyond Room/Shelf/Pot.
	_, err = repo.RelocateSubtree(ctx, shelfToMove.RegionID, destinationPot.RegionID, "Reorganizing", aliceScope, relocAuditEntry("alice"))
	if err == nil {
		t.Fatal("RelocateSubtree of a Shelf under a Pot returned nil error, want a structural refusal")
	}
	f := relocFailureOf(t, err)
	if f.Class != string(contract.FailureInvalidArgument) {
		t.Errorf("failure class = %q, want %q", f.Class, contract.FailureInvalidArgument)
	}
	if !strings.Contains(f.Reason, "Wandering Shelf") {
		t.Errorf("failure reason = %q, want it to name the region being relocated", f.Reason)
	}
	if !strings.Contains(f.Reason, "Room / Shelf / Pot") {
		t.Errorf("failure reason = %q, want it to name the Room / Shelf / Pot depth violation", f.Reason)
	}
	before.assertUnchanged(t, pool, "Shelf-under-Pot structural refusal")
}

// -- FR24: distinguishing "I moved this plant" from "the region moved" ----

// TestRelocateSubtree_FR24_DistinguishesMovedPlantFromRegionMoved is FR74's
// own verifiable clause: GetPlantPlacementTimeline must tell "this plant
// moved" (MovePlant, relocation_induced=false) apart from "the region this
// plant was in moved" (RelocateSubtree, relocation_induced=true), on the
// very same plant's own timeline.
func TestRelocateSubtree_FR24_DistinguishesMovedPlantFromRegionMoved(t *testing.T) {
	repo, resolver, pool := newRelocTestRepository(t)
	ctx := context.Background()

	householdID := relocInsertHousehold(t, pool)
	relocInsertMembership(t, pool, householdID, "alice")
	aliceScope := relocScopeFor(t, resolver, "alice")

	roomA, err := repo.CreateRegion(ctx, nil, "Room A", "", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Room A): %v", err)
	}
	roomB, err := repo.CreateRegion(ctx, nil, "Room B", "", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Room B): %v", err)
	}
	shelf, err := repo.CreateRegion(ctx, &roomA.RegionID, "Shelf", "", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Shelf): %v", err)
	}
	otherShelf, err := repo.CreateRegion(ctx, &roomA.RegionID, "Other Shelf", "", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Other Shelf): %v", err)
	}

	plantTypeID := relocInsertPlantType(t, pool, "Fern")
	plant, err := repo.CreatePlant(ctx, otherShelf.RegionID, plantTypeID, "Traveler", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreatePlant: %v", err)
	}

	// "I moved this plant" -- a direct MovePlant call, relocation_induced
	// must be false.
	if _, err := repo.MovePlant(ctx, plant.PlantID, shelf.RegionID, aliceScope, relocAuditEntry("alice")); err != nil {
		t.Fatalf("MovePlant: %v", err)
	}

	// "The region this plant was in moved" -- Shelf itself relocates.
	result, err := repo.RelocateSubtree(ctx, shelf.RegionID, roomB.RegionID, "Reorganizing", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("RelocateSubtree: %v", err)
	}

	timeline, ok, err := repo.GetPlantPlacementTimeline(ctx, plant.PlantID)
	if err != nil || !ok {
		t.Fatalf("GetPlantPlacementTimeline: ok=%v err=%v", ok, err)
	}
	if len(timeline) != 3 {
		t.Fatalf("timeline = %+v, want 3 intervals (CreatePlant, MovePlant, RelocateSubtree)", timeline)
	}
	if timeline[0].RelocationInduced {
		t.Errorf("timeline[0] (CreatePlant's own interval) RelocationInduced = true, want false")
	}
	if timeline[1].RegionID != shelf.RegionID || timeline[1].RelocationInduced {
		t.Errorf("timeline[1] (MovePlant's interval) = %+v, want RegionID=%d RelocationInduced=false -- \"I moved this plant\"", timeline[1], shelf.RegionID)
	}
	if timeline[2].RegionID != result.NewRoot.RegionID || !timeline[2].RelocationInduced {
		t.Errorf("timeline[2] (RelocateSubtree's interval) = %+v, want RegionID=%d RelocationInduced=true -- \"the region this plant was in moved\"", timeline[2], result.NewRoot.RegionID)
	}
}

// -- Region-keyed series joins across the move via successor_region_id ----

// TestRelocateSubtree_RegionKeyedSeriesJoinsAcrossSuccessor proves FR74's
// "region-keyed series join across it through the successor reference":
// readings recorded against the original region before the relocation, and
// readings recorded against the mirrored region after it, resolve into one
// continuous series when a query follows successor_region_id -- exactly
// what migration 020's up.sql names this column for.
func TestRelocateSubtree_RegionKeyedSeriesJoinsAcrossSuccessor(t *testing.T) {
	repo, resolver, pool := newRelocTestRepository(t)
	ctx := context.Background()

	householdID := relocInsertHousehold(t, pool)
	relocInsertMembership(t, pool, householdID, "alice")
	aliceScope := relocScopeFor(t, resolver, "alice")

	roomA, err := repo.CreateRegion(ctx, nil, "Room A", "", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Room A): %v", err)
	}
	roomB, err := repo.CreateRegion(ctx, nil, "Room B", "", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Room B): %v", err)
	}
	shelf, err := repo.CreateRegion(ctx, &roomA.RegionID, "Shelf", "", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Shelf): %v", err)
	}

	t0 := time.Now().Add(-2 * time.Hour)
	t1 := time.Now().Add(-1 * time.Hour)
	relocInsertReading(t, pool, shelf.RegionID, 10.0, t0)
	relocInsertReading(t, pool, shelf.RegionID, 11.0, t1)

	result, err := repo.RelocateSubtree(ctx, shelf.RegionID, roomB.RegionID, "Reorganizing", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("RelocateSubtree: %v", err)
	}

	t2 := time.Now()
	t3 := time.Now().Add(time.Minute)
	relocInsertReading(t, pool, result.NewRoot.RegionID, 20.0, t2)
	relocInsertReading(t, pool, result.NewRoot.RegionID, 21.0, t3)

	rows, err := pool.Query(ctx, `
		WITH RECURSIVE chain(region_id) AS (
			SELECT $1::BIGINT
			UNION ALL
			SELECT r.successor_region_id
			FROM region r
			JOIN chain c ON r.region_id = c.region_id
			WHERE r.successor_region_id IS NOT NULL
		)
		SELECT sr.value
		FROM sensor_reading sr
		JOIN chain c ON sr.region_id = c.region_id
		ORDER BY sr.recorded_at
	`, shelf.RegionID)
	if err != nil {
		t.Fatalf("region-keyed series join query: %v", err)
	}
	defer rows.Close()
	var values []float64
	for rows.Next() {
		var v float64
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan value: %v", err)
		}
		values = append(values, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("region-keyed series join query: %v", err)
	}

	want := []float64{10.0, 11.0, 20.0, 21.0}
	if len(values) != len(want) {
		t.Fatalf("region-keyed series values = %v, want %v (continuous across the relocation, not discontinued)", values, want)
	}
	for i, v := range want {
		if values[i] != v {
			t.Errorf("region-keyed series[%d] = %v, want %v", i, values[i], v)
		}
	}
}

// -- FR50.5: a refused re-parent names this operation, and it works -------

// TestRelocateSubtree_FR50_5_RefusedReparent_NamedAlternativeSucceeds is
// the round trip FR50.5 requires: SetRegionParent on a region with a
// reading attributed to a descendant is refused, naming FR74/subtree
// relocation as the alternative -- and that named alternative, tried next
// against the exact same regions, actually succeeds.
func TestRelocateSubtree_FR50_5_RefusedReparent_NamedAlternativeSucceeds(t *testing.T) {
	repo, resolver, pool := newRelocTestRepository(t)
	ctx := context.Background()

	householdID := relocInsertHousehold(t, pool)
	relocInsertMembership(t, pool, householdID, "alice")
	aliceScope := relocScopeFor(t, resolver, "alice")

	roomA, err := repo.CreateRegion(ctx, nil, "Room A", "", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Room A): %v", err)
	}
	roomB, err := repo.CreateRegion(ctx, nil, "Room B", "", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Room B): %v", err)
	}
	shelf, err := repo.CreateRegion(ctx, &roomA.RegionID, "Shelf", "", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Shelf): %v", err)
	}
	pot, err := repo.CreateRegion(ctx, &shelf.RegionID, "Pot", "", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Pot): %v", err)
	}
	relocInsertReading(t, pool, pot.RegionID, 5.0, time.Now())

	_, err = repo.SetRegionParent(ctx, shelf.RegionID, &roomB.RegionID, aliceScope, relocAuditEntry("alice"))
	if err == nil {
		t.Fatal("SetRegionParent on a region with a descendant reading returned nil error, want a refusal")
	}
	f := relocFailureOf(t, err)
	if f.Class != string(contract.FailureRefusedWithAlternative) {
		t.Errorf("failure class = %q, want %q", f.Class, contract.FailureRefusedWithAlternative)
	}
	if !strings.Contains(f.Alternative, "FR74") || !strings.Contains(strings.ToLower(f.Alternative), "subtree") {
		t.Fatalf("failure alternative = %q, want it to name FR74/subtree relocation", f.Alternative)
	}

	// Follow the named alternative against the exact same regions -- it
	// must actually work.
	result, err := repo.RelocateSubtree(ctx, shelf.RegionID, roomB.RegionID, "Following the refusal's named alternative", aliceScope, relocAuditEntry("alice"))
	if err != nil {
		t.Fatalf("RelocateSubtree, the refusal's own named alternative, failed: %v", err)
	}
	if result.NewRoot.ParentRegionID == nil || *result.NewRoot.ParentRegionID != roomB.RegionID {
		t.Errorf("RelocateSubtree result = %+v, want the mirrored Shelf under Room B", result.NewRoot)
	}
}

// -- FR56 (not landed): documented stub, per this task's own allowance ----

// TestRelocateSubtree_FR56NeverReportsUnmonitored is stubbed: FR56 ("a
// plant is never reported unmonitored") has not landed on this branch
// lineage (grep the repo: no FR56 symbol exists), and this task's own
// Testing section explicitly allows stubbing this clause until it has
// ("stub if that task has not landed, then re-run once it has"). What is
// testable today -- that a concurrent reader never observes a plant with
// zero open placement intervals during the relocation transaction, which
// is the precondition FR56 would depend on -- is already proven by
// TestRelocateSubtree_ConcurrentReader_NeverObservesIntermediateState
// above.
func TestRelocateSubtree_FR56NeverReportsUnmonitored(t *testing.T) {
	t.Skip("FR56 has not landed on this branch lineage yet -- see this test's doc comment; " +
		"TestRelocateSubtree_ConcurrentReader_NeverObservesIntermediateState already proves the " +
		"precondition FR56 would depend on (a plant's open-interval count never hits zero)")
}
