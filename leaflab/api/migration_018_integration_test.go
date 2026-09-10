//go:build integration

// Real-Postgres coverage for migration 018
// (leaflab/migrate/schema/migrations/018_historical_region_rollup.up.sql) --
// the historical region roll-up (FR4): a reading rolls up under the region
// path as it existed at the reading's recorded_at, resolved by walking
// region_parent_history as-of recorded_at, never the region's current
// parents. Same pattern as migration_016/017_integration_test.go: schema
// setup runs the real migrations via newLeafLabTestPool, so these tests
// exercise the actual view SQL rather than a paraphrase of it.
//
// There are no triggers maintaining region_parent_history (migration 017 is
// schema only, and the FR3 write path lands later), so the helpers below
// model the production SCD2 close-and-open write path (AGENTS.md § SCD2)
// explicitly, with explicit timestamps so every attribution boundary is
// deterministic.
package main

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/leaflab/migrate/schema"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

// migration018SeedRegionAt inserts a region with the given parent (0 =
// top-level) AND its open region_parent_history row starting at validFrom --
// the state FR1 (#2311) guarantees: exactly one open history row per region
// from the moment it exists, NULL parent for top-level (recorded state, not
// an absence).
func migration018SeedRegionAt(t *testing.T, pool *pgxpool.Pool, name string, parentID int64, validFrom time.Time) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	var err error
	if parentID == 0 {
		err = pool.QueryRow(ctx,
			`INSERT INTO region (name) VALUES ($1) RETURNING region_id`, name).Scan(&id)
	} else {
		err = pool.QueryRow(ctx,
			`INSERT INTO region (parent_region_id, name) VALUES ($1, $2) RETURNING region_id`,
			parentID, name).Scan(&id)
	}
	if err != nil {
		t.Fatalf("seed region %s: %v", name, err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO region_parent_history (region_id, parent_region_id, valid_from)
		 VALUES ($1, $2, $3)`, id, nilIfZero(parentID), validFrom); err != nil {
		t.Fatalf("seed open region_parent_history for %s: %v", name, err)
	}
	return id
}

func nilIfZero(id int64) *int64 {
	if id == 0 {
		return nil
	}
	return &id
}

// migration018SeedPlacedSensor inserts a sensor already placed in regionID,
// with its open sensor_name_history row so v_sensor_current resolves a name.
func migration018SeedPlacedSensor(t *testing.T, pool *pgxpool.Pool, boardID, regionID int64, name string) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO sensor (board_id, sensor_type_id, region_id, name, unit)
		VALUES ($1, (SELECT sensor_type_id FROM sensor_type WHERE name = 'temperature'), $2, $3, 'degC')
		RETURNING sensor_id`, boardID, regionID, name).Scan(&id); err != nil {
		t.Fatalf("seed placed sensor %s: %v", name, err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO sensor_name_history (sensor_id, name) VALUES ($1, $2)`, id, name); err != nil {
		t.Fatalf("seed sensor_name_history for %s: %v", name, err)
	}
	return id
}

// migration018SeedReading inserts a reading whose region_id snapshot and
// recorded_at are both explicit -- the processor stamps region at insert time
// (FR10), and the tests control recorded_at precisely rather than relying on
// wall-clock NOW(). regionID may be nil for an unplaced sensor's reading.
func migration018SeedReading(t *testing.T, pool *pgxpool.Pool, sensorID int64, regionID *int64, recordedAt time.Time) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO sensor_reading (sensor_id, region_id, value, valid, uptime_s, recorded_at)
		VALUES ($1, $2, 1.0, TRUE, 1, $3) RETURNING reading_id`, sensorID, regionID, recordedAt).Scan(&id); err != nil {
		t.Fatalf("seed reading for sensor %d: %v", sensorID, err)
	}
	return id
}

// migration018ReparentRegion moves regionID under newParentID (0 = top-level)
// at instant `at`, using the SCD2 close-and-open write path on
// region_parent_history plus the current-tree pointer update on region --
// what FR3's re-parent flow does. `at` must not precede the region's seed
// valid_from.
func migration018ReparentRegion(t *testing.T, pool *pgxpool.Pool, regionID, newParentID int64, at time.Time) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`UPDATE region SET parent_region_id = $2 WHERE region_id = $1`, regionID, nilIfZero(newParentID)); err != nil {
		t.Fatalf("re-parent region %d in region: %v", regionID, err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE region_parent_history SET valid_to = $2 WHERE region_id = $1 AND valid_to IS NULL`,
		regionID, at); err != nil {
		t.Fatalf("close open region_parent_history for region %d: %v", regionID, err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO region_parent_history (region_id, parent_region_id, valid_from)
		 VALUES ($1, $2, $3)`, regionID, nilIfZero(newParentID), at); err != nil {
		t.Fatalf("open new region_parent_history for region %d: %v", regionID, err)
	}
}

