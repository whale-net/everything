//go:build integration

// Real-Postgres integration coverage for migration 017_plant_region_history's
// schema, attribution-neutral snapped-to-hour backfill and database-side
// no-back-dating guard (FR19, FR21, NFR6.1, NFR6.2). Seeds pre-existing
// plant/sensor_reading rows that predate the history table against real
// migrations 001..015, applies 017, and asserts the backfill's
// post-conditions -- something a unit test reading the SQL cannot verify.
// See //libs/go/dbtest's README for how to run tests like this one, and
// ownership_migration_integration_test.go for the sibling pattern this file
// follows.
package main_test

import (
	"context"
	"database/sql"
	"embed"
	"reflect"
	"sort"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

//go:embed migrations/*.sql
var regionHistoryMigrations embed.FS

// regionHistoryTimescaleImage matches timescaleImage in
// ownership_migration_integration_test.go -- migration 001's
// create_hypertable call needs the real TimescaleDB image, not plain
// postgres. Declared again here (rather than reused) because Bazel test
// targets for this file and the ownership one do not always share
// compilation.
const regionHistoryTimescaleImage = "timescale/timescaledb:latest-pg16"

// regionHistoryFixture is the pre-migration-017 plant/reading state seeded
// by newPreRegionHistoryDB, plus the anchor time everything else is computed
// relative to and the "old rule" attribution the test then asserts is
// preserved.
type regionHistoryFixture struct {
	regionA, regionB, regionC int64
	plantTypeID      int64

	// anchor is a fixed past hour boundary (2024-03-11 10:00:00 UTC) that
	// every timestamp below is computed relative to, so assertions never
	// depend on the wall-clock hour at test-run time.
	anchor time.Time

	// plantWithReadings: created at anchor, in regionA, with one reading at
	// anchor+75m (an odd, non-hour-aligned offset) so backfill must snap the
	// earliest-reading time down to anchor+1h, not just reuse created_at.
	// Still present (removed_at NULL).
	plantWithReadings int64

	// plantNoReadings: created at anchor+37m (also non-hour-aligned), in its
	// own regionC (a region can host multiple simultaneously-active plants,
	// so putting it in regionA alongside plantWithReadings would let
	// plantWithReadings' reading fan out onto this plant too -- regionC has
	// zero readings ever recorded, so there is nothing to attribute).
	// Backfill must fall back to created_at, snapped to anchor (the hour
	// containing anchor+37m). Still present.
	plantNoReadings int64

	// plantRemoved: created at anchor, in regionB, with one reading at
	// anchor+30m, removed at anchor+4h20m (the "14:20" example from FR21's
	// disclosed-cost text, relative to anchor's "10:00"). Backfill must snap
	// valid_from to anchor+0h (hour of the reading) and valid_to to
	// anchor+5h (end of the hour bucket containing removed_at).
	plantRemoved int64

	// successorPlant: created at anchor+4h40m (inside the same
	// anchor+4h..anchor+5h hour bucket plantRemoved's valid_to snaps into),
	// in regionB (same region as plantRemoved), no readings. This is FR21's
	// disclosed cost made concrete: successorPlant's backfilled valid_from
	// (anchor+4h) falls before plantRemoved's backfilled valid_to
	// (anchor+5h), so the two share the anchor+4h bucket.
	successorPlant int64
}

// newPreRegionHistoryDB migrates up through 015 (the last migration before
// 017 -- there is no 016 on this branch, see migration 017's up.sql doc
// comment), seeds plant/sensor_reading rows that predate plant_region_history,
// and returns the runner (still positioned at 015) plus a pool for fixture
// setup and assertions.
func newPreRegionHistoryDB(t *testing.T) (*migrate.Runner, *dbtest.Postgres, regionHistoryFixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Image: regionHistoryTimescaleImage})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := sqlDB.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}

	runner := migrate.NewRunner(sqlDB, regionHistoryMigrations, "migrations")
	if err := runner.Migrate(15); err != nil {
		t.Fatalf("migrate to version 15 (pre-plant_region_history): %v", err)
	}

	// Migration 015 (ownership) made plant.household_id NOT NULL; every
	// plant this fixture seeds must carry one. The migration itself already
	// seeded the singleton Unadopted household, so reuse it rather than
	// creating a second one.
	var unadoptedHouseholdID int64
	if err := db.Pool.QueryRow(ctx, `SELECT household_id FROM household WHERE is_unadopted = TRUE`).Scan(&unadoptedHouseholdID); err != nil {
		t.Fatalf("read Unadopted household_id: %v", err)
	}

	f := regionHistoryFixture{
		anchor: time.Date(2024, 3, 11, 10, 0, 0, 0, time.UTC),
	}

	mustExec := func(dest *int64, query string, args ...any) {
		t.Helper()
		if err := db.Pool.QueryRow(ctx, query, args...).Scan(dest); err != nil {
			t.Fatalf("fixture setup %q: %v", query, err)
		}
	}

	mustExec(&f.regionA, `INSERT INTO region (name) VALUES ($1) RETURNING region_id`, "region-a")
	mustExec(&f.regionB, `INSERT INTO region (name) VALUES ($1) RETURNING region_id`, "region-b")
	mustExec(&f.regionC, `INSERT INTO region (name) VALUES ($1) RETURNING region_id`, "region-c")
	mustExec(&f.plantTypeID, `INSERT INTO plant_type (common_name) VALUES ($1) RETURNING plant_type_id`, "test-plant-type")

	var boardID, sensorTypeID, sensorID int64
	mustExec(&boardID, `INSERT INTO board (device_id) VALUES ($1) RETURNING board_id`, "pre-region-history-board")
	if err := db.Pool.QueryRow(ctx, `SELECT sensor_type_id FROM sensor_type LIMIT 1`).Scan(&sensorTypeID); err != nil {
		t.Fatalf("read seeded sensor_type: %v", err)
	}
	mustExec(&sensorID, `
		INSERT INTO sensor (board_id, sensor_type_id, region_id, name, unit) VALUES ($1, $2, $3, $4, $5) RETURNING sensor_id
	`, boardID, sensorTypeID, f.regionA, "test-sensor", "degC")

	insertReading := func(regionID int64, recordedAt time.Time) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, `
			INSERT INTO sensor_reading (sensor_id, region_id, value, uptime_s, recorded_at) VALUES ($1, $2, $3, $4, $5)
		`, sensorID, regionID, 21.5, 1000, recordedAt); err != nil {
			t.Fatalf("insert sensor_reading at %v: %v", recordedAt, err)
		}
	}

	insertPlant := func(regionID int64, name string, createdAt time.Time, removedAt *time.Time) int64 {
		t.Helper()
		var id int64
		if err := db.Pool.QueryRow(ctx, `
			INSERT INTO plant (region_id, plant_type_id, name, created_at, removed_at, household_id) VALUES ($1, $2, $3, $4, $5, $6) RETURNING plant_id
		`, regionID, f.plantTypeID, name, createdAt, removedAt, unadoptedHouseholdID).Scan(&id); err != nil {
			t.Fatalf("insert plant %s: %v", name, err)
		}
		return id
	}

	f.plantWithReadings = insertPlant(f.regionA, "plant-with-readings", f.anchor, nil)
	insertReading(f.regionA, f.anchor.Add(75*time.Minute))

	f.plantNoReadings = insertPlant(f.regionC, "plant-no-readings", f.anchor.Add(37*time.Minute), nil)

	removedAt := f.anchor.Add(4*time.Hour + 20*time.Minute)
	f.plantRemoved = insertPlant(f.regionB, "plant-removed", f.anchor, &removedAt)
	insertReading(f.regionB, f.anchor.Add(30*time.Minute))

	f.successorPlant = insertPlant(f.regionB, "successor-plant", f.anchor.Add(4*time.Hour+40*time.Minute), nil)

	return runner, db, f
}

