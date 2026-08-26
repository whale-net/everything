//go:build integration

package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/whale-net/everything/libs/go/dbtest"
)

const plantRegionTestSchema = `
CREATE TABLE household (
	household_id BIGSERIAL PRIMARY KEY,
	name TEXT NOT NULL UNIQUE
);

CREATE TABLE region (
	region_id BIGSERIAL PRIMARY KEY,
	name VARCHAR(128) NOT NULL,
	household_id BIGINT NOT NULL REFERENCES household(household_id),
	parent_region_id BIGINT REFERENCES region(region_id)
);

CREATE TABLE plant_type (
	plant_type_id BIGSERIAL PRIMARY KEY,
	common_name VARCHAR(128) NOT NULL,
	species VARCHAR(128)
);

CREATE TABLE plant (
	plant_id BIGSERIAL PRIMARY KEY,
	name VARCHAR(128) NOT NULL,
	region_id BIGINT NOT NULL REFERENCES region(region_id),
	plant_type_id BIGINT NOT NULL REFERENCES plant_type(plant_type_id),
	household_id BIGINT NOT NULL REFERENCES household(household_id),
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE plant_region_history (
	plant_id BIGINT NOT NULL REFERENCES plant(plant_id),
	region_id BIGINT NOT NULL REFERENCES region(region_id),
	valid_from TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	valid_to TIMESTAMPTZ
);

CREATE INDEX ON plant_region_history(plant_id) WHERE valid_to IS NULL;
`

// setupPlantForRegionTest creates a household, two regions, a plant type, a
// plant, and its initial plant_region_history placement in region1.
// Returns the plant's id and both region ids.
func setupPlantForRegionTest(ctx context.Context, t *testing.T, db *dbtest.Postgres) (plantID, region1ID, region2ID int64) {
	t.Helper()

	var householdID int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO household (name) VALUES ('test-household')
		RETURNING household_id
	`).Scan(&householdID); err != nil {
		t.Fatalf("insert household: %v", err)
	}

	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO region (name, household_id) VALUES ('region1', $1)
		RETURNING region_id
	`, householdID).Scan(&region1ID); err != nil {
		t.Fatalf("insert region1: %v", err)
	}

	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO region (name, household_id) VALUES ('region2', $1)
		RETURNING region_id
	`, householdID).Scan(&region2ID); err != nil {
		t.Fatalf("insert region2: %v", err)
	}

	var plantTypeID int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO plant_type (common_name) VALUES ('Test Species')
		RETURNING plant_type_id
	`).Scan(&plantTypeID); err != nil {
		t.Fatalf("insert plant_type: %v", err)
	}

	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO plant (name, plant_type_id, region_id, household_id)
		VALUES ('Test Plant', $1, $2, $3)
		RETURNING plant_id
	`, plantTypeID, region1ID, householdID).Scan(&plantID); err != nil {
		t.Fatalf("insert plant: %v", err)
	}

	// Establish the initial placement directly — this is test fixture setup,
	// not the write path under test (MovePlantRegion/MovePlantRegionAt are
	// for subsequent moves, not first placement).
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO plant_region_history (plant_id, region_id, valid_from)
		VALUES ($1, $2, NOW())
	`, plantID, region1ID); err != nil {
		t.Fatalf("insert initial placement: %v", err)
	}

	return plantID, region1ID, region2ID
}

// TestMovePlantRegionAt_BackdatingRefused verifies FR19/FR21: a caller cannot
// move a plant's placement to a valid_from earlier than the current time.
// The guard lives in application code (MovePlantRegionAt), not a database
// constraint — placement boundaries open at the instant the change is
// recorded, and the repository refuses any earlier request before writing.
func TestMovePlantRegionAt_BackdatingRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: plantRegionTestSchema})
	repo := NewRepository(db.Pool)

	plantID, region1ID, region2ID := setupPlantForRegionTest(ctx, t, db)

	pastTime := time.Now().Add(-1 * time.Hour)
	err := repo.MovePlantRegionAt(ctx, plantID, region2ID, pastTime)

	var refusal *BackdatingRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("FR19/FR21: expected *BackdatingRefusal, got %v", err)
	}
	if refusal.Reason == "" {
		t.Error("FR19/FR21: BackdatingRefusal must state a reason")
	}

	// Verify no placement change occurred.
	currentRegionID, err := repo.GetPlantCurrentRegion(ctx, plantID)
	if err != nil {
		t.Fatalf("get current region: %v", err)
	}
	if currentRegionID != region1ID {
		t.Errorf("expected plant to still be in region %d (no change), got %d", region1ID, currentRegionID)
	}
}

