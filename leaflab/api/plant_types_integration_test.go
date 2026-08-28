//go:build integration

// Real-Postgres (TimescaleDB, for CreatePlantWithNewType's capture.Recorder
// call) integration coverage for #1378's plant-type catalog ownership
// split (FR55, A24): CreatePlantType/RenamePlantType/RetirePlantType/
// ListPlantTypes against real SQL, plus CreatePlantWithNewType's SB-1.10
// acquire-and-place -- exercised the same way
// plants_lifecycle_integration_test.go exercises #1377's plant lifecycle
// (see that file's doc comment for why these integration tests stay
// hermetic and self-contained rather than sharing a schema/helper file
// across packages).
//
// Member-vs-grantee coverage here is limited to member-success (FR7's
// member-or-grantee stands in as member-only -- same scope note as
// plants_lifecycle_integration_test.go / regions_lifecycle_integration_test.go:
// authz.MemberOrGrantee does not exist on this branch lineage, see
// plant_types.go's doc comment and scope notes #1417/#1427).
//
// FR77 coverage (a closure transfer copying a household-owned plant type
// into the gaining household) is deliberately NOT included in this file --
// see #1454: copyOwnedPlantTypes' real body needs both
// plant_type.household_id (this branch) and the TransferClosure call site
// + departure-record plumbing, which lives on #1343's branch, a sibling
// (not an ancestor) of this branch's dependency chain. Neither branch
// alone can exercise the coupled behavior; #1454 assigns landing the real
// hook -- and the integration test proving it -- to whoever merges the two
// branches (or the whole-system integration branch).
//
// Schema is a hand-written mirror of the tables plant_types.go's write
// paths touch: household/household_membership (migration 015), plant_type
// with migration 034's household_id/retired_at columns, plant, region,
// sensor (for CreatePlantWithNewType's capture coupling),
// plant_region_history (migration 017, verbatim no-back-dating trigger),
// boundary_capture (migration 033), admin_elevation (migration 029, FR10's
// elevation gate for a global-row write) and audit_log.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //leaflab/api:plant_types_integration_test --test_output=all
package main

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bazelbuild/rules_go/go/runfiles"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/leaflab/api/audit"
	"github.com/whale-net/everything/leaflab/api/authz"
	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/libs/go/dbtest"
)

// plantTypesTimescaleImage matches plantsTimescaleImage
// (plants_lifecycle_integration_test.go) -- CreatePlantWithNewType calls
// capture.Recorder.Record, which calls time_bucket(), a TimescaleDB
// function not present in plain postgres.
const plantTypesTimescaleImage = "timescale/timescaledb:latest-pg16"

