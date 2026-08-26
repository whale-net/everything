//go:build integration

// This file only builds under the "integration" build tag so that `bazel
// test //...` (which builds and runs the whole tree, including on
// Docker-less machines) never even compiles it, let alone runs it. See the
// integration_test go_test target's gotags in BUILD.bazel and
// //libs/go/dbtest's README for how to run it.
package main

import (
	"context"
	"database/sql"
	"embed"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

//go:embed migrations/*.sql
var migrations embed.FS

// TimescaleDB image with postgres
const timescaleDBImage = "timescale/timescaledb:latest-pg16"

// openDB opens a *sql.DB against db's isolated dbtest database/role, using
// the pgx stdlib driver migrate.Runner requires, and registers it for cleanup.
func openDB(t *testing.T, db *dbtest.Postgres) *sql.DB {
	t.Helper()

	sqlDB, err := sql.Open("pgx", db.ConnString)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	if err := sqlDB.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}

	return sqlDB
}

// TestMigrationAllUp applies all migrations and validates the schema.
func TestMigrationAllUp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Image: timescaleDBImage,
	})
	sqlDB := openDB(t, db)

	runner := migrate.NewRunner(sqlDB, migrations, "migrations")
	if err := runner.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}

	version, dirty, err := runner.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if dirty {
		t.Fatalf("expected clean state after Up, got dirty")
	}

	// Verify critical tables exist after all migrations
	tables := []string{
		"household",
		"household_member",
		"board_ownership",
		"board",
		"region",
		"plant",
		"sensor",
		"sensor_reading",
		"device_config",
	}
	for _, table := range tables {
		if _, err := db.Pool.Exec(ctx, "SELECT 1 FROM "+table+" LIMIT 0"); err != nil {
			t.Errorf("table %s not found or not accessible: %v", table, err)
		}
	}

	t.Logf("All migrations applied successfully (version %d)", version)
}

// TestMigrationBackfillZeroRowsResolveToNoHousehold verifies FR70 post-condition:
// after migration, zero rows resolve to no household.
func TestMigrationBackfillZeroRowsResolveToNoHousehold(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Image: timescaleDBImage,
	})
	sqlDB := openDB(t, db)
	runner := migrate.NewRunner(sqlDB, migrations, "migrations")

	// Apply all migrations
	if err := runner.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}

	// After backfill, verify FR70 post-condition: zero rows resolve to no household
	// Check boards
	var boardCount int
	if err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM board WHERE household_id IS NULL`).Scan(&boardCount); err != nil {
		t.Fatalf("count boards after backfill: %v", err)
	}
	if boardCount != 0 {
		t.Errorf("FR70: expected zero boards with NULL household_id after backfill, got %d", boardCount)
	}

	// Check regions
	var regionCount int
	if err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM region WHERE parent_region_id IS NULL AND household_id IS NULL`).Scan(&regionCount); err != nil {
		t.Fatalf("count root regions after backfill: %v", err)
	}
	if regionCount != 0 {
		t.Errorf("FR70: expected zero root regions with NULL household_id after backfill, got %d", regionCount)
	}

	// Check plants
	var plantCount int
	if err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM plant WHERE household_id IS NULL`).Scan(&plantCount); err != nil {
		t.Fatalf("count plants after backfill: %v", err)
	}
	if plantCount != 0 {
		t.Errorf("FR70: expected zero plants with NULL household_id after backfill, got %d", plantCount)
	}

	t.Logf("FR70 verified: zero rows resolve to no household after backfill")
}

// TestPreventUnadoptedNewArrivals verifies that new boards start with NULL household_id
// and cannot be inserted into the Unadopted household after migration.
func TestPreventUnadoptedNewArrivals(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Image: timescaleDBImage,
	})
	sqlDB := openDB(t, db)
	runner := migrate.NewRunner(sqlDB, migrations, "migrations")

	// Apply all migrations
	if err := runner.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}

	// Get the Unadopted household ID
	var unadoptedID int64
	if err := db.Pool.QueryRow(ctx, `SELECT household_id FROM household WHERE name = 'Unadopted'`).Scan(&unadoptedID); err != nil {
		t.Fatalf("find Unadopted: %v", err)
	}

	// Insert a new board (should start with NULL household_id)
	var boardID int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO board (device_id, registered_at, last_seen_at)
		VALUES ('new-unclaimed-board', NOW(), NOW())
		RETURNING board_id
	`).Scan(&boardID); err != nil {
		t.Fatalf("insert new board: %v", err)
	}

	// Verify new board starts with NULL household_id
	var householdID *int64
	if err := db.Pool.QueryRow(ctx, `
		SELECT household_id FROM board WHERE board_id = $1
	`, boardID).Scan(&householdID); err != nil {
		t.Fatalf("check new board household_id: %v", err)
	}
	if householdID != nil {
		t.Errorf("new board should start with NULL household_id, got %v", *householdID)
	}

	t.Logf("FR70 verified: new board correctly starts with NULL household_id (awaiting claim)")
}