// TestMovePlantRegionAt_FutureTimeAccepted verifies that a valid_from at or
// after the current time is accepted (the guard only rejects the past).
func TestMovePlantRegionAt_FutureTimeAccepted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: plantRegionTestSchema})
	repo := NewRepository(db.Pool)

	plantID, _, region2ID := setupPlantForRegionTest(ctx, t, db)

	futureTime := time.Now().Add(1 * time.Hour)
	if err := repo.MovePlantRegionAt(ctx, plantID, region2ID, futureTime); err != nil {
		t.Fatalf("MovePlantRegionAt with future time: %v", err)
	}

	currentRegionID, err := repo.GetPlantCurrentRegion(ctx, plantID)
	if err != nil {
		t.Fatalf("get current region: %v", err)
	}
	if currentRegionID != region2ID {
		t.Errorf("expected plant moved to region %d, got %d", region2ID, currentRegionID)
	}
}

// TestRemovePlantPlacement_BackdatingRefused verifies FR19/FR21 for removal:
// a caller cannot close a plant's placement interval with a removal time
// earlier than the current time.
func TestRemovePlantPlacement_BackdatingRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: plantRegionTestSchema})
	repo := NewRepository(db.Pool)

	plantID, region1ID, _ := setupPlantForRegionTest(ctx, t, db)

	pastTime := time.Now().Add(-1 * time.Hour)
	err := repo.RemovePlantPlacement(ctx, plantID, pastTime)

	var refusal *BackdatingRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("FR19/FR21: expected *BackdatingRefusal, got %v", err)
	}

	// Verify the placement is still open (not closed by the refused attempt).
	currentRegionID, err := repo.GetPlantCurrentRegion(ctx, plantID)
	if err != nil {
		t.Fatalf("get current region: %v", err)
	}
	if currentRegionID != region1ID {
		t.Errorf("expected plant placement unchanged in region %d, got %d", region1ID, currentRegionID)
	}
}

