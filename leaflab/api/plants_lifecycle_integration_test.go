//go:build integration

// Real-Postgres (TimescaleDB, for capture.Recorder's time_bucket call)
// integration coverage for #1377's plant lifecycle (FR54, FR24, FR22.3,
// FR22.1): CreatePlant/CorrectPlant/MovePlant/RetirePlant against real SQL,
// exercised the same way regions_lifecycle_integration_test.go exercises
// #1376's region lifecycle -- see that file's doc comment for why these
// integration tests stay hermetic and self-contained rather than sharing a
// schema/helper file across packages.
//
// Member-vs-grantee coverage here is limited to member-success and
// non-member-refusal, same scope note as regions_lifecycle_integration_test.go
// (authz.MemberOrGrantee does not exist on this branch lineage -- see
// plants.go's doc comment and scope notes #1417/#1427).
//
// Schema is hand-written DDL mirroring the relevant shape of migration 001
// (plant, plant_type, sensor, sensor_reading), migration 015 (household,
// household_membership, plant.household_id), migration 017
// (plant_region_history, its no-back-dating trigger) and migration 033
// (boundary_capture) -- deliberately not shared with any other integration
// test file in this package, per capture's own "Bazel test targets do not
// always share compilation" precedent
// (leaflab/api/capture/boundary_capture_integration_test.go). Requires the
// TimescaleDB image (not plain postgres) because capture.Recorder.Record
// calls time_bucket().
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //leaflab/api:plants_lifecycle_integration_test --test_output=all
package main

import (
	"context"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bazelbuild/rules_go/go/runfiles"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/leaflab/api/audit"
	"github.com/whale-net/everything/leaflab/api/authz"
	"github.com/whale-net/everything/leaflab/api/capture"
	"github.com/whale-net/everything/leaflab/api/contract"
	"github.com/whale-net/everything/leaflab/api/placement"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/libs/go/dbtest"
)

// plantsTimescaleImage matches captureTimescaleImage
// (leaflab/api/capture/boundary_capture_integration_test.go) and
// tiersTimescaleImage (leaflab/migrate/tiers_migration_integration_test.go)
// -- CreatePlant/MovePlant call capture.Recorder.Record, which calls
// time_bucket(), a TimescaleDB function not present in plain postgres.
const plantsTimescaleImage = "timescale/timescaledb:latest-pg16"

// plantsTestSchema is a hermetic, hand-written mirror of the tables
// plants.go's write paths actually touch: household/household_membership
// (migration 015, for real ScopeForPrincipal resolution), region (plain --
// authz.PGResolver.resolveRegion and Repository.CurrentHouseholdForRegion
// both walk the raw region table via a recursive CTE, never
// v_region_household, so no view is needed here), plant_type, plant
// (migrations 001 + 015's household_id), plant_region_history (migration
// 017, verbatim no-back-dating trigger), sensor (trimmed to what
// sensorsInRegionSubtrees reads), sensor_reading (trimmed to what the
// FR22.3 postmortem assertion below reads), boundary_capture (migration
// 033) and audit_log.
const plantsTestSchema = `
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
	CREATE INDEX idx_plants_household_membership_current
		ON household_membership(principal_subject) WHERE valid_to IS NULL;

	CREATE TABLE region (
		region_id        BIGSERIAL PRIMARY KEY,
		parent_region_id BIGINT REFERENCES region(region_id) ON DELETE RESTRICT,
		name              VARCHAR(255) NOT NULL,
		household_id      BIGINT REFERENCES household(household_id)
	);

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
	CREATE INDEX idx_plants_prh_plant_id_current
		ON plant_region_history(plant_id) WHERE valid_to IS NULL;
	CREATE INDEX idx_plants_prh_region_id_current
		ON plant_region_history(region_id) WHERE valid_to IS NULL;

	-- Verbatim from migration 017_plant_region_history.up.sql (NFR6.2's
	-- database-side no-back-dating backstop).
	CREATE FUNCTION enforce_plant_region_history_no_future_valid_from() RETURNS TRIGGER AS $$
	BEGIN
		IF NEW.valid_from > NOW() THEN
			RAISE EXCEPTION 'plant_region_history.valid_from (%) cannot be later than now (%) -- an interval opens at the instant it is recorded (FR19, NFR6.2)',
				NEW.valid_from, NOW();
		END IF;
		RETURN NEW;
	END;
	$$ LANGUAGE plpgsql;

	CREATE TRIGGER trg_plant_region_history_no_future_valid_from
		BEFORE INSERT ON plant_region_history
		FOR EACH ROW
		EXECUTE FUNCTION enforce_plant_region_history_no_future_valid_from();

	CREATE TABLE sensor (
		sensor_id BIGSERIAL PRIMARY KEY,
		region_id BIGINT REFERENCES region(region_id)
	);

	CREATE TABLE sensor_reading (
		reading_id  BIGSERIAL PRIMARY KEY,
		region_id   BIGINT REFERENCES region(region_id),
		value       DOUBLE PRECISION NOT NULL,
		recorded_at TIMESTAMPTZ NOT NULL
	);

	CREATE TABLE boundary_capture (
		capture_id    BIGSERIAL PRIMARY KEY,
		sensor_id     BIGINT NOT NULL REFERENCES sensor(sensor_id) ON DELETE RESTRICT,
		boundary_at   TIMESTAMPTZ NOT NULL,
		tier          TEXT NOT NULL,
		bucket_start  TIMESTAMPTZ NOT NULL,
		state         TEXT NOT NULL DEFAULT 'pending',
		completed_at  TIMESTAMPTZ,
		CONSTRAINT plants_boundary_capture_tier_check
			CHECK (tier IN ('five_minute', 'hourly')),
		CONSTRAINT plants_boundary_capture_state_check
			CHECK (state IN ('pending', 'completed')),
		CONSTRAINT plants_boundary_capture_completed_at_check
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

// newPlantsTestRepository starts a real TimescaleDB container with
// plantsTestSchema applied and returns a *Repository plus a real
// authz.PGResolver (these tests exercise FR7's member-only authorization
// stand-in against real household/household_membership SQL, same as
// regions_lifecycle_integration_test.go's newRegionsTestRepository) and the
// raw pool for fixture setup/assertions.
func newPlantsTestRepository(t *testing.T) (*Repository, *authz.PGResolver, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Image: plantsTimescaleImage, Schema: plantsTestSchema})
	repo := NewRepository(db.Pool)
	resolver := authz.NewPGResolver(db.Pool)
	return repo, resolver, db.Pool
}

func plantsInsertHousehold(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO household DEFAULT VALUES RETURNING household_id`).Scan(&id); err != nil {
		t.Fatalf("insert household: %v", err)
	}
	return id
}