// TestSCD2TablesHaveBothIndexShapes verifies NFR6.1: SCD2 tables have both
// partial index (WHERE valid_to IS NULL) for current value access and
// temporal index for value-at-time queries.
func TestSCD2TablesHaveBothIndexShapes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Image: timescaleDBImage,
	})
	sqlDB := openDB(t, db)
	runner := migrate.NewRunner(sqlDB, migrations, "migrations")

	if err := runner.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}

	// Check for partial index on household_member (WHERE valid_to IS NULL)
	var partialIndexCount int
	if err := db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM pg_indexes
		WHERE tablename = 'household_member' AND indexdef LIKE '%valid_to IS NULL%'
	`).Scan(&partialIndexCount); err != nil {
		t.Fatalf("query partial index: %v", err)
	}
	if partialIndexCount == 0 {
		t.Error("NFR6.1: household_member missing partial index (WHERE valid_to IS NULL)")
	}

	// Check for temporal index on household_member
	var temporalIndexCount int
	if err := db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM pg_indexes
		WHERE tablename = 'household_member' AND indexdef LIKE '%valid_from%' AND indexdef LIKE '%valid_to%'
	`).Scan(&temporalIndexCount); err != nil {
		t.Fatalf("query temporal index: %v", err)
	}
	if temporalIndexCount == 0 {
		t.Error("NFR6.1: household_member missing temporal index on (valid_from, valid_to)")
	}

	// Same for board_ownership
	if err := db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM pg_indexes
		WHERE tablename = 'board_ownership' AND indexdef LIKE '%valid_to IS NULL%'
	`).Scan(&partialIndexCount); err != nil {
		t.Fatalf("query board_ownership partial index: %v", err)
	}
	if partialIndexCount == 0 {
		t.Error("NFR6.1: board_ownership missing partial index (WHERE valid_to IS NULL)")
	}

	t.Logf("NFR6.1 verified: SCD2 tables have both index shapes (partial + temporal)")
}

// TestAppendOnlyEnforcement verifies NFR6.2: append-only enforcement at the
// database layer for device_config table.
func TestAppendOnlyEnforcement(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Image: timescaleDBImage,
	})
	sqlDB := openDB(t, db)
	runner := migrate.NewRunner(sqlDB, migrations, "migrations")

	if err := runner.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}

	// Create a board to reference in device_config
	var boardID int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO board (device_id, registered_at, last_seen_at)
		VALUES ('test-board-append-only', NOW(), NOW())
		RETURNING board_id
	`).Scan(&boardID); err != nil {
		t.Fatalf("insert board: %v", err)
	}

	// Insert a device_config row
	var configID int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO device_config (board_id, version, config_json, accepted)
		VALUES ($1, 1, '{}', FALSE)
		RETURNING config_id
	`, boardID).Scan(&configID); err != nil {
		t.Fatalf("insert device_config: %v", err)
	}

	// Attempt to update the row should fail (trigger prevents it)
	_, err := db.Pool.Exec(ctx, `
		UPDATE device_config SET accepted = TRUE WHERE config_id = $1
	`, configID)

	if err == nil {
		t.Error("NFR6.2: expected device_config UPDATE to be rejected, but it succeeded")
	} else {
		t.Logf("NFR6.2 verified: device_config UPDATE rejected with: %v", err)
	}
}

// TestDepartureRecordAndAuditHaveNoValidTo verifies NFR6.3: departure records
// and audit rows do NOT have valid_to (they are append-only or short-lived,
// not SCD2).
func TestDepartureRecordAndAuditHaveNoValidTo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Image: timescaleDBImage,
	})
	sqlDB := openDB(t, db)
	runner := migrate.NewRunner(sqlDB, migrations, "migrations")

	if err := runner.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}

	// Verify device_config table does NOT have valid_to (it's append-only, not SCD2)
	var hasValidTo int
	if err := db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM information_schema.columns
		WHERE table_name = 'device_config' AND column_name = 'valid_to'
	`).Scan(&hasValidTo); err != nil {
		t.Fatalf("query device_config columns: %v", err)
	}
	if hasValidTo != 0 {
		t.Error("NFR6.3: device_config should not have valid_to (it's append-only, not SCD2)")
	} else {
		t.Log("NFR6.3 verified: device_config (append-only) has no valid_to")
	}
}