// TestPlantRegionHistoryMigration_BackfillCompleteness is the Testing
// section's "every plant has exactly one interval with valid_to IS NULL, or
// a closed final interval if already removed. Zero plants have no interval."
func TestPlantRegionHistoryMigration_BackfillCompleteness(t *testing.T) {
	runner, db, f := newPreRegionHistoryDB(t)
	ctx := context.Background()

	if err := runner.Steps(1); err != nil {
		t.Fatalf("apply migration 017: %v", err)
	}

	var plantsWithNoInterval int
	if err := db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM plant p
		WHERE NOT EXISTS (SELECT 1 FROM plant_region_history h WHERE h.plant_id = p.plant_id)
	`).Scan(&plantsWithNoInterval); err != nil {
		t.Fatalf("count plants with no interval: %v", err)
	}
	if plantsWithNoInterval != 0 {
		t.Errorf("plants with no plant_region_history interval = %d, want 0", plantsWithNoInterval)
	}

	for name, tc := range map[string]struct {
		plantID    int64
		wantClosed bool
	}{
		"plant with readings, still present": {f.plantWithReadings, false},
		"plant with no readings, still present": {f.plantNoReadings, false},
		"removed plant":                       {f.plantRemoved, true},
		"successor plant, still present":      {f.successorPlant, false},
	} {
		t.Run(name, func(t *testing.T) {
			var intervalCount int
			if err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM plant_region_history WHERE plant_id = $1`, tc.plantID).Scan(&intervalCount); err != nil {
				t.Fatalf("count intervals for %s: %v", name, err)
			}
			if intervalCount != 1 {
				t.Fatalf("interval count for %s = %d, want exactly 1", name, intervalCount)
			}

			var validTo *time.Time
			if err := db.Pool.QueryRow(ctx, `SELECT valid_to FROM plant_region_history WHERE plant_id = $1`, tc.plantID).Scan(&validTo); err != nil {
				t.Fatalf("read valid_to for %s: %v", name, err)
			}
			gotClosed := validTo != nil
			if gotClosed != tc.wantClosed {
				t.Errorf("%s: interval closed (valid_to set) = %v, want %v", name, gotClosed, tc.wantClosed)
			}
		})
	}
}