// setupTreeForAttributionTest creates a region tree with parent-child relationships:
// household > root_region > shelf_region > pot_region
// Returns region IDs (household, root, shelf, pot) and plant type ID.
func setupTreeForAttributionTest(ctx context.Context, t *testing.T, db *dbtest.Postgres) (int64, int64, int64, int64, int64) {
	t.Helper()

	var householdID int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO household (name) VALUES ('attribution-test-household')
		RETURNING household_id
	`).Scan(&householdID); err != nil {
		t.Fatalf("insert household: %v", err)
	}

	var rootID int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO region (name, household_id, parent_region_id)
		VALUES ('root', $1, NULL)
		RETURNING region_id
	`, householdID).Scan(&rootID); err != nil {
		t.Fatalf("insert root region: %v", err)
	}

	var shelfID int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO region (name, household_id, parent_region_id)
		VALUES ('shelf', $1, $2)
		RETURNING region_id
	`, householdID, rootID).Scan(&shelfID); err != nil {
		t.Fatalf("insert shelf region: %v", err)
	}

	var potID int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO region (name, household_id, parent_region_id)
		VALUES ('pot', $1, $2)
		RETURNING region_id
	`, householdID, shelfID).Scan(&potID); err != nil {
		t.Fatalf("insert pot region: %v", err)
	}

	var plantTypeID int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO plant_type (common_name) VALUES ('Test Type')
		RETURNING plant_type_id
	`).Scan(&plantTypeID); err != nil {
		t.Fatalf("insert plant_type: %v", err)
	}

	return householdID, rootID, shelfID, potID, plantTypeID
}

// TestResolveReadingAttribution_LocalPlantInOwnRegion verifies that a reading
// in a region with an active plant attributes to that plant, not to ancestors.
func TestResolveReadingAttribution_LocalPlantInOwnRegion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: plantRegionTestSchema})
	repo := NewRepository(db.Pool)

	householdID, _, _, potID, plantTypeID := setupTreeForAttributionTest(ctx, t, db)

	// Create a plant in the pot region
	var plantID int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO plant (name, plant_type_id, region_id, household_id)
		VALUES ('Fern', $1, $2, $3)
		RETURNING plant_id
	`, plantTypeID, potID, householdID).Scan(&plantID); err != nil {
		t.Fatalf("insert plant: %v", err)
	}

	// Place the plant in the pot region now
	readTime := time.Now()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO plant_region_history (plant_id, region_id, valid_from)
		VALUES ($1, $2, $3)
	`, plantID, potID, readTime.Add(-1*time.Hour)); err != nil {
		t.Fatalf("insert placement: %v", err)
	}

	// Resolve the reading
	result, err := repo.ResolveReadingAttribution(ctx, potID, readTime)
	if err != nil {
		t.Fatalf("ResolveReadingAttribution: %v", err)
	}

	if result.AttributedRegionID != potID {
		t.Errorf("expected attribution to pot region %d, got %d", potID, result.AttributedRegionID)
	}

	if len(result.ActivePlants) != 1 {
		t.Errorf("expected 1 active plant, got %d", len(result.ActivePlants))
		return
	}

	if result.ActivePlants[0].PlantID != plantID {
		t.Errorf("expected plant %d, got %d", plantID, result.ActivePlants[0].PlantID)
	}
	if result.ActivePlants[0].PlantName != "Fern" {
		t.Errorf("expected plant name 'Fern', got '%s'", result.ActivePlants[0].PlantName)
	}
}

// TestResolveReadingAttribution_NearestAncestorWithPlant verifies that a reading
// in a region with no active plant attributes to the nearest ancestor with one.
func TestResolveReadingAttribution_NearestAncestorWithPlant(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: plantRegionTestSchema})
	repo := NewRepository(db.Pool)

	householdID, _, shelfID, potID, plantTypeID := setupTreeForAttributionTest(ctx, t, db)

	// Create a plant in the shelf region (parent of pot)
	var plantID int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO plant (name, plant_type_id, region_id, household_id)
		VALUES ('Succulent', $1, $2, $3)
		RETURNING plant_id
	`, plantTypeID, shelfID, householdID).Scan(&plantID); err != nil {
		t.Fatalf("insert plant: %v", err)
	}

	// Place the plant in the shelf region
	readTime := time.Now()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO plant_region_history (plant_id, region_id, valid_from)
		VALUES ($1, $2, $3)
	`, plantID, shelfID, readTime.Add(-1*time.Hour)); err != nil {
		t.Fatalf("insert placement: %v", err)
	}

	// Resolve the reading at the pot level (which has no plants)
	result, err := repo.ResolveReadingAttribution(ctx, potID, readTime)
	if err != nil {
		t.Fatalf("ResolveReadingAttribution: %v", err)
	}

	if result.AttributedRegionID != shelfID {
		t.Errorf("expected attribution to nearest ancestor shelf %d, got %d", shelfID, result.AttributedRegionID)
	}

	if len(result.ActivePlants) != 1 {
		t.Errorf("expected 1 active plant, got %d", len(result.ActivePlants))
		return
	}

	if result.ActivePlants[0].PlantID != plantID {
		t.Errorf("expected plant %d, got %d", plantID, result.ActivePlants[0].PlantID)
	}
}

// TestResolveReadingAttribution_SiblingsInSameRegion verifies that when a reading
// attributes to a region with multiple active plants, all are returned (sibling disclosure).
func TestResolveReadingAttribution_SiblingsInSameRegion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: plantRegionTestSchema})
	repo := NewRepository(db.Pool)

	householdID, _, _, potID, plantTypeID := setupTreeForAttributionTest(ctx, t, db)

	readTime := time.Now()

	// Create two plants in the same pot region
	var plant1ID, plant2ID int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO plant (name, plant_type_id, region_id, household_id)
		VALUES ('Fern', $1, $2, $3)
		RETURNING plant_id
	`, plantTypeID, potID, householdID).Scan(&plant1ID); err != nil {
		t.Fatalf("insert plant 1: %v", err)
	}

	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO plant (name, plant_type_id, region_id, household_id)
		VALUES ('Succulent', $1, $2, $3)
		RETURNING plant_id
	`, plantTypeID, potID, householdID).Scan(&plant2ID); err != nil {
		t.Fatalf("insert plant 2: %v", err)
	}

	// Place both plants in the pot region
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO plant_region_history (plant_id, region_id, valid_from)
		VALUES ($1, $2, $3), ($4, $5, $6)
	`, plant1ID, potID, readTime.Add(-1*time.Hour), plant2ID, potID, readTime.Add(-1*time.Hour)); err != nil {
		t.Fatalf("insert placements: %v", err)
	}

	// Resolve the reading
	result, err := repo.ResolveReadingAttribution(ctx, potID, readTime)
	if err != nil {
		t.Fatalf("ResolveReadingAttribution: %v", err)
	}

	if result.AttributedRegionID != potID {
		t.Errorf("expected attribution to pot region %d, got %d", potID, result.AttributedRegionID)
	}

	if len(result.ActivePlants) != 2 {
		t.Errorf("expected 2 active plants, got %d", len(result.ActivePlants))
		return
	}

	// Verify both plants are returned (order is by plant_id)
	foundPlant1 := false
	foundPlant2 := false
	for _, p := range result.ActivePlants {
		if p.PlantID == plant1ID && p.PlantName == "Fern" {
			foundPlant1 = true
		}
		if p.PlantID == plant2ID && p.PlantName == "Succulent" {
			foundPlant2 = true
		}
	}
	if !foundPlant1 || !foundPlant2 {
		t.Errorf("expected both plants in siblings; found plant1=%v, plant2=%v", foundPlant1, foundPlant2)
	}
}