// migration018Path is the region attribution v_sensor_reading_enriched
// reports for one reading.
type migration018Path struct {
	regionID   *int64
	regionName *string
	pathIDs    []int64
	pathNames  []string
	pathName   *string
}

// migration018EnrichedPath reads one reading's region attribution off
// v_sensor_reading_enriched.
func migration018EnrichedPath(t *testing.T, pool *pgxpool.Pool, readingID int64) migration018Path {
	t.Helper()
	var p migration018Path
	err := pool.QueryRow(context.Background(), `
		SELECT region_id, region_name, region_path_ids, region_path_names, region_path_name
		FROM v_sensor_reading_enriched
		WHERE reading_id = $1`, readingID).Scan(
		&p.regionID, &p.regionName, &p.pathIDs, &p.pathNames, &p.pathName)
	if err != nil {
		t.Fatalf("read v_sensor_reading_enriched for reading %d: %v", readingID, err)
	}
	return p
}

// migration018AssertPath asserts got carries exactly the expected attribution.
func migration018AssertPath(t *testing.T, label string, got migration018Path, wantRegionName string, wantIDs []int64, wantNames []string) {
	t.Helper()
	if got.regionName == nil || *got.regionName != wantRegionName {
		t.Fatalf("%s: expected region_name %q, got %v", label, wantRegionName, got.regionName)
	}
	if len(got.pathIDs) != len(wantIDs) {
		t.Fatalf("%s: expected region_path_ids %v, got %v", label, wantIDs, got.pathIDs)
	}
	for i := range wantIDs {
		if got.pathIDs[i] != wantIDs[i] {
			t.Fatalf("%s: expected region_path_ids %v, got %v", label, wantIDs, got.pathIDs)
		}
	}
	if len(got.pathNames) != len(wantNames) {
		t.Fatalf("%s: expected region_path_names %v, got %v", label, wantNames, got.pathNames)
	}
	for i := range wantNames {
		if got.pathNames[i] != wantNames[i] {
			t.Fatalf("%s: expected region_path_names %v, got %v", label, wantNames, got.pathNames)
		}
	}
	wantPathName := strings.Join(wantNames, " / ")
	if got.pathName == nil || *got.pathName != wantPathName {
		t.Fatalf("%s: expected region_path_name %q, got %v", label, wantPathName, got.pathName)
	}
}

// migration018OpenHistoryRow returns the parent (nil = top-level) of the open
// region_parent_history row for regionID, failing unless exactly one exists.
func migration018OpenHistoryRow(t *testing.T, pool *pgxpool.Pool, regionID int64) *int64 {
	t.Helper()
	var count int
	var parent *int64
	if err := pool.QueryRow(context.Background(), `
		SELECT COUNT(*), MIN(parent_region_id)
		FROM region_parent_history
		WHERE region_id = $1 AND valid_to IS NULL`, regionID).Scan(&count, &parent); err != nil {
		t.Fatalf("count open region_parent_history rows for region %d: %v", regionID, err)
	}
	if count != 1 {
		t.Fatalf("region %d: expected exactly one open region_parent_history row, got %d", regionID, count)
	}
	return parent
}

