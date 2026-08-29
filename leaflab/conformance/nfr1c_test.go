//go:build integration

// Real-Postgres coverage for NFR1.c ("FR72's corrected view and FR71's API
// read path return the same plant attribution for the same plant and
// window -- two implementations of one rule are permitted; disagreeing is
// not"). This file extends leaflab/conformance's existing conformance_test
// target rather than forking a third one (see this package's BUILD.bazel
// and paths_test.go's/nfr1b_test.go's doc comments for the same
// convention) -- the whole target now depends on libs/go/dbtest, so its
// BUILD.bazel rule carries the same manual/integration/no-cache/no-sandbox
// tag set every other real-Postgres test in this repo uses (see
// //libs/go/dbtest's README's "Why this is a separate, manual target"):
// `bazel test //...` stays green on a Docker-less machine, and
// leaflab/conformance:conformance_test -- including NFR1.a's and NFR1.b's
// source-analysis checks that used to run for free under the wildcard --
// now requires an explicit, Docker-backed invocation like every other
// leaflab dbtest-backed target. That tradeoff is accepted deliberately
// here per this task's own instruction to keep NFR1.a/b/c in one target
// rather than fork a third; state it again in the PR body.
//
// Migrates a real database through every checked-in migration (needs
// TimescaleDB for migration 001's create_hypertable call, per
// nfr1cTimescaleImage below) via leaflab/migrate/migrations -- an
// importable twin of leaflab/migrate/main.go's own embed, added by this
// task so this test exercises the real, shipped migration 021 view
// definition (v_sensor_reading_with_plant, FR72) rather than a hand-copied
// trim of it, which would let the two drift without this test noticing.
//
// newNFR1CFixture seeds one shared region/plant/reading fixture (the
// "shared attribution fixture" #1358/FR23's Validation section asks this
// task to reuse) covering every case this task's Scaffold section lists:
//
//   - neverMoved: a plant that never moved.
//   - midBucket: a plant that moved mid-bucket (a non-hour-aligned instant).
//   - multiMove: a plant that moved through three regions.
//   - sibling: two plants sharing one region (fan-out, no double-counting).
//   - nonLeaf: a non-leaf-depth plant, with a nearer-ancestor plant (in the
//     reading's own, previously plant-less region) appearing later.
//   - sharedHour: a plant removed mid-hour, whose plant_region_history
//     valid_to is snapped outward to the end of that hour (FR21's
//     migration-017 backfill rule) -- a reading recorded after the true
//     removal instant but before the snapped valid_to still attributes to
//     the removed plant in both implementations, since both key off the
//     same stored interval, not the "true" removal instant.
//   - stale: a reading stamped with a region_id that would have been stale
//     under FR73's now-fixed defect (leaflab/processor's SensorCache
//     invalidation lag) -- proving NFR1.c holds regardless of whether the
//     stamped region_id reflects a sensor's "true" current region: both
//     implementations key off sensor_reading.region_id as written, not a
//     second, independent resolution of "where is this sensor really".
//
// Scaffold only (this task's Scaffold phase): TestNFR1c_FixtureBuilds is a
// smoke test proving the fixture and both implementations are reachable
// end to end (one case, one window). TestNFR1c_ViewAndAPIAgreeOnAttribution
// enumerates every case above as a skipped subtest -- this task's
// Implementation phase fills each one in with the actual "compute
// attribution twice, assert identical" comparison (see this task's
// Implementation section: aggregate the view's raw rows the same way the
// API would for a window it must coarsen, and assert the API discloses the
// tier rather than silently differing where the two can't answer exactly).
// This task's Testing phase adds the negative fixture: deliberately
// perturbing one implementation and asserting the comparison fails,
// naming the reading and both attributions.
package conformance

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/whale-net/everything/leaflab/api/authz"
	"github.com/whale-net/everything/leaflab/api/readings"
	"github.com/whale-net/everything/leaflab/api/tiers"
	"github.com/whale-net/everything/leaflab/migrate/migrations"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

// nfr1cTimescaleImage matches every other leaflab/migrate real-Postgres
// integration test -- migration 001's create_hypertable call needs the
// real TimescaleDB image, not plain postgres.
const nfr1cTimescaleImage = "timescale/timescaledb:latest-pg16"