// TestPlantRegionHistoryMigration_BackfillSnapsToHourBoundaries proves the
// specific outward-snapping arithmetic in FR21: the earliest attributed
// reading (or created_at, when there is none) determines valid_from,
// truncated down to the containing hour; removed_at determines valid_to,
// truncated up to the end of its containing hour.
func TestPlantRegionHistoryMigration_BackfillSnapsToHourBoundaries(t *testing.T) {
	runner, db, f := newPreRegionHistoryDB(t)
	ctx := context.Background()

	if err := runner.Steps(1); err != nil {
		t.Fatalf("apply migration 017: %v", err)
	}

	readInterval := func(plantID int64) (validFrom time.Time, validTo *time.Time) {
		t.Helper()
		if err := db.Pool.QueryRow(ctx, `SELECT valid_from, valid_to FROM plant_region_history WHERE plant_id = $1`, plantID).Scan(&validFrom, &validTo); err != nil {
			t.Fatalf("read interval for plant %d: %v", plantID, err)
		}
		return validFrom, validTo
	}

	// plantWithReadings: earliest reading at anchor+75m -> valid_from snaps
	// down to anchor+1h (11:00), not anchor (created_at's hour) and not the
	// unsnapped anchor+75m.
	{
		validFrom, validTo := readInterval(f.plantWithReadings)
		want := f.anchor.Add(1 * time.Hour)
		if !validFrom.Equal(want) {
			t.Errorf("plantWithReadings.valid_from = %v, want %v (hour containing the earliest reading)", validFrom, want)
		}
		if validTo != nil {
			t.Errorf("plantWithReadings.valid_to = %v, want NULL (still present)", *validTo)
		}
	}

	// plantNoReadings: no readings -> falls back to created_at (anchor+37m),
	// hour-snapped down to anchor (10:00).
	{
		validFrom, validTo := readInterval(f.plantNoReadings)
		want := f.anchor
		if !validFrom.Equal(want) {
			t.Errorf("plantNoReadings.valid_from = %v, want %v (hour containing created_at, no readings to consider)", validFrom, want)
		}
		if validTo != nil {
			t.Errorf("plantNoReadings.valid_to = %v, want NULL (still present)", *validTo)
		}
	}

	// plantRemoved: earliest reading at anchor+30m -> valid_from snaps down
	// to anchor (10:00). removed_at = anchor+4h20m -> valid_to snaps up to
	// anchor+5h (end of the 14:00-15:00 bucket containing removal).
	{
		validFrom, validTo := readInterval(f.plantRemoved)
		wantFrom := f.anchor
		if !validFrom.Equal(wantFrom) {
			t.Errorf("plantRemoved.valid_from = %v, want %v", validFrom, wantFrom)
		}
		if validTo == nil {
			t.Fatal("plantRemoved.valid_to = NULL, want a closed interval")
		}
		wantTo := f.anchor.Add(5 * time.Hour)
		if !validTo.Equal(wantTo) {
			t.Errorf("plantRemoved.valid_to = %v, want %v (end of the hour bucket containing removed_at)", *validTo, wantTo)
		}
	}
}