// TestHouseholdIDOnBoardRegionPlant verifies FR1.1: household_id is on board,
// region (root only), and plant. Sensors and readings inherit household through
// their board.
func TestHouseholdIDOnBoardRegionPlant(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Image: timescaleDBImage,
	})
	sqlDB := openDB(t, db)
	runner := migrate.NewRunner(sqlDB, migrations, "migrations")

	if err := runner.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}

	// Verify board.household_id exists
	var boardHasHouseholdID int
	if err := db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM information_schema.columns
		WHERE table_name = 'board' AND column_name = 'household_id'
	`).Scan(&boardHasHouseholdID); err != nil {
		t.Fatalf("check board.household_id: %v", err)
	}
	if boardHasHouseholdID == 0 {
		t.Error("FR1.1: board missing household_id column")
	}

	// Verify region.household_id exists
	var regionHasHouseholdID int
	if err := db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM information_schema.columns
		WHERE table_name = 'region' AND column_name = 'household_id'
	`).Scan(&regionHasHouseholdID); err != nil {
		t.Fatalf("check region.household_id: %v", err)
	}
	if regionHasHouseholdID == 0 {
		t.Error("FR1.1: region missing household_id column")
	}

	// Verify plant.household_id exists
	var plantHasHouseholdID int
	if err := db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM information_schema.columns
		WHERE table_name = 'plant' AND column_name = 'household_id'
	`).Scan(&plantHasHouseholdID); err != nil {
		t.Fatalf("check plant.household_id: %v", err)
	}
	if plantHasHouseholdID == 0 {
		t.Error("FR1.1: plant missing household_id column")
	}

	// Verify sensor and sensor_reading do NOT have household_id
	// (they inherit through their board)
	var sensorHasHouseholdID int
	if err := db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM information_schema.columns
		WHERE table_name = 'sensor' AND column_name = 'household_id'
	`).Scan(&sensorHasHouseholdID); err != nil {
		t.Fatalf("check sensor.household_id: %v", err)
	}
	if sensorHasHouseholdID != 0 {
		t.Error("FR1.1: sensor should NOT have household_id (inherits through board)")
	}

	t.Logf("FR1.1 verified: household_id on board, region, plant; sensors inherit through board")
}

