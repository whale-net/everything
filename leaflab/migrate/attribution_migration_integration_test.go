//go:build integration

// Real-Postgres integration coverage for FR23's nearest-ancestor plant
// attribution rule (A2, A11) -- leaflab/api/attribution.Resolver.ResolvePlants
// and its SQL twin, attribute_region_plants (migration 019). Migrates a real
// database up through every migration (001..019, needing TimescaleDB for
// 001's create_hypertable call, per timescaleImage below) and exercises both
// implementations against one shared region/plant fixture, asserting they
// agree on every case (NFR1.c's kernel) as well as each specific rule from
// the issue's Testing section. See
// plant_region_history_migration_integration_test.go for the sibling
// pattern this file follows, and //libs/go/dbtest's README for how to run
// tests like this one.
package main_test

import (
	"context"
	"database/sql"
	"embed"
	"sort"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/whale-net/everything/leaflab/api/attribution"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

//go:embed migrations/*.sql
var attributionMigrations embed.FS

// attributionTimescaleImage matches timescaleImage in the sibling
// integration test files -- migration 001's create_hypertable call needs
// the real TimescaleDB image, not plain postgres.
const attributionTimescaleImage = "timescale/timescaledb:latest-pg16"

// attributionFixture is the shared region/plant tree both implementations
// are asserted against. Shape (parent -> children):
//
//	root1 (plant: plantRoot1)
//	├─ mid1 (no plant)
//	│   └─ branchB (no plant)
//	│       └─ leafB1 (plant: plantLeafB1 -- a descendant-only plant that
//	│                   must never attribute an ancestor's reading)
//	└─ branchA (plant: plantBranchA)
//	    ├─ leafA1 (plants: plantLeafA1a, plantLeafA1b -- siblings)
//	    └─ leafA2 (no plant)
//
//	root2 (no plant)
//	└─ orphan2 (no plant)      -- isolated tree, nothing attributable anywhere
//
// Plus a movingPlant fixture in its own two unrelated top-level regions
// (regionOld, regionNew), exercising "at reading time" independent of the
// tree above.
type attributionFixture struct {
	root1, mid1, branchB, leafB1 int64
	branchA, leafA1, leafA2      int64
	root2, orphan2               int64
	regionOld, regionNew         int64

	plantRoot1                             int64
	plantBranchA                           int64
	plantLeafA1a, plantLeafA1b             int64
	plantLeafB1                            int64
	movingPlant                            int64

	// moveAt is the instant movingPlant's interval closes in regionOld and
	// opens in regionNew -- plant_region_history.Writer-style close-and-open,
	// written directly since this fixture only needs the end state, not
	// leaflab/api/placement's refusal behavior.
	beforeMove, afterMove time.Time
}

// newAttributionDB migrates a fresh database all the way up (through 019)
// and seeds attributionFixture's region tree and plants.
func newAttributionDB(t *testing.T) (*dbtest.Postgres, attributionFixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Image: attributionTimescaleImage})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := sqlDB.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}

	runner := migrate.NewRunner(sqlDB, attributionMigrations, "migrations")
	if err := runner.Up(); err != nil {
		t.Fatalf("migrate up (through 019): %v", err)
	}

	var unadoptedHouseholdID int64
	if err := db.Pool.QueryRow(ctx, `SELECT household_id FROM household WHERE is_unadopted = TRUE`).Scan(&unadoptedHouseholdID); err != nil {
		t.Fatalf("read Unadopted household_id: %v", err)
	}

	var plantTypeID int64
	if err := db.Pool.QueryRow(ctx, `INSERT INTO plant_type (common_name) VALUES ($1) RETURNING plant_type_id`, "test-plant-type").Scan(&plantTypeID); err != nil {
		t.Fatalf("insert plant_type: %v", err)
	}

	insertRegion := func(name string, parentID *int64) int64 {
		t.Helper()
		var id int64
		if err := db.Pool.QueryRow(ctx, `
			INSERT INTO region (name, parent_region_id) VALUES ($1, $2) RETURNING region_id
		`, name, parentID).Scan(&id); err != nil {
			t.Fatalf("insert region %s: %v", name, err)
		}
		return id
	}
	regionPtr := func(id int64) *int64 { return &id }

	insertPlant := func(regionID int64, name string) int64 {
		t.Helper()
		var id int64
		if err := db.Pool.QueryRow(ctx, `
			INSERT INTO plant (region_id, plant_type_id, name, household_id) VALUES ($1, $2, $3, $4) RETURNING plant_id
		`, regionID, plantTypeID, name, unadoptedHouseholdID).Scan(&id); err != nil {
			t.Fatalf("insert plant %s: %v", name, err)
		}
		return id
	}

	// openInterval opens an always-open plant_region_history interval
	// (valid_from defaults to NOW(), valid_to NULL) for plantID in regionID.
	openInterval := func(plantID, regionID int64) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, `
			INSERT INTO plant_region_history (plant_id, region_id) VALUES ($1, $2)
		`, plantID, regionID); err != nil {
			t.Fatalf("open interval for plant %d in region %d: %v", plantID, regionID, err)
		}
	}

	f := attributionFixture{}

	// ── Tree 1 ──────────────────────────────────────────────────────────
	f.root1 = insertRegion("root1", nil)
	f.mid1 = insertRegion("mid1", regionPtr(f.root1))
	f.branchB = insertRegion("branchB", regionPtr(f.mid1))
	f.leafB1 = insertRegion("leafB1", regionPtr(f.branchB))
	f.branchA = insertRegion("branchA", regionPtr(f.root1))
	f.leafA1 = insertRegion("leafA1", regionPtr(f.branchA))
	f.leafA2 = insertRegion("leafA2", regionPtr(f.branchA))

	f.plantRoot1 = insertPlant(f.root1, "plant-root1")
	openInterval(f.plantRoot1, f.root1)

	f.plantBranchA = insertPlant(f.branchA, "plant-branchA")
	openInterval(f.plantBranchA, f.branchA)

	f.plantLeafA1a = insertPlant(f.leafA1, "plant-leafA1a")
	openInterval(f.plantLeafA1a, f.leafA1)
	f.plantLeafA1b = insertPlant(f.leafA1, "plant-leafA1b")
	openInterval(f.plantLeafA1b, f.leafA1)

	f.plantLeafB1 = insertPlant(f.leafB1, "plant-leafB1")
	openInterval(f.plantLeafB1, f.leafB1)

	// ── Tree 2 (isolated, nothing attributable) ────────────────────────
	f.root2 = insertRegion("root2", nil)
	f.orphan2 = insertRegion("orphan2", regionPtr(f.root2))

	// ── Moving plant, "at reading time" fixture ─────────────────────────
	f.regionOld = insertRegion("region-old", nil)
	f.regionNew = insertRegion("region-new", nil)
	f.movingPlant = insertPlant(f.regionOld, "moving-plant")

	f.beforeMove = time.Now().Add(-2 * time.Hour)
	moveAt := time.Now().Add(-1 * time.Hour)
	f.afterMove = time.Now().Add(-30 * time.Minute)

	// Close-and-open written directly (not via leaflab/api/placement.Writer
	// -- this fixture only needs the end state, and the trigger's
	// no-future-valid_from guard only cares that valid_from itself is not
	// later than NOW(), which moveAt (in the past) satisfies).
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO plant_region_history (plant_id, region_id, valid_from, valid_to) VALUES ($1, $2, $3, $4)
	`, f.movingPlant, f.regionOld, f.beforeMove.Add(-1*time.Hour), moveAt); err != nil {
		t.Fatalf("seed movingPlant's pre-move closed interval: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO plant_region_history (plant_id, region_id, valid_from) VALUES ($1, $2, $3)
	`, f.movingPlant, f.regionNew, moveAt); err != nil {
		t.Fatalf("seed movingPlant's post-move open interval: %v", err)
	}

	return db, f
}