// TestResolveReadingAttribution_PlantActiveAtReadingTimeButMovedSince verifies
// that a plant active at reading time receives the reading, even if it has moved since.
func TestResolveReadingAttribution_PlantActiveAtReadingTimeButMovedSince(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: plantRegionTestSchema})
	repo := NewRepository(db.Pool)

	householdID, _, shelfID, potID, plantTypeID := setupTreeForAttributionTest(ctx, t, db)

	// Create a plant
	var plantID int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO plant (name, plant_type_id, region_id, household_id)
		VALUES ('Fern', $1, $2, $3)
		RETURNING plant_id
	`, plantTypeID, potID, householdID).Scan(&plantID); err != nil {
		t.Fatalf("insert plant: %v", err)
	}

	readTime := time.Now()
	futureTime := readTime.Add(1 * time.Hour)

	// Place the plant in the pot region at readTime
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO plant_region_history (plant_id, region_id, valid_from, valid_to)
		VALUES ($1, $2, $3, $4)
	`, plantID, potID, readTime.Add(-1*time.Hour), futureTime); err != nil {
		t.Fatalf("insert first placement: %v", err)
	}

	// Move the plant to the shelf region at futureTime
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO plant_region_history (plant_id, region_id, valid_from)
		VALUES ($1, $2, $3)
	`, plantID, shelfID, futureTime); err != nil {
		t.Fatalf("insert second placement: %v", err)
	}

	// Resolve the reading at readTime (before the move)
	result, err := repo.ResolveReadingAttribution(ctx, potID, readTime)
	if err != nil {
		t.Fatalf("ResolveReadingAttribution: %v", err)
	}

	if result.AttributedRegionID != potID {
		t.Errorf("expected attribution to pot region %d, got %d", potID, result.AttributedRegionID)
	}

	if len(result.ActivePlants) != 1 {
		t.Errorf("expected 1 active plant, got %d", len(result.ActivePlants))
		return
	}

	if result.ActivePlants[0].PlantID != plantID {
		t.Errorf("expected plant %d, got %d", plantID, result.ActivePlants[0].PlantID)
	}
}

// TestResolveReadingAttribution_PlantMovedInLaterNotReceived verifies that a plant
// that moves into a region after reading time does not receive the reading.
func TestResolveReadingAttribution_PlantMovedInLaterNotReceived(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: plantRegionTestSchema})
	repo := NewRepository(db.Pool)

	householdID, _, shelfID, potID, plantTypeID := setupTreeForAttributionTest(ctx, t, db)

	// Create a plant
	var plantID int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO plant (name, plant_type_id, region_id, household_id)
		VALUES ('Fern', $1, $2, $3)
		RETURNING plant_id
	`, plantTypeID, shelfID, householdID).Scan(&plantID); err != nil {
		t.Fatalf("insert plant: %v", err)
	}

	readTime := time.Now()
	futureTime := readTime.Add(1 * time.Hour)

	// Place the plant in the shelf region AFTER readTime
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO plant_region_history (plant_id, region_id, valid_from)
		VALUES ($1, $2, $3)
	`, plantID, shelfID, futureTime); err != nil {
		t.Fatalf("insert placement: %v", err)
	}

	// Resolve the reading at readTime (when the plant was not yet in the tree)
	result, err := repo.ResolveReadingAttribution(ctx, potID, readTime)
	if err == nil {
		t.Fatalf("expected error (no active plants at reading time), got result: %v", result)
	}
}

// TestResolveReadingAttribution_PlantNotAtLeafDepth verifies that a plant placed
// at a non-leaf region (e.g., shelf) correctly attributes readings from its descendants.
func TestResolveReadingAttribution_PlantNotAtLeafDepth(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: plantRegionTestSchema})
	repo := NewRepository(db.Pool)

	householdID, _, shelfID, potID, plantTypeID := setupTreeForAttributionTest(ctx, t, db)

	// Create a plant in the shelf region (not at leaf depth)
	var plantID int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO plant (name, plant_type_id, region_id, household_id)
		VALUES ('Tall Plant', $1, $2, $3)
		RETURNING plant_id
	`, plantTypeID, shelfID, householdID).Scan(&plantID); err != nil {
		t.Fatalf("insert plant: %v", err)
	}

	readTime := time.Now()

	// Place the plant in the shelf region
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO plant_region_history (plant_id, region_id, valid_from)
		VALUES ($1, $2, $3)
	`, plantID, shelfID, readTime.Add(-1*time.Hour)); err != nil {
		t.Fatalf("insert placement: %v", err)
	}

	// Resolve the reading at the pot level (child of shelf)
	result, err := repo.ResolveReadingAttribution(ctx, potID, readTime)
	if err != nil {
		t.Fatalf("ResolveReadingAttribution: %v", err)
	}

	if result.AttributedRegionID != shelfID {
		t.Errorf("expected attribution to shelf region %d, got %d", shelfID, result.AttributedRegionID)
	}

	if len(result.ActivePlants) != 1 {
		t.Errorf("expected 1 active plant, got %d", len(result.ActivePlants))
		return
	}

	if result.ActivePlants[0].PlantID != plantID {
		t.Errorf("expected plant %d, got %d", plantID, result.ActivePlants[0].PlantID)
	}
}

// TestGetActivePlantsInRegionAtTime verifies that GetActivePlantsInRegionAtTime
// correctly returns only plants active at the specified time.
func TestGetActivePlantsInRegionAtTime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: plantRegionTestSchema})
	repo := NewRepository(db.Pool)

	householdID, _, _, potID, plantTypeID := setupTreeForAttributionTest(ctx, t, db)

	readTime := time.Now()
	futureTime := readTime.Add(1 * time.Hour)

	// Create two plants
	var plant1ID, plant2ID int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO plant (name, plant_type_id, region_id, household_id)
		VALUES ('Plant1', $1, $2, $3)
		RETURNING plant_id
	`, plantTypeID, potID, householdID).Scan(&plant1ID); err != nil {
		t.Fatalf("insert plant 1: %v", err)
	}

	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO plant (name, plant_type_id, region_id, household_id)
		VALUES ('Plant2', $1, $2, $3)
		RETURNING plant_id
	`, plantTypeID, potID, householdID).Scan(&plant2ID); err != nil {
		t.Fatalf("insert plant 2: %v", err)
	}

	// Place plant 1 active from past through future
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO plant_region_history (plant_id, region_id, valid_from)
		VALUES ($1, $2, $3)
	`, plant1ID, potID, readTime.Add(-1*time.Hour)); err != nil {
		t.Fatalf("insert plant 1 placement: %v", err)
	}

	// Place plant 2 active only in the future (after readTime)
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO plant_region_history (plant_id, region_id, valid_from)
		VALUES ($1, $2, $3)
	`, plant2ID, potID, futureTime); err != nil {
		t.Fatalf("insert plant 2 placement: %v", err)
	}

	// Get active plants at readTime
	result, err := repo.GetActivePlantsInRegionAtTime(ctx, potID, readTime)
	if err != nil {
		t.Fatalf("GetActivePlantsInRegionAtTime: %v", err)
	}

	if len(result) != 1 {
		t.Errorf("expected 1 active plant at readTime, got %d", len(result))
		return
	}

	if result[0].PlantID != plant1ID {
		t.Errorf("expected plant1 %d, got %d", plant1ID, result[0].PlantID)
	}

	// Get active plants at futureTime
	result, err = repo.GetActivePlantsInRegionAtTime(ctx, potID, futureTime)
	if err != nil {
		t.Fatalf("GetActivePlantsInRegionAtTime: %v", err)
	}

	if len(result) != 2 {
		t.Errorf("expected 2 active plants at futureTime, got %d", len(result))
		return
	}
}

// TestResolveReadingAttribution_DoesNotIncludeFurtherAncestors verifies that
// when a reading attributes to a region with an active plant, further ancestor
// plants do not receive the reading. This guards against FR23's rule that
// attribution is to the nearest ancestor only, not every ancestor level.
func TestResolveReadingAttribution_DoesNotIncludeFurtherAncestors(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: plantRegionTestSchema})
	repo := NewRepository(db.Pool)

	householdID, rootID, shelfID, potID, plantTypeID := setupTreeForAttributionTest(ctx, t, db)

	readTime := time.Now()

	// Create a plant in the root region (furthest ancestor)
	var rootPlantID int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO plant (name, plant_type_id, region_id, household_id)
		VALUES ('Root Plant', $1, $2, $3)
		RETURNING plant_id
	`, plantTypeID, rootID, householdID).Scan(&rootPlantID); err != nil {
		t.Fatalf("insert root plant: %v", err)
	}

	// Place the root plant in the root region
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO plant_region_history (plant_id, region_id, valid_from)
		VALUES ($1, $2, $3)
	`, rootPlantID, rootID, readTime.Add(-1*time.Hour)); err != nil {
		t.Fatalf("insert root plant placement: %v", err)
	}

	// Create a plant in the shelf region (nearer ancestor)
	var shelfPlantID int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO plant (name, plant_type_id, region_id, household_id)
		VALUES ('Shelf Plant', $1, $2, $3)
		RETURNING plant_id
	`, plantTypeID, shelfID, householdID).Scan(&shelfPlantID); err != nil {
		t.Fatalf("insert shelf plant: %v", err)
	}

	// Place the shelf plant in the shelf region
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO plant_region_history (plant_id, region_id, valid_from)
		VALUES ($1, $2, $3)
	`, shelfPlantID, shelfID, readTime.Add(-1*time.Hour)); err != nil {
		t.Fatalf("insert shelf plant placement: %v", err)
	}

	// Resolve the reading at the pot level (child of shelf)
	result, err := repo.ResolveReadingAttribution(ctx, potID, readTime)
	if err != nil {
		t.Fatalf("ResolveReadingAttribution: %v", err)
	}

	// Should attribute to shelf region, not root
	if result.AttributedRegionID != shelfID {
		t.Errorf("expected attribution to nearest ancestor shelf %d, got %d", shelfID, result.AttributedRegionID)
	}

	// Should include only the shelf plant, not the root plant
	if len(result.ActivePlants) != 1 {
		t.Errorf("expected 1 active plant (shelf only), got %d", len(result.ActivePlants))
		return
	}

	if result.ActivePlants[0].PlantID != shelfPlantID {
		t.Errorf("expected shelf plant %d, got %d", shelfPlantID, result.ActivePlants[0].PlantID)
	}
}

// TestResolveReadingAttribution_AllSiblingsReturned verifies that when multiple
// plants are in the same region, the result includes all of them (complete
// sibling disclosure). This guards against accidentally losing siblings in
// the response due to aggregation bugs or DISTINCT/LIMIT issues.
func TestResolveReadingAttribution_AllSiblingsReturned(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: plantRegionTestSchema})
	repo := NewRepository(db.Pool)

	householdID, _, _, potID, plantTypeID := setupTreeForAttributionTest(ctx, t, db)

	readTime := time.Now()

	// Create three plants in the same pot region
	var plant1ID, plant2ID, plant3ID int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO plant (name, plant_type_id, region_id, household_id)
		VALUES ('Plant One', $1, $2, $3)
		RETURNING plant_id
	`, plantTypeID, potID, householdID).Scan(&plant1ID); err != nil {
		t.Fatalf("insert plant 1: %v", err)
	}

	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO plant (name, plant_type_id, region_id, household_id)
		VALUES ('Plant Two', $1, $2, $3)
		RETURNING plant_id
	`, plantTypeID, potID, householdID).Scan(&plant2ID); err != nil {
		t.Fatalf("insert plant 2: %v", err)
	}

	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO plant (name, plant_type_id, region_id, household_id)
		VALUES ('Plant Three', $1, $2, $3)
		RETURNING plant_id
	`, plantTypeID, potID, householdID).Scan(&plant3ID); err != nil {
		t.Fatalf("insert plant 3: %v", err)
	}

	// Place all three plants in the pot region
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO plant_region_history (plant_id, region_id, valid_from)
		VALUES ($1, $2, $3), ($4, $5, $6), ($7, $8, $9)
	`, plant1ID, potID, readTime.Add(-1*time.Hour), 
		plant2ID, potID, readTime.Add(-1*time.Hour),
		plant3ID, potID, readTime.Add(-1*time.Hour)); err != nil {
		t.Fatalf("insert placements: %v", err)
	}

	// Resolve the reading
	result, err := repo.ResolveReadingAttribution(ctx, potID, readTime)
	if err != nil {
		t.Fatalf("ResolveReadingAttribution: %v", err)
	}

	if result.AttributedRegionID != potID {
		t.Errorf("expected attribution to pot region %d, got %d", potID, result.AttributedRegionID)
	}

	// Verify all three plants are returned (complete sibling disclosure)
	if len(result.ActivePlants) != 3 {
		t.Errorf("expected 3 active plants, got %d", len(result.ActivePlants))
		return
	}

	// Verify each plant is in the result
	foundPlants := make(map[int64]bool)
	for _, p := range result.ActivePlants {
		foundPlants[p.PlantID] = true
	}

	if !foundPlants[plant1ID] {
		t.Errorf("plant 1 missing from siblings")
	}
	if !foundPlants[plant2ID] {
		t.Errorf("plant 2 missing from siblings")
	}
	if !foundPlants[plant3ID] {
		t.Errorf("plant 3 missing from siblings")
	}
}