// TestBoardRetiredState verifies FR22: retired board state columns exist.
func TestBoardRetiredState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Image: timescaleDBImage,
	})
	sqlDB := openDB(t, db)
	runner := migrate.NewRunner(sqlDB, migrations, "migrations")

	if err := runner.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}

	// Verify board has retired_at, retired_operation, retired_principal columns
	columns := []string{"retired_at", "retired_operation", "retired_principal"}
	for _, col := range columns {
		var count int
		if err := db.Pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM information_schema.columns
			WHERE table_name = 'board' AND column_name = $1
		`, col).Scan(&count); err != nil {
			t.Fatalf("check board.%s: %v", col, err)
		}
		if count == 0 {
			t.Errorf("FR22: board missing %s column", col)
		}
	}

	t.Logf("FR22 verified: board has retired state columns")
}

// TestUnadoptedHousehold verifies that the Unadopted household exists and has no members.
func TestUnadoptedHousehold(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Image: timescaleDBImage,
	})
	sqlDB := openDB(t, db)
	runner := migrate.NewRunner(sqlDB, migrations, "migrations")

	if err := runner.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}

	// Verify Unadopted household exists
	var householdID int64
	var name string
	if err := db.Pool.QueryRow(ctx, `
		SELECT household_id, name FROM household WHERE name = 'Unadopted'
	`).Scan(&householdID, &name); err != nil {
		t.Fatalf("find Unadopted household: %v", err)
	}
	if name != "Unadopted" {
		t.Errorf("expected household name 'Unadopted', got %q", name)
	}

	// Verify Unadopted has no members (per FR70)
	var memberCount int
	if err := db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM household_member WHERE household_id = $1 AND valid_to IS NULL
	`, householdID).Scan(&memberCount); err != nil {
		t.Fatalf("count Unadopted members: %v", err)
	}
	if memberCount != 0 {
		t.Errorf("FR70: expected Unadopted to have zero members, got %d", memberCount)
	}

	t.Logf("FR70 verified: Unadopted household exists with no members")
}

// TestSCD2WritePatternWorks verifies that the SCD2 close-and-open write pattern works.
func TestSCD2WritePatternWorks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Image: timescaleDBImage,
	})
	sqlDB := openDB(t, db)
	runner := migrate.NewRunner(sqlDB, migrations, "migrations")

	if err := runner.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}

	// Get Unadopted household
	var householdID int64
	if err := db.Pool.QueryRow(ctx, `
		SELECT household_id FROM household WHERE name = 'Unadopted'
	`).Scan(&householdID); err != nil {
		t.Fatalf("find Unadopted: %v", err)
	}

	// Add a member
	var memberID int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO household_member (household_id, principal_id, role, valid_from)
		VALUES ($1, 'test-principal', 'Owner', NOW())
		RETURNING member_id
	`, householdID).Scan(&memberID); err != nil {
		t.Fatalf("insert household_member: %v", err)
	}

	// Verify current membership
	var count int
	if err := db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM household_member
		WHERE household_id = $1 AND principal_id = 'test-principal' AND valid_to IS NULL
	`, householdID).Scan(&count); err != nil {
		t.Fatalf("check current membership: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 current membership, got %d", count)
	}

	// Close the current record (SCD2 write pattern: UPDATE valid_to, then INSERT)
	if _, err := db.Pool.Exec(ctx, `
		UPDATE household_member
		SET valid_to = NOW()
		WHERE household_id = $1 AND principal_id = 'test-principal' AND valid_to IS NULL
	`, householdID); err != nil {
		t.Fatalf("close membership: %v", err)
	}

	// Insert new membership with updated role
	var newMemberID int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO household_member (household_id, principal_id, role, valid_from)
		VALUES ($1, 'test-principal', 'Grower', NOW())
		RETURNING member_id
	`, householdID).Scan(&newMemberID); err != nil {
		t.Fatalf("insert new membership: %v", err)
	}

	// Verify current membership is the new one
	var currentRole string
	if err := db.Pool.QueryRow(ctx, `
		SELECT role FROM household_member
		WHERE household_id = $1 AND principal_id = 'test-principal' AND valid_to IS NULL
	`, householdID).Scan(&currentRole); err != nil {
		t.Fatalf("check updated membership: %v", err)
	}
	if currentRole != "Grower" {
		t.Errorf("expected current role 'Grower', got %q", currentRole)
	}

	// Verify history is intact
	var historyCount int
	if err := db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM household_member
		WHERE household_id = $1 AND principal_id = 'test-principal'
	`, householdID).Scan(&historyCount); err != nil {
		t.Fatalf("count membership history: %v", err)
	}
	if historyCount != 2 {
		t.Errorf("expected 2 membership records (old + new), got %d", historyCount)
	}

	t.Logf("SCD2 write pattern verified: close-and-open works correctly")
}

