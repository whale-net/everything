//go:build integration

// Real-Postgres integration coverage for migration 030_board_display_name
// (FR57, NFR19): the column is a plain nullable column (not SCD2), up and
// down both reverse cleanly, a re-apply after a down is safe, and -- this
// task's "make this a test, not a comment" bullet -- a write to
// board.display_name touches no other table. That last check uses the exact
// statement leaflab/api/repository.go's SetBoardDisplayName issues
// (`UPDATE board SET display_name = $1 WHERE board_id = $2`) against the
// real schema, including sensor_region_history -- a table only present in
// the full migration set, not the hermetic testSchema leaflab/api's own
// integration tests use (see that package's
// dbtest_helpers_integration_test.go's doc comment on why it stays
// hermetic). Requires a real TimescaleDB image, same as
// //leaflab/migrate:ownership_migration_integration_test (migration 001
// creates a hypertable), and shares that file's timescaleImage/migrations
// package-level fixtures. See //libs/go/dbtest's README for how to run
// tests like this one.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //leaflab/migrate:ownership_migration_integration_test --test_output=all
package main_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

// newPreDisplayNameDB migrates up through 016 (the last migration before
// 030_board_display_name -- migrations 017-029 don't exist on this branch;
// see 030_board_display_name.up.sql's doc comment on the migration-number
// gap) and returns the runner (still positioned at 016) plus a pool for
// fixture setup / assertions.
func newPreDisplayNameDB(t *testing.T) (*migrate.Runner, *dbtest.Postgres) {
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
	if err := runner.Migrate(16); err != nil {
		t.Fatalf("migrate to version 16 (pre-display-name): %v", err)
	}

	return runner, db
}

// displayNameFixture is one board plus one row in each table this task's
// "touches only board.display_name" assertion must prove untouched:
// device_config, sensor (via sensor_type), region, and sensor_region_history.
type displayNameFixture struct {
	boardID int64
}

// seedDisplayNameFixture inserts one row into board, region, sensor (via the
// sensor_type migration 001 already seeds), sensor_region_history, and
// device_config -- everything TestBoardDisplayNameMigration_WriteTouchesOnlyDisplayNameColumn
// snapshots before and after the display_name write.
func seedDisplayNameFixture(t *testing.T, ctx context.Context, db *dbtest.Postgres) displayNameFixture {
	t.Helper()

	var f displayNameFixture
	mustExec := func(dest *int64, query string, args ...any) {
		t.Helper()
		if err := db.Pool.QueryRow(ctx, query, args...).Scan(dest); err != nil {
			t.Fatalf("fixture setup %q: %v", query, err)
		}
	}

	mustExec(&f.boardID, `INSERT INTO board (device_id) VALUES ($1) RETURNING board_id`, "display-name-board")

	var sensorTypeID int64
	if err := db.Pool.QueryRow(ctx, `SELECT sensor_type_id FROM sensor_type LIMIT 1`).Scan(&sensorTypeID); err != nil {
		t.Fatalf("read seeded sensor_type: %v", err)
	}

	var regionID int64
	mustExec(&regionID, `INSERT INTO region (name) VALUES ($1) RETURNING region_id`, "display-name-region")

	var sensorID int64
	mustExec(&sensorID, `
		INSERT INTO sensor (board_id, sensor_type_id, region_id, name, unit) VALUES ($1, $2, $3, $4, $5) RETURNING sensor_id
	`, f.boardID, sensorTypeID, regionID, "display-name-sensor", "degC")

	var historyID int64
	mustExec(&historyID, `INSERT INTO sensor_region_history (sensor_id, region_id) VALUES ($1, $2) RETURNING history_id`, sensorID, regionID)

	var configID int64
	mustExec(&configID, `
		INSERT INTO device_config (board_id, version, config_json) VALUES ($1, $2, $3) RETURNING config_id
	`, f.boardID, 1, `{}`)

	return f
}

// tableSnapshot renders every row of table as a single order-independent
// string (each row cast to text, joined and sorted) so a before/after
// comparison catches any change -- insert, update, or delete -- to that
// table without hand-listing its columns.
func tableSnapshot(t *testing.T, ctx context.Context, db *dbtest.Postgres, table string) string {
	t.Helper()
	var snap *string
	q := fmt.Sprintf(`SELECT string_agg(t::text, '|' ORDER BY t::text) FROM %s t`, table)
	if err := db.Pool.QueryRow(ctx, q).Scan(&snap); err != nil {
		t.Fatalf("snapshot %s: %v", table, err)
	}
	if snap == nil {
		return ""
	}
	return *snap
}

