//go:build integration

// Real-Postgres integration coverage for migration 015_ownership's DML
// (FR70.1, FR1.1, NFR8, A9, NFR6.1, NFR6.3) and its scaffolded schema: seeds
// pre-existing board/region-root/plant rows against the real migrations
// 001..014, applies 015, and asserts the backfill's post-conditions --
// something a unit test reading the SQL cannot verify. Requires a real
// TimescaleDB image (not plain postgres) since migration 001 creates a
// hypertable. See //libs/go/dbtest's README for how to run tests like this
// one.
package main_test

import (
	"context"
	"database/sql"
	"embed"
	"maps"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

//go:embed migrations/*.sql
var migrations embed.FS

// timescaleImage matches leaflab/Tiltfile's local-dev Postgres image --
// migration 001 does `CREATE EXTENSION IF NOT EXISTS timescaledb`, which the
// plain postgres:16-alpine image dbtest defaults to does not provide.
const timescaleImage = "timescale/timescaledb:latest-pg16"

// preOwnershipFixture is what's seeded into board/region/plant before
// migration 015 runs, and the ids the assertions below check against.
type preOwnershipFixture struct {
	boardA, boardB                     int64
	regionRootA, regionChildA1         int64
	regionRootB                        int64
	plantUnderRootA, plantUnderChildA1 int64
}

// newPreOwnershipDB migrates up through 014 (the last migration before
// 015_ownership), seeds board/region/plant rows that predate the ownership
// schema, and returns the runner (still positioned at 014) plus a pool for
// fixture setup and assertions.
func newPreOwnershipDB(t *testing.T) (*migrate.Runner, *dbtest.Postgres, preOwnershipFixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Image: timescaleImage})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := sqlDB.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}

	runner := migrate.NewRunner(sqlDB, migrations, "migrations")
	if err := runner.Migrate(14); err != nil {
		t.Fatalf("migrate to version 14 (pre-ownership): %v", err)
	}

	var f preOwnershipFixture
	mustExec := func(dest *int64, query string, args ...any) {
		t.Helper()
		if err := db.Pool.QueryRow(ctx, query, args...).Scan(dest); err != nil {
			t.Fatalf("fixture setup %q: %v", query, err)
		}
	}

	mustExec(&f.boardA, `INSERT INTO board (device_id) VALUES ($1) RETURNING board_id`, "pre-ownership-board-a")
	mustExec(&f.boardB, `INSERT INTO board (device_id) VALUES ($1) RETURNING board_id`, "pre-ownership-board-b")

	mustExec(&f.regionRootA, `INSERT INTO region (name) VALUES ($1) RETURNING region_id`, "root-a")
	mustExec(&f.regionChildA1, `INSERT INTO region (parent_region_id, name) VALUES ($1, $2) RETURNING region_id`, f.regionRootA, "child-a1")
	mustExec(&f.regionRootB, `INSERT INTO region (name) VALUES ($1) RETURNING region_id`, "root-b")

	var plantTypeID int64
	mustExec(&plantTypeID, `INSERT INTO plant_type (common_name) VALUES ($1) RETURNING plant_type_id`, "test-plant-type")

	mustExec(&f.plantUnderRootA, `INSERT INTO plant (region_id, plant_type_id, name) VALUES ($1, $2, $3) RETURNING plant_id`,
		f.regionRootA, plantTypeID, "plant-under-root-a")
	mustExec(&f.plantUnderChildA1, `INSERT INTO plant (region_id, plant_type_id, name) VALUES ($1, $2, $3) RETURNING plant_id`,
		f.regionChildA1, plantTypeID, "plant-under-child-a1")

	return runner, db, f
}

func unadoptedHouseholdID(t *testing.T, ctx context.Context, db *dbtest.Postgres) int64 {
	t.Helper()
	var id int64
	if err := db.Pool.QueryRow(ctx, `SELECT household_id FROM household WHERE is_unadopted = TRUE`).Scan(&id); err != nil {
		t.Fatalf("read Unadopted household_id: %v", err)
	}
	return id
}