// TestPlantRegionHistoryMigration_DisclosedCostSharedBucket asserts FR21's
// explicitly-disclosed cost as a tested fact: a plant removed mid-hour and
// its successor occupying the same region share the hour bucket the removal
// fell into -- plantRemoved's valid_to (end of that bucket) lands strictly
// after successorPlant's valid_from (start of that same bucket), so both
// intervals are simultaneously "open" to region B for part of the bucket.
// FR26.2's marker keys on exactly this overlap.
func TestPlantRegionHistoryMigration_DisclosedCostSharedBucket(t *testing.T) {
	runner, db, f := newPreRegionHistoryDB(t)
	ctx := context.Background()

	if err := runner.Steps(1); err != nil {
		t.Fatalf("apply migration 017: %v", err)
	}

	var removedValidTo time.Time
	if err := db.Pool.QueryRow(ctx, `SELECT valid_to FROM plant_region_history WHERE plant_id = $1`, f.plantRemoved).Scan(&removedValidTo); err != nil {
		t.Fatalf("read plantRemoved.valid_to: %v", err)
	}
	var successorValidFrom time.Time
	if err := db.Pool.QueryRow(ctx, `SELECT valid_from FROM plant_region_history WHERE plant_id = $1`, f.successorPlant).Scan(&successorValidFrom); err != nil {
		t.Fatalf("read successorPlant.valid_from: %v", err)
	}

	wantBucketStart := f.anchor.Add(4 * time.Hour)
	if !successorValidFrom.Equal(wantBucketStart) {
		t.Fatalf("successorPlant.valid_from = %v, want %v (start of the shared hour bucket)", successorValidFrom, wantBucketStart)
	}
	wantBucketEnd := f.anchor.Add(5 * time.Hour)
	if !removedValidTo.Equal(wantBucketEnd) {
		t.Fatalf("plantRemoved.valid_to = %v, want %v (end of the shared hour bucket)", removedValidTo, wantBucketEnd)
	}

	if !successorValidFrom.Before(removedValidTo) {
		t.Errorf("successorPlant.valid_from (%v) is not before plantRemoved.valid_to (%v): the disclosed cost (shared bucket) does not hold", successorValidFrom, removedValidTo)
	}
}