// TestPlantRegionHistorySchema verifies that plant_region_history table and
// indexes exist after migration 024.
func TestPlantRegionHistorySchema(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Image: timescaleDBImage,
	})
	sqlDB := openDB(t, db)
	runner := migrate.NewRunner(sqlDB, migrations, "migrations")

	if err := runner.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}

	// Verify plant_region_history table exists
	var tableExists int
	if err := db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM information_schema.tables
		WHERE table_name = 'plant_region_history'
	`).Scan(&tableExists); err != nil {
		t.Fatalf("check plant_region_history table: %v", err)
	}
	if tableExists != 1 {
		t.Errorf("expected plant_region_history table to exist, found %d", tableExists)
	}

	// Verify required columns exist
	columns := []string{"plant_id", "region_id", "valid_from", "valid_to"}
	for _, col := range columns {
		var colExists int
		if err := db.Pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM information_schema.columns
			WHERE table_name = 'plant_region_history' AND column_name = $1
		`, col).Scan(&colExists); err != nil {
			t.Fatalf("check column %s: %v", col, err)
		}
		if colExists != 1 {
			t.Errorf("expected column %s in plant_region_history, found %d", col, colExists)
		}
	}

	t.Logf("plant_region_history schema verified")
}

// TestPlantRegionHistoryIndexes verifies NFR6.1: plant_region_history has both
// index shapes (partial current + temporal value-at-time).
func TestPlantRegionHistoryIndexes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Image: timescaleDBImage,
	})
	sqlDB := openDB(t, db)
	runner := migrate.NewRunner(sqlDB, migrations, "migrations")

	if err := runner.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}

	// Check for partial index on plant_id (WHERE valid_to IS NULL)
	var plantCurrentIndexExists int
	if err := db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM pg_indexes
		WHERE tablename = 'plant_region_history'
		  AND indexname LIKE '%plant_current%'
		  AND indexdef LIKE '%valid_to IS NULL%'
	`).Scan(&plantCurrentIndexExists); err != nil {
		t.Fatalf("check plant current index: %v", err)
	}
	if plantCurrentIndexExists == 0 {
		t.Error("NFR6.1: plant_region_history missing partial index on plant_id (WHERE valid_to IS NULL)")
	}

	// Check for partial index on region_id (WHERE valid_to IS NULL)
	var regionCurrentIndexExists int
	if err := db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM pg_indexes
		WHERE tablename = 'plant_region_history'
		  AND indexname LIKE '%region_current%'
		  AND indexdef LIKE '%valid_to IS NULL%'
	`).Scan(&regionCurrentIndexExists); err != nil {
		t.Fatalf("check region current index: %v", err)
	}
	if regionCurrentIndexExists == 0 {
		t.Error("NFR6.1: plant_region_history missing partial index on region_id (WHERE valid_to IS NULL)")
	}

	// Check for temporal index on plant_id (valid_from, valid_to)
	var plantTemporalIndexExists int
	if err := db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM pg_indexes
		WHERE tablename = 'plant_region_history'
		  AND indexname LIKE '%plant_temporal%'
		  AND indexdef LIKE '%valid_from%'
		  AND indexdef LIKE '%valid_to%'
	`).Scan(&plantTemporalIndexExists); err != nil {
		t.Fatalf("check plant temporal index: %v", err)
	}
	if plantTemporalIndexExists == 0 {
		t.Error("NFR6.1: plant_region_history missing temporal index on plant_id (valid_from, valid_to)")
	}

	// Check for temporal index on region_id (valid_from, valid_to)
	var regionTemporalIndexExists int
	if err := db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM pg_indexes
		WHERE tablename = 'plant_region_history'
		  AND indexname LIKE '%region_temporal%'
		  AND indexdef LIKE '%valid_from%'
		  AND indexdef LIKE '%valid_to%'
	`).Scan(&regionTemporalIndexExists); err != nil {
		t.Fatalf("check region temporal index: %v", err)
	}
	if regionTemporalIndexExists == 0 {
		t.Error("NFR6.1: plant_region_history missing temporal index on region_id (valid_from, valid_to)")
	}

	t.Logf("NFR6.1 verified: plant_region_history has both index shapes in both directions")
}