const plantTypesTestSchema = `
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
	CREATE INDEX idx_plant_types_household_membership_current
		ON household_membership(principal_subject) WHERE valid_to IS NULL;

	CREATE TABLE region (
		region_id        BIGSERIAL PRIMARY KEY,
		parent_region_id BIGINT REFERENCES region(region_id) ON DELETE RESTRICT,
		name              VARCHAR(255) NOT NULL,
		household_id      BIGINT REFERENCES household(household_id)
	);

	-- Migration 034's ownership split: household_id NULL means global.
	CREATE TABLE plant_type (
		plant_type_id BIGSERIAL PRIMARY KEY,
		common_name   VARCHAR(128) NOT NULL,
		species       VARCHAR(128),
		household_id  BIGINT REFERENCES household(household_id) ON DELETE RESTRICT,
		retired_at    TIMESTAMPTZ
	);
	CREATE INDEX idx_plant_types_household_id ON plant_type(household_id) WHERE retired_at IS NULL;

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
	CREATE INDEX idx_plant_types_prh_plant_id_current
		ON plant_region_history(plant_id) WHERE valid_to IS NULL;
	CREATE INDEX idx_plant_types_prh_region_id_current
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

	CREATE TRIGGER trg_plant_types_prh_no_future_valid_from
		BEFORE INSERT ON plant_region_history
		FOR EACH ROW
		EXECUTE FUNCTION enforce_plant_region_history_no_future_valid_from();

	CREATE TABLE sensor (
		sensor_id BIGSERIAL PRIMARY KEY,
		region_id BIGINT REFERENCES region(region_id)
	);

	CREATE TABLE boundary_capture (
		capture_id    BIGSERIAL PRIMARY KEY,
		sensor_id     BIGINT NOT NULL REFERENCES sensor(sensor_id) ON DELETE RESTRICT,
		boundary_at   TIMESTAMPTZ NOT NULL,
		tier          TEXT NOT NULL,
		bucket_start  TIMESTAMPTZ NOT NULL,
		state         TEXT NOT NULL DEFAULT 'pending',
		completed_at  TIMESTAMPTZ,
		CONSTRAINT plant_types_boundary_capture_tier_check
			CHECK (tier IN ('five_minute', 'hourly')),
		CONSTRAINT plant_types_boundary_capture_state_check
			CHECK (state IN ('pending', 'completed')),
		CONSTRAINT plant_types_boundary_capture_completed_at_check
			CHECK ((state = 'completed') = (completed_at IS NOT NULL))
	);

	-- Migration 029's admin_elevation table -- FR10's elevation gate for a
	-- global plant_type write (requireGlobalWriteElevation /
	-- Repository.AnyActiveElevation).
	CREATE TABLE admin_elevation (
		elevation_id         BIGSERIAL PRIMARY KEY,
		admin_subject        TEXT NOT NULL,
		target_household_id  BIGINT NOT NULL REFERENCES household(household_id) ON DELETE RESTRICT,
		reason                TEXT NOT NULL,
		started_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		expires_at            TIMESTAMPTZ NOT NULL,
		ended_at              TIMESTAMPTZ NULL
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

// newPlantTypesTestRepository starts a real TimescaleDB container with
// plantTypesTestSchema applied and returns a *Repository plus a real
// authz.PGResolver (member-only authorization stand-in against real
// household/household_membership SQL) and the raw pool for fixture
// setup/assertions.
func newPlantTypesTestRepository(t *testing.T) (*Repository, *authz.PGResolver, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Image: plantTypesTimescaleImage, Schema: plantTypesTestSchema})
	repo := NewRepository(db.Pool)
	resolver := authz.NewPGResolver(db.Pool)
	return repo, resolver, db.Pool
}

func plantTypesInsertHousehold(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO household DEFAULT VALUES RETURNING household_id`).Scan(&id); err != nil {
		t.Fatalf("insert household: %v", err)
	}
	return id
}

func plantTypesInsertMembership(t *testing.T, pool *pgxpool.Pool, householdID int64, subject string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO household_membership (household_id, principal_subject) VALUES ($1, $2)`,
		householdID, subject); err != nil {
		t.Fatalf("insert membership for %q: %v", subject, err)
	}
}

func plantTypesScopeFor(t *testing.T, resolver *authz.PGResolver, subject string) authz.Scope {
	t.Helper()
	scope, err := resolver.ScopeForPrincipal(context.Background(), subject)
	if err != nil {
		t.Fatalf("ScopeForPrincipal(%q): %v", subject, err)
	}
	return scope
}

func plantTypesInsertRegion(t *testing.T, pool *pgxpool.Pool, name string, householdID int64) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO region (name, household_id) VALUES ($1, $2) RETURNING region_id`,
		name, householdID).Scan(&id); err != nil {
		t.Fatalf("insert region %s: %v", name, err)
	}
	return id
}

// plantTypesInsertPlant inserts a plant row directly, referencing
// plantTypeID -- used to build the "a plant references this type" fixture
// FR59.3's retirement guard tests need, without going through
// CreatePlant/CreatePlantWithNewType.
func plantTypesInsertPlant(t *testing.T, pool *pgxpool.Pool, regionID, plantTypeID, householdID int64, name string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO plant (region_id, plant_type_id, household_id, name) VALUES ($1, $2, $3, $4) RETURNING plant_id`,
		regionID, plantTypeID, householdID, name).Scan(&id); err != nil {
		t.Fatalf("insert plant %s: %v", name, err)
	}
	return id
}

// plantTypesOpenElevation opens a real admin_elevation row for adminSubject
// against targetHouseholdID, expiring an hour from now -- FR10's elevation
// fixture for a global plant_type write.
func plantTypesOpenElevation(t *testing.T, pool *pgxpool.Pool, adminSubject string, targetHouseholdID int64) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO admin_elevation (admin_subject, target_household_id, reason, expires_at)
		VALUES ($1, $2, 'testing', $3)
	`, adminSubject, targetHouseholdID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("open elevation for %q: %v", adminSubject, err)
	}
}

