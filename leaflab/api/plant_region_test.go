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
	household_id BIGINT NOT NULL REFERENCES household(household_id)
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