// TestBoardDisplayNameMigration_WriteTouchesOnlyDisplayNameColumn is this
// task's named "make this a test, not a comment" check: setting
// board.display_name changes only that column -- device_config, sensor,
// region and sensor_region_history (and board's own other columns) are
// byte-for-byte unchanged. Runs the exact UPDATE statement
// Repository.SetBoardDisplayName issues, against the real post-030 schema.
func TestBoardDisplayNameMigration_WriteTouchesOnlyDisplayNameColumn(t *testing.T) {
	runner, db := newPreDisplayNameDB(t)
	ctx := context.Background()

	if err := runner.Steps(1); err != nil {
		t.Fatalf("apply migration 030: %v", err)
	}

	f := seedDisplayNameFixture(t, ctx, db)

	unaffectedTables := []string{"device_config", "sensor", "region", "sensor_region_history"}
	before := make(map[string]string, len(unaffectedTables))
	for _, table := range unaffectedTables {
		before[table] = tableSnapshot(t, ctx, db, table)
	}

	var boardBefore string
	if err := db.Pool.QueryRow(ctx, `
		SELECT (device_id, registered_at, last_seen_at)::text FROM board WHERE board_id = $1
	`, f.boardID).Scan(&boardBefore); err != nil {
		t.Fatalf("snapshot board's other columns (before): %v", err)
	}

	if _, err := db.Pool.Exec(ctx, `UPDATE board SET display_name = $1 WHERE board_id = $2`, "Living Room Board", f.boardID); err != nil {
		t.Fatalf("update board.display_name: %v", err)
	}

	for _, table := range unaffectedTables {
		if got := tableSnapshot(t, ctx, db, table); got != before[table] {
			t.Errorf("%s changed after setting board.display_name -- FR57 requires this write touch only board.display_name\nbefore: %s\nafter:  %s", table, before[table], got)
		}
	}

	var boardAfter string
	if err := db.Pool.QueryRow(ctx, `
		SELECT (device_id, registered_at, last_seen_at)::text FROM board WHERE board_id = $1
	`, f.boardID).Scan(&boardAfter); err != nil {
		t.Fatalf("snapshot board's other columns (after): %v", err)
	}
	if boardAfter != boardBefore {
		t.Errorf("board's other columns changed after setting display_name: before=%q after=%q", boardBefore, boardAfter)
	}

	var displayName *string
	if err := db.Pool.QueryRow(ctx, `SELECT display_name FROM board WHERE board_id = $1`, f.boardID).Scan(&displayName); err != nil {
		t.Fatalf("read back display_name: %v", err)
	}
	if displayName == nil || *displayName != "Living Room Board" {
		t.Errorf("display_name = %v, want %q", displayName, "Living Room Board")
	}
}

// TestBoardDisplayNameMigration_ColumnIsPlainNullable_NotSCD2 proves the
// scaffold's own claim: board.display_name is a plain TEXT NULL column --
// no valid_from/valid_to sibling was added alongside it (historised board
// names are explicitly out of scope) -- and a board that never had its
// display name set reads back as NULL, not an empty string or a default.
func TestBoardDisplayNameMigration_ColumnIsPlainNullable_NotSCD2(t *testing.T) {
	runner, db := newPreDisplayNameDB(t)
	ctx := context.Background()

	if err := runner.Steps(1); err != nil {
		t.Fatalf("apply migration 030: %v", err)
	}

	var dataType, isNullable string
	if err := db.Pool.QueryRow(ctx, `
		SELECT data_type, is_nullable FROM information_schema.columns
		WHERE table_name = 'board' AND column_name = 'display_name'
	`).Scan(&dataType, &isNullable); err != nil {
		t.Fatalf("query information_schema.columns for board.display_name: %v", err)
	}
	if dataType != "text" {
		t.Errorf("board.display_name data_type = %q, want \"text\"", dataType)
	}
	if isNullable != "YES" {
		t.Errorf("board.display_name is_nullable = %q, want \"YES\"", isNullable)
	}

	var validToExists bool
	if err := db.Pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name = 'board' AND column_name = 'valid_to')
	`).Scan(&validToExists); err != nil {
		t.Fatalf("query information_schema.columns for board.valid_to: %v", err)
	}
	if validToExists {
		t.Error("board has a valid_to column, want none -- display_name is a plain column, not SCD2 (historised board names are out of scope, per AGENTS.md's SCD2 convention)")
	}

	var boardID int64
	if err := db.Pool.QueryRow(ctx, `INSERT INTO board (device_id) VALUES ($1) RETURNING board_id`, "no-display-name-board").Scan(&boardID); err != nil {
		t.Fatalf("insert board with no display_name: %v", err)
	}
	var displayName *string
	if err := db.Pool.QueryRow(ctx, `SELECT display_name FROM board WHERE board_id = $1`, boardID).Scan(&displayName); err != nil {
		t.Fatalf("read display_name: %v", err)
	}
	if displayName != nil {
		t.Errorf("display_name = %q for a board where it was never set, want NULL", *displayName)
	}
}

// TestBoardDisplayNameMigration_DownReversesCleanly and
// TestBoardDisplayNameMigration_UpDownUpIsIdempotentSafe mirror the
// Validation discipline 015_ownership's and 016_audit_log's migration tests
// already apply: up and down both clean, and re-running up after down is
// safe against a fresh database.
func TestBoardDisplayNameMigration_DownReversesCleanly(t *testing.T) {
	runner, db := newPreDisplayNameDB(t)
	ctx := context.Background()

	if err := runner.Steps(1); err != nil {
		t.Fatalf("apply migration 030: %v", err)
	}
	if err := runner.Steps(-1); err != nil {
		t.Fatalf("reverse migration 030: %v", err)
	}

	var exists bool
	if err := db.Pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name = 'board' AND column_name = 'display_name')
	`).Scan(&exists); err != nil {
		t.Fatalf("check board.display_name exists after down: %v", err)
	}
	if exists {
		t.Error("board.display_name still exists after reversing migration 030")
	}
}

func TestBoardDisplayNameMigration_UpDownUpIsIdempotentSafe(t *testing.T) {
	runner, db := newPreDisplayNameDB(t)
	ctx := context.Background()

	if err := runner.Steps(1); err != nil {
		t.Fatalf("first apply of migration 030: %v", err)
	}
	if err := runner.Steps(-1); err != nil {
		t.Fatalf("reverse migration 030: %v", err)
	}
	if err := runner.Steps(1); err != nil {
		t.Fatalf("second apply of migration 030: %v", err)
	}

	var exists bool
	if err := db.Pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name = 'board' AND column_name = 'display_name')
	`).Scan(&exists); err != nil {
		t.Fatalf("check board.display_name exists after up-down-up: %v", err)
	}
	if !exists {
		t.Error("board.display_name does not exist after up-down-up, want it re-created by the second apply")
	}
}