// nfr1cFixture is the shared region/plant/reading tree every NFR1.c case
// seeds -- see this file's package doc comment for the case list. Every ID
// field is populated by newNFR1CFixture; every time field is a
// sensor_reading.recorded_at (or plant_region_history boundary) instant a
// later comparison window is built around.
type nfr1cFixture struct {
	sensorID int64

	// neverMoved: a plant that never moved.
	neverMovedRegion    int64
	neverMovedPlant     int64
	neverMovedReadingID int64
	neverMovedReadingAt time.Time

	// midBucket: a plant that moved mid-bucket (a non-hour-aligned instant).
	midBucketRegionOld, midBucketRegionNew            int64
	midBucketPlant                                    int64
	midBucketMoveAt                                   time.Time
	midBucketReadingBeforeID, midBucketReadingAfterID int64
	midBucketReadingBeforeAt, midBucketReadingAfterAt time.Time

	// multiMove: a plant that moved through three regions.
	multiMoveRegion1, multiMoveRegion2, multiMoveRegion3          int64
	multiMovePlant                                                int64
	multiMove1At, multiMove2At                                    time.Time
	multiMoveReading1ID, multiMoveReading2ID, multiMoveReading3ID int64

	// sibling: two plants sharing one region (fan-out).
	siblingRegion                int64
	siblingPlantA, siblingPlantB int64
	siblingReadingID             int64
	siblingReadingAt             time.Time

	// nonLeaf: a non-leaf-depth plant, with a nearer-ancestor plant
	// (appearing later) in the reading's own, previously plant-less region.
	nonLeafRoot, nonLeafChild           int64
	nonLeafRootPlant, nonLeafChildPlant int64
	nonLeafChildPlantOpensAt            time.Time
	nonLeafReadingBeforeChildPlantID    int64
	nonLeafReadingAfterChildPlantID     int64

	// sharedHour: a plant removed mid-hour, valid_to snapped outward to the
	// end of that hour (FR21's migration-017 backfill rule).
	sharedHourRegion                       int64
	sharedHourPlantOld, sharedHourPlantNew int64
	sharedHourTrueRemovalAt                time.Time
	sharedHourSnappedValidTo               time.Time
	sharedHourReadingBeforeRemovalID       int64
	sharedHourReadingInSharedHourID        int64
	sharedHourReadingAfterSnapID           int64

	// stale: a reading stamped with a region_id that would have been stale
	// under FR73's now-fixed processor-cache defect.
	staleOldRegion, staleNewRegion int64
	stalePlantOld, stalePlantNew   int64
	staleReadingID                 int64
	staleReadingAt                 time.Time
}

