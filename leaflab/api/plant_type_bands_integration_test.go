//go:build integration

// Real-Postgres integration coverage for #1382's FR58/FR27 band criterion:
// Repository.SetPlantTypeBands/GetPlantTypeBands (plant_type_bands.go)
// exercised against real SQL -- same rationale as this package's other
// hermetic, self-contained integration tests (see
// plants_lifecycle_integration_test.go's doc comment). Plain postgres, not
// TimescaleDB: unlike CreatePlantWithNewType, neither CreatePlantType nor
// SetPlantTypeBands/GetPlantTypeBands ever calls capture.Recorder.Record
// (no time_bucket() dependency).
//
// Schema is a hand-written trim of the tables plant_type_bands.go's write
// path touches: household/household_membership (migration 015), plant_type
// with migration 034's household_id column (FR55), sensor_type, the new
// plant_type_band table (migration 035), admin_elevation (migration 029,
// FR10's elevation gate for a global-row write) and audit_log.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //leaflab/api:plant_type_bands_integration_test --test_output=all
package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/leaflab/api/audit"
	"github.com/whale-net/everything/leaflab/api/authz"
	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/libs/go/dbtest"
)

const plantTypeBandsTestSchema = `
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
	CREATE INDEX idx_ptb_household_membership_current
		ON household_membership(principal_subject) WHERE valid_to IS NULL;

	-- Migration 034's ownership split: household_id NULL means global.
	CREATE TABLE plant_type (
		plant_type_id BIGSERIAL PRIMARY KEY,
		common_name   VARCHAR(128) NOT NULL,
		species       VARCHAR(128),
		household_id  BIGINT REFERENCES household(household_id) ON DELETE RESTRICT,
		retired_at    TIMESTAMPTZ
	);
	CREATE INDEX idx_ptb_plant_type_household_id ON plant_type(household_id) WHERE retired_at IS NULL;

	CREATE TABLE sensor_type (
		sensor_type_id BIGSERIAL PRIMARY KEY,
		name           VARCHAR(64) NOT NULL UNIQUE
	);
	INSERT INTO sensor_type (name) VALUES ('temperature'), ('humidity');

	-- Migration 035's plant_type_band table, verbatim.
	CREATE TABLE plant_type_band (
		plant_type_band_id BIGSERIAL PRIMARY KEY,
		plant_type_id  BIGINT NOT NULL REFERENCES plant_type(plant_type_id),
		sensor_type_id BIGINT NOT NULL REFERENCES sensor_type(sensor_type_id),
		band_label TEXT NOT NULL,
		min_value DOUBLE PRECISION NULL,
		max_value DOUBLE PRECISION NULL,
		sort_order INT NOT NULL,
		UNIQUE (plant_type_id, sensor_type_id, band_label)
	);
	CREATE INDEX idx_ptb_plant_type_band_lookup
		ON plant_type_band(plant_type_id, sensor_type_id, sort_order);

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

// temperatureSensorTypeID is seeded by plantTypeBandsTestSchema's
// INSERT INTO sensor_type above (id 1, since sensor_type_id is a fresh
// BIGSERIAL in every test's own isolated database).
const temperatureSensorTypeID int64 = 1

func newPlantTypeBandsTestRepository(t *testing.T) (*Repository, *authz.PGResolver, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: plantTypeBandsTestSchema})
	repo := NewRepository(db.Pool)
	resolver := authz.NewPGResolver(db.Pool)
	return repo, resolver, db.Pool
}

func ptbInsertHousehold(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO household DEFAULT VALUES RETURNING household_id`).Scan(&id); err != nil {
		t.Fatalf("insert household: %v", err)
	}
	return id
}

func ptbInsertMembership(t *testing.T, pool *pgxpool.Pool, householdID int64, subject string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO household_membership (household_id, principal_subject) VALUES ($1, $2)`,
		householdID, subject); err != nil {
		t.Fatalf("insert membership for %q: %v", subject, err)
	}
}

func ptbScopeFor(t *testing.T, resolver *authz.PGResolver, subject string) authz.Scope {
	t.Helper()
	scope, err := resolver.ScopeForPrincipal(context.Background(), subject)
	if err != nil {
		t.Fatalf("ScopeForPrincipal(%q): %v", subject, err)
	}
	return scope
}

func ptbOpenElevation(t *testing.T, pool *pgxpool.Pool, adminSubject string, targetHouseholdID int64) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO admin_elevation (admin_subject, target_household_id, reason, expires_at)
		VALUES ($1, $2, 'testing', $3)
	`, adminSubject, targetHouseholdID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("open elevation for %q: %v", adminSubject, err)
	}
}

func ptbAuditEntry(actor, action string) audit.Entry {
	return audit.Entry{
		ActorSubject: actor,
		ActorKind:    audit.ActorKindHuman,
		Action:       action,
		EntityKind:   "plant_type_band",
	}
}