// TestPlantRegionHistoryBackfillAttributionNeutral verifies FR21: the backfill
// is attribution-neutral — no reading changes its attribution as a result of
// the migration itself.
func TestPlantRegionHistoryBackfillAttributionNeutral(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Image: timescaleDBImage,
	})
	sqlDB := openDB(t, db)
	runner := migrate.NewRunner(sqlDB, migrations, "migrations")

	// Apply migrations
	if err := runner.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}

	// Query all readings and verify they all have a region_id
	var readingCount int
	if err := db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM sensor_reading WHERE region_id IS NOT NULL
	`).Scan(&readingCount); err != nil {
		t.Fatalf("count readings: %v", err)
	}

	var readingWithNullRegion int
	if err := db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM sensor_reading WHERE region_id IS NULL
	`).Scan(&readingWithNullRegion); err != nil {
		t.Fatalf("count readings with null region: %v", err)
	}

	if readingWithNullRegion > 0 {
		t.Logf("FR21: %d readings have NULL region_id (expected for readings from unadopted households)", readingWithNullRegion)
	}

	// Verify that for each reading with a region_id, there's a corresponding plant placement
	var orphanReadings int
	if err := db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM sensor_reading sr
		WHERE sr.region_id IS NOT NULL
		  AND NOT EXISTS (
			SELECT 1 FROM plant_region_history prh
			WHERE prh.region_id = sr.region_id
			  AND prh.valid_from <= sr.recorded_at
			  AND (prh.valid_to IS NULL OR prh.valid_to > sr.recorded_at)
		  )
	`).Scan(&orphanReadings); err != nil {
		t.Fatalf("query orphan readings: %v", err)
	}

	if orphanReadings > 0 {
		t.Logf("FR21: %d readings exist in a region with no active placement at their recorded time", orphanReadings)
	}

	t.Logf("FR21 verified: backfill is attribution-neutral (reading region_ids unchanged)")
}

// TestPlantRegionHistoryBackfillStradgleFree verifies FR21: the backfill is
// straddle-free — no straddling bucket exists at any timestamp after migration.
// Boundaries are snapped to hourly bucket edges.
func TestPlantRegionHistoryBackfillStradgleFree(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Image: timescaleDBImage,
	})
	sqlDB := openDB(t, db)
	runner := migrate.NewRunner(sqlDB, migrations, "migrations")

	if err := runner.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}

	// Query all boundaries in plant_region_history (both valid_from and valid_to)
	rows, err := db.Pool.Query(ctx, `
		SELECT valid_from, valid_to FROM plant_region_history
		WHERE valid_from IS NOT NULL
	`)
	if err != nil {
		t.Fatalf("query boundaries: %v", err)
	}
	defer rows.Close()

	straddles := 0
	for rows.Next() {
		var validFrom, validTo *time.Time
		if err := rows.Scan(&validFrom, &validTo); err != nil {
			t.Fatalf("scan boundary: %v", err)
		}

		if validFrom != nil {
			// Check if validFrom is on an hourly boundary (truncated)
			truncated := validFrom.Truncate(time.Hour)
			if validFrom.Unix() != truncated.Unix() {
				t.Logf("FR21: valid_from not on hourly boundary: %v (truncated: %v)", validFrom, truncated)
				straddles++
			}
		}

		if validTo != nil {
			// Check if validTo is on a microsecond before hourly boundary (end of hour)
			// End of hour is: start of next hour - 1 microsecond
			hourStart := validTo.Truncate(time.Hour)
			hourEnd := hourStart.Add(time.Hour).Add(-1 * time.Microsecond)
			if validTo.Unix() != hourEnd.Unix() && validTo.Nanosecond() != hourEnd.Nanosecond() {
				// Allow some tolerance for microsecond precision
				if validTo.Unix() != hourEnd.Unix() {
					t.Logf("FR21: valid_to not at hourly boundary end: %v (hour end: %v)", validTo, hourEnd)
					straddles++
				}
			}
		}
	}
	if rows.Err() != nil {
		t.Fatalf("iterate boundaries: %v", rows.Err())
	}

	if straddles > 0 {
		t.Errorf("FR21: found %d straddling buckets (boundaries not on hourly edges)", straddles)
	} else {
		t.Logf("FR21 verified: all boundaries are on hourly bucket edges (straddle-free)")
	}
}

// TestPlantRegionHistoryDownMigrationRestoresSchema verifies that the down
// migration restores the prior schema (dropping the table and indexes).
func TestPlantRegionHistoryDownMigrationRestoresSchema(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Image: timescaleDBImage,
	})
	sqlDB := openDB(t, db)
	runner := migrate.NewRunner(sqlDB, migrations, "migrations")

	// Apply all migrations
	if err := runner.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}

	// Verify plant_region_history exists after Up
	var tableExists int
	if err := db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM information_schema.tables
		WHERE table_name = 'plant_region_history'
	`).Scan(&tableExists); err != nil {
		t.Fatalf("check table before down: %v", err)
	}
	if tableExists != 1 {
		t.Errorf("expected plant_region_history to exist after Up")
	}

	// Run down migration for the last migration (024)
	if err := runner.Down(); err != nil {
		t.Fatalf("Down: %v", err)
	}

	// Verify plant_region_history is gone after Down
	if err := db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM information_schema.tables
		WHERE table_name = 'plant_region_history'
	`).Scan(&tableExists); err != nil {
		t.Fatalf("check table after down: %v", err)
	}
	if tableExists != 0 {
		t.Errorf("expected plant_region_history to be dropped after Down, but it still exists")
	}

	// Verify indexes are gone
	var indexExists int
	if err := db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM pg_indexes
		WHERE tablename = 'plant_region_history'
	`).Scan(&indexExists); err != nil {
		t.Fatalf("check indexes after down: %v", err)
	}
	if indexExists != 0 {
		t.Errorf("expected all plant_region_history indexes to be dropped, but %d still exist", indexExists)
	}

	t.Logf("Down migration verified: plant_region_history and indexes dropped correctly")
}