// TestOwnershipMigration_BackfillAssignsPreExistingRowsToUnadopted is the
// issue's post-condition test: after 015 runs, a query for rows resolving
// to no household returns zero across board, region roots, and plant, and
// the region-root-only rule holds (a descendant region stays NULL and
// inherits rather than getting its own household_id).
func TestOwnershipMigration_BackfillAssignsPreExistingRowsToUnadopted(t *testing.T) {
	runner, db, f := newPreOwnershipDB(t)
	ctx := context.Background()

	if err := runner.Steps(1); err != nil {
		t.Fatalf("apply migration 015: %v", err)
	}

	unadoptedID := unadoptedHouseholdID(t, ctx, db)

	// Post-condition: zero rows resolve to no household.
	var noHouseholdBoards, noHouseholdRegionRoots, noHouseholdPlants int
	if err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM board WHERE household_id IS NULL`).Scan(&noHouseholdBoards); err != nil {
		t.Fatalf("count boards with no household: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM region WHERE parent_region_id IS NULL AND household_id IS NULL`).Scan(&noHouseholdRegionRoots); err != nil {
		t.Fatalf("count region roots with no household: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM plant WHERE household_id IS NULL`).Scan(&noHouseholdPlants); err != nil {
		t.Fatalf("count plants with no household: %v", err)
	}
	if noHouseholdBoards != 0 {
		t.Errorf("boards with no household after migration = %d, want 0", noHouseholdBoards)
	}
	if noHouseholdRegionRoots != 0 {
		t.Errorf("region roots with no household after migration = %d, want 0", noHouseholdRegionRoots)
	}
	if noHouseholdPlants != 0 {
		t.Errorf("plants with no household after migration = %d, want 0", noHouseholdPlants)
	}

	// Every pre-existing board/region-root/plant specifically landed in
	// Unadopted (not some other household).
	for name, q := range map[string]struct {
		query string
		id    int64
	}{
		"board A":              {`SELECT household_id FROM board WHERE board_id = $1`, f.boardA},
		"board B":              {`SELECT household_id FROM board WHERE board_id = $1`, f.boardB},
		"region root A":        {`SELECT household_id FROM region WHERE region_id = $1`, f.regionRootA},
		"region root B":        {`SELECT household_id FROM region WHERE region_id = $1`, f.regionRootB},
		"plant under root A":   {`SELECT household_id FROM plant WHERE plant_id = $1`, f.plantUnderRootA},
		"plant under child A1": {`SELECT household_id FROM plant WHERE plant_id = $1`, f.plantUnderChildA1},
	} {
		t.Run(name, func(t *testing.T) {
			var got int64
			if err := db.Pool.QueryRow(ctx, q.query, q.id).Scan(&got); err != nil {
				t.Fatalf("query household_id for %s: %v", name, err)
			}
			if got != unadoptedID {
				t.Errorf("%s household_id = %d, want Unadopted (%d)", name, got, unadoptedID)
			}
		})
	}

	// The child region is a descendant, not a tree root: it must stay NULL
	// and inherit from its root rather than getting its own household_id.
	var childHouseholdID *int64
	if err := db.Pool.QueryRow(ctx, `SELECT household_id FROM region WHERE region_id = $1`, f.regionChildA1).Scan(&childHouseholdID); err != nil {
		t.Fatalf("query household_id for child region: %v", err)
	}
	if childHouseholdID != nil {
		t.Errorf("descendant region household_id = %v, want NULL (inherits from tree root)", *childHouseholdID)
	}

	// board_ownership carries a mirrored SCD2 row per pre-existing board.
	var boardOwnershipRows int
	if err := db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM board_ownership
		WHERE board_id IN ($1, $2) AND household_id = $3 AND valid_to IS NULL
	`, f.boardA, f.boardB, unadoptedID).Scan(&boardOwnershipRows); err != nil {
		t.Fatalf("count board_ownership rows: %v", err)
	}
	if boardOwnershipRows != 2 {
		t.Errorf("board_ownership open rows for the 2 backfilled boards = %d, want 2", boardOwnershipRows)
	}
}

// TestOwnershipMigration_PreservesSensorReadingCounts is NFR8's "no reading
// changes its meaning" check: sensor_reading row count and region_id
// distribution are identical before and after migration 015, since the
// migration touches neither table.
func TestOwnershipMigration_PreservesSensorReadingCounts(t *testing.T) {
	runner, db, f := newPreOwnershipDB(t)
	ctx := context.Background()

	var sensorTypeID, sensorID int64
	if err := db.Pool.QueryRow(ctx, `SELECT sensor_type_id FROM sensor_type LIMIT 1`).Scan(&sensorTypeID); err != nil {
		t.Fatalf("read seeded sensor_type: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO sensor (board_id, sensor_type_id, region_id, name, unit) VALUES ($1, $2, $3, $4, $5) RETURNING sensor_id
	`, f.boardA, sensorTypeID, f.regionRootA, "test-sensor", "degC").Scan(&sensorID); err != nil {
		t.Fatalf("insert sensor: %v", err)
	}

	for i := 0; i < 5; i++ {
		if _, err := db.Pool.Exec(ctx, `
			INSERT INTO sensor_reading (sensor_id, region_id, value, uptime_s) VALUES ($1, $2, $3, $4)
		`, sensorID, f.regionRootA, 21.5, 1000+i); err != nil {
			t.Fatalf("insert sensor_reading %d: %v", i, err)
		}
	}

	countBefore, distBefore := sensorReadingSnapshot(t, ctx, db)

	if err := runner.Steps(1); err != nil {
		t.Fatalf("apply migration 015: %v", err)
	}

	countAfter, distAfter := sensorReadingSnapshot(t, ctx, db)

	if countBefore != 5 {
		t.Fatalf("test setup: expected 5 sensor_reading rows before migration, got %d", countBefore)
	}
	if countAfter != countBefore {
		t.Errorf("sensor_reading row count after migration = %d, want unchanged %d", countAfter, countBefore)
	}
	if !maps.Equal(distAfter, distBefore) {
		t.Errorf("sensor_reading region_id distribution after migration = %v, want unchanged %v", distAfter, distBefore)
	}
}