func ptbCountRows(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("count rows in %s: %v", table, err)
	}
	return n
}

func ptbFailureOf(t *testing.T, err error) *pb.Failure {
	t.Helper()
	f, ok := contract.FromError(err)
	if !ok {
		t.Fatalf("error carries no contract.Failure detail: %v", err)
	}
	return f
}

func ptbFloat(v float64) *float64 { return &v }

// -- Bands are stored and readable per plant type and measurement type ----

// TestSetGetPlantTypeBands_HouseholdOwned_NoElevationRequired_RoundTrips
// proves this task's core storage criterion: a member sets bands on a
// household-owned plant type with no elevation held at all, and reads
// them back verbatim, ordered by sort_order.
func TestSetGetPlantTypeBands_HouseholdOwned_NoElevationRequired_RoundTrips(t *testing.T) {
	repo, resolver, pool := newPlantTypeBandsTestRepository(t)
	ctx := context.Background()

	householdID := ptbInsertHousehold(t, pool)
	ptbInsertMembership(t, pool, householdID, "alice")
	aliceScope := ptbScopeFor(t, resolver, "alice")

	typeRow, err := repo.CreatePlantType(ctx, "Boston Fern", nil, false, "alice", aliceScope, ptbAuditEntry("alice", "CreatePlantType"))
	if err != nil {
		t.Fatalf("CreatePlantType (household-owned): %v", err)
	}

	bands := []PlantTypeBandSpec{
		{BandLabel: "low", MinValue: nil, MaxValue: ptbFloat(18), SortOrder: 1},
		{BandLabel: "ideal", MinValue: ptbFloat(18), MaxValue: ptbFloat(25), SortOrder: 2},
		{BandLabel: "high", MinValue: ptbFloat(25), MaxValue: nil, SortOrder: 3},
	}
	stored, err := repo.SetPlantTypeBands(ctx, typeRow.PlantTypeID, temperatureSensorTypeID, bands, "alice", aliceScope, ptbAuditEntry("alice", "SetPlantTypeBands"))
	if err != nil {
		t.Fatalf("SetPlantTypeBands (household-owned, no elevation held): %v", err)
	}
	if len(stored) != 3 {
		t.Fatalf("SetPlantTypeBands returned %d bands, want 3", len(stored))
	}
	if n := ptbCountRows(t, pool, "admin_elevation"); n != 0 {
		t.Errorf("admin_elevation has %d rows, want 0 -- household-owned band write must never touch elevation", n)
	}

	readBack, err := repo.GetPlantTypeBands(ctx, typeRow.PlantTypeID, temperatureSensorTypeID)
	if err != nil {
		t.Fatalf("GetPlantTypeBands: %v", err)
	}
	if len(readBack) != 3 {
		t.Fatalf("GetPlantTypeBands returned %d bands, want 3", len(readBack))
	}
	wantLabels := []string{"low", "ideal", "high"}
	for i, row := range readBack {
		if row.BandLabel != wantLabels[i] {
			t.Errorf("readBack[%d].BandLabel = %q, want %q (sort_order order)", i, row.BandLabel, wantLabels[i])
		}
	}
}

// -- Global type: member refused, elevated admin succeeds ------------------

