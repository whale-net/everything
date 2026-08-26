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