func plantTypesAuditEntry(actor, action string) audit.Entry {
	return audit.Entry{
		ActorSubject: actor,
		ActorKind:    audit.ActorKindHuman,
		Action:       action,
		EntityKind:   "plant_type",
	}
}

func plantTypesCountRows(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("count rows in %s: %v", table, err)
	}
	return n
}

func plantTypesFailureOf(t *testing.T, err error) *pb.Failure {
	t.Helper()
	f, ok := contract.FromError(err)
	if !ok {
		t.Fatalf("error carries no contract.Failure detail: %v", err)
	}
	return f
}

// -- Household-owned create: no elevation needed ----------------------------

// TestCreatePlantType_HouseholdOwned_NoElevationRequired proves FR55's
// "a household member may create ... plant types owned by their own
// household ... acquire-and-place never requires an elevated principal":
// a plain member, holding no admin_elevation row at all, creates a
// household-owned plant type successfully.
func TestCreatePlantType_HouseholdOwned_NoElevationRequired(t *testing.T) {
	repo, resolver, pool := newPlantTypesTestRepository(t)
	ctx := context.Background()

	householdID := plantTypesInsertHousehold(t, pool)
	plantTypesInsertMembership(t, pool, householdID, "alice")
	aliceScope := plantTypesScopeFor(t, resolver, "alice")

	row, err := repo.CreatePlantType(ctx, "Boston Fern", nil, false, "alice", aliceScope, plantTypesAuditEntry("alice", "CreatePlantType"))
	if err != nil {
		t.Fatalf("CreatePlantType (household-owned, no elevation held): %v", err)
	}
	if row.HouseholdID == nil || *row.HouseholdID != householdID {
		t.Fatalf("row.HouseholdID = %v, want %d", row.HouseholdID, householdID)
	}
	if n := plantTypesCountRows(t, pool, "admin_elevation"); n != 0 {
		t.Errorf("admin_elevation has %d rows, want 0 -- household-owned creation must never touch elevation", n)
	}
}

// -- Global row: member refused, elevated admin succeeds and is audited ----

// TestCreatePlantType_GlobalRow_MemberRefused_ElevatedAdminSucceedsAndAudited
// proves FR55's "global rows ... writable only under elevation": a plain
// member with no elevation is refused; an admin holding an active
// elevation (against *some* household -- AnyActiveElevation, not a
// specific one, since a global row has none) succeeds, and the resulting
// audit row carries a null target household and a self-explanatory reason
// (plant_types.go's globalWriteAuditNote).
func TestCreatePlantType_GlobalRow_MemberRefused_ElevatedAdminSucceedsAndAudited(t *testing.T) {
	repo, resolver, pool := newPlantTypesTestRepository(t)
	ctx := context.Background()

	householdID := plantTypesInsertHousehold(t, pool)
	plantTypesInsertMembership(t, pool, householdID, "alice")
	aliceScope := plantTypesScopeFor(t, resolver, "alice")

	// Plain member, no elevation: refused.
	_, err := repo.CreatePlantType(ctx, "Global Boston Fern", nil, true, "alice", aliceScope, plantTypesAuditEntry("alice", "CreatePlantType"))
	if err == nil {
		t.Fatal("CreatePlantType(global=true) by an unelevated member succeeded, want a refusal (FR55/FR10)")
	}
	f := plantTypesFailureOf(t, err)
	if f.Class != string(contract.FailurePermissionDenied) {
		t.Errorf("failure class = %q, want %q", f.Class, contract.FailurePermissionDenied)
	}
	if n := plantTypesCountRows(t, pool, "plant_type"); n != 0 {
		t.Fatalf("plant_type has %d rows after a refused global write, want 0", n)
	}

	// Elevated admin: succeeds. adminSubject need not be a household
	// member at all -- elevation is what gates a global write, not
	// membership.
	adminHousehold := plantTypesInsertHousehold(t, pool)
	plantTypesOpenElevation(t, pool, "admin1", adminHousehold)
	adminScope := plantTypesScopeFor(t, resolver, "admin1")

	row, err := repo.CreatePlantType(ctx, "Global Boston Fern", nil, true, "admin1", adminScope, plantTypesAuditEntry("admin1", "CreatePlantType"))
	if err != nil {
		t.Fatalf("CreatePlantType(global=true) by an elevated admin: %v", err)
	}
	if row.HouseholdID != nil {
		t.Errorf("row.HouseholdID = %v, want nil (global row)", row.HouseholdID)
	}

	var targetHouseholdID *int64
	var reason *string
	if err := pool.QueryRow(ctx, `SELECT target_household_id, reason FROM audit_log WHERE actor_subject = 'admin1' ORDER BY audit_id DESC LIMIT 1`).Scan(&targetHouseholdID, &reason); err != nil {
		t.Fatalf("read audit_log row for global write: %v", err)
	}
	if targetHouseholdID != nil {
		t.Errorf("audit_log.target_household_id = %v, want NULL for a global-row write", *targetHouseholdID)
	}
	if reason == nil || strings.TrimSpace(*reason) == "" {
		t.Error("audit_log.reason is empty for a global-row write, want a self-explanatory note (\"audited with a null target household, and say so in the audit row\")")
	}
}