// sqlAttribute calls attribute_region_plants directly, returning the
// attributed region ID (0 if nothing attributed) and the sorted plant IDs.
func sqlAttribute(t *testing.T, db *dbtest.Postgres, regionID int64, at time.Time) (attributedRegionID int64, plantIDs []int64) {
	t.Helper()
	ctx := context.Background()
	rows, err := db.Pool.Query(ctx, `SELECT attributed_region_id, plant_id FROM attribute_region_plants($1, $2)`, regionID, at)
	if err != nil {
		t.Fatalf("attribute_region_plants(%d, %v): %v", regionID, at, err)
	}
	defer rows.Close()
	for rows.Next() {
		var rid, pid int64
		if err := rows.Scan(&rid, &pid); err != nil {
			t.Fatalf("scan attribute_region_plants row: %v", err)
		}
		attributedRegionID = rid
		plantIDs = append(plantIDs, pid)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate attribute_region_plants rows: %v", err)
	}
	sort.Slice(plantIDs, func(i, j int) bool { return plantIDs[i] < plantIDs[j] })
	return attributedRegionID, plantIDs
}

// goAttribute calls Resolver.ResolvePlants, returning the attributed region
// ID (0 if nothing attributed) and the sorted plant IDs -- same shape as
// sqlAttribute so the two are directly comparable.
func goAttribute(t *testing.T, db *dbtest.Postgres, regionID int64, at time.Time) (attributedRegionID int64, plantIDs []int64) {
	t.Helper()
	ctx := context.Background()
	resolver := attribution.NewResolver(db.Pool)
	refs, rid, err := resolver.ResolvePlants(ctx, regionID, at)
	if err != nil {
		t.Fatalf("ResolvePlants(%d, %v): %v", regionID, at, err)
	}
	attributedRegionID = rid
	for _, ref := range refs {
		plantIDs = append(plantIDs, ref.PlantID)
	}
	sort.Slice(plantIDs, func(i, j int) bool { return plantIDs[i] < plantIDs[j] })
	return attributedRegionID, plantIDs
}