// TestSetPlantTypeBands_GlobalType_MemberRefused_ElevatedSucceeds proves
// FR58/FR10: setting bands on a global plant type requires elevation, the
// same as any other global-row write on this catalog.
func TestSetPlantTypeBands_GlobalType_MemberRefused_ElevatedSucceeds(t *testing.T) {
	repo, resolver, pool := newPlantTypeBandsTestRepository(t)
	ctx := context.Background()

	adminHousehold := ptbInsertHousehold(t, pool)
	ptbOpenElevation(t, pool, "admin1", adminHousehold)
	adminScope := ptbScopeFor(t, resolver, "admin1")

	globalType, err := repo.CreatePlantType(ctx, "Global Boston Fern", nil, true, "admin1", adminScope, ptbAuditEntry("admin1", "CreatePlantType"))
	if err != nil {
		t.Fatalf("CreatePlantType (global): %v", err)
	}

	householdID := ptbInsertHousehold(t, pool)
	ptbInsertMembership(t, pool, householdID, "alice")
	aliceScope := ptbScopeFor(t, resolver, "alice")

	bands := []PlantTypeBandSpec{
		{BandLabel: "ideal", MinValue: ptbFloat(18), MaxValue: ptbFloat(25), SortOrder: 1},
	}

	// Plain member, no elevation: refused.
	_, err = repo.SetPlantTypeBands(ctx, globalType.PlantTypeID, temperatureSensorTypeID, bands, "alice", aliceScope, ptbAuditEntry("alice", "SetPlantTypeBands"))
	if err == nil {
		t.Fatal("SetPlantTypeBands on a global type by an unelevated member succeeded, want a refusal (FR58/FR10)")
	}
	f := ptbFailureOf(t, err)
	if f.Class != string(contract.FailurePermissionDenied) {
		t.Errorf("failure class = %q, want %q", f.Class, contract.FailurePermissionDenied)
	}
	if n := ptbCountRows(t, pool, "plant_type_band"); n != 0 {
		t.Fatalf("plant_type_band has %d rows after a refused global write, want 0", n)
	}

	// Elevated admin: succeeds.
	stored, err := repo.SetPlantTypeBands(ctx, globalType.PlantTypeID, temperatureSensorTypeID, bands, "admin1", adminScope, ptbAuditEntry("admin1", "SetPlantTypeBands"))
	if err != nil {
		t.Fatalf("SetPlantTypeBands on a global type by an elevated admin: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("SetPlantTypeBands returned %d bands, want 1", len(stored))
	}
}

// -- Overlapping bands are refused, naming the offending bands -------------

// TestSetPlantTypeBands_OverlappingBands_RefusedNamingOffenders_NothingWritten
// proves this task's overlap rule ("bands must not overlap; gaps are
// permitted") and FR59.3's refused-with-alternative shape: an overlapping
// set is refused, the failure reason names the offending bands, and
// nothing is written.
func TestSetPlantTypeBands_OverlappingBands_RefusedNamingOffenders_NothingWritten(t *testing.T) {
	repo, resolver, pool := newPlantTypeBandsTestRepository(t)
	ctx := context.Background()

	householdID := ptbInsertHousehold(t, pool)
	ptbInsertMembership(t, pool, householdID, "alice")
	aliceScope := ptbScopeFor(t, resolver, "alice")

	typeRow, err := repo.CreatePlantType(ctx, "Overlap Fern", nil, false, "alice", aliceScope, ptbAuditEntry("alice", "CreatePlantType"))
	if err != nil {
		t.Fatalf("CreatePlantType: %v", err)
	}

	// "low" is [-inf, 20); "ideal" is [18, 25) -- these overlap in [18, 20).
	bands := []PlantTypeBandSpec{
		{BandLabel: "low", MinValue: nil, MaxValue: ptbFloat(20), SortOrder: 1},
		{BandLabel: "ideal", MinValue: ptbFloat(18), MaxValue: ptbFloat(25), SortOrder: 2},
	}
	_, err = repo.SetPlantTypeBands(ctx, typeRow.PlantTypeID, temperatureSensorTypeID, bands, "alice", aliceScope, ptbAuditEntry("alice", "SetPlantTypeBands"))
	if err == nil {
		t.Fatal("SetPlantTypeBands with overlapping bands succeeded, want a refusal")
	}
	f := ptbFailureOf(t, err)
	if f.Class != string(contract.FailureRefusedWithAlternative) {
		t.Errorf("failure class = %q, want %q", f.Class, contract.FailureRefusedWithAlternative)
	}
	if !strings.Contains(f.Reason, "low") || !strings.Contains(f.Reason, "ideal") {
		t.Errorf("failure reason = %q, want it to name both offending bands %q and %q", f.Reason, "low", "ideal")
	}
	if n := ptbCountRows(t, pool, "plant_type_band"); n != 0 {
		t.Fatalf("plant_type_band has %d rows after a refused overlapping write, want 0", n)
	}
}

// -- A plant type with no bands returns empty, not an error -----------------

// TestGetPlantTypeBands_NoBandsConfigured_ReturnsEmptyNotError proves this
// task's Testing criterion: "A plant type with no bands returns values
// with no band field, not an error" -- at the read layer, GetPlantTypeBands
// itself returns an empty slice and no error.
func TestGetPlantTypeBands_NoBandsConfigured_ReturnsEmptyNotError(t *testing.T) {
	repo, resolver, pool := newPlantTypeBandsTestRepository(t)
	ctx := context.Background()

	householdID := ptbInsertHousehold(t, pool)
	ptbInsertMembership(t, pool, householdID, "alice")
	aliceScope := ptbScopeFor(t, resolver, "alice")

	typeRow, err := repo.CreatePlantType(ctx, "Bandless Fern", nil, false, "alice", aliceScope, ptbAuditEntry("alice", "CreatePlantType"))
	if err != nil {
		t.Fatalf("CreatePlantType: %v", err)
	}

	bands, err := repo.GetPlantTypeBands(ctx, typeRow.PlantTypeID, temperatureSensorTypeID)
	if err != nil {
		t.Fatalf("GetPlantTypeBands on a type with no bands configured: %v", err)
	}
	if len(bands) != 0 {
		t.Errorf("GetPlantTypeBands = %v, want empty", bands)
	}
}