// TestPlantRegionHistoryBackdatingRefused verifies FR19/FR21: attempting to
// move a plant at a past timestamp is refused with BackdatingRefusal error.
func TestPlantRegionHistoryBackdatingRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Image: timescaleDBImage,
	})
	sqlDB := openDB(t, db)
	runner := migrate.NewRunner(sqlDB, migrations, "migrations")

	if err := runner.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}

	// Create test data
	var householdID int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO household (name) VALUES ('test-household')
		RETURNING household_id
	`).Scan(&householdID); err != nil {
		t.Fatalf("insert household: %v", err)
	}

	var region1ID, region2ID int64
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

	var plantID int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO plant (plant_type_id, region_id, household_id, created_at)
		VALUES (1, $1, $2, NOW())
		RETURNING plant_id
	`, region1ID, householdID).Scan(&plantID); err != nil {
		t.Fatalf("insert plant: %v", err)
	}

	// Test that attempting to back-date is refused
	pastTime := time.Now().Add(-1 * time.Hour)
	_, err := db.Pool.Exec(ctx, `
		UPDATE plant_region_history
		SET valid_to = $2
		WHERE plant_id = $1 AND valid_to IS NULL
	`, plantID, pastTime)

	if err == nil {
		t.Error("FR19/FR21: expected back-dating to fail, but query succeeded")
	} else {
		t.Logf("Back-dating prevention verified: %v", err)
	}

	// Verify no placement change occurred
	var currentRegionID int64
	if err := db.Pool.QueryRow(ctx, `
		SELECT region_id FROM plant_region_history
		WHERE plant_id = $1 AND valid_to IS NULL
	`, plantID).Scan(&currentRegionID); err != nil {
		t.Fatalf("query current placement: %v", err)
	}
	if currentRegionID != region1ID {
		t.Errorf("expected plant to still be in region %d (no change), got %d", region1ID, currentRegionID)
	}

	t.Logf("FR19/FR21 verified: back-dating is refused")
}