// newNFR1CFixture migrates a fresh database through every checked-in
// migration and seeds nfr1cFixture.
func newNFR1CFixture(t *testing.T) (*dbtest.Postgres, nfr1cFixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Image: nfr1cTimescaleImage})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := sqlDB.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}

	runner := migrate.NewRunner(sqlDB, migrations.FS, ".")
	if err := runner.Up(); err != nil {
		t.Fatalf("migrate up (every checked-in migration): %v", err)
	}

	var unadoptedHouseholdID int64
	if err := db.Pool.QueryRow(ctx, `SELECT household_id FROM household WHERE is_unadopted = TRUE`).Scan(&unadoptedHouseholdID); err != nil {
		t.Fatalf("read Unadopted household_id: %v", err)
	}

	var plantTypeID int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO plant_type (common_name) VALUES ($1) RETURNING plant_type_id
	`, "nfr1c-test-plant-type").Scan(&plantTypeID); err != nil {
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

	// insertHistoryRow inserts one plant_region_history interval directly
	// (not through leaflab/api/placement.Writer) with an explicit
	// valid_from/valid_to -- every case below needs full control over both
	// boundaries, unlike a plant that has only ever had one open interval.
	insertHistoryRow := func(plantID, regionID int64, from time.Time, to *time.Time) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, `
			INSERT INTO plant_region_history (plant_id, region_id, valid_from, valid_to) VALUES ($1, $2, $3, $4)
		`, plantID, regionID, from, to); err != nil {
			t.Fatalf("insert plant_region_history for plant %d in region %d: %v", plantID, regionID, err)
		}
	}

	var boardID, sensorTypeID int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO board (device_id) VALUES ($1) RETURNING board_id
	`, "nfr1c-test-board").Scan(&boardID); err != nil {
		t.Fatalf("insert board: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT sensor_type_id FROM sensor_type WHERE name = 'temperature'`).Scan(&sensorTypeID); err != nil {
		t.Fatalf("read seeded temperature sensor_type: %v", err)
	}

	f := nfr1cFixture{}
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO sensor (board_id, sensor_type_id, name, unit) VALUES ($1, $2, $3, $4) RETURNING sensor_id
	`, boardID, sensorTypeID, "nfr1c-test-sensor", "degC").Scan(&f.sensorID); err != nil {
		t.Fatalf("insert sensor: %v", err)
	}

	insertReading := func(regionID int64, recordedAt time.Time) int64 {
		t.Helper()
		var id int64
		if err := db.Pool.QueryRow(ctx, `
			INSERT INTO sensor_reading (sensor_id, region_id, value, uptime_s, recorded_at) VALUES ($1, $2, $3, $4, $5) RETURNING reading_id
		`, f.sensorID, regionID, 21.5, 1000, recordedAt).Scan(&id); err != nil {
			t.Fatalf("insert sensor_reading in region %d at %v: %v", regionID, recordedAt, err)
		}
		return id
	}

	now := time.Now().UTC().Truncate(time.Second)
	longAgo := now.Add(-30 * 24 * time.Hour)

	// ── neverMoved ───────────────────────────────────────────────────────
	f.neverMovedRegion = insertRegion("nfr1c-never-moved", nil)
	f.neverMovedPlant = insertPlant(f.neverMovedRegion, "nfr1c-never-moved-plant")
	insertHistoryRow(f.neverMovedPlant, f.neverMovedRegion, longAgo, nil)
	f.neverMovedReadingAt = now.Add(-6 * time.Hour)
	f.neverMovedReadingID = insertReading(f.neverMovedRegion, f.neverMovedReadingAt)

	// ── midBucket ────────────────────────────────────────────────────────
	f.midBucketRegionOld = insertRegion("nfr1c-mid-bucket-old", nil)
	f.midBucketRegionNew = insertRegion("nfr1c-mid-bucket-new", nil)
	f.midBucketPlant = insertPlant(f.midBucketRegionOld, "nfr1c-mid-bucket-plant")
	f.midBucketMoveAt = now.Add(-5 * time.Hour).Add(37 * time.Minute) // deliberately off an hour boundary
	insertHistoryRow(f.midBucketPlant, f.midBucketRegionOld, longAgo, &f.midBucketMoveAt)
	insertHistoryRow(f.midBucketPlant, f.midBucketRegionNew, f.midBucketMoveAt, nil)
	f.midBucketReadingBeforeAt = f.midBucketMoveAt.Add(-10 * time.Minute)
	f.midBucketReadingAfterAt = f.midBucketMoveAt.Add(10 * time.Minute)
	f.midBucketReadingBeforeID = insertReading(f.midBucketRegionOld, f.midBucketReadingBeforeAt)
	f.midBucketReadingAfterID = insertReading(f.midBucketRegionNew, f.midBucketReadingAfterAt)

	// ── multiMove ────────────────────────────────────────────────────────
	f.multiMoveRegion1 = insertRegion("nfr1c-multi-move-1", nil)
	f.multiMoveRegion2 = insertRegion("nfr1c-multi-move-2", nil)
	f.multiMoveRegion3 = insertRegion("nfr1c-multi-move-3", nil)
	f.multiMovePlant = insertPlant(f.multiMoveRegion1, "nfr1c-multi-move-plant")
	f.multiMove1At = now.Add(-4 * time.Hour)
	f.multiMove2At = now.Add(-3 * time.Hour)
	insertHistoryRow(f.multiMovePlant, f.multiMoveRegion1, longAgo, &f.multiMove1At)
	insertHistoryRow(f.multiMovePlant, f.multiMoveRegion2, f.multiMove1At, &f.multiMove2At)
	insertHistoryRow(f.multiMovePlant, f.multiMoveRegion3, f.multiMove2At, nil)
	f.multiMoveReading1ID = insertReading(f.multiMoveRegion1, f.multiMove1At.Add(-30*time.Minute))
	f.multiMoveReading2ID = insertReading(f.multiMoveRegion2, f.multiMove1At.Add(30*time.Minute))
	f.multiMoveReading3ID = insertReading(f.multiMoveRegion3, f.multiMove2At.Add(30*time.Minute))

	// ── sibling (fan-out, no double-counting) ───────────────────────────
	f.siblingRegion = insertRegion("nfr1c-sibling", nil)
	f.siblingPlantA = insertPlant(f.siblingRegion, "nfr1c-sibling-plant-a")
	f.siblingPlantB = insertPlant(f.siblingRegion, "nfr1c-sibling-plant-b")
	insertHistoryRow(f.siblingPlantA, f.siblingRegion, longAgo, nil)
	insertHistoryRow(f.siblingPlantB, f.siblingRegion, longAgo, nil)
	f.siblingReadingAt = now.Add(-2 * time.Hour)
	f.siblingReadingID = insertReading(f.siblingRegion, f.siblingReadingAt)

	// ── nonLeaf (non-leaf-depth plant, nearer ancestor appears later) ────
	f.nonLeafRoot = insertRegion("nfr1c-non-leaf-root", nil)
	f.nonLeafChild = insertRegion("nfr1c-non-leaf-child", regionPtr(f.nonLeafRoot))
	f.nonLeafRootPlant = insertPlant(f.nonLeafRoot, "nfr1c-non-leaf-root-plant")
	insertHistoryRow(f.nonLeafRootPlant, f.nonLeafRoot, longAgo, nil)
	f.nonLeafChildPlantOpensAt = now.Add(-90 * time.Minute)
	f.nonLeafReadingBeforeChildPlantID = insertReading(f.nonLeafChild, f.nonLeafChildPlantOpensAt.Add(-30*time.Minute))
	f.nonLeafChildPlant = insertPlant(f.nonLeafChild, "nfr1c-non-leaf-child-plant")
	insertHistoryRow(f.nonLeafChildPlant, f.nonLeafChild, f.nonLeafChildPlantOpensAt, nil)
	f.nonLeafReadingAfterChildPlantID = insertReading(f.nonLeafChild, f.nonLeafChildPlantOpensAt.Add(30*time.Minute))

	// ── sharedHour (FR21's migration-017 hour-outward snap) ──────────────
	f.sharedHourRegion = insertRegion("nfr1c-shared-hour", nil)
	f.sharedHourPlantOld = insertPlant(f.sharedHourRegion, "nfr1c-shared-hour-plant-old")
	f.sharedHourPlantNew = insertPlant(f.sharedHourRegion, "nfr1c-shared-hour-plant-new")
	hourStart := now.Add(-8 * time.Hour).Truncate(time.Hour)
	f.sharedHourTrueRemovalAt = hourStart.Add(20 * time.Minute)
	f.sharedHourSnappedValidTo = hourStart.Add(1 * time.Hour)
	insertHistoryRow(f.sharedHourPlantOld, f.sharedHourRegion, longAgo, &f.sharedHourSnappedValidTo)
	insertHistoryRow(f.sharedHourPlantNew, f.sharedHourRegion, f.sharedHourSnappedValidTo, nil)
	f.sharedHourReadingBeforeRemovalID = insertReading(f.sharedHourRegion, hourStart.Add(10*time.Minute))
	f.sharedHourReadingInSharedHourID = insertReading(f.sharedHourRegion, hourStart.Add(45*time.Minute))
	f.sharedHourReadingAfterSnapID = insertReading(f.sharedHourRegion, hourStart.Add(65*time.Minute))

	// ── stale (pre-FR73 stale-attribution window) ────────────────────────
	f.staleOldRegion = insertRegion("nfr1c-stale-old", nil)
	f.staleNewRegion = insertRegion("nfr1c-stale-new", nil)
	f.stalePlantOld = insertPlant(f.staleOldRegion, "nfr1c-stale-plant-old")
	f.stalePlantNew = insertPlant(f.staleNewRegion, "nfr1c-stale-plant-new")
	insertHistoryRow(f.stalePlantOld, f.staleOldRegion, longAgo, nil)
	insertHistoryRow(f.stalePlantNew, f.staleNewRegion, longAgo, nil)
	f.staleReadingAt = now.Add(-1 * time.Hour)
	// Deliberately stamped with staleOldRegion -- standing in for a reading
	// the processor stamped with a cached, stale region_id under FR73's
	// now-fixed defect. Both implementations key off this stored column, not
	// a second, independent "where is this sensor really" resolution, so
	// they must agree regardless of whether the stamp itself was stale.
	f.staleReadingID = insertReading(f.staleOldRegion, f.staleReadingAt)

	return db, f
}

// viewAttributedPlantIDs reads every plant_id v_sensor_reading_with_plant
// attributes to readingID (FR72's Grafana path) -- more than one row for a
// sibling fan-out reading.
func viewAttributedPlantIDs(t *testing.T, db *dbtest.Postgres, readingID int64) []int64 {
	t.Helper()
	ctx := context.Background()
	rows, err := db.Pool.Query(ctx, `
		SELECT plant_id FROM v_sensor_reading_with_plant WHERE reading_id = $1 AND plant_id IS NOT NULL ORDER BY plant_id
	`, readingID)
	if err != nil {
		t.Fatalf("query v_sensor_reading_with_plant for reading %d: %v", readingID, err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan v_sensor_reading_with_plant row for reading %d: %v", readingID, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate v_sensor_reading_with_plant rows for reading %d: %v", readingID, err)
	}
	return ids
}

// TestNFR1c_FixtureBuilds is a Scaffold-phase smoke test: it proves the
// shared fixture migrates and seeds cleanly, and that both NFR1.c
// implementations -- the view (FR72) and the API read path (FR71's
// leaflab/api/readings.Reader) -- are reachable and answer the fixture's
// simplest case (neverMoved) consistently. It is deliberately not the full
// NFR1.c comparison matrix; this task's Implementation phase adds that (see
// TestNFR1c_ViewAndAPIAgreeOnAttribution below and this task's
// Implementation section).
func TestNFR1c_FixtureBuilds(t *testing.T) {
	db, f := newNFR1CFixture(t)
	ctx := context.Background()

	viewPlants := viewAttributedPlantIDs(t, db, f.neverMovedReadingID)
	if len(viewPlants) != 1 || viewPlants[0] != f.neverMovedPlant {
		t.Fatalf("v_sensor_reading_with_plant for neverMoved reading %d = %v, want [%d]",
			f.neverMovedReadingID, viewPlants, f.neverMovedPlant)
	}

	reader := readings.NewReader(db.Pool)
	window := readings.Window{Start: f.neverMovedReadingAt.Add(-time.Minute), End: f.neverMovedReadingAt.Add(time.Minute)}
	result, err := reader.Series(ctx, authz.EntityRef{Kind: authz.EntityPlant, ID: f.neverMovedPlant}, window, 0, tiers.TierRaw, readings.Page{})
	if err != nil {
		t.Fatalf("Series(neverMoved plant %d): %v", f.neverMovedPlant, err)
	}
	if len(result.Points) != 1 {
		t.Fatalf("Series(neverMoved plant %d) len(Points) = %d, want 1", f.neverMovedPlant, len(result.Points))
	}
	if !result.Points[0].RecordedAt.Equal(f.neverMovedReadingAt) {
		t.Errorf("Series(neverMoved plant %d) RecordedAt = %s, want %s", f.neverMovedPlant, result.Points[0].RecordedAt, f.neverMovedReadingAt)
	}
}

// TestNFR1c_ViewAndAPIAgreeOnAttribution enumerates every fixture case this
// task's Scaffold section lists. Each is a skipped placeholder until this
// task's Implementation phase fills in the "compute attribution twice,
// assert identical" comparison this task's Implementation section
// describes -- t.Skip rather than silence so the roadmap for the next
// phase is visible directly in `bazel test` output, not only in this
// file's comments.
func TestNFR1c_ViewAndAPIAgreeOnAttribution(t *testing.T) {
	db, f := newNFR1CFixture(t)
	_ = db
	_ = f

	for _, name := range []string{
		"neverMoved: a plant that never moved",
		"midBucket: a plant that moved mid-bucket",
		"multiMove: a plant that moved through three regions",
		"sibling: two plants in one region, fan-out with no double-counting",
		"nonLeaf: a non-leaf-depth plant, with a nearer-ancestor plant appearing later",
		"sharedHour: a removed plant inside FR21's shared migration hour",
		"stale: a reading in the pre-FR73 stale-attribution window",
	} {
		t.Run(name, func(t *testing.T) {
			t.Skip("Implementation phase: wire up the view-vs-API attribution comparison for this case (NFR1.c)")
		})
	}
}