// TestPlantRegionHistoryMigration_StraddleFreePostCondition asserts the
// Testing section's straddle-free check over every row: no interval boundary
// falls strictly inside an hourly bucket.
func TestPlantRegionHistoryMigration_StraddleFreePostCondition(t *testing.T) {
	runner, db, _ := newPreRegionHistoryDB(t)
	ctx := context.Background()

	if err := runner.Steps(1); err != nil {
		t.Fatalf("apply migration 017: %v", err)
	}

	var straddlingFrom, straddlingTo int
	if err := db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM plant_region_history WHERE valid_from <> date_trunc('hour', valid_from)
	`).Scan(&straddlingFrom); err != nil {
		t.Fatalf("count straddling valid_from: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM plant_region_history WHERE valid_to IS NOT NULL AND valid_to <> date_trunc('hour', valid_to)
	`).Scan(&straddlingTo); err != nil {
		t.Fatalf("count straddling valid_to: %v", err)
	}
	if straddlingFrom != 0 {
		t.Errorf("rows with valid_from not on an hour boundary = %d, want 0", straddlingFrom)
	}
	if straddlingTo != 0 {
		t.Errorf("rows with non-null valid_to not on an hour boundary = %d, want 0", straddlingTo)
	}
}

// attributionRow is one (reading, attributed plant) pair. A region can host
// more than one simultaneously-active plant, so a single reading can
// legitimately fan out into more than one row (mirroring
// v_sensor_reading_with_plant's documented "one row per reading × active
// plant" shape) -- attributionRows below is therefore compared as a full
// multiset, not a reading_id-keyed map that would silently collapse a
// fan-out down to one winner.
type attributionRow struct {
	readingID int64
	plantID   int64 // 0 means NULL (no attributed plant); plant_id is BIGSERIAL, never 0.
}

// TestPlantRegionHistoryMigration_AttributionNeutral is NFR8's check: for
// every sensor_reading row that existed before the migration, the plant(s)
// attributed under the new history-based rule (join through
// plant_region_history at recorded_at) equal the plant(s) attributed under
// the old current-placement rule (plant.region_id equality plus
// created_at/removed_at window -- the exact predicate
// v_sensor_reading_with_plant already uses). Asserted over the full fixture
// as a multiset, not a sample and not collapsed by reading_id.
func TestPlantRegionHistoryMigration_AttributionNeutral(t *testing.T) {
	runner, db, _ := newPreRegionHistoryDB(t)
	ctx := context.Background()

	fetchRows := func(query string) []attributionRow {
		t.Helper()
		rows, err := db.Pool.Query(ctx, query)
		if err != nil {
			t.Fatalf("query attribution (%s): %v", query, err)
		}
		defer rows.Close()
		var out []attributionRow
		for rows.Next() {
			var readingID int64
			var plantID *int64
			if err := rows.Scan(&readingID, &plantID); err != nil {
				t.Fatalf("scan attribution row: %v", err)
			}
			row := attributionRow{readingID: readingID}
			if plantID != nil {
				row.plantID = *plantID
			}
			out = append(out, row)
		}
		return out
	}
	sortRows := func(rows []attributionRow) {
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].readingID != rows[j].readingID {
				return rows[i].readingID < rows[j].readingID
			}
			return rows[i].plantID < rows[j].plantID
		})
	}

	// Old-rule attribution, computed before the migration runs (so it
	// reflects exactly what today's read path attributes, independent of
	// anything migration 017 does).
	oldRows := fetchRows(`
		SELECT sr.reading_id, p.plant_id
		FROM sensor_reading sr
		LEFT JOIN plant p
		       ON p.region_id  = sr.region_id
		      AND p.created_at <= sr.recorded_at
		      AND (p.removed_at IS NULL OR p.removed_at > sr.recorded_at)
	`)
	if len(oldRows) == 0 {
		t.Fatal("test setup: expected at least one sensor_reading row to compare attribution over")
	}
	sortRows(oldRows)

	if err := runner.Steps(1); err != nil {
		t.Fatalf("apply migration 017: %v", err)
	}

	// New-rule attribution: join through plant_region_history at
	// recorded_at instead of plant.region_id directly.
	newRows := fetchRows(`
		SELECT sr.reading_id, h.plant_id
		FROM sensor_reading sr
		LEFT JOIN plant_region_history h
		       ON h.region_id  = sr.region_id
		      AND h.valid_from <= sr.recorded_at
		      AND (h.valid_to IS NULL OR h.valid_to > sr.recorded_at)
	`)
	sortRows(newRows)

	if !reflect.DeepEqual(oldRows, newRows) {
		t.Errorf("attribution changed by migration 017:\n  old rule = %+v\n  new rule = %+v", oldRows, newRows)
	}
}