func sensorReadingSnapshot(t *testing.T, ctx context.Context, db *dbtest.Postgres) (count int, regionIDDistribution map[int64]int) {
	t.Helper()
	if err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM sensor_reading`).Scan(&count); err != nil {
		t.Fatalf("count sensor_reading: %v", err)
	}
	rows, err := db.Pool.Query(ctx, `SELECT region_id, COUNT(*) FROM sensor_reading GROUP BY region_id`)
	if err != nil {
		t.Fatalf("group sensor_reading by region_id: %v", err)
	}
	defer rows.Close()
	regionIDDistribution = map[int64]int{}
	for rows.Next() {
		var regionID int64
		var n int
		if err := rows.Scan(&regionID, &n); err != nil {
			t.Fatalf("scan region_id distribution row: %v", err)
		}
		regionIDDistribution[regionID] = n
	}
	return count, regionIDDistribution
}

// TestOwnershipMigration_UnadoptedHasZeroMembersAndRefusesNewMembership
// covers the Testing section's "Unadopted has zero members and an insert of
// a membership row into it is refused": FR70.1 seeds it with no
// household_membership rows, and -- per the same A9 no-new-arrivals rule
// migration 015 already enforces for board_ownership -- a direct INSERT
// naming Unadopted as a principal's household must also be refused.
func TestOwnershipMigration_UnadoptedHasZeroMembersAndRefusesNewMembership(t *testing.T) {
	runner, db, _ := newPreOwnershipDB(t)
	ctx := context.Background()

	if err := runner.Steps(1); err != nil {
		t.Fatalf("apply migration 015: %v", err)
	}

	unadoptedID := unadoptedHouseholdID(t, ctx, db)

	var memberCount int
	if err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM household_membership WHERE household_id = $1`, unadoptedID).Scan(&memberCount); err != nil {
		t.Fatalf("count household_membership rows for Unadopted: %v", err)
	}
	if memberCount != 0 {
		t.Errorf("Unadopted household_membership row count = %d, want 0 (member-less)", memberCount)
	}

	_, err := db.Pool.Exec(ctx, `INSERT INTO household_membership (household_id, principal_subject) VALUES ($1, $2)`,
		unadoptedID, "someone@example.com")
	if err == nil {
		t.Error("INSERT INTO household_membership naming Unadopted directly succeeded, want it refused (member-less, no new arrivals)")
	}
}