// TestMigration018_ReparentDoesNotRewritePreExistingReadings is FR4's core
// guarantee: after shelf-1 is re-parented from campus > shelf-1 to
// annex > greenhouse-b > shelf-1, a reading recorded before the re-parent
// still rolls up under the path as it existed at its recorded_at, while a
// reading recorded after it rolls up under the new (multi-hop) path -- both
// off the same region_id snapshot.
func TestMigration018_ReparentDoesNotRewritePreExistingReadings(t *testing.T) {
	pool := newLeafLabTestPool(t)
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Second)
	tSeed := base.Add(-3 * time.Hour)
	tOld := base.Add(-2 * time.Hour)
	tReparent := base.Add(-1 * time.Hour)
	tNew := base

	// Tree before: campus > shelf-1. Move target tree: annex > greenhouse-b.
	campusID := migration018SeedRegionAt(t, pool, "m018-campus", 0, tSeed)
	annexID := migration018SeedRegionAt(t, pool, "m018-annex", 0, tSeed)
	ghbID := migration018SeedRegionAt(t, pool, "m018-greenhouse-b", annexID, tSeed)
	shelfID := migration018SeedRegionAt(t, pool, "m018-shelf-1", campusID, tSeed)

	boardID := seedBoard(t, pool, "board-m018-reparent")
	sensorID := migration018SeedPlacedSensor(t, pool, boardID, shelfID, "m018-temp-reparent")

	oldReadingID := migration018SeedReading(t, pool, sensorID, &shelfID, tOld)

	// FR3 re-parent: shelf-1 moves from campus to greenhouse-b at tReparent.
	migration018ReparentRegion(t, pool, shelfID, ghbID, tReparent)

	newReadingID := migration018SeedReading(t, pool, sensorID, &shelfID, tNew)

	// Pre-re-parent reading: unchanged roll-up under the old path.
	old := migration018EnrichedPath(t, pool, oldReadingID)
	migration018AssertPath(t, "pre-re-parent reading", old, "m018-shelf-1",
		[]int64{campusID, shelfID}, []string{"m018-campus", "m018-shelf-1"})

	// Post-re-parent reading: multi-hop ancestor walk as of its recorded_at.
	got := migration018EnrichedPath(t, pool, newReadingID)
	migration018AssertPath(t, "post-re-parent reading", got, "m018-shelf-1",
		[]int64{annexID, ghbID, shelfID},
		[]string{"m018-annex", "m018-greenhouse-b", "m018-shelf-1"})

	// FR1 invariant still holds after the re-parent: exactly one open
	// history row, pointing at the new parent.
	if parent := migration018OpenHistoryRow(t, pool, shelfID); parent == nil || *parent != ghbID {
		t.Fatalf("expected shelf-1's open history row to point at greenhouse-b (%d), got %v", ghbID, parent)
	}

	// Layering: v_sensor_reading_with_plant inherits the historical path --
	// it must not re-resolve region paths against the current tree.
	var withPlantPath string
	if err := pool.QueryRow(ctx, `
		SELECT region_path_name FROM v_sensor_reading_with_plant
		WHERE reading_id = $1`, oldReadingID).Scan(&withPlantPath); err != nil {
		t.Fatalf("read v_sensor_reading_with_plant for pre-re-parent reading: %v", err)
	}
	if withPlantPath != "m018-campus / m018-shelf-1" {
		t.Fatalf("v_sensor_reading_with_plant should inherit the historical roll-up for the pre-re-parent reading, got %q", withPlantPath)
	}
}

// TestMigration018_ReparentToTopLevel_TerminatesOnNullParentOpenRow proves
// the ancestor walk terminates on an open history row whose parent_region_id
// IS NULL -- recorded top-level-ness (#2311), never on a missing row. After
// shelf-1 becomes top-level, readings from then on resolve to the region
// alone (single-element path); earlier readings keep the pre-move path.
func TestMigration018_ReparentToTopLevel_TerminatesOnNullParentOpenRow(t *testing.T) {
	pool := newLeafLabTestPool(t)

	base := time.Now().UTC().Truncate(time.Second)
	tSeed := base.Add(-3 * time.Hour)
	tOld := base.Add(-2 * time.Hour)
	tReparent := base.Add(-1 * time.Hour)
	tNew := base

	campusID := migration018SeedRegionAt(t, pool, "m018-campus-top", 0, tSeed)
	shelfID := migration018SeedRegionAt(t, pool, "m018-shelf-top", campusID, tSeed)

	boardID := seedBoard(t, pool, "board-m018-toplevel")
	sensorID := migration018SeedPlacedSensor(t, pool, boardID, shelfID, "m018-temp-top")

	oldReadingID := migration018SeedReading(t, pool, sensorID, &shelfID, tOld)

	// Make shelf top-level at tReparent: close its open row and open one
	// with a recorded NULL parent.
	migration018ReparentRegion(t, pool, shelfID, 0, tReparent)

	newReadingID := migration018SeedReading(t, pool, sensorID, &shelfID, tNew)

	// The walk from shelf at tNew must terminate on shelf's own NULL-parent
	// open row: path is shelf alone, not campus/shelf, and not an error.
	got := migration018EnrichedPath(t, pool, newReadingID)
	migration018AssertPath(t, "post-top-level reading", got, "m018-shelf-top",
		[]int64{shelfID}, []string{"m018-shelf-top"})

	// Pre-move readings are untouched.
	old := migration018EnrichedPath(t, pool, oldReadingID)
	migration018AssertPath(t, "pre-re-parent reading", old, "m018-shelf-top",
		[]int64{campusID, shelfID}, []string{"m018-campus-top", "m018-shelf-top"})
}