// TestPlantRegionHistoryMigration_NoBackdatingDatabaseGuard covers the
// Testing section's "the database guard also refuses a direct insert"
// (NFR6.2): a direct INSERT with a future valid_from is refused by
// trg_plant_region_history_no_future_valid_from, while now/past values are
// accepted.
func TestPlantRegionHistoryMigration_NoBackdatingDatabaseGuard(t *testing.T) {
	runner, db, f := newPreRegionHistoryDB(t)
	ctx := context.Background()

	if err := runner.Steps(1); err != nil {
		t.Fatalf("apply migration 017: %v", err)
	}

	var extraRegionID int64
	if err := db.Pool.QueryRow(ctx, `INSERT INTO region (name) VALUES ($1) RETURNING region_id`, "guard-test-region").Scan(&extraRegionID); err != nil {
		t.Fatalf("insert extra region: %v", err)
	}

	future := time.Now().Add(1 * time.Hour)
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO plant_region_history (plant_id, region_id, valid_from) VALUES ($1, $2, $3)
	`, f.plantWithReadings, extraRegionID, future)
	if err == nil {
		t.Error("direct INSERT with a future valid_from succeeded, want it refused by the no-back-dating trigger (NFR6.2)")
	}

	// Accepted: a valid_from that is now or in the past.
	past := time.Now().Add(-1 * time.Minute)
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO plant_region_history (plant_id, region_id, valid_from) VALUES ($1, $2, $3)
	`, f.plantWithReadings, extraRegionID, past); err != nil {
		t.Errorf("direct INSERT with a past valid_from failed, want it accepted: %v", err)
	}
}