func plantsInsertMembership(t *testing.T, pool *pgxpool.Pool, householdID int64, subject string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO household_membership (household_id, principal_subject) VALUES ($1, $2)`,
		householdID, subject); err != nil {
		t.Fatalf("insert membership for %q: %v", subject, err)
	}
}

// plantsScopeFor resolves subject's real authz.Scope via ScopeForPrincipal,
// mirroring regions_lifecycle_integration_test.go's scopeFor.
func plantsScopeFor(t *testing.T, resolver *authz.PGResolver, subject string) authz.Scope {
	t.Helper()
	scope, err := resolver.ScopeForPrincipal(context.Background(), subject)
	if err != nil {
		t.Fatalf("ScopeForPrincipal(%q): %v", subject, err)
	}
	return scope
}

// plantsInsertRegion inserts a region row directly via SQL -- these tests
// don't exercise FR50's structural rules (regions.go's CreateRegion), only
// plants.go's own logic, so fixture regions are seeded directly.
func plantsInsertRegion(t *testing.T, pool *pgxpool.Pool, name string, householdID int64) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO region (name, household_id) VALUES ($1, $2) RETURNING region_id`,
		name, householdID).Scan(&id); err != nil {
		t.Fatalf("insert region %s: %v", name, err)
	}
	return id
}

func plantsInsertPlantType(t *testing.T, pool *pgxpool.Pool, commonName string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO plant_type (common_name) VALUES ($1) RETURNING plant_type_id`,
		commonName).Scan(&id); err != nil {
		t.Fatalf("insert plant_type %s: %v", commonName, err)
	}
	return id
}

func plantsInsertSensor(t *testing.T, pool *pgxpool.Pool, regionID int64) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO sensor (region_id) VALUES ($1) RETURNING sensor_id`,
		regionID).Scan(&id); err != nil {
		t.Fatalf("insert sensor in region %d: %v", regionID, err)
	}
	return id
}