// assertAgree is NFR1.c's kernel: for a given (region, at) case, the Go
// resolver and the SQL function must return identical results. It also
// asserts the case's expected attributed region and plant set, so a
// regression in either implementation (or a divergence between them) is
// caught by name.
func assertAgree(t *testing.T, db *dbtest.Postgres, name string, regionID int64, at time.Time, wantRegionID int64, wantPlantIDs []int64) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		sqlRegion, sqlPlants := sqlAttribute(t, db, regionID, at)
		goRegion, goPlants := goAttribute(t, db, regionID, at)

		if sqlRegion != goRegion {
			t.Errorf("SQL and Go disagree on attributed region: SQL = %d, Go = %d (NFR1.c violated)", sqlRegion, goRegion)
		}
		if !equalInt64s(sqlPlants, goPlants) {
			t.Errorf("SQL and Go disagree on attributed plants: SQL = %v, Go = %v (NFR1.c violated)", sqlPlants, goPlants)
		}

		if sqlRegion != wantRegionID {
			t.Errorf("attributed region = %d, want %d", sqlRegion, wantRegionID)
		}
		if !equalInt64s(sqlPlants, wantPlantIDs) {
			t.Errorf("attributed plants = %v, want %v", sqlPlants, wantPlantIDs)
		}
	})
}

func equalInt64s(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestAttribution_NearestAncestorAcrossDepths covers the Testing section's
// first three bullets in one fixture: a plant in the reading's own region
// attributes (leafA1); a plant only in a grandparent attributes when no
// nearer ancestor has one, and a plant in a strict descendant never
// attributes an ancestor's reading (branchB -> root1, skipping leafB1's
// descendant-only plant); and a nearer ancestor wins over one further up
// (leafA2 -> branchA, not root1).
func TestAttribution_NearestAncestorAcrossDepths(t *testing.T) {
	db, f := newAttributionDB(t)
	at := time.Now()

	assertAgree(t, db, "own region attributes (leafA1)", f.leafA1, at,
		f.leafA1, []int64{f.plantLeafA1a, f.plantLeafA1b})

	assertAgree(t, db, "grandparent attributes, descendant does not (branchB)", f.branchB, at,
		f.root1, []int64{f.plantRoot1})

	assertAgree(t, db, "nearer ancestor wins over further (leafA2)", f.leafA2, at,
		f.branchA, []int64{f.plantBranchA})
}

// TestAttribution_DescendantDoesNotAttributeAncestorReading isolates the
// Testing section's "a plant in a descendant region does not attribute a
// parent region's reading" bullet: leafB1 (descendant of branchB) has a
// plant, but a reading at branchB itself -- and at mid1, another ancestor --
// must never see it.
func TestAttribution_DescendantDoesNotAttributeAncestorReading(t *testing.T) {
	db, f := newAttributionDB(t)
	at := time.Now()

	for _, tc := range []struct {
		name     string
		regionID int64
	}{
		{"branchB (direct parent of leafB1)", f.branchB},
		{"mid1 (grandparent of leafB1)", f.mid1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, plants := sqlAttribute(t, db, tc.regionID, at)
			for _, pid := range plants {
				if pid == f.plantLeafB1 {
					t.Errorf("reading at region %d attributed to descendant-only plant %d, want it excluded (A11)", tc.regionID, f.plantLeafB1)
				}
			}
			_, goPlants := goAttribute(t, db, tc.regionID, at)
			for _, pid := range goPlants {
				if pid == f.plantLeafB1 {
					t.Errorf("Go: reading at region %d attributed to descendant-only plant %d, want it excluded (A11)", tc.regionID, f.plantLeafB1)
				}
			}
		})
	}
}