// TestMigration018_BoundaryReadingAtReparentInstantTakesNewParent pins the
// half-open interval the walk filters on
// (valid_from <= recorded_at AND (valid_to IS NULL OR valid_to > recorded_at)):
// a reading recorded exactly at the re-parent instant resolves through the
// NEW open row (the old row's valid_to is not > recorded_at), while a reading
// one second earlier still resolves through the old row.
func TestMigration018_BoundaryReadingAtReparentInstantTakesNewParent(t *testing.T) {
	pool := newLeafLabTestPool(t)

	base := time.Now().UTC().Truncate(time.Second)
	tSeed := base.Add(-3 * time.Hour)
	tReparent := base.Add(-1 * time.Hour)

	campusID := migration018SeedRegionAt(t, pool, "m018-campus-edge", 0, tSeed)
	annexID := migration018SeedRegionAt(t, pool, "m018-annex-edge", 0, tSeed)
	shelfID := migration018SeedRegionAt(t, pool, "m018-shelf-edge", campusID, tSeed)

	boardID := seedBoard(t, pool, "board-m018-boundary")
	sensorID := migration018SeedPlacedSensor(t, pool, boardID, shelfID, "m018-temp-boundary")

	beforeID := migration018SeedReading(t, pool, sensorID, &shelfID, tReparent.Add(-time.Second))

	migration018ReparentRegion(t, pool, shelfID, annexID, tReparent)

	boundaryID := migration018SeedReading(t, pool, sensorID, &shelfID, tReparent)

	before := migration018EnrichedPath(t, pool, beforeID)
	migration018AssertPath(t, "reading 1s before the re-parent instant", before, "m018-shelf-edge",
		[]int64{campusID, shelfID}, []string{"m018-campus-edge", "m018-shelf-edge"})

	boundary := migration018EnrichedPath(t, pool, boundaryID)
	migration018AssertPath(t, "reading exactly at the re-parent instant", boundary, "m018-shelf-edge",
		[]int64{annexID, shelfID}, []string{"m018-annex-edge", "m018-shelf-edge"})
}