func plantsInsertReading(t *testing.T, pool *pgxpool.Pool, regionID int64, value float64, recordedAt time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO sensor_reading (region_id, value, recorded_at) VALUES ($1, $2, $3)`,
		regionID, value, recordedAt); err != nil {
		t.Fatalf("insert sensor_reading in region %d: %v", regionID, err)
	}
}

// plantAuditEntry returns a minimal valid audit.Entry for actor, with
// action distinguishing which operation performed it -- these tests assert
// on that distinction (FR24) as well as on the fact a row was written at
// all (FR8).
func plantAuditEntry(actor, action string) audit.Entry {
	return audit.Entry{
		ActorSubject: actor,
		ActorKind:    audit.ActorKindHuman,
		Action:       action,
		EntityKind:   "plant",
	}
}

func plantsCountRows(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("count rows in %s: %v", table, err)
	}
	return n
}

// plantsFailureOf extracts the pb.Failure detail from err, mirroring
// regions_lifecycle_integration_test.go's failureOf: these tests assert on
// FR59's structured failure (class/entity/field/reason), never on
// message-string parsing.
func plantsFailureOf(t *testing.T, err error) *pb.Failure {
	t.Helper()
	f, ok := contract.FromError(err)
	if !ok {
		t.Fatalf("error carries no contract.Failure detail: %v", err)
	}
	return f
}

// -- CreatePlant: opens exactly one placement interval (FR54, FR19) --------

// TestCreatePlant_OpensOneOpenInterval_SyncsRegionCache proves CreatePlant's
// FR19 coupling: creating and placing a plant in one operation opens
// exactly one open plant_region_history interval, and plant.region_id (the
// pre-FR72 read-path cache) is synced to match.
func TestCreatePlant_OpensOneOpenInterval_SyncsRegionCache(t *testing.T) {
	repo, resolver, pool := newPlantsTestRepository(t)
	ctx := context.Background()

	householdID := plantsInsertHousehold(t, pool)
	plantsInsertMembership(t, pool, householdID, "alice")
	aliceScope := plantsScopeFor(t, resolver, "alice")

	regionID := plantsInsertRegion(t, pool, "Grow Room", householdID)
	plantTypeID := plantsInsertPlantType(t, pool, "Fern")

	row, err := repo.CreatePlant(ctx, regionID, plantTypeID, "Fred the Fern", aliceScope, plantAuditEntry("alice", "CreatePlant"))
	if err != nil {
		t.Fatalf("CreatePlant: %v", err)
	}
	if row.RegionID != regionID {
		t.Errorf("returned RegionID = %d, want %d", row.RegionID, regionID)
	}

	var totalIntervals, openIntervals int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM plant_region_history WHERE plant_id = $1`, row.PlantID).Scan(&totalIntervals); err != nil {
		t.Fatalf("count intervals: %v", err)
	}
	if totalIntervals != 1 {
		t.Fatalf("plant_region_history rows for new plant = %d, want exactly 1", totalIntervals)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM plant_region_history WHERE plant_id = $1 AND valid_to IS NULL`, row.PlantID).Scan(&openIntervals); err != nil {
		t.Fatalf("count open intervals: %v", err)
	}
	if openIntervals != 1 {
		t.Fatalf("open plant_region_history rows for new plant = %d, want exactly 1", openIntervals)
	}

	var cachedRegionID int64
	if err := pool.QueryRow(ctx, `SELECT region_id FROM plant WHERE plant_id = $1`, row.PlantID).Scan(&cachedRegionID); err != nil {
		t.Fatalf("read plant.region_id cache: %v", err)
	}
	if cachedRegionID != regionID {
		t.Errorf("plant.region_id cache = %d, want %d", cachedRegionID, regionID)
	}
}

// TestCreatePlant_NonMemberRegionRefused_NotFound proves NFR2's
// no-existence-oracle: a caller outside the region's household is refused
// as if the region did not exist, same class authorizeRegionWrite already
// gives every other region write.
func TestCreatePlant_NonMemberRegionRefused_NotFound(t *testing.T) {
	repo, resolver, pool := newPlantsTestRepository(t)
	ctx := context.Background()

	householdID := plantsInsertHousehold(t, pool)
	plantsInsertMembership(t, pool, householdID, "alice")
	regionID := plantsInsertRegion(t, pool, "Alice's Room", householdID)
	plantTypeID := plantsInsertPlantType(t, pool, "Fern")

	bobScope := plantsScopeFor(t, resolver, "bob")
	_, err := repo.CreatePlant(ctx, regionID, plantTypeID, "Bob's Fern", bobScope, plantAuditEntry("bob", "CreatePlant"))
	if err == nil {
		t.Fatal("CreatePlant into another household's region succeeded, want a refusal")
	}
	f := plantsFailureOf(t, err)
	if f.Class != string(contract.FailureInvalidArgument) {
		t.Errorf("failure class = %q, want %q", f.Class, contract.FailureInvalidArgument)
	}
}

// -- MovePlant: closes old interval, opens new (FR54, FR19) ----------------

// TestMovePlant_ClosesOldOpensNew_OldRowValidFromUnchanged proves FR19's
// core SCD2 rule: MovePlant closes the plant's current open interval and
// opens a new one, and the closed row's own valid_from is byte-identical
// to what it was before the move -- nothing is updated in place, only
// valid_to changes on the old row.
func TestMovePlant_ClosesOldOpensNew_OldRowValidFromUnchanged(t *testing.T) {
	repo, resolver, pool := newPlantsTestRepository(t)
	ctx := context.Background()

	householdID := plantsInsertHousehold(t, pool)
	plantsInsertMembership(t, pool, householdID, "alice")
	aliceScope := plantsScopeFor(t, resolver, "alice")

	regionA := plantsInsertRegion(t, pool, "Room A", householdID)
	regionB := plantsInsertRegion(t, pool, "Room B", householdID)
	plantTypeID := plantsInsertPlantType(t, pool, "Fern")

	plant, err := repo.CreatePlant(ctx, regionA, plantTypeID, "Mover", aliceScope, plantAuditEntry("alice", "CreatePlant"))
	if err != nil {
		t.Fatalf("CreatePlant: %v", err)
	}

	var originalValidFrom time.Time
	if err := pool.QueryRow(ctx, `SELECT valid_from FROM plant_region_history WHERE plant_id = $1 AND valid_to IS NULL`, plant.PlantID).Scan(&originalValidFrom); err != nil {
		t.Fatalf("read original valid_from: %v", err)
	}

	moved, err := repo.MovePlant(ctx, plant.PlantID, regionB, aliceScope, plantAuditEntry("alice", "MovePlant"))
	if err != nil {
		t.Fatalf("MovePlant: %v", err)
	}
	if moved.RegionID != regionB {
		t.Errorf("moved.RegionID = %d, want %d", moved.RegionID, regionB)
	}

	var totalIntervals int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM plant_region_history WHERE plant_id = $1`, plant.PlantID).Scan(&totalIntervals); err != nil {
		t.Fatalf("count total intervals: %v", err)
	}
	if totalIntervals != 2 {
		t.Fatalf("total plant_region_history rows = %d, want 2 (original closed + new open, nothing updated in place)", totalIntervals)
	}

	var oldValidFrom time.Time
	var oldValidTo *time.Time
	if err := pool.QueryRow(ctx, `SELECT valid_from, valid_to FROM plant_region_history WHERE plant_id = $1 AND region_id = $2`, plant.PlantID, regionA).Scan(&oldValidFrom, &oldValidTo); err != nil {
		t.Fatalf("read old interval: %v", err)
	}
	if !oldValidFrom.Equal(originalValidFrom) {
		t.Errorf("old interval's valid_from changed: was %v, now %v -- nothing may be updated in place (FR19)", originalValidFrom, oldValidFrom)
	}
	if oldValidTo == nil {
		t.Error("old interval's valid_to is still NULL after MovePlant, want it closed")
	}

	var newRegion int64
	if err := pool.QueryRow(ctx, `SELECT region_id FROM plant_region_history WHERE plant_id = $1 AND valid_to IS NULL`, plant.PlantID).Scan(&newRegion); err != nil {
		t.Fatalf("read new open interval: %v", err)
	}
	if newRegion != regionB {
		t.Errorf("new open interval region_id = %d, want %d", newRegion, regionB)
	}
}

// TestMovePlant_DatabaseGuardRefusesDirectBackdatedInsert proves NFR6.2's
// independent database-side no-back-dating guard holds in this schema too
// (migration 017's trigger): a direct INSERT with a future valid_from,
// bypassing MovePlant/placement.MoveTx entirely, is refused. MovePlant
// itself always writes through placement.MoveTx with time.Now() (proto's
// MovePlantRequest carries no caller-supplied boundary field at all, so a
// caller-facing back-dated request is not reachable through this RPC
// surface to begin with) -- placement's own package tests
// (leaflab/api/placement/move_integration_test.go's
// TestWriterMove_RefusesBackdatedRequest) already cover
// RefuseIfBackdated's caller-facing refusal and its stated reason; this
// test is the trigger backstop, reproduced against this file's own schema.
func TestMovePlant_DatabaseGuardRefusesDirectBackdatedInsert(t *testing.T) {
	repo, resolver, pool := newPlantsTestRepository(t)
	ctx := context.Background()

	householdID := plantsInsertHousehold(t, pool)
	plantsInsertMembership(t, pool, householdID, "alice")
	aliceScope := plantsScopeFor(t, resolver, "alice")

	regionA := plantsInsertRegion(t, pool, "Room A", householdID)
	plantTypeID := plantsInsertPlantType(t, pool, "Fern")

	plant, err := repo.CreatePlant(ctx, regionA, plantTypeID, "Guarded", aliceScope, plantAuditEntry("alice", "CreatePlant"))
	if err != nil {
		t.Fatalf("CreatePlant: %v", err)
	}

	future := time.Now().Add(1 * time.Hour)
	_, err = pool.Exec(ctx, `
		INSERT INTO plant_region_history (plant_id, region_id, valid_from) VALUES ($1, $2, $3)
	`, plant.PlantID, regionA, future)
	if err == nil {
		t.Error("direct INSERT with a future valid_from succeeded, want it refused by the database trigger (NFR6.2)")
	}
}