// -- Acquire-and-place: one call, no elevation anywhere ---------------------

// TestCreatePlantWithNewType_AcquireAndPlace_NoElevationAnywhere proves
// SB-1.10's whole point: one CreatePlantWithNewType call creates a
// household-owned plant type and places a plant against it in a single
// transaction, with no elevated principal anywhere in the call -- the
// caller holds no admin_elevation row at all, and the resulting audit
// row(s) never reference elevation.
func TestCreatePlantWithNewType_AcquireAndPlace_NoElevationAnywhere(t *testing.T) {
	repo, resolver, pool := newPlantTypesTestRepository(t)
	ctx := context.Background()

	householdID := plantTypesInsertHousehold(t, pool)
	plantTypesInsertMembership(t, pool, householdID, "alice")
	aliceScope := plantTypesScopeFor(t, resolver, "alice")
	regionID := plantTypesInsertRegion(t, pool, "Grow Room", householdID)

	plantRow, typeRow, err := repo.CreatePlantWithNewType(ctx, regionID, "Freddy", "Boston Fern", nil, aliceScope, plantTypesAuditEntry("alice", "CreatePlantWithNewType"))
	if err != nil {
		t.Fatalf("CreatePlantWithNewType: %v", err)
	}
	if plantRow.PlantTypeID != typeRow.PlantTypeID {
		t.Errorf("plantRow.PlantTypeID = %d, want it to reference the newly created type %d", plantRow.PlantTypeID, typeRow.PlantTypeID)
	}
	if typeRow.HouseholdID == nil || *typeRow.HouseholdID != householdID {
		t.Fatalf("new type's HouseholdID = %v, want %d (household-owned)", typeRow.HouseholdID, householdID)
	}

	if n := plantTypesCountRows(t, pool, "admin_elevation"); n != 0 {
		t.Errorf("admin_elevation has %d rows, want 0 -- acquire-and-place must never touch elevation", n)
	}

	rows, err := pool.Query(ctx, `SELECT action FROM audit_log ORDER BY audit_id`)
	if err != nil {
		t.Fatalf("read audit_log: %v", err)
	}
	defer rows.Close()
	var sawElevationAction bool
	var n int
	for rows.Next() {
		var action string
		if err := rows.Scan(&action); err != nil {
			t.Fatalf("scan action: %v", err)
		}
		n++
		if strings.Contains(strings.ToLower(action), "elevat") {
			sawElevationAction = true
		}
	}
	if n == 0 {
		t.Fatal("audit_log has no rows after acquire-and-place, want at least one (FR8)")
	}
	if sawElevationAction {
		t.Error("audit_log contains an elevation-related action after acquire-and-place, want none -- SB-1.10 requires no elevated principal anywhere in the call")
	}
}

// -- ListPlantTypes: global + own, distinguishable, cross-household hidden -