// TestAttribution_SiblingDisclosureNoDoubleCount covers "two active plants
// in one region: both attribute, both are listed as siblings of each other,
// and an average over the region is unchanged by the second plant's
// presence (no double-count)". The no-double-count half is proven by
// aggregating per attributed region (COUNT(DISTINCT attributed_region_id)
// over the fan-out rows, mirroring FR20.1's "aggregate per region, then
// project onto plants" rule) rather than per returned row, which would
// double-count.
func TestAttribution_SiblingDisclosureNoDoubleCount(t *testing.T) {
	db, f := newAttributionDB(t)
	at := time.Now()

	assertAgree(t, db, "leafA1 siblings", f.leafA1, at,
		f.leafA1, []int64{f.plantLeafA1a, f.plantLeafA1b})

	_, plants := sqlAttribute(t, db, f.leafA1, at)
	if len(plants) != 2 {
		t.Fatalf("plant count for leafA1 = %d, want 2 (both siblings attribute)", len(plants))
	}
	if plants[0] == plants[1] {
		t.Fatalf("plants attributed to leafA1 are not distinct: %v", plants)
	}

	// No-double-count: an aggregate keyed by attributed_region_id (FR20.1's
	// rule), not by row, counts leafA1's fan-out exactly once.
	var distinctRegionCount int
	if err := db.Pool.QueryRow(t.Context(), `
		SELECT COUNT(DISTINCT attributed_region_id) FROM attribute_region_plants($1, $2)
	`, f.leafA1, at).Scan(&distinctRegionCount); err != nil {
		t.Fatalf("count distinct attributed_region_id: %v", err)
	}
	if distinctRegionCount != 1 {
		t.Errorf("distinct attributed regions for leafA1's fan-out = %d, want 1 (a per-region aggregate must not double-count the 2-plant fan-out)", distinctRegionCount)
	}
}

// TestAttribution_AtReadingTime covers "a reading recorded before a plant
// moved attributes to the pre-move region's plant, after the move to the
// new one" -- asserted against plant_region_history via both
// implementations, not plant.region_id (which this fixture never even
// sets for movingPlant, so a defect reading plant.region_id would fail
// with a SQL error, not silently pass).
func TestAttribution_AtReadingTime(t *testing.T) {
	db, f := newAttributionDB(t)

	assertAgree(t, db, "before move: old region attributes", f.regionOld, f.beforeMove,
		f.regionOld, []int64{f.movingPlant})
	assertAgree(t, db, "before move: new region attributes nothing yet", f.regionNew, f.beforeMove,
		0, nil)

	assertAgree(t, db, "after move: new region attributes", f.regionNew, f.afterMove,
		f.regionNew, []int64{f.movingPlant})
	assertAgree(t, db, "after move: old region no longer attributes", f.regionOld, f.afterMove,
		0, nil)
}

// TestAttribution_NonLeafDepthPlant covers A2: a plant that is not at
// leaf/pot depth (plantBranchA sits at branchA, which has leafA1 and leafA2
// as children) attributes correctly for a reading taken directly in its own
// non-leaf region.
func TestAttribution_NonLeafDepthPlant(t *testing.T) {
	db, f := newAttributionDB(t)
	at := time.Now()

	var childCount int
	if err := db.Pool.QueryRow(t.Context(), `SELECT COUNT(*) FROM region WHERE parent_region_id = $1`, f.branchA).Scan(&childCount); err != nil {
		t.Fatalf("count branchA's children: %v", err)
	}
	if childCount == 0 {
		t.Fatal("test setup: branchA must have children to be a non-leaf region")
	}

	assertAgree(t, db, "non-leaf-depth plant attributes its own region", f.branchA, at,
		f.branchA, []int64{f.plantBranchA})
}

// TestAttribution_NoAttributablePlantsAnywhere covers "a region with no
// attributable plants anywhere up its ancestor chain" -- tree 2 is entirely
// plant-free, so both a mid-tree region and its root must resolve to
// nothing attributed, not an error.
func TestAttribution_NoAttributablePlantsAnywhere(t *testing.T) {
	db, f := newAttributionDB(t)
	at := time.Now()

	assertAgree(t, db, "orphan2 (leaf, no plant anywhere up its chain)", f.orphan2, at, 0, nil)
	assertAgree(t, db, "root2 (root, no plant at all)", f.root2, at, 0, nil)
}
