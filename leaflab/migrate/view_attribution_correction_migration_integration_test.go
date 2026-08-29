//go:build integration

// Real-Postgres integration coverage for migration 021's repoint of
// v_sensor_reading_with_plant onto nearest-ancestor attribution (FR72,
// NFR16 views half). Migrates a real database up to version 19 (the last
// migration before 021 -- attribute_region_plants and plant_region_history
// already exist, but the view is still migration 012's original
// `p.region_id = e.region_id` current-placement join), seeds a shared
// fixture, and asserts the Testing section's checks: the defect's exact
// symptom (a pre-move reading loses its attribution once the plant's
// current region_id changes) reproduces against the pre-021 view and is
// fixed once migration 021 is applied; the column contract holds
// (household_id added, everything else unchanged); the view's fan-out
// agrees with attribute_region_plants (migration 019's SQL twin of
// leaflab/api/attribution.Resolver.ResolvePlants) on nearest-ancestor and
// sibling behaviour; no parallel view exists; household_id resolves
// through the region tree root; and up/down/up is clean. See
// attribution_migration_integration_test.go and
// plant_region_history_migration_integration_test.go for the sibling
// patterns this file follows, and //libs/go/dbtest's README for how to run
// tests like this one.
package main_test

import (
	"context"
	"database/sql"
	"embed"
	"sort"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

//go:embed migrations/*.sql
var viewAttributionMigrations embed.FS

// viewAttributionTimescaleImage matches timescaleImage in the sibling
// integration test files -- migration 001's create_hypertable call needs
// the real TimescaleDB image, not plain postgres.
const viewAttributionTimescaleImage = "timescale/timescaledb:latest-pg16"

// viewFixture is the shared region/plant/sensor fixture every test in this
// file seeds, migrated up only to version 19 (pre-021) so tests that need
// the defective pre-021 view for a red/green comparison can query it before
// stepping forward.
//
// Shape:
//
//	testHousehold (its own household, distinct from Unadopted)
//	regionOld (root, household_id = testHousehold) -- movingPlant starts here
//	regionNew (root, no household)                  -- movingPlant ends here
//
//	ancestryRoot (root, no household, plant: plantRoot)
//	└─ mid (no plant)
//	    ├─ branchB (no plant)
//	    │   └─ leafB1 (plant: plantLeafB1 -- descendant-only, must never
//	    │               attribute a reading recorded at branchB or mid)
//	    └─ leafA1 (plants: plantLeafA1a, plantLeafA1b -- siblings)
//
// One sensor is reused for every reading; sensor_reading.region_id is
// stamped explicitly per insert (independent of the sensor's own
// registered region), mirroring
// plant_region_history_migration_integration_test.go's insertReading.
type viewFixture struct {
	unadoptedHouseholdID int64
	testHouseholdID      int64

	regionOld, regionNew int64
	movingPlant          int64

	// moveAt is the instant movingPlant's plant_region_history interval
	// closes in regionOld and opens in regionNew. plant.region_id is
	// updated to regionNew at the same instant, mirroring
	// leaflab/api/placement.Writer.Move's current-value cache sync
	// (migration 017's up.sql doc comment) -- this is what makes the
	// pre-021 view's current-placement join actually go wrong for a
	// reading recorded before the move.
	beforeMove, moveAt, afterMove time.Time

	ancestryRoot, mid, branchB, leafB1, leafA1 int64
	plantRoot, plantLeafB1                     int64
	plantLeafA1a, plantLeafA1b                 int64

	sensorID int64
}

// newViewFixtureAt19 migrates a fresh database up through version 19 (the
// last migration before 021) and seeds viewFixture. Callers that want the
// post-021 (fixed) state must call runner.Migrate(26) themselves -- left to
// the caller so tests needing the pre-021 (defective) state can query it
// first.
func newViewFixtureAt19(t *testing.T) (*migrate.Runner, *dbtest.Postgres, viewFixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Image: viewAttributionTimescaleImage})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := sqlDB.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}

	runner := migrate.NewRunner(sqlDB, viewAttributionMigrations, "migrations")
	if err := runner.Migrate(19); err != nil {
		t.Fatalf("migrate to version 19 (pre-021): %v", err)
	}

	f := viewFixture{}

	if err := db.Pool.QueryRow(ctx, `SELECT household_id FROM household WHERE is_unadopted = TRUE`).Scan(&f.unadoptedHouseholdID); err != nil {
		t.Fatalf("read Unadopted household_id: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO household (name, is_unadopted) VALUES ($1, FALSE) RETURNING household_id
	`, "test-household").Scan(&f.testHouseholdID); err != nil {
		t.Fatalf("insert test household: %v", err)
	}

	var plantTypeID int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO plant_type (common_name) VALUES ($1) RETURNING plant_type_id
	`, "test-plant-type").Scan(&plantTypeID); err != nil {
		t.Fatalf("insert plant_type: %v", err)
	}

	insertRegion := func(name string, parentID *int64, householdID *int64) int64 {
		t.Helper()
		var id int64
		if err := db.Pool.QueryRow(ctx, `
			INSERT INTO region (name, parent_region_id, household_id) VALUES ($1, $2, $3) RETURNING region_id
		`, name, parentID, householdID).Scan(&id); err != nil {
			t.Fatalf("insert region %s: %v", name, err)
		}
		return id
	}
	regionPtr := func(id int64) *int64 { return &id }
	hhPtr := func(id int64) *int64 { return &id }

	insertPlant := func(regionID int64, name string, householdID int64) int64 {
		t.Helper()
		var id int64
		if err := db.Pool.QueryRow(ctx, `
			INSERT INTO plant (region_id, plant_type_id, name, household_id) VALUES ($1, $2, $3, $4) RETURNING plant_id
		`, regionID, plantTypeID, name, householdID).Scan(&id); err != nil {
			t.Fatalf("insert plant %s: %v", name, err)
		}
		return id
	}

	openInterval := func(plantID, regionID int64) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, `
			INSERT INTO plant_region_history (plant_id, region_id) VALUES ($1, $2)
		`, plantID, regionID); err != nil {
			t.Fatalf("open interval for plant %d in region %d: %v", plantID, regionID, err)
		}
	}

	var boardID, sensorTypeID int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO board (device_id) VALUES ($1) RETURNING board_id
	`, "view-attribution-test-board").Scan(&boardID); err != nil {
		t.Fatalf("insert board: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT sensor_type_id FROM sensor_type LIMIT 1`).Scan(&sensorTypeID); err != nil {
		t.Fatalf("read seeded sensor_type: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO sensor (board_id, sensor_type_id, name, unit) VALUES ($1, $2, $3, $4) RETURNING sensor_id
	`, boardID, sensorTypeID, "view-attribution-test-sensor", "degC").Scan(&f.sensorID); err != nil {
		t.Fatalf("insert sensor: %v", err)
	}

	// ── Moving-plant fixture ────────────────────────────────────────────
	f.regionOld = insertRegion("region-old", nil, hhPtr(f.testHouseholdID))
	f.regionNew = insertRegion("region-new", nil, nil)
	f.movingPlant = insertPlant(f.regionOld, "moving-plant", f.testHouseholdID)

	f.beforeMove = time.Now().Add(-2 * time.Hour)
	f.moveAt = time.Now().Add(-1 * time.Hour)
	f.afterMove = time.Now().Add(-30 * time.Minute)

	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO plant_region_history (plant_id, region_id, valid_from, valid_to) VALUES ($1, $2, $3, $4)
	`, f.movingPlant, f.regionOld, f.beforeMove.Add(-1*time.Hour), f.moveAt); err != nil {
		t.Fatalf("seed movingPlant's pre-move closed interval: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO plant_region_history (plant_id, region_id, valid_from) VALUES ($1, $2, $3)
	`, f.movingPlant, f.regionNew, f.moveAt); err != nil {
		t.Fatalf("seed movingPlant's post-move open interval: %v", err)
	}
	// Mirror leaflab/api/placement.Writer.Move's current-value cache sync
	// (migration 017's up.sql doc comment): plant.region_id now points at
	// regionNew, even though the pre-move reading below was recorded while
	// the plant was still in regionOld. This is what makes the pre-021
	// view's `p.region_id = e.region_id` join wrong for that reading.
	if _, err := db.Pool.Exec(ctx, `UPDATE plant SET region_id = $1 WHERE plant_id = $2`, f.regionNew, f.movingPlant); err != nil {
		t.Fatalf("sync movingPlant.region_id to regionNew: %v", err)
	}

	// ── Nearest-ancestor / sibling fixture ──────────────────────────────
	f.ancestryRoot = insertRegion("ancestry-root", nil, nil)
	f.mid = insertRegion("mid", regionPtr(f.ancestryRoot), nil)
	f.branchB = insertRegion("branchB", regionPtr(f.mid), nil)
	f.leafB1 = insertRegion("leafB1", regionPtr(f.branchB), nil)
	f.leafA1 = insertRegion("leafA1", regionPtr(f.mid), nil)

	f.plantRoot = insertPlant(f.ancestryRoot, "plant-root", f.unadoptedHouseholdID)
	openInterval(f.plantRoot, f.ancestryRoot)

	f.plantLeafB1 = insertPlant(f.leafB1, "plant-leafB1", f.unadoptedHouseholdID)
	openInterval(f.plantLeafB1, f.leafB1)

	f.plantLeafA1a = insertPlant(f.leafA1, "plant-leafA1a", f.unadoptedHouseholdID)
	openInterval(f.plantLeafA1a, f.leafA1)
	f.plantLeafA1b = insertPlant(f.leafA1, "plant-leafA1b", f.unadoptedHouseholdID)
	openInterval(f.plantLeafA1b, f.leafA1)

	return runner, db, f
}

// insertReading inserts a sensor_reading stamped with an explicit
// region_id and recorded_at, independent of the sensor's own registered
// region -- mirrors
// plant_region_history_migration_integration_test.go's insertReading.
func insertReading(t *testing.T, db *dbtest.Postgres, sensorID, regionID int64, recordedAt time.Time) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO sensor_reading (sensor_id, region_id, value, uptime_s, recorded_at) VALUES ($1, $2, $3, $4, $5) RETURNING reading_id
	`, sensorID, regionID, 21.5, 1000, recordedAt).Scan(&id); err != nil {
		t.Fatalf("insert sensor_reading in region %d at %v: %v", regionID, recordedAt, err)
	}
	return id
}

// viewPlantID reads the plant_id column of v_sensor_reading_with_plant for
// one reading. sensor_reading's primary key is (reading_id, recorded_at)
// but reading_id alone is unique in this fixture (BIGSERIAL), so this is
// safe.
func viewPlantID(t *testing.T, db *dbtest.Postgres, readingID int64) *int64 {
	t.Helper()
	ctx := context.Background()
	var plantID *int64
	if err := db.Pool.QueryRow(ctx, `SELECT plant_id FROM v_sensor_reading_with_plant WHERE reading_id = $1`, readingID).Scan(&plantID); err != nil {
		t.Fatalf("read plant_id for reading %d: %v", readingID, err)
	}
	return plantID
}

// TestViewAttributionCorrection_MovedPlantFixture is the defect
// reproduction the issue asks for: before migration 021, a reading
// recorded while movingPlant was still in regionOld loses its attribution
// once movingPlant's region_id changes (the "moving a plant retroactively
// rewrites every reading it ever produced" defect, in its NULL-out form --
// see the fixture's inline comment on plant.region_id sync). After
// migration 021, the same reading is unchanged (FR72's exact requirement:
// "the pre-T rows are unchanged by the move"), and post-move readings
// attribute to the post-move region.
func TestViewAttributionCorrection_MovedPlantFixture(t *testing.T) {
	runner, db, f := newViewFixtureAt19(t)

	readingBeforeMoveOld := insertReading(t, db, f.sensorID, f.regionOld, f.beforeMove)
	readingBeforeMoveNew := insertReading(t, db, f.sensorID, f.regionNew, f.beforeMove)
	readingAfterMoveNew := insertReading(t, db, f.sensorID, f.regionNew, f.afterMove)
	readingAfterMoveOld := insertReading(t, db, f.sensorID, f.regionOld, f.afterMove)

	// ── Red: pre-021, the defect reproduces ─────────────────────────────
	t.Run("pre-021: pre-move reading has lost its attribution (defect)", func(t *testing.T) {
		got := viewPlantID(t, db, readingBeforeMoveOld)
		if got != nil {
			t.Fatalf("pre-021 view plant_id for pre-move reading = %d, want NULL -- test setup assumption about the pre-021 defective join is wrong (recheck plant.region_id sync)", *got)
		}
	})

	if err := runner.Migrate(26); err != nil {
		t.Fatalf("apply migration 021: %v", err)
	}

	// ── Green: post-021, the defect is fixed ────────────────────────────
	t.Run("post-021: pre-move reading keeps pre-move attribution", func(t *testing.T) {
		got := viewPlantID(t, db, readingBeforeMoveOld)
		if got == nil || *got != f.movingPlant {
			t.Errorf("post-021 view plant_id for pre-move reading (regionOld) = %v, want %d (unchanged by the later move, FR72)", got, f.movingPlant)
		}
	})
	t.Run("post-021: regionNew attributes nothing before the move", func(t *testing.T) {
		got := viewPlantID(t, db, readingBeforeMoveNew)
		if got != nil {
			t.Errorf("post-021 view plant_id for regionNew before the move = %v, want NULL (movingPlant was not there yet)", *got)
		}
	})
	t.Run("post-021: regionNew attributes after the move", func(t *testing.T) {
		got := viewPlantID(t, db, readingAfterMoveNew)
		if got == nil || *got != f.movingPlant {
			t.Errorf("post-021 view plant_id for regionNew after the move = %v, want %d", got, f.movingPlant)
		}
	})
	t.Run("post-021: regionOld attributes nothing after the move", func(t *testing.T) {
		got := viewPlantID(t, db, readingAfterMoveOld)
		if got != nil {
			t.Errorf("post-021 view plant_id for regionOld after the move = %v, want NULL (movingPlant left)", *got)
		}
	})
}

// viewColumn is one (name, data_type) pair from information_schema.columns.
type viewColumn struct {
	name     string
	dataType string
}

func readViewColumns(t *testing.T, db *dbtest.Postgres) []viewColumn {
	t.Helper()
	ctx := context.Background()
	rows, err := db.Pool.Query(ctx, `
		SELECT column_name, data_type FROM information_schema.columns
		WHERE table_name = 'v_sensor_reading_with_plant'
		ORDER BY ordinal_position
	`)
	if err != nil {
		t.Fatalf("query information_schema.columns: %v", err)
	}
	defer rows.Close()
	var out []viewColumn
	for rows.Next() {
		var c viewColumn
		if err := rows.Scan(&c.name, &c.dataType); err != nil {
			t.Fatalf("scan information_schema.columns row: %v", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate information_schema.columns rows: %v", err)
	}
	return out
}

// TestViewAttributionCorrection_ColumnContract is NFR16's in-contract half:
// every column present before migration 021 is present after, with the
// same name and type, and household_id is added -- nothing is dropped or
// renamed (FR72).
func TestViewAttributionCorrection_ColumnContract(t *testing.T) {
	runner, db, _ := newViewFixtureAt19(t)

	before := readViewColumns(t, db)
	if len(before) == 0 {
		t.Fatal("test setup: v_sensor_reading_with_plant has no columns before migration 021")
	}

	if err := runner.Migrate(26); err != nil {
		t.Fatalf("apply migration 021: %v", err)
	}
	after := readViewColumns(t, db)

	if len(after) != len(before)+1 {
		t.Fatalf("column count after migration 021 = %d, want %d (before) + 1 (household_id)", len(after), len(before))
	}

	for i, b := range before {
		a := after[i]
		if a.name != b.name || a.dataType != b.dataType {
			t.Errorf("column %d changed: before = %+v, after = %+v (FR72 requires existing columns to keep their name and type)", i, b, a)
		}
	}

	last := after[len(after)-1]
	if last.name != "household_id" {
		t.Errorf("last column after migration 021 = %q, want %q (household_id must be appended last, migration 021's own comment)", last.name, "household_id")
	}
}

// TestViewAttributionCorrection_NearestAncestorAndSiblingsMatchFunction
// asserts the view's fan-out (grouped per reading) agrees with
// attribute_region_plants -- migration 019's SQL twin of
// leaflab/api/attribution.Resolver.ResolvePlants, itself already asserted
// against the Go resolver in attribution_migration_integration_test.go --
// on nearest-ancestor and sibling behaviour, proving the LATERAL join
// wiring in migration 021 (not the attribution rule itself, which is
// out of scope for this file) is correct.
func TestViewAttributionCorrection_NearestAncestorAndSiblingsMatchFunction(t *testing.T) {
	runner, db, f := newViewFixtureAt19(t)
	if err := runner.Migrate(26); err != nil {
		t.Fatalf("apply migration 021: %v", err)
	}
	ctx := context.Background()
	at := time.Now()

	sqlAttribute := func(regionID int64) []int64 {
		t.Helper()
		rows, err := db.Pool.Query(ctx, `SELECT plant_id FROM attribute_region_plants($1, $2)`, regionID, at)
		if err != nil {
			t.Fatalf("attribute_region_plants(%d, %v): %v", regionID, at, err)
		}
		defer rows.Close()
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				t.Fatalf("scan attribute_region_plants row: %v", err)
			}
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		return ids
	}

	viewAttribute := func(regionID int64, readingID int64) []int64 {
		t.Helper()
		rows, err := db.Pool.Query(ctx, `SELECT plant_id FROM v_sensor_reading_with_plant WHERE reading_id = $1 AND plant_id IS NOT NULL`, readingID)
		if err != nil {
			t.Fatalf("query view for reading %d: %v", readingID, err)
		}
		defer rows.Close()
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				t.Fatalf("scan view row: %v", err)
			}
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		return ids
	}

	equal := func(a, b []int64) bool {
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

	for _, tc := range []struct {
		name     string
		regionID int64
		want     []int64
	}{
		{"own region attributes (leafA1 siblings)", f.leafA1, []int64{f.plantLeafA1a, f.plantLeafA1b}},
		{"grandparent attributes, descendant does not (branchB)", f.branchB, []int64{f.plantRoot}},
		{"grandparent attributes, descendant does not (mid)", f.mid, []int64{f.plantRoot}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			readingID := insertReading(t, db, f.sensorID, tc.regionID, at)

			gotFunc := sqlAttribute(tc.regionID)
			gotView := viewAttribute(tc.regionID, readingID)

			if !equal(gotFunc, tc.want) {
				t.Fatalf("attribute_region_plants(%d) = %v, want %v", tc.regionID, gotFunc, tc.want)
			}
			if !equal(gotView, tc.want) {
				t.Errorf("view fan-out for region %d = %v, want %v (must match attribute_region_plants)", tc.regionID, gotView, tc.want)
			}
			if !equal(gotView, gotFunc) {
				t.Errorf("view and attribute_region_plants disagree for region %d: view = %v, function = %v", tc.regionID, gotView, gotFunc)
			}
		})
	}

	t.Run("descendant-only plant never attributes an ancestor reading", func(t *testing.T) {
		readingID := insertReading(t, db, f.sensorID, f.branchB, at)
		got := viewAttribute(f.branchB, readingID)
		for _, id := range got {
			if id == f.plantLeafB1 {
				t.Errorf("reading at branchB attributed to descendant-only plant %d, want it excluded (A11)", f.plantLeafB1)
			}
		}
	})
}

// TestViewAttributionCorrection_NoParallelView is FR72/A10's "no parallel
// deprecated view is retained" check: exactly one view named
// v_sensor_reading_with_plant (or anything that looks like a versioned
// sibling of it) exists.
func TestViewAttributionCorrection_NoParallelView(t *testing.T) {
	runner, db, _ := newViewFixtureAt19(t)
	if err := runner.Migrate(26); err != nil {
		t.Fatalf("apply migration 021: %v", err)
	}
	ctx := context.Background()

	rows, err := db.Pool.Query(ctx, `
		SELECT table_name FROM information_schema.views
		WHERE table_name LIKE 'v_sensor_reading_with_plant%'
		ORDER BY table_name
	`)
	if err != nil {
		t.Fatalf("query information_schema.views: %v", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan information_schema.views row: %v", err)
		}
		names = append(names, name)
	}
	if len(names) != 1 || names[0] != "v_sensor_reading_with_plant" {
		t.Errorf("views matching v_sensor_reading_with_plant%%  = %v, want exactly [\"v_sensor_reading_with_plant\"] (A10: no add-and-deprecate parallel view)", names)
	}
}

// TestViewAttributionCorrection_HouseholdIDResolvesThroughRoot asserts
// household_id on the view is the region tree root's household_id (region
// tree roots carry household_id; descendants inherit, migration 015), not
// the reading's own region or the plant's household.
func TestViewAttributionCorrection_HouseholdIDResolvesThroughRoot(t *testing.T) {
	runner, db, f := newViewFixtureAt19(t)
	if err := runner.Migrate(26); err != nil {
		t.Fatalf("apply migration 021: %v", err)
	}
	ctx := context.Background()

	// regionOld is itself a root with household_id = testHousehold.
	readingID := insertReading(t, db, f.sensorID, f.regionOld, f.afterMove)

	var householdID *int64
	if err := db.Pool.QueryRow(ctx, `SELECT household_id FROM v_sensor_reading_with_plant WHERE reading_id = $1`, readingID).Scan(&householdID); err != nil {
		t.Fatalf("read household_id: %v", err)
	}
	if householdID == nil || *householdID != f.testHouseholdID {
		t.Errorf("household_id for a reading in regionOld = %v, want %d (regionOld's own household_id, a root region)", householdID, f.testHouseholdID)
	}

	// ancestryRoot has no household_id set (NULL) -- a descendant reading
	// there must resolve to NULL, not silently fall back to Unadopted.
	readingUnclaimed := insertReading(t, db, f.sensorID, f.leafA1, at(f.afterMove))
	var householdIDUnclaimed *int64
	if err := db.Pool.QueryRow(ctx, `SELECT household_id FROM v_sensor_reading_with_plant WHERE reading_id = $1`, readingUnclaimed).Scan(&householdIDUnclaimed); err != nil {
		t.Fatalf("read household_id for unclaimed tree: %v", err)
	}
	if householdIDUnclaimed != nil {
		t.Errorf("household_id for a reading under ancestryRoot (no household set) = %v, want NULL", *householdIDUnclaimed)
	}
}

// at is a tiny helper so the second insertReading call above reads cleanly
// with a named time rather than a bare time.Now() call that could race
// against the first.
func at(t time.Time) time.Time { return t }

// TestViewAttributionCorrection_DownReversesCleanly and
// TestViewAttributionCorrection_UpDownUpIsIdempotentSafe cover the
// Validation section's "up and down both clean" / "the down migration
// restores the previous definition" checks.
func TestViewAttributionCorrection_DownReversesCleanly(t *testing.T) {
	runner, db, _ := newViewFixtureAt19(t)
	ctx := context.Background()

	if err := runner.Migrate(26); err != nil {
		t.Fatalf("apply migration 021: %v", err)
	}
	if err := runner.Migrate(25); err != nil {
		t.Fatalf("reverse migration 021: %v", err)
	}

	var householdIDExists bool
	if err := db.Pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name = 'v_sensor_reading_with_plant' AND column_name = 'household_id')
	`).Scan(&householdIDExists); err != nil {
		t.Fatalf("check household_id column after down: %v", err)
	}
	if householdIDExists {
		t.Error("v_sensor_reading_with_plant.household_id still exists after reversing migration 021")
	}

	var viewDef string
	if err := db.Pool.QueryRow(ctx, `SELECT pg_get_viewdef('v_sensor_reading_with_plant'::regclass, true)`).Scan(&viewDef); err != nil {
		t.Fatalf("read view definition after down: %v", err)
	}
	if !containsAll(viewDef, "p.region_id", "e.region_id") {
		t.Errorf("view definition after down does not look like migration 012's original current-placement join: %s", viewDef)
	}
	if containsAll(viewDef, "attribute_region_plants") {
		t.Errorf("view definition after down still references attribute_region_plants, want the pre-021 join restored: %s", viewDef)
	}
}

func TestViewAttributionCorrection_UpDownUpIsIdempotentSafe(t *testing.T) {
	runner, db, f := newViewFixtureAt19(t)
	ctx := context.Background()

	if err := runner.Migrate(26); err != nil {
		t.Fatalf("first apply of migration 021: %v", err)
	}
	if err := runner.Migrate(25); err != nil {
		t.Fatalf("reverse migration 021: %v", err)
	}
	if err := runner.Migrate(26); err != nil {
		t.Fatalf("second apply of migration 021: %v", err)
	}

	var householdIDExists bool
	if err := db.Pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name = 'v_sensor_reading_with_plant' AND column_name = 'household_id')
	`).Scan(&householdIDExists); err != nil {
		t.Fatalf("check household_id column after up-down-up: %v", err)
	}
	if !householdIDExists {
		t.Error("v_sensor_reading_with_plant.household_id missing after up-down-up")
	}

	// The view must still be functionally correct after the round trip --
	// re-check the moved-plant fixture's key case.
	readingID := insertReading(t, db, f.sensorID, f.regionOld, f.beforeMove)
	got := viewPlantID(t, db, readingID)
	if got == nil || *got != f.movingPlant {
		t.Errorf("after up-down-up, view plant_id for pre-move reading = %v, want %d", got, f.movingPlant)
	}
}

// containsAll reports whether s contains every one of subs.
func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