// TestListPlantTypes_GlobalPlusOwn_CrossHouseholdHidden_Distinguishable
// proves FR55's read contract: ListPlantTypes returns global rows plus the
// caller's own household-owned rows; another household's owned rows are
// never returned; and two same-named types (one global, one owned) are
// distinguishable by their HouseholdID field.
func TestListPlantTypes_GlobalPlusOwn_CrossHouseholdHidden_Distinguishable(t *testing.T) {
	repo, resolver, pool := newPlantTypesTestRepository(t)
	ctx := context.Background()

	aliceHousehold := plantTypesInsertHousehold(t, pool)
	plantTypesInsertMembership(t, pool, aliceHousehold, "alice")
	aliceScope := plantTypesScopeFor(t, resolver, "alice")

	bobHousehold := plantTypesInsertHousehold(t, pool)
	plantTypesInsertMembership(t, pool, bobHousehold, "bob")
	bobScope := plantTypesScopeFor(t, resolver, "bob")

	// Global "Boston Fern".
	adminHousehold := plantTypesInsertHousehold(t, pool)
	plantTypesOpenElevation(t, pool, "admin1", adminHousehold)
	adminScope := plantTypesScopeFor(t, resolver, "admin1")
	globalFern, err := repo.CreatePlantType(ctx, "Boston Fern", nil, true, "admin1", adminScope, plantTypesAuditEntry("admin1", "CreatePlantType"))
	if err != nil {
		t.Fatalf("CreatePlantType (global): %v", err)
	}

	// Alice's own, same-named, household-owned "Boston Fern".
	aliceFern, err := repo.CreatePlantType(ctx, "Boston Fern", nil, false, "alice", aliceScope, plantTypesAuditEntry("alice", "CreatePlantType"))
	if err != nil {
		t.Fatalf("CreatePlantType (alice-owned): %v", err)
	}

	// Bob's own, unrelated, household-owned type -- must never appear in
	// alice's listing.
	bobType, err := repo.CreatePlantType(ctx, "Snake Plant", nil, false, "bob", bobScope, plantTypesAuditEntry("bob", "CreatePlantType"))
	if err != nil {
		t.Fatalf("CreatePlantType (bob-owned): %v", err)
	}

	listed, err := repo.ListPlantTypes(ctx, 0, false, 100, aliceScope)
	if err != nil {
		t.Fatalf("ListPlantTypes(alice): %v", err)
	}

	byID := map[int64]PlantTypeRow{}
	for _, row := range listed {
		byID[row.PlantTypeID] = row
	}

	globalRow, ok := byID[globalFern.PlantTypeID]
	if !ok {
		t.Fatal("ListPlantTypes(alice) did not return the global Boston Fern")
	}
	if globalRow.HouseholdID != nil {
		t.Errorf("global row's HouseholdID = %v, want nil", globalRow.HouseholdID)
	}

	ownRow, ok := byID[aliceFern.PlantTypeID]
	if !ok {
		t.Fatal("ListPlantTypes(alice) did not return alice's own Boston Fern")
	}
	if ownRow.HouseholdID == nil || *ownRow.HouseholdID != aliceHousehold {
		t.Errorf("alice's own row's HouseholdID = %v, want %d", ownRow.HouseholdID, aliceHousehold)
	}

	// Distinguishable: two different plant_type_ids, one nil HouseholdID,
	// one set to alice's household, despite the identical common_name.
	if globalRow.PlantTypeID == ownRow.PlantTypeID {
		t.Fatal("global and alice-owned same-named types collapsed to one row")
	}
	if globalRow.CommonName != ownRow.CommonName {
		t.Fatalf("test setup: expected identical common names, got %q and %q", globalRow.CommonName, ownRow.CommonName)
	}

	if _, ok := byID[bobType.PlantTypeID]; ok {
		t.Error("ListPlantTypes(alice) returned bob's household-owned type, want it hidden -- no path may reach another household's owned types")
	}
}

// -- FR59.3: a referenced type cannot be retired -----------------------------

