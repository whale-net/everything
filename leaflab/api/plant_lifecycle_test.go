//go:build integration

package main

import (
	"context"
	"testing"
	"time"

	"github.com/whale-net/everything/libs/go/dbtest"
)

// This file exercises Repository.CreatePlant / CorrectPlant / RetirePlant /
// MovePlantRegion / GetPlantPlacementTimeline directly — the write-path layer
// server.go's plant lifecycle RPC handlers call into (#1221, FR54, FR24,
// FR22.1, FR22.3, FR22.5). It shares plantRegionTestSchema and
// setupPlantForRegionTest with plant_region_test.go.

// setupHouseholdRegionAndType creates a household, one region, and one
// plant_type row, returning their ids. Used by tests that create their own
// plant(s) via Repository.CreatePlant rather than relying on
// setupPlantForRegionTest's pre-seeded fixture plant.
func setupHouseholdRegionAndType(ctx context.Context, t *testing.T, db *dbtest.Postgres) (householdID, regionID, plantTypeID int64) {
	t.Helper()

	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO household (name) VALUES ('test-household')
		RETURNING household_id
	`).Scan(&householdID); err != nil {
		t.Fatalf("insert household: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO region (name, household_id) VALUES ('region', $1)
		RETURNING region_id
	`, householdID).Scan(&regionID); err != nil {
		t.Fatalf("insert region: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO plant_type (common_name) VALUES ('Test Species')
		RETURNING plant_type_id
	`).Scan(&plantTypeID); err != nil {
		t.Fatalf("insert plant_type: %v", err)
	}
	return householdID, regionID, plantTypeID
}

// countRows returns the row count of a fixed, test-controlled table name.
func countRows(ctx context.Context, t *testing.T, db *dbtest.Postgres, table string) int64 {
	t.Helper()
	var count int64
	if err := db.Pool.QueryRow(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

// TestCreatePlant_CreatesRowAndInitialNeverClosedPlacement verifies FR54's
// acquire-and-place: CreatePlant writes both the plant row and its first,
// never-closed plant_region_history interval.
func TestCreatePlant_CreatesRowAndInitialNeverClosedPlacement(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: plantRegionTestSchema})
	repo := NewRepository(db.Pool)

	householdID, regionID, plantTypeID := setupHouseholdRegionAndType(ctx, t, db)

	plantID, err := repo.CreatePlant(ctx, "Ficus", plantTypeID, regionID, householdID)
	if err != nil {
		t.Fatalf("CreatePlant: %v", err)
	}
	if plantID == 0 {
		t.Fatal("expected a non-zero plant id")
	}

	plant, err := repo.GetPlant(ctx, plantID)
	if err != nil {
		t.Fatalf("GetPlant: %v", err)
	}
	if plant.Name != "Ficus" || plant.PlantTypeID != plantTypeID || plant.RegionID != regionID || plant.HouseholdID != householdID {
		t.Errorf("unexpected plant row: %+v", plant)
	}
	if plant.RemovedAt != nil {
		t.Error("a newly created plant must not be retired")
	}

	entries, err := repo.GetPlantPlacementTimeline(ctx, plantID)
	if err != nil {
		t.Fatalf("GetPlantPlacementTimeline: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one placement interval at creation, got %d", len(entries))
	}
	if entries[0].RegionID != regionID {
		t.Errorf("expected initial placement in region %d, got %d", regionID, entries[0].RegionID)
	}
	if entries[0].ValidTo != nil {
		t.Error("FR54: the first placement interval must never be closed at creation")
	}
}

// TestCreatePlant_TwoPlantsSameRegionDifferentTimes_SeparateHistories verifies
// the issue's explicit scope statement: two plants occupying the same region
// at different times keep separate histories, and neither inherits the
// other's placement/attribution.
func TestCreatePlant_TwoPlantsSameRegionDifferentTimes_SeparateHistories(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: plantRegionTestSchema})
	repo := NewRepository(db.Pool)

	householdID, regionID, plantTypeID := setupHouseholdRegionAndType(ctx, t, db)

	plantAID, err := repo.CreatePlant(ctx, "Plant A", plantTypeID, regionID, householdID)
	if err != nil {
		t.Fatalf("create plant A: %v", err)
	}
	timeDuringA := time.Now()

	if _, err := repo.RetirePlant(ctx, plantAID, "retire_plant", "principal-1"); err != nil {
		t.Fatalf("retire plant A: %v", err)
	}

	plantBID, err := repo.CreatePlant(ctx, "Plant B", plantTypeID, regionID, householdID)
	if err != nil {
		t.Fatalf("create plant B: %v", err)
	}
	timeDuringB := time.Now()

	// Each plant's own timeline shows only its own interval(s).
	aEntries, err := repo.GetPlantPlacementTimeline(ctx, plantAID)
	if err != nil {
		t.Fatalf("timeline A: %v", err)
	}
	if len(aEntries) != 1 || aEntries[0].ValidTo == nil {
		t.Fatalf("expected plant A to have exactly one closed interval, got %+v", aEntries)
	}

	bEntries, err := repo.GetPlantPlacementTimeline(ctx, plantBID)
	if err != nil {
		t.Fatalf("timeline B: %v", err)
	}
	if len(bEntries) != 1 || bEntries[0].ValidTo != nil {
		t.Fatalf("expected plant B to have exactly one open interval, got %+v", bEntries)
	}

	// A point-in-time query during A's occupancy finds only A; during B's
	// occupancy finds only B. Neither plant's history bleeds into the other's.
	duringA, err := repo.GetActivePlantsInRegionAtTime(ctx, regionID, timeDuringA)
	if err != nil {
		t.Fatalf("active plants during A: %v", err)
	}
	if len(duringA) != 1 || duringA[0].PlantID != plantAID {
		t.Fatalf("expected only plant A active during A's occupancy, got %+v", duringA)
	}

	duringB, err := repo.GetActivePlantsInRegionAtTime(ctx, regionID, timeDuringB)
	if err != nil {
		t.Fatalf("active plants during B: %v", err)
	}
	if len(duringB) != 1 || duringB[0].PlantID != plantBID {
		t.Fatalf("expected only plant B active during B's occupancy, got %+v", duringB)
	}
}

// TestCorrectPlant_DoesNotOpenPlacementInterval verifies FR24: correcting a
// plant's name or type is distinct from a move and never touches
// plant_region_history.
func TestCorrectPlant_DoesNotOpenPlacementInterval(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: plantRegionTestSchema})
	repo := NewRepository(db.Pool)

	plantID, _, _ := setupPlantForRegionTest(ctx, t, db)

	before := countRows(ctx, t, db, "plant_region_history")

	if err := repo.CorrectPlant(ctx, plantID, "Corrected Name", 0); err != nil {
		t.Fatalf("CorrectPlant: %v", err)
	}

	after := countRows(ctx, t, db, "plant_region_history")
	if after != before {
		t.Errorf("FR24: correcting a plant must not open a placement interval; row count went from %d to %d", before, after)
	}

	plant, err := repo.GetPlant(ctx, plantID)
	if err != nil {
		t.Fatalf("GetPlant: %v", err)
	}
	if plant.Name != "Corrected Name" {
		t.Errorf("expected corrected name, got %q", plant.Name)
	}
}

// TestCorrectPlant_RefusesRetiredPlant verifies FR22.5: a retired plant
// accepts no new writes, including a correction.
func TestCorrectPlant_RefusesRetiredPlant(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: plantRegionTestSchema})
	repo := NewRepository(db.Pool)

	plantID, _, _ := setupPlantForRegionTest(ctx, t, db)

	if _, err := repo.RetirePlant(ctx, plantID, "retire_plant", "principal-1"); err != nil {
		t.Fatalf("retire plant: %v", err)
	}

	if err := repo.CorrectPlant(ctx, plantID, "New Name", 0); err == nil {
		t.Fatal("FR22.5: expected correcting a retired plant to be refused")
	}
}

// TestRetirePlant_ClosesPlacementNamesOperationAndPrincipal verifies FR22.5:
// retirement names the operation, records the acting principal (FR8), and
// closes the plant's open placement interval at the same instant.
func TestRetirePlant_ClosesPlacementNamesOperationAndPrincipal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: plantRegionTestSchema})
	repo := NewRepository(db.Pool)

	plantID, _, _ := setupPlantForRegionTest(ctx, t, db)

	retiredAt, err := repo.RetirePlant(ctx, plantID, "retire_plant", "principal-1")
	if err != nil {
		t.Fatalf("RetirePlant: %v", err)
	}
	if retiredAt.IsZero() {
		t.Error("expected a non-zero retired_at timestamp")
	}

	plant, err := repo.GetPlant(ctx, plantID)
	if err != nil {
		t.Fatalf("GetPlant: %v", err)
	}
	if plant.RemovedAt == nil {
		t.Fatal("FR22.5: expected removed_at to be set")
	}
	if plant.RetiredOperation == nil || *plant.RetiredOperation != "retire_plant" {
		t.Errorf("FR22.5: expected retired_operation to name the operation, got %+v", plant.RetiredOperation)
	}
	if plant.RetiredPrincipal == nil || *plant.RetiredPrincipal != "principal-1" {
		t.Errorf("FR8/FR22.5: expected retired_principal to record the acting principal, got %+v", plant.RetiredPrincipal)
	}

	entries, err := repo.GetPlantPlacementTimeline(ctx, plantID)
	if err != nil {
		t.Fatalf("GetPlantPlacementTimeline: %v", err)
	}
	if len(entries) != 1 || entries[0].ValidTo == nil {
		t.Fatalf("expected retirement to close the open placement interval, got %+v", entries)
	}
}

// TestRetirePlant_RefusesAlreadyRetired verifies retirement is not idempotent
// silent-no-op — a second retirement attempt is refused.
func TestRetirePlant_RefusesAlreadyRetired(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: plantRegionTestSchema})
	repo := NewRepository(db.Pool)

	plantID, _, _ := setupPlantForRegionTest(ctx, t, db)

	if _, err := repo.RetirePlant(ctx, plantID, "retire_plant", "principal-1"); err != nil {
		t.Fatalf("first retire: %v", err)
	}
	if _, err := repo.RetirePlant(ctx, plantID, "retire_plant", "principal-1"); err == nil {
		t.Fatal("expected retiring an already-retired plant to be refused")
	}
}

// TestRetirePlant_NothingHardDeleted verifies FR22.1: a move followed by a
// retirement never shrinks plant or plant_region_history — everything is
// append-only or soft state, and the plant remains readable by explicit id
// (FR22.3).
func TestRetirePlant_NothingHardDeleted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: plantRegionTestSchema})
	repo := NewRepository(db.Pool)

	plantID, _, region2ID := setupPlantForRegionTest(ctx, t, db)

	plantsBefore := countRows(ctx, t, db, "plant")
	historyBefore := countRows(ctx, t, db, "plant_region_history")

	if err := repo.MovePlantRegion(ctx, plantID, region2ID, false); err != nil {
		t.Fatalf("MovePlantRegion: %v", err)
	}
	if _, err := repo.RetirePlant(ctx, plantID, "retire_plant", "principal-1"); err != nil {
		t.Fatalf("RetirePlant: %v", err)
	}

	if got := countRows(ctx, t, db, "plant"); got != plantsBefore {
		t.Errorf("FR22.1: plant row count changed from %d to %d; nothing should be hard-deleted", plantsBefore, got)
	}
	if got := countRows(ctx, t, db, "plant_region_history"); got <= historyBefore {
		t.Errorf("FR22.1/FR19: expected plant_region_history to grow (append-only), got %d (was %d)", got, historyBefore)
	}

	// The plant remains readable by explicit id (FR22.3).
	plant, err := repo.GetPlant(ctx, plantID)
	if err != nil {
		t.Fatalf("GetPlant after retirement: %v", err)
	}
	if plant.RemovedAt == nil {
		t.Error("expected plant to be retired")
	}
}

// TestMovePlantRegion_ClosesCurrentAndOpensNew_AppendOnly verifies FR19/FR54:
// a move closes the current interval and opens a new one — nothing is
// updated in place — and syncs the plant.region_id current-value cache.
func TestMovePlantRegion_ClosesCurrentAndOpensNew_AppendOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: plantRegionTestSchema})
	repo := NewRepository(db.Pool)

	plantID, region1ID, region2ID := setupPlantForRegionTest(ctx, t, db)

	if err := repo.MovePlantRegion(ctx, plantID, region2ID, false); err != nil {
		t.Fatalf("MovePlantRegion: %v", err)
	}

	entries, err := repo.GetPlantPlacementTimeline(ctx, plantID)
	if err != nil {
		t.Fatalf("GetPlantPlacementTimeline: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected two intervals after one move (nothing updated in place), got %d", len(entries))
	}
	if entries[0].RegionID != region1ID || entries[0].ValidTo == nil {
		t.Errorf("expected the first interval (region %d) to be closed, got %+v", region1ID, entries[0])
	}
	if entries[1].RegionID != region2ID || entries[1].ValidTo != nil {
		t.Errorf("expected the second interval (region %d) to be open, got %+v", region2ID, entries[1])
	}

	var cachedRegionID int64
	if err := db.Pool.QueryRow(ctx, `SELECT region_id FROM plant WHERE plant_id = $1`, plantID).Scan(&cachedRegionID); err != nil {
		t.Fatalf("read plant.region_id cache: %v", err)
	}
	if cachedRegionID != region2ID {
		t.Errorf("expected plant.region_id cache synced to %d, got %d", region2ID, cachedRegionID)
	}
}

// TestGetPlantPlacementTimeline_DistinguishesRelocationInducedFromPlantMove
// verifies FR24/FR74/#1223: a placement change caused by a subtree
// relocation is marked relocation_induced, distinguishing "this plant moved"
// from "the region this plant was in moved" in the timeline.
func TestGetPlantPlacementTimeline_DistinguishesRelocationInducedFromPlantMove(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: plantRegionTestSchema})
	repo := NewRepository(db.Pool)

	plantID, _, region2ID := setupPlantForRegionTest(ctx, t, db)

	// Simulate a plant-initiated move (FR24: relocation_induced = false),
	// mirroring the MovePlant RPC.
	if err := repo.MovePlantRegion(ctx, plantID, region2ID, false); err != nil {
		t.Fatalf("plant-initiated move: %v", err)
	}

	var householdID int64
	if err := db.Pool.QueryRow(ctx, `SELECT household_id FROM plant WHERE plant_id = $1`, plantID).Scan(&householdID); err != nil {
		t.Fatalf("get household: %v", err)
	}
	var region3ID int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO region (name, household_id) VALUES ('region3', $1) RETURNING region_id
	`, householdID).Scan(&region3ID); err != nil {
		t.Fatalf("insert region3: %v", err)
	}

	// Simulate a subtree relocation trigger (#1223: relocation_induced = true).
	if err := repo.MovePlantRegion(ctx, plantID, region3ID, true); err != nil {
		t.Fatalf("relocation-induced move: %v", err)
	}

	entries, err := repo.GetPlantPlacementTimeline(ctx, plantID)
	if err != nil {
		t.Fatalf("GetPlantPlacementTimeline: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected three intervals, got %d", len(entries))
	}
	if entries[1].RelocationInduced {
		t.Error("FR24: the plant-initiated move must not be marked relocation_induced")
	}
	if !entries[2].RelocationInduced {
		t.Error("FR24/FR74: the subtree-relocation-driven move must be marked relocation_induced")
	}
}