// TestMigration018_RegionPathViewStaysCurrentTree proves v_region_path is
// UNCHANGED by migration 018: it resolves the current tree (FR6's tree /
// drill-down view), one row per region, with the re-parented region showing
// its new path and the historical path nowhere in the view.
func TestMigration018_RegionPathViewStaysCurrentTree(t *testing.T) {
	pool := newLeafLabTestPool(t)
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Second)
	tSeed := base.Add(-3 * time.Hour)
	tReparent := base.Add(-1 * time.Hour)

	campusID := migration018SeedRegionAt(t, pool, "m018-campus-path", 0, tSeed)
	annexID := migration018SeedRegionAt(t, pool, "m018-annex-path", 0, tSeed)
	shelfID := migration018SeedRegionAt(t, pool, "m018-shelf-path", campusID, tSeed)

	migration018ReparentRegion(t, pool, shelfID, annexID, tReparent)

	var regionCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM region`).Scan(&regionCount); err != nil {
		t.Fatalf("count regions: %v", err)
	}

	rows, err := pool.Query(ctx, `SELECT region_id, path_name, depth FROM v_region_path`)
	if err != nil {
		t.Fatalf("query v_region_path: %v", err)
	}
	defer rows.Close()
	got := map[int64]struct {
		name  string
		depth int
	}{}
	for rows.Next() {
		var id int64
		var name string
		var depth int
		if err := rows.Scan(&id, &name, &depth); err != nil {
			t.Fatalf("scan v_region_path row: %v", err)
		}
		got[id] = struct {
			name  string
			depth int
		}{name, depth}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate v_region_path: %v", err)
	}

	// Exactly one row per region -- no historical duplicates.
	if len(got) != regionCount {
		t.Fatalf("v_region_path must return one row per region (%d), got %d", regionCount, len(got))
	}
	want := map[int64]struct {
		name  string
		depth int
	}{
		campusID: {"m018-campus-path", 0},
		annexID:  {"m018-annex-path", 0},
		shelfID:  {"m018-annex-path / m018-shelf-path", 1},
	}
	for id, w := range want {
		g, ok := got[id]
		if !ok {
			t.Fatalf("v_region_path is missing region %d entirely", id)
		}
		if g.name != w.name || g.depth != w.depth {
			t.Fatalf("v_region_path for region %d: expected (%q, depth %d), got (%q, depth %d)",
				id, w.name, w.depth, g.name, g.depth)
		}
	}

	// The pre-re-parent path must not exist anywhere in the current-tree view.
	for id, g := range got {
		if g.name == "m018-campus-path / m018-shelf-path" {
			t.Fatalf("v_region_path must keep current-tree semantics; region %d still exposes the pre-re-parent path %q", id, g.name)
		}
	}
}

// TestMigration018_ReadingWithoutHistoryRowFallsBackToSnapshotRegion pins the
// documented fallback for a region with no region_parent_history row covering
// recorded_at (e.g. readings recorded before migration 017's backfill): the
// reading resolves to its snapshot region alone, with no ancestors and no
// error. A reading with a NULL region (unplaced sensor) keeps its row with
// NULL region fields -- the LEFT JOIN anchor never drops readings.
func TestMigration018_ReadingWithoutHistoryRowFallsBackToSnapshotRegion(t *testing.T) {
	pool := newLeafLabTestPool(t)
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Second)
	tSeed := base.Add(-3 * time.Hour)
	tReading := base.Add(-2 * time.Hour)

	campusID := migration018SeedRegionAt(t, pool, "m018-campus-fallback", 0, tSeed)

	// A region with a current parent but no history rows at all -- pre-017
	// backfill-shaped data, inserted directly (no trigger maintains history).
	var orphanID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO region (parent_region_id, name) VALUES ($1, 'm018-orphan')
		RETURNING region_id`, campusID).Scan(&orphanID); err != nil {
		t.Fatalf("seed orphan region: %v", err)
	}

	boardID := seedBoard(t, pool, "board-m018-fallback")
	sensorID := migration018SeedPlacedSensor(t, pool, boardID, orphanID, "m018-temp-fallback")

	orphanReadingID := migration018SeedReading(t, pool, sensorID, &orphanID, tReading)

	got := migration018EnrichedPath(t, pool, orphanReadingID)
	migration018AssertPath(t, "reading in region without history rows", got, "m018-orphan",
		[]int64{orphanID}, []string{"m018-orphan"})

	// Unplaced reading (NULL region snapshot): row survives with NULL region
	// fields, exactly as the previous v_region_path LEFT JOIN behaved.
	unplacedID := migration018SeedReading(t, pool, sensorID, nil, tReading)
	var exists bool
	var regionFieldsNull bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM v_sensor_reading_enriched WHERE reading_id = $1
		), EXISTS (
			SELECT 1 FROM v_sensor_reading_enriched
			WHERE reading_id = $1
			  AND region_id IS NULL AND region_name IS NULL
			  AND region_path_ids IS NULL AND region_path_names IS NULL
			  AND region_path_name IS NULL
		)`, unplacedID).Scan(&exists, &regionFieldsNull); err != nil {
		t.Fatalf("read unplaced reading from v_sensor_reading_enriched: %v", err)
	}
	if !exists {
		t.Fatalf("unplaced reading %d must still appear in v_sensor_reading_enriched (LEFT JOIN, not dropped)", unplacedID)
	}
	if !regionFieldsNull {
		t.Fatalf("unplaced reading %d must have NULL region_id/region_name/region_path_* fields", unplacedID)
	}
}

// TestMigration018_UpDownRoundTrip drives the runner itself (bare Postgres →
// 17 → 18 → 17 → 18) and pins both directions of migration 018: up gives
// readings the historical as-of-recorded_at roll-up, down restores the 012
// current-tree join (a pre-re-parent reading then shows the CURRENT path),
// and re-up restores the historical behaviour.
func TestMigration018_UpDownRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Image: leaflabTimescaleImage})
	dbSQL, err := sql.Open("pgx", db.ConnString)
	if err != nil {
		t.Fatalf("open sql handle: %v", err)
	}
	t.Cleanup(func() { _ = dbSQL.Close() })
	runner := migrate.NewRunner(dbSQL, schema.Migrations, schema.Dir)

	if err := runner.Migrate(17); err != nil {
		t.Fatalf("migrate to 017: %v", err)
	}

	base := time.Now().UTC().Truncate(time.Second)
	tSeed := base.Add(-3 * time.Hour)
	tOld := base.Add(-2 * time.Hour)
	tReparent := base.Add(-1 * time.Hour)
	tNew := base

	// Seed through db.Pool (the pgxpool over the same connection string) so
	// the pool-based helpers are shared with the newLeafLabTestPool tests.
	campusID := migration018SeedRegionAt(t, db.Pool, "m018-campus-rt", 0, tSeed)
	annexID := migration018SeedRegionAt(t, db.Pool, "m018-annex-rt", 0, tSeed)
	shelfID := migration018SeedRegionAt(t, db.Pool, "m018-shelf-rt", campusID, tSeed)

	boardID := seedBoard(t, db.Pool, "board-m018-roundtrip")
	sensorID := migration018SeedPlacedSensor(t, db.Pool, boardID, shelfID, "m018-temp-rt")

	oldReadingID := migration018SeedReading(t, db.Pool, sensorID, &shelfID, tOld)

	migration018ReparentRegion(t, db.Pool, shelfID, annexID, tReparent)

	newReadingID := migration018SeedReading(t, db.Pool, sensorID, &shelfID, tNew)

	pathAt := func(label string, readingID int64) string {
		t.Helper()
		var pathName *string
		if err := db.Pool.QueryRow(ctx, `
			SELECT region_path_name FROM v_sensor_reading_enriched WHERE reading_id = $1`,
			readingID).Scan(&pathName); err != nil {
			t.Fatalf("%s: read v_sensor_reading_enriched for reading %d: %v", label, readingID, err)
		}
		if pathName == nil {
			t.Fatalf("%s: reading %d has NULL region_path_name", label, readingID)
		}
		return *pathName
	}

	// Up at 018: historical attribution.
	if err := runner.Migrate(18); err != nil {
		t.Fatalf("migrate up to 018: %v", err)
	}
	if got := pathAt("018 up, pre-re-parent reading", oldReadingID); got != "m018-campus-rt / m018-shelf-rt" {
		t.Fatalf("018 up: pre-re-parent reading should keep its historical path, got %q", got)
	}
	if got := pathAt("018 up, post-re-parent reading", newReadingID); got != "m018-annex-rt / m018-shelf-rt" {
		t.Fatalf("018 up: post-re-parent reading should take the new path, got %q", got)
	}

	// Down to 017: the 012-definition views are back, and the same
	// pre-re-parent reading now rolls up under the CURRENT tree.
	if err := runner.Migrate(17); err != nil {
		t.Fatalf("migrate down to 017: %v", err)
	}
	version, dirty, verr := runner.Version()
	if verr != nil || version != 17 || dirty {
		t.Fatalf("expected clean version 17 after down, got version=%d dirty=%v err=%v", version, dirty, verr)
	}
	if got := pathAt("018 down, pre-re-parent reading", oldReadingID); got != "m018-annex-rt / m018-shelf-rt" {
		t.Fatalf("018 down: with the 012 views restored, the pre-re-parent reading should follow the CURRENT tree, got %q", got)
	}

	// Re-up: historical behaviour returns.
	if err := runner.Migrate(18); err != nil {
		t.Fatalf("re-migrate up to 018: %v", err)
	}
	if got := pathAt("018 re-up, pre-re-parent reading", oldReadingID); got != "m018-campus-rt / m018-shelf-rt" {
		t.Fatalf("018 re-up: pre-re-parent reading should keep its historical path again, got %q", got)
	}
	if got := pathAt("018 re-up, boundary reading at tNew", newReadingID); got != "m018-annex-rt / m018-shelf-rt" {
		t.Fatalf("018 re-up: post-re-parent reading should take the new path again, got %q", got)
	}
}