// TestRetirePlantType_ReferencedByPlant_Refused_NamesPlant_BothClasses
// proves FR59.3 for both ownership classes: retiring a plant type still
// referenced by an active plant is refused, naming the referencing plant,
// whether the type is global or household-owned.
func TestRetirePlantType_ReferencedByPlant_Refused_NamesPlant_BothClasses(t *testing.T) {
	repo, resolver, pool := newPlantTypesTestRepository(t)
	ctx := context.Background()

	householdID := plantTypesInsertHousehold(t, pool)
	plantTypesInsertMembership(t, pool, householdID, "alice")
	aliceScope := plantTypesScopeFor(t, resolver, "alice")
	regionID := plantTypesInsertRegion(t, pool, "Grow Room", householdID)

	// Household-owned, referenced.
	ownedType, err := repo.CreatePlantType(ctx, "Referenced Owned Fern", nil, false, "alice", aliceScope, plantTypesAuditEntry("alice", "CreatePlantType"))
	if err != nil {
		t.Fatalf("CreatePlantType (owned): %v", err)
	}
	plantTypesInsertPlant(t, pool, regionID, ownedType.PlantTypeID, householdID, "Ferdinand")

	_, err = repo.RetirePlantType(ctx, ownedType.PlantTypeID, "alice", aliceScope, plantTypesAuditEntry("alice", "RetirePlantType"))
	if err == nil {
		t.Fatal("RetirePlantType on an owned type referenced by a plant succeeded, want a refusal (FR59.3)")
	}
	f := plantTypesFailureOf(t, err)
	if f.Class != string(contract.FailureRefusedWithAlternative) {
		t.Errorf("failure class = %q, want %q", f.Class, contract.FailureRefusedWithAlternative)
	}
	if !strings.Contains(f.Reason, "Ferdinand") {
		t.Errorf("failure reason = %q, want it to name the referencing plant %q", f.Reason, "Ferdinand")
	}

	// Global, referenced.
	adminHousehold := plantTypesInsertHousehold(t, pool)
	plantTypesOpenElevation(t, pool, "admin1", adminHousehold)
	adminScope := plantTypesScopeFor(t, resolver, "admin1")
	globalType, err := repo.CreatePlantType(ctx, "Referenced Global Fern", nil, true, "admin1", adminScope, plantTypesAuditEntry("admin1", "CreatePlantType"))
	if err != nil {
		t.Fatalf("CreatePlantType (global): %v", err)
	}
	plantTypesInsertPlant(t, pool, regionID, globalType.PlantTypeID, householdID, "Globetrotter")

	_, err = repo.RetirePlantType(ctx, globalType.PlantTypeID, "admin1", adminScope, plantTypesAuditEntry("admin1", "RetirePlantType"))
	if err == nil {
		t.Fatal("RetirePlantType on a global type referenced by a plant succeeded, want a refusal (FR59.3)")
	}
	f = plantTypesFailureOf(t, err)
	if !strings.Contains(f.Reason, "Globetrotter") {
		t.Errorf("failure reason = %q, want it to name the referencing plant %q", f.Reason, "Globetrotter")
	}

	// Neither retirement wrote anything -- both types remain active.
	for _, id := range []int64{ownedType.PlantTypeID, globalType.PlantTypeID} {
		var retiredAt *time.Time
		if err := pool.QueryRow(ctx, `SELECT retired_at FROM plant_type WHERE plant_type_id = $1`, id).Scan(&retiredAt); err != nil {
			t.Fatalf("read retired_at for type %d: %v", id, err)
		}
		if retiredAt != nil {
			t.Errorf("type %d retired_at = %v, want NULL -- a refused retirement must not write anything", id, retiredAt)
		}
	}

	// Once the referencing plant is gone (soft-removed), retirement of the
	// now-unreferenced owned type succeeds -- proves the guard is about
	// active references, and retirement itself remains reachable.
	if _, err := pool.Exec(ctx, `UPDATE plant SET removed_at = NOW() WHERE name = 'Ferdinand'`); err != nil {
		t.Fatalf("soft-remove Ferdinand: %v", err)
	}
	if _, err := repo.RetirePlantType(ctx, ownedType.PlantTypeID, "alice", aliceScope, plantTypesAuditEntry("alice", "RetirePlantType")); err != nil {
		t.Fatalf("RetirePlantType on an owned type after its only referencing plant was removed: %v", err)
	}
}

// -- FR22.1: nothing is hard-deleted -----------------------------------------

// TestPlantTypeWritePaths_NoHardDelete is a structural assertion for
// FR22.1's "nothing is hard-deleted": plant_types.go issues no DELETE
// against the plant_type table. Retirement is soft (retired_at), same as
// RetirePlant/RetireRegion/RetireBoard. Located via the Bazel runfiles
// manifest (see BUILD.bazel's data attribute on this go_test target)
// rather than a bare relative path, which is not reliably resolvable from
// a sandboxed test's working directory.
func TestPlantTypeWritePaths_NoHardDelete(t *testing.T) {
	path, err := runfiles.Rlocation("_main/leaflab/api/plant_types.go")
	if err != nil {
		t.Fatalf("runfiles.Rlocation(plant_types.go): %v (is it listed in this target's data attribute?)", err)
	}
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read plant_types.go: %v", err)
	}
	if hardDeletePlantTypePattern.Match(src) {
		t.Error("plant_types.go contains a DELETE FROM plant_type statement -- FR22.1 requires retirement to be soft (retired_at), never a hard delete")
	}
}

var hardDeletePlantTypePattern = regexp.MustCompile(`(?i)DELETE\s+FROM\s+plant_type\b`)