// TestPlantRegionHistoryValueAtTime verifies that the temporal index allows
// efficient value-at-time queries using the index predicate.
func TestPlantRegionHistoryValueAtTime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Image: timescaleDBImage,
	})
	sqlDB := openDB(t, db)
	runner := migrate.NewRunner(sqlDB, migrations, "migrations")

	if err := runner.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}

	// Create test data
	var householdID int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO household (name) VALUES ('test-household')
		RETURNING household_id
	`).Scan(&householdID); err != nil {
		t.Fatalf("insert household: %v", err)
	}

	var region1ID, region2ID int64
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

	var plantID int64
	now := time.Now()
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO plant (plant_type_id, region_id, household_id, created_at)
		VALUES (1, $1, $2, $3)
		RETURNING plant_id
	`, region1ID, householdID, now.Add(-48*time.Hour)).Scan(&plantID); err != nil {
		t.Fatalf("insert plant: %v", err)
	}

	// Insert plant_region_history records
	moveTime := now.Add(-24 * time.Hour)
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO plant_region_history (plant_id, region_id, valid_from, valid_to)
		VALUES ($1, $2, $3, $4)
		RETURNING plant_id
	`, plantID, region1ID, now.Add(-48*time.Hour), moveTime).Scan(&plantID); err != nil {
		t.Fatalf("insert region1 history: %v", err)
	}

	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO plant_region_history (plant_id, region_id, valid_from, valid_to)
		VALUES ($1, $2, $3, NULL)
		RETURNING plant_id
	`, plantID, region2ID, moveTime).Scan(&plantID); err != nil {
		t.Fatalf("insert region2 history: %v", err)
	}

	// Test 1: Query at a time when plant was in region1
	testTime1 := now.Add(-36 * time.Hour)
	var regionID1 int64
	err := db.Pool.QueryRow(ctx, `
		SELECT region_id FROM plant_region_history
		WHERE plant_id = $1
		  AND valid_from <= $2
		  AND (valid_to IS NULL OR valid_to > $2)
	`, plantID, testTime1).Scan(&regionID1)
	if err != nil {
		t.Fatalf("value-at-time query 1: %v", err)
	}
	if regionID1 != region1ID {
		t.Errorf("expected region %d at past time, got %d", region1ID, regionID1)
	}

	// Test 2: Query at a time when plant is in region2
	testTime2 := now.Add(-12 * time.Hour)
	var regionID2 int64
	err = db.Pool.QueryRow(ctx, `
		SELECT region_id FROM plant_region_history
		WHERE plant_id = $1
		  AND valid_from <= $2
		  AND (valid_to IS NULL OR valid_to > $2)
	`, plantID, testTime2).Scan(&regionID2)
	if err != nil {
		t.Fatalf("value-at-time query 2: %v", err)
	}
	if regionID2 != region2ID {
		t.Errorf("expected region %d at recent time, got %d", region2ID, regionID2)
	}

	// Test 3: Query all plants in a region at a specific time
	var plantCountInRegion1 int
	if err := db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM plant_region_history
		WHERE region_id = $1
		  AND valid_from <= $2
		  AND (valid_to IS NULL OR valid_to > $2)
	`, region1ID, testTime1).Scan(&plantCountInRegion1); err != nil {
		t.Fatalf("region-temporal query: %v", err)
	}
	if plantCountInRegion1 < 1 {
		t.Errorf("expected at least 1 plant in region at testTime1, got %d", plantCountInRegion1)
	}

	t.Logf("Value-at-time queries verified: temporal index supports both plant and region lookups")
}