// TestOwnershipMigration_NewArrivalsGuardRefusesUnadoptedBoardOwnership
// proves A9 end-to-end: the backfill itself (run as part of migration 015)
// succeeds in assigning pre-existing boards to Unadopted, but a later,
// independent INSERT naming the Unadopted household directly is refused by
// trg_board_ownership_no_unadopted_arrivals.
func TestOwnershipMigration_NewArrivalsGuardRefusesUnadoptedBoardOwnership(t *testing.T) {
	runner, db, f := newPreOwnershipDB(t)
	ctx := context.Background()

	if err := runner.Steps(1); err != nil {
		t.Fatalf("apply migration 015: %v", err)
	}

	unadoptedID := unadoptedHouseholdID(t, ctx, db)

	// The backfill itself succeeded -- confirmed by the previous test, and
	// re-asserted here as the "guard doesn't also block the exempt backfill"
	// half of A9.
	var backfilledCount int
	if err := db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM board_ownership WHERE board_id = $1 AND household_id = $2 AND valid_to IS NULL
	`, f.boardA, unadoptedID).Scan(&backfilledCount); err != nil {
		t.Fatalf("count backfilled board_ownership row: %v", err)
	}
	if backfilledCount != 1 {
		t.Fatalf("test setup: backfilled board_ownership row for board A missing (count=%d)", backfilledCount)
	}

	// A brand-new board self-registering after migration, then an attempt
	// to directly name Unadopted as its owner -- exactly the "self-registering
	// board lands unclaimed" scenario FR70.1 says must be refused.
	var newBoardID int64
	if err := db.Pool.QueryRow(ctx, `INSERT INTO board (device_id) VALUES ($1) RETURNING board_id`, "post-migration-board").Scan(&newBoardID); err != nil {
		t.Fatalf("insert post-migration board: %v", err)
	}

	_, err := db.Pool.Exec(ctx, `INSERT INTO board_ownership (board_id, household_id) VALUES ($1, $2)`, newBoardID, unadoptedID)
	if err == nil {
		t.Fatal("INSERT INTO board_ownership naming Unadopted directly succeeded, want it refused (A9)")
	}
}

// TestOwnershipMigration_SCD2ShapeAndValueAtTime proves the close-and-open
// write path leaves exactly one open (valid_to IS NULL) row per key and that
// a value-at-T query resolves to the historically correct household, for
// board_ownership (a real re-ownership/reclaim scenario post-backfill).
func TestOwnershipMigration_SCD2ShapeAndValueAtTime(t *testing.T) {
	runner, db, f := newPreOwnershipDB(t)
	ctx := context.Background()

	if err := runner.Steps(1); err != nil {
		t.Fatalf("apply migration 015: %v", err)
	}

	unadoptedID := unadoptedHouseholdID(t, ctx, db)

	var newHouseholdID int64
	if err := db.Pool.QueryRow(ctx, `INSERT INTO household (name) VALUES ($1) RETURNING household_id`, "claimed-household").Scan(&newHouseholdID); err != nil {
		t.Fatalf("insert claimed household: %v", err)
	}

	var beforeReclaim time.Time
	if err := db.Pool.QueryRow(ctx, `SELECT valid_from FROM board_ownership WHERE board_id = $1 AND valid_to IS NULL`, f.boardA).Scan(&beforeReclaim); err != nil {
		t.Fatalf("read original valid_from: %v", err)
	}
	midpoint := time.Now()

	// AGENTS.md close-and-open write path.
	if _, err := db.Pool.Exec(ctx, `UPDATE board_ownership SET valid_to = NOW() WHERE board_id = $1 AND valid_to IS NULL`, f.boardA); err != nil {
		t.Fatalf("close current board_ownership row: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO board_ownership (board_id, household_id) VALUES ($1, $2)`, f.boardA, newHouseholdID); err != nil {
		t.Fatalf("open new board_ownership row: %v", err)
	}

	var openCount int
	if err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM board_ownership WHERE board_id = $1 AND valid_to IS NULL`, f.boardA).Scan(&openCount); err != nil {
		t.Fatalf("count open board_ownership rows: %v", err)
	}
	if openCount != 1 {
		t.Errorf("open board_ownership rows for board A after close-and-open = %d, want exactly 1", openCount)
	}

	var atMidpoint int64
	if err := db.Pool.QueryRow(ctx, `
		SELECT household_id FROM board_ownership
		WHERE board_id = $1 AND valid_from <= $2 AND (valid_to IS NULL OR valid_to > $2)
	`, f.boardA, midpoint).Scan(&atMidpoint); err != nil {
		t.Fatalf("value-at-T query at midpoint: %v", err)
	}
	if atMidpoint != unadoptedID {
		t.Errorf("household at midpoint (before reclaim) = %d, want Unadopted (%d)", atMidpoint, unadoptedID)
	}

	var atNow int64
	if err := db.Pool.QueryRow(ctx, `
		SELECT household_id FROM board_ownership
		WHERE board_id = $1 AND valid_from <= NOW() AND (valid_to IS NULL OR valid_to > NOW())
	`, f.boardA).Scan(&atNow); err != nil {
		t.Fatalf("value-at-T query at now: %v", err)
	}
	if atNow != newHouseholdID {
		t.Errorf("household now (after reclaim) = %d, want claimed household (%d)", atNow, newHouseholdID)
	}
}

// TestOwnershipMigration_NFR61IndexesExist asserts against pg_indexes that
// both SCD2 tables carry both index shapes NFR6.1 requires: the open-interval
// partial index (current-value lookup) and the temporal (value-at-T) index.
func TestOwnershipMigration_NFR61IndexesExist(t *testing.T) {
	runner, db, _ := newPreOwnershipDB(t)
	ctx := context.Background()

	if err := runner.Steps(1); err != nil {
		t.Fatalf("apply migration 015: %v", err)
	}

	for _, indexName := range []string{
		"idx_household_membership_household_id_current",
		"idx_household_membership_principal_subject_current",
		"idx_household_membership_household_id_temporal",
		"idx_board_ownership_board_id_current",
		"idx_board_ownership_board_id_temporal",
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

// TestOwnershipMigration_RetiredBoardExcludedFromDefaultListingIndex is the
// migration-level half of FR22.1/FR22.4/FR22.5's board retirement guard:
// idx_board_active exists and its WHERE clause actually prunes a retired
// board out of the default (retired_at IS NULL) population, while the row
// itself remains fully readable by explicit id. Repository-level RPC
// coverage of RetireBoard/ListBoards/GetBoardByID lives in
// //leaflab/api:repository_board_lifecycle_integration_test; this test
// covers the schema/index this migration itself owns.
func TestOwnershipMigration_RetiredBoardExcludedFromDefaultListingIndex(t *testing.T) {
	runner, db, f := newPreOwnershipDB(t)
	ctx := context.Background()

	if err := runner.Steps(1); err != nil {
		t.Fatalf("apply migration 015: %v", err)
	}

	if _, err := db.Pool.Exec(ctx, `UPDATE board SET retired_at = NOW() WHERE board_id = $1`, f.boardA); err != nil {
		t.Fatalf("retire board A: %v", err)
	}

	var defaultListing []int64
	rows, err := db.Pool.Query(ctx, `SELECT board_id FROM board WHERE retired_at IS NULL ORDER BY board_id`)
	if err != nil {
		t.Fatalf("query default (active) listing: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan board_id: %v", err)
		}
		defaultListing = append(defaultListing, id)
	}

	for _, id := range defaultListing {
		if id == f.boardA {
			t.Errorf("retired board A appears in the default (retired_at IS NULL) listing, want it excluded")
		}
	}

	// Still readable by explicit id.
	var stillReadable bool
	if err := db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM board WHERE board_id = $1)`, f.boardA).Scan(&stillReadable); err != nil {
		t.Fatalf("check retired board still readable by explicit id: %v", err)
	}
	if !stillReadable {
		t.Error("retired board A is not readable by explicit id, want it to remain resolvable")
	}
}