// -- FR1.2: cross-household placement is refused ----------------------------

// TestMovePlant_CrossHouseholdRegionRefused proves FR1.2's guard
// (assertRegionHousehold): moving a plant into a region belonging to a
// different household is refused, naming region_id as the offending field,
// and writes nothing.
func TestMovePlant_CrossHouseholdRegionRefused(t *testing.T) {
	repo, resolver, pool := newPlantsTestRepository(t)
	ctx := context.Background()

	householdA := plantsInsertHousehold(t, pool)
	plantsInsertMembership(t, pool, householdA, "alice")
	aliceScope := plantsScopeFor(t, resolver, "alice")
	regionA := plantsInsertRegion(t, pool, "Alice's Room", householdA)
	plantTypeID := plantsInsertPlantType(t, pool, "Fern")

	householdB := plantsInsertHousehold(t, pool)
	regionB := plantsInsertRegion(t, pool, "Bob's Room", householdB)

	plant, err := repo.CreatePlant(ctx, regionA, plantTypeID, "Territorial Fern", aliceScope, plantAuditEntry("alice", "CreatePlant"))
	if err != nil {
		t.Fatalf("CreatePlant: %v", err)
	}

	_, err = repo.MovePlant(ctx, plant.PlantID, regionB, aliceScope, plantAuditEntry("alice", "MovePlant"))
	if err == nil {
		t.Fatal("MovePlant into another household's region succeeded, want a refusal (FR1.2)")
	}
	f := plantsFailureOf(t, err)
	if f.Class != string(contract.FailureInvalidArgument) {
		t.Errorf("failure class = %q, want %q", f.Class, contract.FailureInvalidArgument)
	}
	if f.Field != "region_id" {
		t.Errorf("failure field = %q, want %q", f.Field, "region_id")
	}
	if !strings.Contains(strings.ToLower(f.Reason), "household") {
		t.Errorf("failure reason = %q, want it to name the cross-household violation", f.Reason)
	}

	var totalIntervals int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM plant_region_history WHERE plant_id = $1`, plant.PlantID).Scan(&totalIntervals); err != nil {
		t.Fatalf("count intervals after refused cross-household move: %v", err)
	}
	if totalIntervals != 1 {
		t.Errorf("plant_region_history rows after a refused cross-household move = %d, want unchanged 1", totalIntervals)
	}
}

// -- CorrectPlant vs MovePlant: distinct operations (FR24) ------------------

// TestCorrectPlant_WritesNoPlacementInterval_MovePlant_WritesNoNameChange
// proves FR24: CorrectPlant never writes a plant_region_history interval,
// and MovePlant never touches name/plant_type -- the two are distinguishable
// in the response and in the audit log.
func TestCorrectPlant_WritesNoPlacementInterval_MovePlant_WritesNoNameChange(t *testing.T) {
	repo, resolver, pool := newPlantsTestRepository(t)
	ctx := context.Background()

	householdID := plantsInsertHousehold(t, pool)
	plantsInsertMembership(t, pool, householdID, "alice")
	aliceScope := plantsScopeFor(t, resolver, "alice")

	regionA := plantsInsertRegion(t, pool, "Room A", householdID)
	regionB := plantsInsertRegion(t, pool, "Room B", householdID)
	fernType := plantsInsertPlantType(t, pool, "Fern")

	plant, err := repo.CreatePlant(ctx, regionA, fernType, "Typo'd Nmae", aliceScope, plantAuditEntry("alice", "CreatePlant"))
	if err != nil {
		t.Fatalf("CreatePlant: %v", err)
	}

	// CorrectPlant: fixes the typo, must not touch placement.
	fixedName := "Fixed Name"
	corrected, err := repo.CorrectPlant(ctx, plant.PlantID, &fixedName, nil, aliceScope, plantAuditEntry("alice", "CorrectPlant"))
	if err != nil {
		t.Fatalf("CorrectPlant: %v", err)
	}
	if corrected.Name != fixedName {
		t.Errorf("corrected.Name = %q, want %q", corrected.Name, fixedName)
	}
	if corrected.RegionID != regionA {
		t.Errorf("corrected.RegionID = %d, want unchanged %d -- CorrectPlant must never move a plant", corrected.RegionID, regionA)
	}
	var intervalsAfterCorrect int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM plant_region_history WHERE plant_id = $1`, plant.PlantID).Scan(&intervalsAfterCorrect); err != nil {
		t.Fatalf("count intervals after CorrectPlant: %v", err)
	}
	if intervalsAfterCorrect != 1 {
		t.Errorf("plant_region_history rows after CorrectPlant = %d, want unchanged 1 -- CorrectPlant must write no placement interval", intervalsAfterCorrect)
	}

	// MovePlant: relocates, must not touch name.
	moved, err := repo.MovePlant(ctx, plant.PlantID, regionB, aliceScope, plantAuditEntry("alice", "MovePlant"))
	if err != nil {
		t.Fatalf("MovePlant: %v", err)
	}
	if moved.RegionID != regionB {
		t.Errorf("moved.RegionID = %d, want %d", moved.RegionID, regionB)
	}
	if moved.Name != fixedName {
		t.Errorf("moved.Name = %q, want unchanged %q -- MovePlant must never rename a plant", moved.Name, fixedName)
	}

	// Distinguishable in the audit log: two rows, one per action, in order.
	rows, err := pool.Query(ctx, `SELECT action FROM audit_log ORDER BY audit_id`)
	if err != nil {
		t.Fatalf("read audit_log actions: %v", err)
	}
	defer rows.Close()
	var actions []string
	for rows.Next() {
		var action string
		if err := rows.Scan(&action); err != nil {
			t.Fatalf("scan action: %v", err)
		}
		actions = append(actions, action)
	}
	wantActions := []string{"CreatePlant", "CorrectPlant", "MovePlant"}
	if len(actions) != len(wantActions) {
		t.Fatalf("audit_log actions = %v, want %v", actions, wantActions)
	}
	for i, want := range wantActions {
		if actions[i] != want {
			t.Errorf("audit_log action[%d] = %q, want %q -- CorrectPlant and MovePlant must be distinguishable in the audit log", i, actions[i], want)
		}
	}
}