// TestPlantRegionHistoryMigration_NFR61IndexesExist asserts against
// pg_indexes that plant_region_history carries both index shapes NFR6.1
// requires, in both directions (plant-to-region and region-to-plant).
func TestPlantRegionHistoryMigration_NFR61IndexesExist(t *testing.T) {
	runner, db, _ := newPreRegionHistoryDB(t)
	ctx := context.Background()

	if err := runner.Steps(1); err != nil {
		t.Fatalf("apply migration 017: %v", err)
	}

	for _, indexName := range []string{
		"idx_plant_region_history_plant_id_current",
		"idx_plant_region_history_region_id_current",
		"idx_plant_region_history_plant_id_temporal",
		"idx_plant_region_history_region_id_temporal",
	} {
		t.Run(indexName, func(t *testing.T) {
			var exists bool
			if err := db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_indexes WHERE indexname = $1)`, indexName).Scan(&exists); err != nil {
				t.Fatalf("query pg_indexes for %s: %v", indexName, err)
			}
			if !exists {
				t.Errorf("index %s does not exist", indexName)
			}
		})
	}
}

// TestPlantRegionHistoryMigration_PlantRegionIDCacheUntouched proves the
// up.sql doc comment's claim: this migration does not drop or repurpose
// plant.region_id, and the backfill does not change its value.
func TestPlantRegionHistoryMigration_PlantRegionIDCacheUntouched(t *testing.T) {
	runner, db, f := newPreRegionHistoryDB(t)
	ctx := context.Background()

	var before int64
	if err := db.Pool.QueryRow(ctx, `SELECT region_id FROM plant WHERE plant_id = $1`, f.plantWithReadings).Scan(&before); err != nil {
		t.Fatalf("read plant.region_id before migration: %v", err)
	}

	if err := runner.Steps(1); err != nil {
		t.Fatalf("apply migration 017: %v", err)
	}

	var after int64
	if err := db.Pool.QueryRow(ctx, `SELECT region_id FROM plant WHERE plant_id = $1`, f.plantWithReadings).Scan(&after); err != nil {
		t.Fatalf("read plant.region_id after migration: %v", err)
	}
	if after != before {
		t.Errorf("plant.region_id changed by migration 017 = %d, want unchanged %d", after, before)
	}
}

// TestPlantRegionHistoryMigration_DownReversesCleanly and
// TestPlantRegionHistoryMigration_UpDownUpIsIdempotentSafe cover the
// Validation section's "up and down both clean" check at the Runner level.
func TestPlantRegionHistoryMigration_DownReversesCleanly(t *testing.T) {
	runner, db, _ := newPreRegionHistoryDB(t)
	ctx := context.Background()

	if err := runner.Steps(1); err != nil {
		t.Fatalf("apply migration 017: %v", err)
	}
	if err := runner.Steps(-1); err != nil {
		t.Fatalf("reverse migration 017: %v", err)
	}

	var tableExists bool
	if err := db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name = 'plant_region_history')`).Scan(&tableExists); err != nil {
		t.Fatalf("check plant_region_history exists after down: %v", err)
	}
	if tableExists {
		t.Error("plant_region_history still exists after reversing migration 017")
	}

	var triggerExists bool
	if err := db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_trigger WHERE tgname = 'trg_plant_region_history_no_future_valid_from')`).Scan(&triggerExists); err != nil {
		t.Fatalf("check trigger exists after down: %v", err)
	}
	if triggerExists {
		t.Error("trg_plant_region_history_no_future_valid_from still exists after reversing migration 017")
	}

	var functionExists bool
	if err := db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_proc WHERE proname = 'enforce_plant_region_history_no_future_valid_from')`).Scan(&functionExists); err != nil {
		t.Fatalf("check trigger function exists after down: %v", err)
	}
	if functionExists {
		t.Error("enforce_plant_region_history_no_future_valid_from still exists after reversing migration 017")
	}

	// plant.region_id must survive the down migration -- it was never
	// dropped or altered by 017's up.sql.
	var columnExists bool
	if err := db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name = 'plant' AND column_name = 'region_id')`).Scan(&columnExists); err != nil {
		t.Fatalf("check plant.region_id exists after down: %v", err)
	}
	if !columnExists {
		t.Error("plant.region_id no longer exists after reversing migration 017, want it untouched")
	}
}

func TestPlantRegionHistoryMigration_UpDownUpIsIdempotentSafe(t *testing.T) {
	runner, db, f := newPreRegionHistoryDB(t)
	ctx := context.Background()

	if err := runner.Steps(1); err != nil {
		t.Fatalf("first apply of migration 017: %v", err)
	}
	if err := runner.Steps(-1); err != nil {
		t.Fatalf("reverse migration 017: %v", err)
	}
	if err := runner.Steps(1); err != nil {
		t.Fatalf("second apply of migration 017: %v", err)
	}

	var plantsWithNoInterval int
	if err := db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM plant p
		WHERE NOT EXISTS (SELECT 1 FROM plant_region_history h WHERE h.plant_id = p.plant_id)
	`).Scan(&plantsWithNoInterval); err != nil {
		t.Fatalf("count plants with no interval after up-down-up: %v", err)
	}
	if plantsWithNoInterval != 0 {
		t.Errorf("plants with no interval after up-down-up = %d, want 0 (post-condition holds again on the second apply)", plantsWithNoInterval)
	}

	var intervalCount int
	if err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM plant_region_history WHERE plant_id = $1`, f.plantRemoved).Scan(&intervalCount); err != nil {
		t.Fatalf("count intervals for removed plant after up-down-up: %v", err)
	}
	if intervalCount != 1 {
		t.Errorf("interval count for removed plant after up-down-up = %d, want exactly 1", intervalCount)
	}
}