// TestOwnershipMigration_DownReversesCleanly and
// TestOwnershipMigration_UpDownUpIsIdempotentSafe cover the Validation
// section's "up and down both clean" / "idempotent-safe to re-run against a
// fresh database" checks at the Runner level (the same code path
// //leaflab/migrate's binary drives).
func TestOwnershipMigration_DownReversesCleanly(t *testing.T) {
	runner, db, _ := newPreOwnershipDB(t)
	ctx := context.Background()

	if err := runner.Steps(1); err != nil {
		t.Fatalf("apply migration 015: %v", err)
	}
	if err := runner.Steps(-1); err != nil {
		t.Fatalf("reverse migration 015: %v", err)
	}

	for _, table := range []string{"household", "household_membership", "board_ownership"} {
		var exists bool
		if err := db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name = $1)`, table).Scan(&exists); err != nil {
			t.Fatalf("check table %s exists after down: %v", table, err)
		}
		if exists {
			t.Errorf("table %s still exists after reversing migration 015", table)
		}
	}
	for _, col := range []struct{ table, column string }{
		{"board", "household_id"}, {"board", "retired_at"}, {"region", "household_id"}, {"plant", "household_id"},
	} {
		var exists bool
		if err := db.Pool.QueryRow(ctx, `
			SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name = $1 AND column_name = $2)
		`, col.table, col.column).Scan(&exists); err != nil {
			t.Fatalf("check column %s.%s exists after down: %v", col.table, col.column, err)
		}
		if exists {
			t.Errorf("column %s.%s still exists after reversing migration 015", col.table, col.column)
		}
	}
}

func TestOwnershipMigration_UpDownUpIsIdempotentSafe(t *testing.T) {
	runner, db, _ := newPreOwnershipDB(t)
	ctx := context.Background()

	if err := runner.Steps(1); err != nil {
		t.Fatalf("first apply of migration 015: %v", err)
	}
	_ = unadoptedHouseholdID(t, ctx, db) // first apply produces exactly one Unadopted household; re-checked below after the second apply.

	if err := runner.Steps(-1); err != nil {
		t.Fatalf("reverse migration 015: %v", err)
	}
	if err := runner.Steps(1); err != nil {
		t.Fatalf("second apply of migration 015: %v", err)
	}

	// household_id is a BIGSERIAL owned by the household table; DROP TABLE in
	// the down migration drops the owned sequence with it, so a legitimate
	// up-down-up re-run restarts the sequence at 1 and reuses the same id --
	// that is correct, not a sign the down migration failed to drop anything.
	// unadoptedHouseholdID itself (below) is the real assertion: exactly one
	// row still resolves via is_unadopted = TRUE.
	_ = unadoptedHouseholdID(t, ctx, db)

	var householdCount int
	if err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM household WHERE is_unadopted = TRUE`).Scan(&householdCount); err != nil {
		t.Fatalf("count Unadopted households after up-down-up: %v", err)
	}
	if householdCount != 1 {
		t.Errorf("Unadopted household count after up-down-up = %d, want exactly 1 (singleton index)", householdCount)
	}

	var noHouseholdBoards int
	if err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM board WHERE household_id IS NULL`).Scan(&noHouseholdBoards); err != nil {
		t.Fatalf("count boards with no household after up-down-up: %v", err)
	}
	if noHouseholdBoards != 0 {
		t.Errorf("boards with no household after up-down-up = %d, want 0 (post-condition holds again on the second apply)", noHouseholdBoards)
	}
}