// -- FR20 coupling: boundary capture in the same transaction ----------------

// TestMovePlant_RecordsBoundaryCaptureForAffectedSensorsInSameTransaction
// proves FR20's coupling requirement: a move creates boundary_capture rows
// -- one per affected sensor (the union of the old and new region's sensor
// subtrees) and per capture tier (five_minute, hourly) -- durably, as part
// of the same successful write.
func TestMovePlant_RecordsBoundaryCaptureForAffectedSensorsInSameTransaction(t *testing.T) {
	repo, resolver, pool := newPlantsTestRepository(t)
	ctx := context.Background()

	householdID := plantsInsertHousehold(t, pool)
	plantsInsertMembership(t, pool, householdID, "alice")
	aliceScope := plantsScopeFor(t, resolver, "alice")

	regionA := plantsInsertRegion(t, pool, "Room A", householdID)
	regionB := plantsInsertRegion(t, pool, "Room B", householdID)
	fernType := plantsInsertPlantType(t, pool, "Fern")

	sensorA := plantsInsertSensor(t, pool, regionA)
	sensorB := plantsInsertSensor(t, pool, regionB)

	plant, err := repo.CreatePlant(ctx, regionA, fernType, "Captured", aliceScope, plantAuditEntry("alice", "CreatePlant"))
	if err != nil {
		t.Fatalf("CreatePlant: %v", err)
	}
	// CreatePlant itself records phase-one capture for regionA's sensor --
	// clear it so this test's assertions are scoped to what MovePlant
	// alone produces.
	if _, err := pool.Exec(ctx, `DELETE FROM boundary_capture`); err != nil {
		t.Fatalf("clear boundary_capture from CreatePlant: %v", err)
	}

	if _, err := repo.MovePlant(ctx, plant.PlantID, regionB, aliceScope, plantAuditEntry("alice", "MovePlant")); err != nil {
		t.Fatalf("MovePlant: %v", err)
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM boundary_capture`).Scan(&n); err != nil {
		t.Fatalf("count boundary_capture rows: %v", err)
	}
	// 2 sensors (old region + new region) x 2 tiers (five_minute, hourly).
	if n != 4 {
		t.Fatalf("boundary_capture rows after MovePlant = %d, want 4 (2 affected sensors x 2 tiers)", n)
	}

	for _, sensorID := range []int64{sensorA, sensorB} {
		var perSensor int
		if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM boundary_capture WHERE sensor_id = $1`, sensorID).Scan(&perSensor); err != nil {
			t.Fatalf("count boundary_capture rows for sensor %d: %v", sensorID, err)
		}
		if perSensor != 2 {
			t.Errorf("boundary_capture rows for sensor %d = %d, want 2 (one per tier)", sensorID, perSensor)
		}
	}
}

// TestMovePlant_RollbackDiscardsBoundaryCaptureAndPlacementWrite proves the
// "same transaction" half of FR20's coupling requirement from the other
// direction: if the transaction MovePlant's write runs inside is rolled
// back instead of committed, neither the placement write nor the boundary
// capture rows persist. This exercises the exact sequence
// Repository.MovePlant's closure runs (placement.MoveTx,
// sensorsInRegionSubtrees, capture.Recorder.Record) against a
// caller-controlled transaction, rolled back deliberately, since
// MovePlant's own signature always commits on success and there is no
// application-level failure point reachable after the capture write to
// force a real rollback through the public API.
func TestMovePlant_RollbackDiscardsBoundaryCaptureAndPlacementWrite(t *testing.T) {
	repo, resolver, pool := newPlantsTestRepository(t)
	ctx := context.Background()

	householdID := plantsInsertHousehold(t, pool)
	plantsInsertMembership(t, pool, householdID, "alice")
	aliceScope := plantsScopeFor(t, resolver, "alice")

	regionA := plantsInsertRegion(t, pool, "Room A", householdID)
	regionB := plantsInsertRegion(t, pool, "Room B", householdID)
	fernType := plantsInsertPlantType(t, pool, "Fern")
	plantsInsertSensor(t, pool, regionA)
	plantsInsertSensor(t, pool, regionB)

	plant, err := repo.CreatePlant(ctx, regionA, fernType, "Rolled Back", aliceScope, plantAuditEntry("alice", "CreatePlant"))
	if err != nil {
		t.Fatalf("CreatePlant: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM boundary_capture`); err != nil {
		t.Fatalf("clear boundary_capture from CreatePlant: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	validFrom, err := placement.MoveTx(ctx, tx, plant.PlantID, regionB, time.Now())
	if err != nil {
		t.Fatalf("placement.MoveTx: %v", err)
	}
	affected, err := sensorsInRegionSubtrees(ctx, tx, []int64{regionA, regionB})
	if err != nil {
		t.Fatalf("sensorsInRegionSubtrees: %v", err)
	}
	if err := capture.NewRecorder().Record(ctx, tx, affected, validFrom); err != nil {
		t.Fatalf("capture.Recorder.Record: %v", err)
	}
	// Deliberately roll back instead of committing.
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	if n := plantsCountRows(t, pool, "boundary_capture"); n != 0 {
		t.Errorf("boundary_capture rows after rollback = %d, want 0", n)
	}

	var totalIntervals int
	var openRegion int64
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM plant_region_history WHERE plant_id = $1`, plant.PlantID).Scan(&totalIntervals); err != nil {
		t.Fatalf("count intervals after rollback: %v", err)
	}
	if totalIntervals != 1 {
		t.Errorf("plant_region_history rows after rollback = %d, want unchanged 1", totalIntervals)
	}
	if err := pool.QueryRow(ctx, `SELECT region_id FROM plant_region_history WHERE plant_id = $1 AND valid_to IS NULL`, plant.PlantID).Scan(&openRegion); err != nil {
		t.Fatalf("read open interval after rollback: %v", err)
	}
	if openRegion != regionA {
		t.Errorf("open interval region after rollback = %d, want unchanged original %d", openRegion, regionA)
	}
}

// -- Separate histories: two plants, one region, different times -----------

// TestTwoPlantsSameRegionDifferentTimes_KeepSeparateHistories proves FR54's
// "two plants that occupy the same region at different times keep separate
// histories": plant A moves out of a region, plant B is later created into
// it -- the region ends up with two distinct plant_region_history rows,
// each keyed to its own plant, never merged into one.
func TestTwoPlantsSameRegionDifferentTimes_KeepSeparateHistories(t *testing.T) {
	repo, resolver, pool := newPlantsTestRepository(t)
	ctx := context.Background()

	householdID := plantsInsertHousehold(t, pool)
	plantsInsertMembership(t, pool, householdID, "alice")
	aliceScope := plantsScopeFor(t, resolver, "alice")

	sharedRegion := plantsInsertRegion(t, pool, "Shared Pot", householdID)
	elsewhere := plantsInsertRegion(t, pool, "Elsewhere", householdID)
	fernType := plantsInsertPlantType(t, pool, "Fern")

	plantA, err := repo.CreatePlant(ctx, sharedRegion, fernType, "First Occupant", aliceScope, plantAuditEntry("alice", "CreatePlant"))
	if err != nil {
		t.Fatalf("CreatePlant (A): %v", err)
	}
	if _, err := repo.MovePlant(ctx, plantA.PlantID, elsewhere, aliceScope, plantAuditEntry("alice", "MovePlant")); err != nil {
		t.Fatalf("MovePlant (A out): %v", err)
	}

	plantB, err := repo.CreatePlant(ctx, sharedRegion, fernType, "Second Occupant", aliceScope, plantAuditEntry("alice", "CreatePlant"))
	if err != nil {
		t.Fatalf("CreatePlant (B): %v", err)
	}

	rows, err := pool.Query(ctx, `SELECT DISTINCT plant_id FROM plant_region_history WHERE region_id = $1 ORDER BY plant_id`, sharedRegion)
	if err != nil {
		t.Fatalf("query plant_region_history for shared region: %v", err)
	}
	defer rows.Close()
	var plantIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan plant_id: %v", err)
		}
		plantIDs = append(plantIDs, id)
	}
	if len(plantIDs) != 2 {
		t.Fatalf("distinct plant_ids with a plant_region_history interval in the shared region = %v, want exactly [%d, %d]", plantIDs, plantA.PlantID, plantB.PlantID)
	}
	if plantIDs[0] != plantA.PlantID && plantIDs[1] != plantA.PlantID {
		t.Errorf("plant A (%d) missing from shared region's history: %v", plantA.PlantID, plantIDs)
	}
	if plantIDs[0] != plantB.PlantID && plantIDs[1] != plantB.PlantID {
		t.Errorf("plant B (%d) missing from shared region's history: %v", plantB.PlantID, plantIDs)
	}

	// Each plant's own timeline is independently readable and distinct.
	timelineA, ok, err := repo.GetPlantPlacementTimeline(ctx, plantA.PlantID)
	if err != nil || !ok {
		t.Fatalf("GetPlantPlacementTimeline(A): ok=%v err=%v", ok, err)
	}
	timelineB, ok, err := repo.GetPlantPlacementTimeline(ctx, plantB.PlantID)
	if err != nil || !ok {
		t.Fatalf("GetPlantPlacementTimeline(B): ok=%v err=%v", ok, err)
	}
	if len(timelineA) != 2 { // sharedRegion, then elsewhere
		t.Errorf("plant A timeline = %+v, want 2 intervals", timelineA)
	}
	if len(timelineB) != 1 { // sharedRegion only
		t.Errorf("plant B timeline = %+v, want 1 interval", timelineB)
	}
}

// -- FR22.3: retired plant's readings remain reachable -----------------------

// TestRetirePlant_ReadingsRemainReachable_PostmortemOverFinalWeeks proves
// FR22.3: after a plant is retired, a postmortem query over its final weeks
// -- the same region/created_at/removed_at join predicate
// v_sensor_reading_with_plant (migration 012) uses -- still returns the
// readings recorded while the plant was active, right up to (not
// including) the retirement instant.
func TestRetirePlant_ReadingsRemainReachable_PostmortemOverFinalWeeks(t *testing.T) {
	repo, resolver, pool := newPlantsTestRepository(t)
	ctx := context.Background()

	householdID := plantsInsertHousehold(t, pool)
	plantsInsertMembership(t, pool, householdID, "alice")
	aliceScope := plantsScopeFor(t, resolver, "alice")

	regionID := plantsInsertRegion(t, pool, "Deathbed", householdID)
	fernType := plantsInsertPlantType(t, pool, "Fern")

	plant, err := repo.CreatePlant(ctx, regionID, fernType, "Departed", aliceScope, plantAuditEntry("alice", "CreatePlant"))
	if err != nil {
		t.Fatalf("CreatePlant: %v", err)
	}

	now := time.Now()
	// CreatePlant always stamps created_at as the real NOW() -- backdate it
	// directly so this fixture can plausibly have readings recorded across
	// the plant's "final weeks" before today's retirement, without
	// depending on plants.go accepting a caller-supplied created_at (it
	// does not, and should not).
	longAgo := now.Add(-60 * 24 * time.Hour)
	if _, err := pool.Exec(ctx, `UPDATE plant SET created_at = $1 WHERE plant_id = $2`, longAgo, plant.PlantID); err != nil {
		t.Fatalf("backdate plant.created_at for fixture: %v", err)
	}
	finalWeekStart := now.Add(-7 * 24 * time.Hour)
	// Readings across the plant's final week, plus one well before it.
	plantsInsertReading(t, pool, regionID, 1.0, now.Add(-30*24*time.Hour))
	plantsInsertReading(t, pool, regionID, 2.0, finalWeekStart.Add(1*time.Hour))
	plantsInsertReading(t, pool, regionID, 3.0, finalWeekStart.Add(48*time.Hour))
	plantsInsertReading(t, pool, regionID, 4.0, now.Add(-1*time.Minute))

	if _, err := repo.RetirePlant(ctx, plant.PlantID, aliceScope, plantAuditEntry("alice", "RetirePlant")); err != nil {
		t.Fatalf("RetirePlant: %v", err)
	}

	// Postmortem: readings in the plant's final week, using the same join
	// predicate v_sensor_reading_with_plant uses (region_id equality plus
	// the created_at/removed_at attribution window).
	rows, err := pool.Query(ctx, `
		SELECT sr.value
		FROM sensor_reading sr
		JOIN plant p
		  ON p.region_id  = sr.region_id
		 AND p.created_at <= sr.recorded_at
		 AND (p.removed_at IS NULL OR p.removed_at > sr.recorded_at)
		WHERE p.plant_id = $1
		  AND sr.recorded_at >= $2
		ORDER BY sr.recorded_at
	`, plant.PlantID, finalWeekStart)
	if err != nil {
		t.Fatalf("postmortem query: %v", err)
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
	wantValues := []float64{2.0, 3.0, 4.0}
	if len(values) != len(wantValues) {
		t.Fatalf("postmortem values = %v, want %v -- a retired plant's readings must remain reachable (FR22.3)", values, wantValues)
	}
	for i, want := range wantValues {
		if values[i] != want {
			t.Errorf("postmortem values[%d] = %v, want %v", i, values[i], want)
		}
	}

	// GetPlantByID and GetPlantPlacementTimeline (FR22.3's other half) also
	// still work by explicit id after retirement.
	got, err := repo.GetPlantByID(ctx, plant.PlantID)
	if err != nil {
		t.Fatalf("GetPlantByID after retirement: %v", err)
	}
	if got.RemovedAt == nil {
		t.Error("GetPlantByID after retirement: RemovedAt is nil, want it populated")
	}
	timeline, ok, err := repo.GetPlantPlacementTimeline(ctx, plant.PlantID)
	if err != nil || !ok {
		t.Fatalf("GetPlantPlacementTimeline after retirement: ok=%v err=%v", ok, err)
	}
	if len(timeline) != 1 {
		t.Errorf("timeline after retirement = %+v, want 1 interval (still readable)", timeline)
	}
}

// TestRetirePlant_ExcludedFromDefaultListing_RejectsNewWrites proves
// FR22.1's other two clauses: ListPlants excludes a retired plant by
// default, and CorrectPlant/MovePlant both refuse to write to it.
func TestRetirePlant_ExcludedFromDefaultListing_RejectsNewWrites(t *testing.T) {
	repo, resolver, pool := newPlantsTestRepository(t)
	ctx := context.Background()

	householdID := plantsInsertHousehold(t, pool)
	plantsInsertMembership(t, pool, householdID, "alice")
	aliceScope := plantsScopeFor(t, resolver, "alice")

	regionID := plantsInsertRegion(t, pool, "Room", householdID)
	otherRegion := plantsInsertRegion(t, pool, "Other Room", householdID)
	fernType := plantsInsertPlantType(t, pool, "Fern")

	active, err := repo.CreatePlant(ctx, regionID, fernType, "Still Here", aliceScope, plantAuditEntry("alice", "CreatePlant"))
	if err != nil {
		t.Fatalf("CreatePlant (active): %v", err)
	}
	retired, err := repo.CreatePlant(ctx, regionID, fernType, "Gone", aliceScope, plantAuditEntry("alice", "CreatePlant"))
	if err != nil {
		t.Fatalf("CreatePlant (to retire): %v", err)
	}
	if _, err := repo.RetirePlant(ctx, retired.PlantID, aliceScope, plantAuditEntry("alice", "RetirePlant")); err != nil {
		t.Fatalf("RetirePlant: %v", err)
	}

	plants, err := repo.ListPlants(ctx, 0, false, 10, aliceScope)
	if err != nil {
		t.Fatalf("ListPlants: %v", err)
	}
	var sawActive, sawRetired bool
	for _, p := range plants {
		switch p.PlantID {
		case active.PlantID:
			sawActive = true
		case retired.PlantID:
			sawRetired = true
		}
	}
	if !sawActive {
		t.Error("ListPlants did not return the active plant")
	}
	if sawRetired {
		t.Error("ListPlants returned the retired plant, want it excluded from the default listing")
	}

	newName := "Resurrected"
	if _, err := repo.CorrectPlant(ctx, retired.PlantID, &newName, nil, aliceScope, plantAuditEntry("alice", "CorrectPlant")); err == nil {
		t.Error("CorrectPlant on a retired plant succeeded, want a refusal")
	}
	if _, err := repo.MovePlant(ctx, retired.PlantID, otherRegion, aliceScope, plantAuditEntry("alice", "MovePlant")); err == nil {
		t.Error("MovePlant on a retired plant succeeded, want a refusal")
	}

	// Retirement is not idempotent-by-design.
	if _, err := repo.RetirePlant(ctx, retired.PlantID, aliceScope, plantAuditEntry("alice", "RetirePlant")); err == nil {
		t.Error("second RetirePlant call succeeded, want ErrPlantAlreadyRetired")
	} else if err != ErrPlantAlreadyRetired {
		t.Errorf("second RetirePlant call error = %v, want ErrPlantAlreadyRetired", err)
	}
}

// -- FR22.1: nothing is hard-deleted -----------------------------------------

// TestPlantWritePaths_NoHardDeleteOfPlantRow is a structural assertion for
// FR22.1's "nothing is hard-deleted": none of this file's write paths issue
// a DELETE against the plant table. Retirement is soft (removed_at), same
// as RetireBoard/RetireRegion. plants.go is located via the Bazel runfiles
// manifest (see BUILD.bazel's data attribute on this go_test target) rather
// than a bare relative path, which is not reliably resolvable from a
// sandboxed test's working directory.
func TestPlantWritePaths_NoHardDeleteOfPlantRow(t *testing.T) {
	path, err := runfiles.Rlocation("_main/leaflab/api/plants.go")
	if err != nil {
		t.Fatalf("runfiles.Rlocation(plants.go): %v (is it listed in this target's data attribute?)", err)
	}
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read plants.go: %v", err)
	}
	if hardDeletePlantPattern.Match(src) {
		t.Error("plants.go contains a DELETE FROM plant statement -- FR22.1 requires retirement to be soft (removed_at), never a hard delete")
	}
}

var hardDeletePlantPattern = regexp.MustCompile(`(?i)DELETE\s+FROM\s+plant\b`)

// -- Audit (FR8) / NFR2 non-member refusal -----------------------------------

// TestPlantLifecycle_EveryWriteIsAudited_NonMemberRefused proves every
// plant write -- create, correct, move, retire -- records exactly one
// audit_log row, and that a caller who is not a member of any household is
// refused (NFR2's collapsed not-found/out-of-scope failure) rather than
// silently permitted.
func TestPlantLifecycle_EveryWriteIsAudited_NonMemberRefused(t *testing.T) {
	repo, resolver, pool := newPlantsTestRepository(t)
	ctx := context.Background()

	householdID := plantsInsertHousehold(t, pool)
	plantsInsertMembership(t, pool, householdID, "alice")
	aliceScope := plantsScopeFor(t, resolver, "alice")

	regionA := plantsInsertRegion(t, pool, "Room A", householdID)
	regionB := plantsInsertRegion(t, pool, "Room B", householdID)
	fernType := plantsInsertPlantType(t, pool, "Fern")

	if n := plantsCountRows(t, pool, "audit_log"); n != 0 {
		t.Fatalf("test setup: audit_log has %d rows before any write, want 0", n)
	}

	plant, err := repo.CreatePlant(ctx, regionA, fernType, "Audited", aliceScope, plantAuditEntry("alice", "CreatePlant"))
	if err != nil {
		t.Fatalf("CreatePlant: %v", err)
	}
	if n := plantsCountRows(t, pool, "audit_log"); n != 1 {
		t.Fatalf("audit_log rows after CreatePlant = %d, want 1", n)
	}

	newName := "Audited Renamed"
	if _, err := repo.CorrectPlant(ctx, plant.PlantID, &newName, nil, aliceScope, plantAuditEntry("alice", "CorrectPlant")); err != nil {
		t.Fatalf("CorrectPlant: %v", err)
	}
	if n := plantsCountRows(t, pool, "audit_log"); n != 2 {
		t.Fatalf("audit_log rows after CorrectPlant = %d, want 2", n)
	}

	if _, err := repo.MovePlant(ctx, plant.PlantID, regionB, aliceScope, plantAuditEntry("alice", "MovePlant")); err != nil {
		t.Fatalf("MovePlant: %v", err)
	}
	if n := plantsCountRows(t, pool, "audit_log"); n != 3 {
		t.Fatalf("audit_log rows after MovePlant = %d, want 3", n)
	}

	if _, err := repo.RetirePlant(ctx, plant.PlantID, aliceScope, plantAuditEntry("alice", "RetirePlant")); err != nil {
		t.Fatalf("RetirePlant: %v", err)
	}
	if n := plantsCountRows(t, pool, "audit_log"); n != 4 {
		t.Fatalf("audit_log rows after RetirePlant = %d, want 4", n)
	}

	var lastEntityID string
	if err := pool.QueryRow(ctx, `SELECT entity_id FROM audit_log ORDER BY audit_id DESC LIMIT 1`).Scan(&lastEntityID); err != nil {
		t.Fatalf("read last audit_log row: %v", err)
	}
	wantEntityID := strconv.FormatInt(plant.PlantID, 10)
	if lastEntityID != wantEntityID {
		t.Errorf("last audit_log entity_id = %q, want %q", lastEntityID, wantEntityID)
	}

	// bob has no household_membership row at all -- not a member of any
	// household. Every write refuses him, and no additional audit row is
	// added by a refused write.
	bobScope := plantsScopeFor(t, resolver, "bob")
	if _, err := repo.CorrectPlant(ctx, plant.PlantID, &newName, nil, bobScope, plantAuditEntry("bob", "CorrectPlant")); err == nil {
		t.Error("CorrectPlant by a non-member succeeded, want a refusal (NFR2)")
	}
	if _, err := repo.MovePlant(ctx, plant.PlantID, regionA, bobScope, plantAuditEntry("bob", "MovePlant")); err == nil {
		t.Error("MovePlant by a non-member succeeded, want a refusal (NFR2)")
	}
	if _, err := repo.RetirePlant(ctx, plant.PlantID, bobScope, plantAuditEntry("bob", "RetirePlant")); err == nil {
		t.Error("RetirePlant by a non-member succeeded, want a refusal (NFR2)")
	}
	if n := plantsCountRows(t, pool, "audit_log"); n != 4 {
		t.Errorf("audit_log rows after non-member refusals = %d, want unchanged 4", n)
	}
}
